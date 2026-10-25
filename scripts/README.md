# 构建脚本

Go 1.27.3：`go test ./...`、`go vet ./...`、`go build ./cmd/asterhand`。完整包在 Windows PowerShell 7 执行。先按 [依赖维护流程](../packaging/upgrade.md) 生成 prepared.json；从该文件读取依赖目录，Go 使用 PATH 中与清单一致的版本：

```powershell
$prepared = Get-Content .\out\prepared-upgrade\prepared.json -Raw | ConvertFrom-Json
.\scripts\build.ps1 -Go go -NodeDistribution $prepared.node_distribution -RuntimeRoot $prepared.runtime_root -OutputDir .\out\candidate
```

RuntimeRoot 包含 package.json、与 packaging/runtime-lock.json 完全匹配的 package-lock.json 和按锁安装的 node_modules；安装 OpenClaw 和 @openclaw/deepseek-provider 2026.10.7。NodeDistribution 使用依赖清单中已验证摘要的官方 Windows 发行目录。

构建脚本验证版本与锁文件，产生 Windows 完整 ZIP、面向目标仓库的安装入口副本、包含仓库和标签的 release-manifest.json、SHA256SUMS 和操作指南。包内包含节点程序、无控制台 AsterhandLauncher.exe 和 start.ps1；节点与启动器均嵌入应用图标，节点另含托盘图标。脚本默认 Latest，复现安装时显式传 ReleaseTag。当前正式构建不输出 Linux/macOS 安装包。out 下 staging 供人工检查，按需清理自己生成的目录。

prepare-dependencies.ps1（隔离准备）、verify-dependencies.ps1（统一报告）、verify-live.ps1（真实 probe/文件/问答/取消）、test-install.ps1（PowerShell 5.1 安装/升级回归）、dependency-tools.ps1 / test-dependency-tools.ps1（共用清单检查与拒绝测试）。完整命令见 [升级流程](../packaging/upgrade.md)。

schema 2 清单使用通用 components、bundle.targets/default_target；准备和锁检查遍历声明，拒绝未声明的直接依赖及未知安装技术。test-dependency-tools 同时覆盖任意名称的 fixture，安装回归从节点配置读取 provider/runtime/credential_ref，不写死厂商与凭据文件名。

Windows 测试需要当前用户 DPAPI、私有目录和进程权限。受限执行环境导致的 ACL 拒绝记为环境失败，不删掉权限测试绕过。安装回归跳过 Tailscale/防火墙，不替代干净双机验收。

## 本地一键安装

在 Windows 双击根目录 [install-local.cmd](../install-local.cmd)，或在 PowerShell 7 执行 `./scripts/install-local.ps1`。正式 [build.ps1](build.ps1) 成功后原子更新生成目录中的 out/latest-build.json，记录源码指纹与清单路径。入口核对当前指纹、清单和 ZIP 大小/摘要，一致则复用现成包，跳过依赖准备和构建；源码变化、包缺失或损坏时重新构建。指纹覆盖程序、脚本、运行依赖描述、锁、图标、插件、测试和入包安装指南/许可证，构建期间变更会拒绝登记为可复用包。

之后调用根目录 [install.ps1](../install.ps1) 的本地包模式，显式选择 StopRunning 和 StartAfterInstall。构建或包检查失败时在线节点继续运行；正常停止会取消本机在途任务，重启后按既有规则人工对账。

首次使用标准程序目录和数据目录，并询问是否创建桌面图标。覆盖安装未传 DataDir 时沿用 installation.json 的数据目录；DesktopShortcut 默认 Auto，已有本安装图标就保留，没有就保持没有。安装成功后通过标准启动器启动节点并打开 Web；停止失败或未知进程时中止安装，查看错误和节点日志后再重试。

开发入口需要 PowerShell 7；需要重建且 Go 缺失或版本不符时按依赖清单下载并校验工具链。Node 与运行依赖通过现有 prepare-dependencies.ps1 按锁准备，缓存于生成的 out/local-install 目录；清单或锁变化时重新准备，构建前再次校验。新包输出到独立 runs 子目录；只有正式构建登记且匹配当前指纹的包才会复用。可显式提供现有准备记录及编译器：

```powershell
./scripts/install-local.ps1 -PreparedPath ./out/prepared-upgrade/prepared.json -Go go
```

可选参数为 InstallDir、DataDir、PreparedPath、Go、DesktopShortcut 和 SkipTailscale；默认保留正式安装器的依赖检查。首次依赖准备需要联网，常规覆盖复用缓存。入口不提交源码、不上传 Release，也不自动重跑中断任务。

