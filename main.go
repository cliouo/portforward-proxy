package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"strings"
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
	Status bool   `toml:"status"`
}

func main() {
	// 定义命令行参数
	configFile := flag.String("c", "config.toml", "配置文件路径")
	localPort := flag.String("local", "", "本地监听端口")
	remoteAddr := flag.String("remote", "", "远程地址")
	remotePort := flag.String("rport", "", "远程端口")
	proxyURL := flag.String("proxy", "", "HTTP代理地址 (可选)")

	// 解析命令行参数
	flag.Parse()

	// 检查是否使用命令行参数
	if *localPort != "" || *remoteAddr != "" || *remotePort != "" || *proxyURL != "" {
		if *configFile != "config.toml" {
			log.Fatal("不能同时使用命令行参数和 -c 参数指定配置文件")
		}
		handleSingleForward(*localPort, *remoteAddr, *remotePort, *proxyURL)
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
			go func(index int, fwdConfig ForwardConfig) {
				logger := log.New(os.Stdout, fmt.Sprintf("[转发 %d] ", index+1), log.LstdFlags)
				handleForward(fwdConfig, logger)
			}(i, fc)
		}
	}

	// 保持主程序运行
	select {}
}

func handleSingleForward(localPort, remoteAddr, remotePort, proxyURL string) {
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

		go handleConnection(localConn, remoteAddrFull, proxyURL, logger)
	}
}

func handleForward(fc ForwardConfig, logger *log.Logger) {
	remoteAddrFull := fc.Remote + ":" + fc.RPort
	
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

		go handleConnection(localConn, remoteAddrFull, fc.Proxy, logger)
	}
}

func handleConnection(localConn net.Conn, remoteAddr, proxyURL string, logger *log.Logger) {
	defer localConn.Close()

	logger.Printf("新连接来自: %s\n", localConn.RemoteAddr())

	var remoteConn net.Conn
	var err error
	if proxyURL != "" {
		remoteConn, err = dialThroughProxy(remoteAddr, proxyURL)
	} else {
		remoteConn, err = net.DialTimeout("tcp", remoteAddr, 30*time.Second)
	}
	if err != nil {
		logger.Printf("连接到远程地址错误: %v\n", err)
		return
	}
	defer remoteConn.Close()

	logger.Printf("成功连接到远程地址: %s\n", remoteAddr)

	errChan := make(chan error, 2)
	go func() {
		_, err := io.Copy(remoteConn, localConn)
		errChan <- err
	}()
	go func() {
		_, err := io.Copy(localConn, remoteConn)
		errChan <- err
	}()

	err = <-errChan
	if err != nil && err != io.EOF {
		logger.Printf("数据传输错误: %v\n", err)
	}

	logger.Printf("连接关闭: %s\n", localConn.RemoteAddr())
}

func dialThroughProxy(remoteAddr, proxyURL string) (net.Conn, error) {
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
	if _, err := conn.Write([]byte(connectReq)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("发送CONNECT请求错误: %v", err)
	}

	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("读取代理响应错误: %v", err)
	}

	response := string(buf[:n])
	if !strings.Contains(response, "200 Connection established") {
		conn.Close()
		return nil, fmt.Errorf("代理连接失败: %s", response)
	}

	return conn, nil
}