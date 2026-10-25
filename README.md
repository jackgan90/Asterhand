# Asterhand · 星伴

A personal agent system across your devices.

Asterhand 让自己的设备通过自然语言协作。A 提交任务，B 用自己的 OpenClaw、DeepSeek 凭据和授权目录执行；任务与结果保存在 B，A 下线后 B 已接收的任务继续运行。

## 能力与支持范围

- Windows x64 完整节点：Go 持久任务服务、SQLite、OpenClaw + DeepSeek 和统一 Web。
- 授权目录内创建和读取 UTF-8 文本，提供实际路径、文件大小、内容摘要和预览；创建操作拒绝覆盖已有文件。
- 节点配对、远端任务、普通问答、取消、断线补读和重启后的人工对账。
- 每个节点使用自己的凭据和授权目录；Windows 使用当前用户 DPAPI 保护秘密，节点间使用 TLS 与固定证书。
- 首版每节点一个执行槽、一个授权目录，文件最大 64 KiB；删除文件、任意 shell 和具体 coding harness 尚未提供。

版本与支持边界见 [平台矩阵](docs/testing/platforms.md)。Linux 基础包仅完成交叉编译，macOS 和手机远程访问尚未验证。Web 默认只绑定本机 loopback，在各自设备打开。

## 安装与使用

从同一构建取得安装脚本、发布清单和 Windows ZIP，放入同一目录。发布前使用构建产物，公开版本从 [GitHub Releases](https://github.com/jackgan90/Asterhand/releases) 获取。安装方式：

~~~powershell
.\install.ps1 -PackagePath .\asterhand-0.1.0-alpha.1-windows-amd64.zip
~~~

安装包包含 Node.js、OpenClaw 和模型插件。A、B 加入同一 Tailscale 网络，在各自设置页配置凭据、授权目录并验证，再用 B 生成的配对码在 A 配对。

在 A 选择 B，发送“在授权目录新建 hello-world.txt，内容精确为 hello world，不添加换行或 BOM”。成功后检查 B 的实际文件及页面显示的 11 字节结果。完整步骤见 [安装、升级与诊断](docs/installation/quickstart.md)。

## 开发

工具链版本以 [依赖清单](packaging/dependencies.json) 为准。运行 `go test ./...` 和 `go vet ./...`；完整构建、依赖升级和验证入口见 [脚本指南](scripts/README.md)、[依赖维护](packaging/upgrade.md) 和 [测试指南](tests/README.md)。

[开发规范](AGENTS.md) · [文档导航](docs/README.md) · [需求](docs/overview/requirements.md) · [架构](docs/overview/design.md) · [路线图](docs/development/roadmap.md)

源码：[jackgan90/Asterhand](https://github.com/jackgan90/Asterhand)。项目采用 [MIT License](LICENSE)，分发时保留第三方许可与 NOTICE。提交、推送和二进制发布分别执行，发布状态以仓库记录与 Release 为准。
