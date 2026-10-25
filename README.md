# Asterhand · 星伴

A personal agent system across your devices.

Asterhand 让自己的设备通过自然语言协作。A 提交任务，B 用自己的 OpenClaw、DeepSeek 凭据和授权目录执行；任务与结果保存在 B，A 下线后 B 已接收的任务继续运行。

## 能力与支持范围

- Windows x64 完整节点：Go 持久任务服务、SQLite、OpenClaw + DeepSeek 和统一 Web。
- 授权目录内创建和读取 UTF-8 文本，提供实际路径、文件大小、内容摘要和预览；创建操作拒绝覆盖已有文件。
- 节点配对、远端任务、普通问答、取消、断线补读和重启后的人工对账。
- Windows 原生启动入口与系统托盘；桌面图标可选创建，运行时可从托盘打开 Web 或退出节点。
- Windows 全局快捷输入：默认 Ctrl+Alt+J 呼出多行文本面板，支持两种内置主题；在 Web「节点设置 → 快捷输入」自定义组合、检查占用并持久保存本机设置。
- 每个节点使用自己的凭据和授权目录；Windows 使用当前用户 DPAPI 保护秘密，节点间使用 TLS 与固定证书。
- 首版每节点一个执行槽、一个授权目录，文件最大 64 KiB；删除文件、任意 shell 和具体 coding harness 尚未提供。

版本与支持边界见 [平台矩阵](docs/testing/platforms.md)。当前只发布 Windows 完整包；Linux/macOS 保留扩展接口，手机远程访问尚未验证。Web 默认只绑定本机 loopback，在各自设备打开。

## 安装与使用

复制以下一行到 PowerShell，自动从仓库[固定安装入口](https://raw.githubusercontent.com/jackgan90/Asterhand/main/install.ps1) 下载并执行安装器，默认安装最新稳定 Release：

~~~powershell
irm https://raw.githubusercontent.com/jackgan90/Asterhand/main/install.ps1 -ErrorAction Stop | iex
~~~

`irm` 下载脚本，`iex` 在内存中执行，无需手动保存文件。指定版本或标签的一行命令见 [安装指南](docs/installation/quickstart.md)。版本必须已公开发布；没有稳定版时，可显式选择已发布的预发布版。

脚本解析目标后下载同一标签的完整包，包含 Node.js、OpenClaw 和模型插件，并补齐 Microsoft VC++ x64 运行库、WebView2 Runtime 与 Tailscale。安装可选择创建桌面图标，也可显式传入 `-DesktopShortcut Create` 或 `Skip`；完成后退出，不启动节点、不要求模型凭据或网络登录。用户随后双击桌面图标，或运行安装目录的 `AsterhandLauncher.exe` / `start.ps1`。让 A、B 加入同一 Tailscale 网络，在各自设置页配置凭据、授权目录并验证，再用 B 生成的配对码在 A 配对。桌面选择、旧包兼容和本地包模式见安装指南。

在 A 选择 B，发送“在授权目录新建 hello-world.txt，内容精确为 hello world，不添加换行或 BOM”。成功后检查 B 的实际文件及页面显示的 11 字节结果。完整步骤见 [安装、升级与诊断](docs/installation/quickstart.md)。

## 开发

工具链版本以 [依赖清单](packaging/dependencies.json) 为准。运行 `go test ./...` 和 `go vet ./...`；完整构建、依赖升级和验证入口见 [脚本指南](scripts/README.md)、[依赖维护](packaging/upgrade.md) 和 [测试指南](tests/README.md)。

统一发布入口为 `scripts/release.ps1`，自动准备依赖、构建验证并上传 GitHub 草稿。公开测试版或正式版使用显式发布模式；命令、恢复方式与平台扩展契约见 [发布指南](docs/installation/releases.md)。

本机验收改动时，双击根目录 [install-local.cmd](install-local.cmd)：源码未变且已有完整、校验通过的构建包时直接复用，否则准备锁定依赖并构建，随后通过正式安装器安装。首次安装询问桌面图标；覆盖安装保留实际图标状态和原数据目录。包检查成功后自动正常退出在线节点，安装成功后启动并打开 Web。需要 PowerShell 7；首次准备需要联网，详见 [本地安装](scripts/README.md#本地一键安装)。

[开发规范](AGENTS.md) · [文档导航](docs/README.md) · [需求](docs/overview/requirements.md) · [架构](docs/overview/design.md) · [路线图](docs/development/roadmap.md)

源码：[jackgan90/Asterhand](https://github.com/jackgan90/Asterhand)。项目采用 [MIT License](LICENSE)，分发时保留第三方许可与 NOTICE。提交、推送和二进制发布分别执行，发布状态以仓库记录与 Release 为准。