安装生命周期及开发入口验证：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File ./scripts/test-install-lifecycle.ps1 -PackageDirectory ./out/candidate -OutputDirectory ./out/install-lifecycle-check
pwsh.exe -NoProfile -File ./scripts/test-local-install-entry.ps1 -OutputDirectory ./out/local-entry-check
powershell.exe -NoProfile -ExecutionPolicy Bypass -File ./scripts/test-installer-bootstrap.ps1 -OutputDirectory ./out/bootstrap-check
```

生命周期回归需要完整包及已满足清单的 VC++ 运行库，用隔离 Known Folder 替代桌面与 Startup，通过完整安装器和真实基础节点验证在线覆盖、离线重复、数据目录/身份/token/文件与图标状态保留、错误包不中断服务及外部进程拒绝。首次自动启动检查入口调用及锁释放，后续重启实际运行标准启动器，测试抑制浏览器；Tailscale 跳过，不调用模型。开发入口回归使用受控构建和安装边界验证源码变化重建、源码未变复用、损坏包重建、依赖缓存失效、参数传递和构建失败保护，并调用在线引导回归。test-installer-bootstrap.ps1 同时支持 PowerShell 5.1/7，通过受控网络响应验证 README/指南默认命令确为一行、下载和脚本错误传播、版本/仓库/标签参数传递及生产安装器的内存脚本语法；不实际联网或安装。

`test-install-paths.ps1` 在 Windows PowerShell 5.1 中使用真实候选，强制 PowerShell 当前目录与 .NET 进程目录不同，覆盖相对/绝对包、自动/显式清单、中文空格目标目录、重复安装数据保留及非文件 provider 拒绝：

```powershell
powershell.exe -NoProfile -File .\scripts\test-install-paths.ps1 -Installer .\install.ps1 -PackageDirectory .\out\candidate -OutputDirectory .\out\install-path-check
```

这些示例目录是由构建和测试命令生成的输出，由执行者选择；不依赖仓库作者的本机文件。可用 BaselineInstaller 参数提供旧安装器比较路径行为。测试不联网、不启动服务、不调用模型；test-install.ps1 在完整安装检查前自动执行路径回归。

## Windows 桌面与图标验证

`build-icons.ps1` 从 [透明 PNG 源](../internal/desktop/assets/README.md) 生成多尺寸应用/托盘 ICO；修改 PNG 后须先显式运行该脚本并保存更新后的 ICO。`build.ps1` 编译时嵌入现有托盘 ICO，随后调用 `set-executable-icon.ps1`，用 Windows 资源 API 给节点和启动器嵌入现有应用 ICO；不要求额外资源编译器或运行时。`test-icon-resources.ps1` 验证透明度、ICO 帧与可执行文件资源往返，生成用于人工检查的尺寸预览，支持 Windows PowerShell 5.1 和 PowerShell 7：

```powershell
powershell.exe -NoProfile -File .\scripts\test-icon-resources.ps1 -Go go -OutputDirectory .\out\desktop-icons
powershell.exe -NoProfile -File .\scripts\test-installer-desktop.ps1 -OutputDirectory .\out\desktop-installer
```

`test-installer-desktop.ps1` 用隔离目录替代当前用户桌面和 Startup，实际创建并读取 Windows 快捷方式，覆盖 11 组选择、归属、升级、Unicode 路径、旧包和激活互斥边界；不修改真实桌面或系统启动项。

`test-desktop-launch.ps1` 需要已构建的节点与启动器，创建中文含空格的隔离安装，启动真实基础节点但不配置模型、不打开浏览器：

```powershell
powershell.exe -NoProfile -File .\scripts\test-desktop-launch.ps1 -NodeExecutable <candidate-asterhand.exe> -LauncherExecutable <candidate-AsterhandLauncher.exe> -OutputDirectory .\out\desktop-launch
```

该回归检查并发启动、CLI status、脚本复用、日志追加与登录 token 不泄漏、Web 停止后的进程/锁/端口释放、重启数据保留和端口占用失败。输出目录必须新建；完整安装包仍需 test-install.ps1 和 test-install-paths.ps1。托盘可见性、真实 Explorer 重启、DPI、注销及两台 Windows 验收见 [桌面验证说明](../docs/development/features/windows-desktop-lifecycle/validation.md)。

## 统一发布

`release.ps1` 自动准备 Go、Node 和锁定运行依赖，执行构建、Go/Node 契约与 Windows 安装回归，组装完整附件并上传 GitHub。默认 Draft，公开发布显式选择 Prerelease/Stable。需要已登录的 GitHub CLI；无需手工提供 NodeDistribution/RuntimeRoot。命令、草稿恢复和平台接入契约见 [发布指南](../docs/installation/releases.md)。

`release-prepare.ps1` 负责平台描述和源码/附件摘要；`prepare-release-windows.ps1` 负责 Windows 工具链和依赖准备；`release-github.ps1` 负责上传、回读验证与发布。`test-release.ps1` 验证失败保护，`test-release-download.ps1` 验证公开的单脚本安装。

`test-installer-system-dependencies.ps1` 通过模拟边界检查 VC++ 缺失、复用、升级和失败处理。真正安装时会使用固定 Microsoft 安装器并验证原生模块加载；普通开发机回归应预先具有清单要求的 VC++ 版本，避免在工程测试中触发系统安装。Tailscale 与 VC++ 官方安装器在发布准备阶段均下载校验，但不进入 Release 附件。

`test-installer-release-selection.ps1` 在 Windows PowerShell 5.1 验证固定入口的 Latest、指定版本、预发布版、自定义标签、本地清单与失败保护，GitHub 边界使用可控响应。`test-release-download.ps1 -InstallerPath <candidate-install.ps1>` 可使用本地候选入口安装真实公开包；省略 InstallerPath 时下载 Release 附件中的入口。该回归总是显式选择 Tag，以复现同一发布物。
