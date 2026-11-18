# fsx · 文件传送

局域网 / 跨网的点对点文件传送客户端。单个 Go 二进制，零第三方依赖，自带托盘、内嵌 Web 控制台与命令行。

- **P2P 直连**：同一局域网自动发现设备，输入节点 ID 或 `IP:端口` 也可直接连接
- **TLS 1.3 双向认证**：身份由 Ed25519 公钥派生，证书每次启动现签，不依赖 CA
- **6 位核对码**：收发两端显示同一串数字，用于确认连接没有被中间人替换
- **断点续传**：按块 SHA-256 校验，中断后从已写入位置继续
- **跨网传输**：自带中继（只转发 TLS 密文）；支持 IPv6 全局地址直连并一键复制
- **桌面集成**：Windows 托盘 + 系统气泡通知；macOS 菜单栏 `.app` + 原生窗口
- **浏览器接收**：手机等其他设备打开控制台地址，输入 6 位配对码即可收发

## 快速开始

要求 Go 1.22 或更高版本。

```bash
go build -trimpath -ldflags "-s -w" -o fsx .
./fsx            # 等价于 ./fsx serve
```

启动后会在终端打印节点 ID、控制台地址、配对码和接收目录，并自动打开控制台。
控制台左侧显示「手机 / 其他设备配对码」：在对方浏览器打开 `http://本机IP:9271`，输入该 6 位数字即可互传。

默认端口：控制台 `9271`、P2P `9272`、局域网发现 `9273`、中继 `9270`。
状态目录默认是系统配置目录下的 `fsx`，可用 `--home` 或环境变量 `FSX_HOME` 指定；接收目录默认 `~/Downloads/fsx`（没有 `Downloads` 目录时为 `~/fsx-downloads`）。

## 使用方式

### 同一局域网

双方运行 `fsx`，会自动发现彼此。把文件拖进窗口或点「选择文件」，在左侧选好设备即可发送。
接收方会看到核对码与文件清单，确认后开始传输；勾选「以后自动接收该设备」后，该设备再发送就不必重复确认。

### 手机 / 其他浏览器

在对方浏览器打开 `http://<本机局域网 IP>:9271`，输入 6 位配对码，即可上传或下载文件。

### 跨网

在一台有公网地址的机器上启动中继：

```bash
./fsx relay --listen :9270
```

双方启动时指定中继地址（首次连接会记录中继指纹）：

```bash
./fsx serve --relay 203.0.113.10:9270
```

中继只负责转发，看到的是 TLS 密文，无法读取文件内容。若双方都有公网 IPv6，可在界面底部复制直连地址，让对方在「添加设备」中粘贴，走 IPv6 直通，不经过中继。

### 核对码

接收方在待确认卡片上看到 6 位数字，发送方在「等待对方」的任务行看到同一串数字。两边一致再确认接收，可排除中间人替换连接的可能。

## 命令行

```text
fsx [serve] [选项]                          启动节点与控制台
fsx send <节点ID|名称|IP:端口> <文件|目录>...  直接发送并显示进度
fsx ls [选项]                               扫描局域网在线节点
fsx relay [--listen :9270]                  启动中继 / 信令服务
fsx version | help
```

常用选项（`serve` 与 `send` 通用）：

| 选项 | 说明 |
| --- | --- |
| `--home <dir>` | 状态目录（默认用户配置目录下的 `fsx`，或 `FSX_HOME`） |
| `--name <name>` | 节点显示名（默认主机名） |
| `--p2p <port>` / `--http <port>` / `--disc <port>` | 覆盖默认端口 |
| `--download <dir>` | 接收目录 |
| `--relay <host:port>` | 中继地址，跨网传输需要 |
| `--relay-fp <id>` | 固定中继身份指纹（默认首次连接时记录） |
| `--force-relay` | 始终经由中继（对称 NAT 场景） |
| `--no-discovery` | 关闭局域网发现 |
| `--accept-all` | 自动接收所有传输（谨慎使用） |
| `--no-browser` | 不自动打开浏览器 |
| `--no-tray` | 关闭托盘客户端（Windows） |

## 安全模型

- **节点身份**：Ed25519 长期密钥，节点 ID 为公钥 SHA-256 的前 16 字节。
- **传输加密**：TLS 1.3 双向认证；证书每次启动现签，用「证书公钥 == 对方声明的节点 ID」完成身份固定，不依赖任何 CA。
- **防中间人**：核对码由 TLS 导出密钥与双方 ID 派生，收发两端一致；两端比对一致再接收。
- **完整性**：每个数据块附带 SHA-256，接收端逐块校验；文件完成前写入 `.part`。
- **中继不可见明文**：中继转发的是端到端 TLS 会话，服务端看不到文件内容。
- **控制台访问**：非本机访问需 6 位配对码换取会话；写操作额外要求 `X-FSX: 1` 请求头，降低跨站请求风险。

## 桌面集成

- **Windows**：纯 Win32 托盘（无额外依赖），控制台以 Edge/Chrome `--app` 独立窗口打开；关闭窗口后仍驻留托盘继续接收，收到消息或文件请求时弹系统气泡通知；托盘菜单可复制配对码/节点 ID 或退出。
- **macOS**：以 `.app` 启动时使用 Cocoa + WKWebView 原生窗口，菜单栏常驻，关闭窗口即隐藏，重新打开 `.app` 会唤回窗口；后台收到消息走系统通知中心。
- **Linux**：控制台在浏览器中打开，无托盘。

## 项目结构

```text
main.go                 命令入口（serve / send / ls / relay）
internal/addrs          本机 IPv6 地址探测
internal/discovery      UDP 局域网发现与节点注册表
internal/identity       Ed25519 身份、TLS 配置、核对码派生
internal/proto          帧协议与连接封装
internal/relay          中继 / 信令服务与客户端
internal/store          配置、信任名单与历史记录
internal/transfer       任务管理、发送、接收、断点续传
internal/httpsrv        本地 HTTP API、配对鉴权、下载
internal/ui             内嵌 Web 控制台（go:embed）
internal/tray           各平台托盘与桌面窗口
internal/tray/win       Windows 图标资源（ico / rc）
rsrc_windows_*.syso     编译进 exe 的 Windows 图标资源
```

## 构建说明

```bash
# 本机
go build -trimpath -ldflags "-s -w" -o fsx .

# Linux / Windows（纯 Go，关闭 cgo）
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o fsx-linux-amd64 .
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o fsx-windows-amd64.exe .

# macOS 原生窗口需要 cgo
CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o fsx .
```

Windows 图标由 `internal/tray/win/fsx.ico` 与 `icon.rc` 经 windres 编译为 `.syso`，构建时按 GOOS/GOARCH 自动只链接对应架构的资源。重新生成图标：

```bash
go run internal/tray/mkico.go internal/tray/win/fsx.ico
x86_64-w64-mingw32-windres -i internal/tray/win/icon.rc \
  -o rsrc_windows_amd64.syso -O coff --include-dir internal/tray/win
```

## 数据目录

`--home`（默认 `UserConfigDir/fsx`，可用 `FSX_HOME` 覆盖）下主要文件：

- `identity.key`：节点私钥，请勿泄露；删除后会生成新身份，节点 ID 随之改变
- `state.json`：节点名、接收目录、自动接收（信任）名单、历史记录、已知对端

未完成的接收文件保存在接收目录下的 `.part`，同名 `.fsx` 记录续传位置；中断后再次接收会从已写入位置继续。
