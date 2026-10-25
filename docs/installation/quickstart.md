# 首版安装与 hello-world 验收

Windows x64 是完整助手目标。支持边界见 [平台矩阵](../testing/platforms.md)，可下载版本以 GitHub Release 为准。下文的 0.1.0-alpha.1 用于演示指定版本，执行时应选择目标仓库实际已发布的版本。

## 1. 在 B 安装

在 Windows PowerShell 5.1 或更高版本中粘贴以下一行，自动从仓库[固定安装入口](https://raw.githubusercontent.com/jackgan90/Asterhand/main/install.ps1) 下载并执行安装器：

```powershell
irm https://raw.githubusercontent.com/jackgan90/Asterhand/main/install.ps1 -ErrorAction Stop | iex
```

`irm` 是 Invoke-RestMethod 的别名，`iex` 是 Invoke-Expression 的别名；脚本在内存中执行，下载失败立即停止。默认查询 GitHub Latest 稳定版，解析后从同一标签下载清单和 Windows 完整包，按摘要与大小校验。Latest 不包含草稿或预发布版。若仓库尚无稳定版，脚本会明确提示；指定版本或标签时，使用支持参数的一行入口：

```powershell
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/jackgan90/Asterhand/main/install.ps1 -ErrorAction Stop))) -Version 0.1.0-alpha.1
# 自定义标签与应用版本不同，用精确 Release 标签选择。
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/jackgan90/Asterhand/main/install.ps1 -ErrorAction Stop))) -Repository <owner>/<repository> -ReleaseTag <published-tag>
```

Version 也接受 v 前缀；同时提供 Version 与 ReleaseTag 时，清单必须匹配指定应用版本。普通用户不需要 Go、Git、npm、PowerShell 7 或预装 Node。私有仓库和未公开草稿不支持匿名安装。

脚本还检查 Microsoft VC++ x64 运行库：符合清单要求时复用，缺失或较旧时自动下载固定版本的 Microsoft 官方安装器，核对摘要和签名后请求 Windows 管理员许可。随后实际加载包内原生模块，确认系统依赖完整。VC++ 与 Tailscale 的安装都不会自动重启 Windows；如提示需要重启，完成重启后再配置节点。

开发者验收当前源码时，双击仓库根目录 [install-local.cmd](../../install-local.cmd)，自动复用指纹匹配且校验通过的正式构建包；没有可复用包时准备锁定依赖并构建，通过正式安装器安装。包与系统依赖检查成功后自动正常停止本安装节点，安装完成自动启动并打开 Web。首次安装询问桌面图标，覆盖安装保留实际图标状态；未传 DataDir 时沿用已有安装记录，首次使用标准数据目录。需要 PowerShell 7，参数及缓存见 [脚本指南](../../scripts/README.md#本地一键安装)。正常退出会取消本机在途任务，重启后按既有规则人工对账。

需要手动使用已有本地包时，先下载固定入口脚本，并从同一构建取得 `release-manifest.json` 和对应 ZIP，例如 `asterhand-0.1.0-alpha.1-windows-amd64.zip`。

在该目录打开 PowerShell，执行本地包安装：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -PackagePath .\asterhand-0.1.0-alpha.1-windows-amd64.zip
```

相对路径按当前 PowerShell FileSystem 目录解析；也可向 PackagePath 和 ManifestPath 传绝对路径。默认清单从包所在目录读取，本地包不查询 Latest，版本由清单推导。ZIP 与 manifest 必须来自同一构建；入口脚本可以使用当前兼容版本。来自其他发布仓库的包同时传入对应 Repository；显式版本或标签与清单冲突时拒绝安装。

在线入口直接执行从官方 HTTPS 地址取得的脚本，不修改用户或整机执行策略。本地文件命令只为本次 PowerShell 进程设置 Bypass。运行前确认地址来自官方仓库；也可手动下载审阅后执行，Release 附带的脚本副本可核对该 Release 的 SHA256SUMS。受管理设备仍按组织要求执行。策略说明见 [Microsoft 文档](https://learn.microsoft.com/powershell/module/microsoft.powershell.core/about/about_execution_policies)。

首次交互安装会询问是否创建桌面图标，直接回车选择创建。也可显式传入 `-DesktopShortcut Create` 或 `-DesktopShortcut Skip`；非交互首次安装默认跳过。安装完成后脚本退出，输出固定 `AsterhandLauncher.exe` 与 `start.ps1` 入口。此时组件已安装，节点尚未启动，身份、模型凭据、工作目录与配对尚未配置。

默认程序目录 `%LOCALAPPDATA%\Programs\Asterhand`，首次数据目录 `%APPDATA%\Asterhand`。覆盖安装未显式传 DataDir 时沿用 installation.json 的数据目录，保留节点身份、配对、key 和数据库。可用 `-InstallDir`、`-DataDir` 指定独立目录，二者不得互相包含。桌面快捷方式创建在当前用户桌面，支持中文、空格和重定向桌面。

正式安装器也支持 `-StopRunning` 和 `-StartAfterInstall`：前者在包及依赖检查通过后验证节点归属并请求正常退出，后者在激活成功、锁释放后复用标准启动器启动。未知进程、身份不符或停止超时会中止覆盖；默认仍要求先手动退出在线节点。`-NoStart` 为兼容选项，与 `-StartAfterInstall` 互斥。

`-DesktopShortcut Auto` 是默认值；升级时按实际存在的本安装图标保留，手动删除后不会重新创建。`Keep` 也保留实际状态；`Create` 可恢复图标，`Skip` 移除本安装的桌面图标。同名其他安装的快捷方式保留并在需要创建时提示冲突。旧包未包含启动器时继续使用 `start.ps1`，显式 `Create` 会提示改选新包。

## 2. 网络与初次配置

安装器检测 Tailscale。缺失时展示系统网络服务安装范围，请求 Windows 管理员许可并安装锁定的官方 MSI；已有 Tailscale 会复用，不静默升级。安装过程不执行登录或修改防火墙。若 Windows 要求重启，先重启再开始配置。

后续配置：从 Tailscale 自身入口登录，让 A、B 加入同一 Tailnet；双击 Asterhand 桌面图标或安装目录的 `AsterhandLauncher.exe` 打开本地设置页。也可运行 `start.ps1`。节点启动后在 Windows 系统托盘显示图标，可能位于隐藏图标区域。

网页中填写：

1. 节点名称，例如 `Windows B`。
2. 授权目录，例如 `D:\AsterhandWorkspace`。不存在时自动创建；不要使用凭据、应用数据或运行时目录。
3. 点击“使用 Tailscale 地址”，同时填写 `100.x.x.x:17433` 节点监听地址和 `https://100.x.x.x:17433` 公布地址。
4. DeepSeek 模型和 API key。key 在对应机器的设置表单输入，Windows 使用当前用户 DPAPI 加密。
5. 保存。若显示监听配置需要重启，先点击“停止本机节点”，等待托盘消失后再双击 Asterhand 图标或运行 start.ps1。
6. 点击“验证模型与工具”。这会消耗一次小型模型调用；通过后显示助手就绪。只修改节点名称或网络地址会保留既有模型验证结果。

本机页面使用 `127.0.0.1:17432`，不向局域网暴露登录入口。用户在 B 本机打开 B 页面可以独立处理任务。跨节点 API 使用 TLS 与带外配对证书；无需浏览器忽略证书告警。

若 Windows 防火墙阻止 TCP 17433，运行安装目录下的 `configure-firewall.ps1`；它明确询问后只开放 Tailscale 地址范围到 Asterhand 程序的该端口。升级后程序路径改变时重新运行该脚本。Tailnet ACL / grants 也要允许 A↔B。不做公网端口转发。

## 已有节点升级

在任务完成后，打开本机设置，记下页面显示的数据目录，并点击“停止本机节点”，或从托盘菜单退出。等待托盘消失后运行固定入口 install.ps1 安装最新稳定版，或用 Version / ReleaseTag 指定目标；也可使用当前脚本及同一构建的清单与 ZIP 本地安装。默认安装会拒绝激活在线节点；显式 StopRunning 可正常停止节点，本地双击入口已启用该选项。建议从固定入口更新脚本，以取得最新安装兼容性修复。覆盖安装默认沿用安装记录中的原数据目录，包括自定义 DataDir；仅更换数据目录时才显式传入新值。

升级后双击原桌面图标或运行安装目录 start.ps1 打开原节点，确认原名称、授权目录、配对和历史任务仍在。快捷方式指向固定启动器，升级无需手工修改目标。点击“使用 Tailscale 地址”并保存；出现重启提示时再停止并启动一次。运行安装目录 configure-firewall.ps1 更新规则。新程序首次启动需要重新验证一次模型与工具。

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

- 启动：双击桌面图标、安装目录 `AsterhandLauncher.exe`，或运行 `start.ps1`。重复点击和并发启动复用同一节点；脚本加 `-NoBrowser` 或启动器加 `--no-browser` 可只启动节点。
- 打开页面：托盘双击或选择“打开 Asterhand”，也可在当前安装版本目录运行 `asterhand.exe open --home <数据目录>`。登录过期时同样用这些入口恢复；程序输出不展示带登录 token 的地址。
- 保持运行：关闭浏览器或锁屏后节点继续执行。托盘显示节点名称、助手就绪/待配置/正在退出状态，菜单可打开日志目录。
- 停止：托盘菜单“退出 Asterhand”或本机设置页“停止本机节点”。有本机任务或模型验证在运行时先确认；取消确认会继续运行。确认后停止接收、关闭服务并完成持久化，再移除托盘。本机在途任务重启后进入待核对，保留已有文件；远端已接收任务继续，队列按原期限处理，Tailscale 保持运行。
- 可选登录后启动：安装时显式加 `-LoginAtStartup`；该选择与桌面图标独立，登录后启动不弹浏览器。升级保留已启用入口，移除当前用户 Windows Startup 中的 Asterhand 快捷方式即可关闭。
- 诊断：安装版本目录中运行 `asterhand.exe doctor --home <数据目录>`；不输出 key。
- 启动失败：按提示查看数据目录的 `launcher.log`、`error.log` 和 `startup.log`，日志以追加方式保留。启动互斥量和健康检查各等待最多 20 秒；超时保留正在启动的进程，先检查日志与托盘，再重试同一入口。
- 修复：`repair` 重新发现并检查同包运行时；缺少组件时重新运行安装器，不删除数据。
- 仅 LAN 或隔离安装测试：`-SkipTailscale`；默认安装即不启动，`-NoStart` 保留兼容。跳过 Tailscale 的检查不覆盖系统网络组件安装。
- 未知执行状态显示“需要核对”。检查文件、执行记录后在 B 本机填写证据并关闭；该操作记录人工处置，不伪造成功。

其他验收：A 服务退出后 B 继续、问题回答去重、执行中重启的对账，按 [人工验收](../testing/acceptance.md) 的 V04—V06；桌面、托盘、升级和 Windows 会话行为按 [桌面验证说明](../development/features/windows-desktop-lifecycle/validation.md)。当前 Release 只包含 Windows x64；Linux/macOS 尚未接入发布。
