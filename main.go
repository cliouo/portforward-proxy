package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BurntSushi/toml"
)

// 配置结构体
type Config struct {
	Forwards []ForwardConfig `toml:"forward"`
}

type ForwardConfig struct {
	Local  string `toml:"local"`
	Remote string `toml:"remote"`
	RPort  string `toml:"rport"`
	Proxy  string `toml:"proxy"`
	Debug  bool   `toml:"debug"`
	Status bool   `toml:"status"`
}

func main() {
	// 定义命令行参数
	configFile := flag.String("c", "config.toml", "配置文件路径")
	localPort := flag.String("local", "", "本地监听端口")
	remoteAddr := flag.String("remote", "", "远程地址")
	remotePort := flag.String("rport", "", "远程端口")
	proxyURL := flag.String("proxy", "", "HTTP代理地址 (可选)")
	debugMode := flag.Bool("debug", false, "启用调试模式，记录流量")

	// 解析命令行参数
	flag.Parse()

	// 检查是否使用命令行参数
	if *localPort != "" || *remoteAddr != "" || *remotePort != "" || *proxyURL != "" {
		if *configFile != "config.toml" {
			log.Fatal("不能同时使用命令行参数和 -c 参数指定配置文件")
		}
		handleSingleForward(*localPort, *remoteAddr, *remotePort, *proxyURL, *debugMode)
		return
	}

	// 读取配置文件
	var config Config
	if _, err := toml.DecodeFile(*configFile, &config); err != nil {
		log.Printf("无法读取配置文件 %s: %v\n", *configFile, err)
		log.Fatal("请提供有效的配置文件或使用命令行参数")
	}

	// 处理多组转发配置
	for i, fc := range config.Forwards {
		if fc.Status {
			go func(index int, fwdConfig ForwardConfig, debug bool) {
				logger := log.New(os.Stdout, fmt.Sprintf("[转发 %d] ", index+1), log.LstdFlags)
				handleForward(fwdConfig, logger, debug)
			}(i, fc, *debugMode || fc.Debug)
		}
	}

	// 保持主程序运行
	select {}
}

func handleSingleForward(localPort, remoteAddr, remotePort, proxyURL string, debug bool) {
	remoteAddrFull := remoteAddr + ":" + remotePort
	logger := log.New(os.Stdout, "[单一转发] ", log.LstdFlags)

	listener, err := net.Listen("tcp", ":"+localPort)
	if err != nil {
		logger.Fatal("无法启动本地监听:", err)
	}
	defer listener.Close()

	logger.Printf("正在监听本地端口 %s\n", localPort)

	for {
		localConn, err := listener.Accept()
		if err != nil {
			logger.Println("接受连接错误:", err)
			continue
		}

		go handleConnection(localConn, remoteAddrFull, proxyURL, logger, debug)
	}
}

func handleForward(fc ForwardConfig, logger *log.Logger, globalDebug bool) {
	remoteAddrFull := fc.Remote + ":" + fc.RPort
	debugEnabled := globalDebug || fc.Debug

	listener, err := net.Listen("tcp", ":"+fc.Local)
	if err != nil {
		logger.Fatal("无法启动本地监听:", err)
	}
	defer listener.Close()

	logger.Printf("正在监听本地端口 %s\n", fc.Local)

	for {
		localConn, err := listener.Accept()
		if err != nil {
			logger.Println("接受连接错误:", err)
			continue
		}

		go handleConnection(localConn, remoteAddrFull, fc.Proxy, logger, debugEnabled)
	}
}

