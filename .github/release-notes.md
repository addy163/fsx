## 下载选择

| 平台 | 文件 | 说明 |
| --- | --- | --- |
| Windows 10/11 | `fsx-windows-amd64.exe` | 绝大多数电脑用这个 |
| Windows on ARM | `fsx-windows-arm64.exe` | ARM64 设备 |
| macOS（Apple 芯片） | `fsx-darwin-arm64.app.zip` | 解压后拖入「应用程序」，菜单栏常驻 |
| macOS（Intel） | `fsx-darwin-amd64.app.zip` | 同上 |
| macOS 命令行 | `fsx-darwin-*.tar.gz` | 解压得到 CLI 二进制 |
| Linux x86_64 | `fsx-linux-amd64.tar.gz` | 解压后 `chmod +x fsx-linux-amd64` |
| Linux ARM64 | `fsx-linux-arm64.tar.gz` | 同上 |
| Linux ARMv7 | `fsx-linux-arm.tar.gz` | 同上 |

## 校验

下载 `SHA256SUMS` 后，在同目录执行：

```bash
sha256sum -c SHA256SUMS        # Linux
shasum -a 256 -c SHA256SUMS    # macOS
```

Windows PowerShell：

```powershell
Get-FileHash .\fsx-windows-amd64.exe -Algorithm SHA256
```

与 `SHA256SUMS` 中的值比对即可。

## 使用

Windows 双击运行，关闭窗口后仍驻留托盘继续接收；macOS 从「应用程序」启动，图标在菜单栏。
首次运行会生成节点身份并打开控制台，默认端口：控制台 `9271`、P2P `9272`、发现 `9273`。
详见仓库 README。
