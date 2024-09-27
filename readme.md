
# 端口转发工具

这是一个用 Go 语言编写的灵活的端口转发工具。它支持通过命令行参数或配置文件进行单一或多组端口转发设置，并可选择使用 HTTP 代理。

## 功能特点

1. 支持命令行参数和配置文件两种方式进行设置
2. 可配置多组端口转发规则
3. 支持HTTP代理
4. 提供后台运行和管理脚本
5. 详细的日志记录，支持按组划分的美化输出

## 使用方法

### 命令行参数
```bash
portforward -local <本地端口> -remote <远程地址> -rport <远程端口> [-proxy <HTTP代理地址>]
```
使用命令行参数时，不能同时使用配置文件。支持的参数如下：

- `-local`: 本地监听地址
- `-remote`: 远程目标地址
- `-rport`: 远程端口
- `-proxy`: HTTP代理地址（可选）
### 配置文件
使用 -c 参数指定配置文件路径，默认为当前目录下的 config.toml：
```bash
portforward -c <配置文件路径>
```

配置文件示例 (config.toml.example):

```toml
[[forward]]
local = "8080"
remote = "example.com"
rport = "80"
proxy = ""
status = true

[[forward]]
local = "8443"
remote = "secure.example.com"
rport = "443"
proxy = "http://proxy.example.com:8080"
status = false
```
## 管理脚本
- start.bat: 启动端口转发程序
- stop.bat: 停止端口转发程序
- restart.bat: 重启端口转发程序

## 代码实现逻辑
1. 主函数 main() 首先解析命令行参数。
2. 如果提供了 -local, -remote, -rport 等参数，程序将使用单一转发模式。
3. 否则，程序尝试读取配置文件（默认为 config.toml 或通过 -c 参数指定）。
4. 如果既没有命令行参数也没有有效的配置文件，程序将退出并记录错误日志。
5. 对于配置文件中的每个启用的转发规则，程序会启动一个独立的 goroutine 来处理。
6. 每个转发处理 goroutine 会监听指定的本地端口，并将接收到的连接转发到相应的远程地址。
7. 如果指定了代理，连接将通过 HTTP 代理建立。
8. 程序使用 io.Copy() 在本地连接和远程连接之间双向传输数据。
9. 每个转发组都有独立的日志输出，包含组号信息。

## 注意事项
- 不能同时使用命令行参数和 -c 参数指定配置文件。
- 如果没有提供有效的配置或参数，程序将退出并记录错误日志。
- 确保有足够的权限来监听指定的本地端口。

## 依赖

- github.com/BurntSushi/toml: 用于解析 TOML 配置文件


## 构建和运行

```
go build -o portforward.exe
./portforward.exe [参数]
```

或使用提供的管理脚本来启动、停止和重启程序。