func handleConnection(localConn net.Conn, remoteAddr, proxyURL string, logger *log.Logger, debug bool) {
	start := time.Now()
	clientAddr := localConn.RemoteAddr().String()
	listenAddr := localConn.LocalAddr().String()
	proxyDesc := "直连"
	if proxyURL != "" {
		proxyDesc = proxyURL
	}

	logger.Printf("新连接建立: 客户端 %s -> 本地 %s -> 目标 %s (代理: %s, 调试: %t)\n", clientAddr, listenAddr, remoteAddr, proxyDesc, debug)

	var remoteConn net.Conn
	var err error
	if proxyURL != "" {
		remoteConn, err = dialThroughProxy(remoteAddr, proxyURL, logger, debug)
	} else {
		remoteConn, err = net.DialTimeout("tcp", remoteAddr, 30*time.Second)
	}
	if err != nil {
		logger.Printf("连接到远程地址错误: %v\n", err)
		return
	}
	logger.Printf("远程连接建立: 本地 %s <-> 远端 %s (目标: %s)\n", remoteConn.LocalAddr(), remoteConn.RemoteAddr(), remoteAddr)

	var closeOnce sync.Once
	closeAll := func() {
		localConn.Close()
		remoteConn.Close()
	}
	defer closeOnce.Do(closeAll)

	var asciiLog *os.File
	var hexLog *os.File
	var trafficMu *sync.Mutex
	if debug {
		asciiLog, hexLog, trafficMu = createTrafficLog(listenAddr, logger)
		if asciiLog != nil {
			defer asciiLog.Close()
		}
		if hexLog != nil {
			defer hexLog.Close()
		}
	}

	errChan := make(chan error, 2)
	var clientToServerBytes int64
	var serverToClientBytes int64

	go func() {
		reader := io.Reader(localConn)
		if debug && (asciiLog != nil || hexLog != nil) {
			reader = io.TeeReader(localConn, &captureWriter{
				direction: "C->S",
				asciiW:    asciiLog,
				hexW:      hexLog,
				mu:        trafficMu,
				logger:    logger,
			})
		}
		n, copyErr := io.Copy(remoteConn, reader)
		atomic.AddInt64(&clientToServerBytes, n)
		errChan <- copyErr
	}()
	go func() {
		reader := io.Reader(remoteConn)
		if debug && (asciiLog != nil || hexLog != nil) {
			reader = io.TeeReader(remoteConn, &captureWriter{
				direction: "S->C",
				asciiW:    asciiLog,
				hexW:      hexLog,
				mu:        trafficMu,
				logger:    logger,
			})
		}
		n, copyErr := io.Copy(localConn, reader)
		atomic.AddInt64(&serverToClientBytes, n)
		errChan <- copyErr
	}()

	firstErr := <-errChan
	closeOnce.Do(closeAll)
	secondErr := <-errChan

	if err := firstNonEOF(firstErr, secondErr); err != nil {
		logger.Printf("数据传输错误: %v\n", err)
	}

	duration := time.Since(start).Round(time.Millisecond)
	logger.Printf("连接关闭: 客户端 %s -> 目标 %s，耗时 %s，上行 %d 字节，下行 %d 字节\n",
		clientAddr, remoteAddr, duration, atomic.LoadInt64(&clientToServerBytes), atomic.LoadInt64(&serverToClientBytes))
}

func dialThroughProxy(remoteAddr, proxyURL string, logger *log.Logger, debug bool) (net.Conn, error) {
	proxyURLParsed, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("解析代理URL错误: %v", err)
	}

	dialer := &net.Dialer{
		Timeout: 30 * time.Second,
	}

	conn, err := dialer.Dial("tcp", proxyURLParsed.Host)
	if err != nil {
		return nil, fmt.Errorf("连接到代理服务器错误: %v", err)
	}

	connectReq := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", remoteAddr, remoteAddr)
	if debug && logger != nil {
		logger.Printf("CONNECT 请求:\n%s", connectReq)
	}
	if _, err := conn.Write([]byte(connectReq)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("发送CONNECT请求错误: %v", err)
	}

	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("读取代理响应错误: %v", err)
	}

	response := string(buf[:n])
	if debug && logger != nil {
		logger.Printf("CONNECT 响应:\n%s", response)
	}
	if !strings.Contains(response, "200 Connection established") {
		conn.Close()
		return nil, fmt.Errorf("代理连接失败: %s", response)
	}

	logger.Printf("通过代理建立连接成功: %s -> %s\n", proxyURLParsed.Host, remoteAddr)

	return conn, nil
}

