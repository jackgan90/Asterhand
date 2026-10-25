# 首版安装与 hello-world 验收

Windows x64 是完整助手目标。支持边界见 [平台矩阵](../testing/platforms.md)，可下载版本以 GitHub Release 为准。下文的 0.1.0-alpha.1 用于演示指定版本，执行时应选择目标仓库实际已发布的版本。

## 1. 在 B 安装

从仓库[固定安装入口](https://raw.githubusercontent.com/jackgan90/Asterhand/main/install.ps1) 下载 `install.ps1`，在下载目录打开 Windows PowerShell 5.1 或更高版本，运行：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\install.ps1
```

默认查询 GitHub Latest 稳定版，解析后从同一标签下载清单和 Windows 完整包，按摘要与大小校验。Latest 不包含草稿或预发布版。若仓库尚无稳定版，脚本会明确提示；指定已公开的版本或标签可安装预发布版：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Version 0.1.0-alpha.1
# 自定义标签与应用版本不同，用精确 Release 标签选择。
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Repository <owner>/<repository> -ReleaseTag <published-tag>
```

Version 也接受 v 前缀；同时提供 Version 与 ReleaseTag 时，清单必须匹配指定应用版本。普通用户不需要 Go、Git、npm、PowerShell 7 或预装 Node。私有仓库和未公开草稿不支持匿名安装。

脚本还检查 Microsoft VC++ x64 运行库：符合清单要求时复用，缺失或较旧时自动下载固定版本的 Microsoft 官方安装器，核对摘要和签名后请求 Windows 管理员许可。随后实际加载包内原生模块，确认系统依赖完整。VC++ 与 Tailscale 的安装都不会自动重启 Windows；如提示需要重启，完成重启后再配置节点。

需要使用本地包时，使用固定入口脚本，并从同一版本发布物取得 `release-manifest.json` 和对应 ZIP，例如 `asterhand-0.1.0-alpha.1-windows-amd64.zip`。

在该目录打开 PowerShell，执行本地包安装：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -PackagePath .\asterhand-0.1.0-alpha.1-windows-amd64.zip
```

相对路径按当前 PowerShell FileSystem 目录解析；也可向 PackagePath 和 ManifestPath 传绝对路径。默认清单从包所在目录读取，本地包不查询 Latest，版本由清单推导。ZIP 与 manifest 必须来自同一构建；入口脚本可以使用当前兼容版本。来自其他发布仓库的包同时传入对应 Repository；显式版本或标签与清单冲突时拒绝安装。

上述命令只为本次 PowerShell 进程设置脚本执行策略，不修改用户或整机策略，适用于默认限制脚本执行的全新 Windows。运行前确认脚本来自官方仓库固定入口；Release 附带的脚本副本可核对该 Release 的 SHA256SUMS。组织组策略优先于进程参数；受管理设备仍按组织要求执行。策略说明见 [Microsoft 文档](https://learn.microsoft.com/powershell/module/microsoft.powershell.core/about/about_execution_policies)。

安装完成后脚本退出，输出安装目录内的 `start.ps1` 入口。此时组件已安装；节点身份、模型凭据、工作目录与配对尚未配置。

默认程序目录 `%LOCALAPPDATA%\Programs\Asterhand`，数据目录 `%APPDATA%\Asterhand`。重复安装保留节点身份、配对、key 和数据库。可用 `-InstallDir`、`-DataDir` 指定独立目录；程序和数据分开。

## 2. 网络与初次配置

安装器检测 Tailscale。缺失时展示系统网络服务安装范围，请求 Windows 管理员许可并安装锁定的官方 MSI；已有 Tailscale 会复用，不静默升级。安装过程不执行登录或修改防火墙。若 Windows 要求重启，先重启再开始配置。

后续配置：从 Tailscale 自身入口登录，让 A、B 加入同一 Tailnet；运行安装目录的 `start.ps1` 打开本地设置页。

网页中填写：

1. 节点名称，例如 `Windows B`。
2. 授权目录，例如 `D:\AsterhandWorkspace`。不存在时自动创建；不要使用凭据、应用数据或运行时目录。
3. 点击“使用 Tailscale 地址”，同时填写 `100.x.x.x:17433` 节点监听地址和 `https://100.x.x.x:17433` 公布地址。
4. DeepSeek 模型和 API key。key 在对应机器的设置表单输入，Windows 使用当前用户 DPAPI 加密。
5. 保存。若显示监听配置需要重启，先点击“停止本机节点”，再运行安装目录的 start.ps1。
6. 点击“验证模型与工具”。这会消耗一次小型模型调用；通过后显示助手就绪。只修改节点名称或网络地址会保留既有模型验证结果。

本机页面使用 `127.0.0.1:17432`，不向局域网暴露登录入口。用户在 B 本机打开 B 页面可以独立处理任务。跨节点 API 使用 TLS 与带外配对证书；无需浏览器忽略证书告警。

若 Windows 防火墙阻止 TCP 17433，运行安装目录下的 `configure-firewall.ps1`；它明确询问后只开放 Tailscale 地址范围到 Asterhand 程序的该端口。升级后程序路径改变时重新运行该脚本。Tailnet ACL / grants 也要允许 A↔B。不做公网端口转发。

## 已有节点升级

在任务完成后，打开本机设置，记下页面显示的数据目录，并点击“停止本机节点”。运行固定入口 install.ps1 安装最新稳定版，或用 Version / ReleaseTag 指定目标；也可使用当前脚本及同一构建的清单与 ZIP 本地安装。建议从固定入口更新脚本，以取得最新安装兼容性修复。默认安装沿用原数据；曾指定自定义 DataDir 的节点必须继续传入同一目录。

升级后运行安装目录 start.ps1 打开原节点，确认原名称、授权目录、配对和历史任务仍在。点击“使用 Tailscale 地址”并保存；出现重启提示时再停止并启动一次。运行安装目录 configure-firewall.ps1 更新规则。新程序首次启动需要重新验证一次模型与工具。

两端均升级后，等待 20—40 秒并刷新。双方节点列表应显示对端当前名称、在线状态及真实 Tailscale 地址。既有配对会通过已固定的证书和节点身份回查新地址，无需重新配对；至少一个方向可达时自动修复另一个方向的旧地址。探测允许 10 秒完成冷连接与身份校验，超时会显示具体错误并在后续周期重试。

验证时使用新文件名（例如 hello-world-on-B.txt）：从 A 指定 B 创建精确的 hello world，确认任务立即排在列表顶部，完成后刷新仍在顶部且只有一张任务卡。然后从 B 指定 A 创建 hello-world-on-A.txt，检查两台机器各自的实际文件。修改任一节点名称后，等待 20—40 秒检查另一端显示新名称，本机应始终保持模型就绪。

## 3. 在 A 配对 B

A 以同样步骤安装、配置并验证独立助手。在 B 点击“配对另一台设备”→“在本机生成配对码”，把完整配对码带外复制到 A 的配对表单。

配对码 10 分钟有效，包含证书与一次性凭据，只交给自己的设备。成功后 A 的节点列表显示 B 与授权目录。配对不传送 DeepSeek key。

## 4. 从 A 创建 B 上的文件

在 A 页面选择 `Windows B` 和 B 的授权目录，发送：

> 让节点 B 在授权目录下新建 hello-world.txt，内容精确为 hello world，不添加换行或 BOM。

等待 B 已接收、执行中、完成并验证。页面显示 B 的实际文件路径、11 字节大小、内容和 SHA-256。在 B 的资源管理器打开文件检查；A 的授权目录不应出现该文件。已存在同名文件会拒绝覆盖，请换新文件名或使用新的验收目录。

## 启停与诊断

- 启动：安装目录 `start.ps1`。已经运行时打开现有节点，不创建第二个服务。
- 停止：本机设置页“停止本机节点”。在途任务重启后按记录核对，不自动重跑。
- 可选登录后启动：安装时显式加 `-LoginAtStartup`；移除 Windows Startup 中的 Asterhand 快捷方式即可关闭。
- 诊断：安装版本目录中运行 `asterhand.exe doctor --home <数据目录>`；不输出 key。
- 修复：`repair` 重新发现并检查同包运行时；缺少组件时重新运行安装器，不删除数据。
- 仅 LAN 或隔离安装测试：`-SkipTailscale`；默认安装即不启动，`-NoStart` 保留兼容。跳过 Tailscale 的检查不覆盖系统网络组件安装。
- 未知执行状态显示“需要核对”。检查文件、执行记录后在 B 本机填写证据并关闭；该操作记录人工处置，不伪造成功。

其他验收：A 服务退出后 B 继续、问题回答去重、执行中重启的对账，按 [人工验收](../testing/acceptance.md) 的 V04—V06。当前 Release 只包含 Windows x64；Linux/macOS 尚未接入发布。