func createTrafficLog(localAddr string, logger *log.Logger) (*os.File, *os.File, *sync.Mutex) {
	if err := os.MkdirAll("logs", 0755); err != nil {
		logger.Printf("创建流量日志目录失败: %v\n", err)
		return nil, nil, nil
	}

	baseName := fmt.Sprintf("%s_%s", time.Now().Format("20060102T150405.000"), sanitizeFilename(localAddr))
	asciiPath := filepath.Join("logs", baseName+".ascii.log")
	hexPath := filepath.Join("logs", baseName+".hex.log")

	asciiFile, err := os.Create(asciiPath)
	if err != nil {
		logger.Printf("创建ASCII流量日志文件失败: %v\n", err)
		return nil, nil, nil
	}

	hexFile, err := os.Create(hexPath)
	if err != nil {
		logger.Printf("创建HEX流量日志文件失败: %v\n", err)
		asciiFile.Close()
		return nil, nil, nil
	}

	logger.Printf("调试模式启用，流量将写入: %s 和 %s\n", asciiPath, hexPath)
	return asciiFile, hexFile, &sync.Mutex{}
}

type captureWriter struct {
	direction string
	asciiW    io.Writer
	hexW      io.Writer
	mu        *sync.Mutex
	logger    *log.Logger
}

func (cw *captureWriter) Write(p []byte) (int, error) {
	if cw.asciiW == nil && cw.hexW == nil {
		return len(p), nil
	}

	if cw.mu != nil {
		cw.mu.Lock()
		defer cw.mu.Unlock()
	}

	if len(p) > 0 {
		if cw.asciiW != nil {
			writeASCIILog(cw.asciiW, cw.direction, p, cw.logger)
		}
		if cw.hexW != nil {
			writeHexLog(cw.hexW, cw.direction, p, cw.logger)
		}
	}

	return len(p), nil
}

func writeASCIILog(w io.Writer, direction string, data []byte, logger *log.Logger) {
	timestamp := time.Now().Format("2006-01-02 15:04:05.000")
	header := fmt.Sprintf("--- [%s] [%s] %d bytes ---\n", timestamp, direction, len(data))

	if _, err := io.WriteString(w, header); err != nil {
		if logger != nil {
			logger.Printf("写入ASCII流量日志失败: %v\n", err)
		}
		return
	}

	payload := sanitizeToASCII(data)
	if _, err := io.WriteString(w, payload); err != nil {
		if logger != nil {
			logger.Printf("写入ASCII流量日志失败: %v\n", err)
		}
		return
	}

	if _, err := io.WriteString(w, "\n"); err != nil && logger != nil {
		logger.Printf("写入ASCII流量日志失败: %v\n", err)
	}
}

func writeHexLog(w io.Writer, direction string, data []byte, logger *log.Logger) {
	timestamp := time.Now().Format("15:04:05.000")

	var hexBuilder strings.Builder
	if len(data) > 0 {
		hexBuilder.Grow(len(data)*3 - 1)
	}
	for i, b := range data {
		if i > 0 {
			hexBuilder.WriteByte(' ')
		}
		hexBuilder.WriteString(fmt.Sprintf("%02X", b))
	}

	asciiPreview := sanitizeToASCII(data)
	if len(asciiPreview) > 32 {
		asciiPreview = asciiPreview[:32]
	}

	line := fmt.Sprintf("%s|%s|%d|%s|%s\n", timestamp, direction, len(data), hexBuilder.String(), asciiPreview)
	if _, err := io.WriteString(w, line); err != nil && logger != nil {
		logger.Printf("写入HEX流量日志失败: %v\n", err)
	}
}

func sanitizeToASCII(data []byte) string {
	var builder strings.Builder
	builder.Grow(len(data))

	for _, b := range data {
		if b >= 32 && b <= 126 {
			builder.WriteByte(b)
		} else {
			builder.WriteByte('.')
		}
	}

	return builder.String()
}

func sanitizeFilename(name string) string {
	replacer := strings.NewReplacer(
		":", "-",
		"/", "_",
		"\\", "_",
		"*", "-",
		"?", "-",
		"\"", "-",
		"<", "-",
		">", "-",
		"|", "-",
		" ", "_",
	)

	clean := replacer.Replace(name)
	clean = strings.ReplaceAll(clean, "..", "_")
	return clean
}

func firstNonEOF(errs ...error) error {
	for _, err := range errs {
		if err != nil && err != io.EOF {
			return err
		}
	}
	return nil
}
