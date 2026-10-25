# 安装与环境设计

## 快捷输入宿主

Windows 快捷输入由节点内独立 STA 宿主承载，热键注册归现有托盘 OS 线程；控制器负责本地配置事务、录制租约与故障降级。宿主保持 CGO_ENABLED=0，使用固定 Go WebView2 loader/COM bridge 与经官方 SDK 校验的窄接口，go.mod/go.sum 锁定版本与摘要，构建保留 MIT/ISC 许可证。Evergreen Runtime 检测实际版本和最低 API 能力，安装材料固定来源/版本/摘要；补齐失败保留在线节点，运行中更新不重启任务节点。依赖与分发边界见[宿主依赖说明](../../packaging/webview2.md)。

快捷键、主题保存在节点数据目录的独立 desktop-settings.json，profile 为同目录的专用受保护子目录；程序升级不复制真实 Edge profile，不卸载共享 Runtime。标准权限交互会话启用面板，管理员/非交互环境和依赖失败报告具体降级状态，现有 Web、托盘和任务继续。

范围：Windows 安装、启动、诊断与构建；使用 schema 2 包描述。验证方法见 [安装路径](../development/features/installer-path-resolution/plan.md) 与 [公共包契约](../development/features/provider-neutral-flows/validation.md)。

## 发布与安装边界

仓库根目录 install.ps1 是独立固定引导入口，默认解析 GitHub Latest 稳定 Release；Version 指定应用版本，ReleaseTag 指定精确标签。解析只执行一次，随后从固定标签下载 release-manifest.json 和完整平台包，检查摘要、大小、版本及 bundle-check，补齐 Microsoft VC++ x64 运行库并加载声明的原生模块，再安装缺失的 Tailscale。成功后激活固定 AsterhandLauncher.exe、start.ps1 和 installation.json，并按用户选择处理快捷方式；默认不启动节点，不登录 Tailscale、不读取模型凭据。setup/start 属于安装后的节点配置入口。doctor/repair 继续由 Go 实现。

README 提供 `irm <官方固定入口> -ErrorAction Stop | iex` 一行命令，下载失败立即停止，成功后在内存执行同一 install.ps1，不创建临时脚本文件或改变执行策略。指定版本等参数时用 ScriptBlock.Create 编译同一下载内容，再通过调用运算符传参，保留交互提示与安装契约。本地 install-local.cmd 通过 PowerShell 7 比对标准 build.ps1 登记的源码指纹、清单和包摘要/大小；一致则复用已构建包，否则准备并缓存清单锁定的工具与依赖并构建。指纹包含源码、脚本、依赖描述/锁、图标及入包指南与许可证；构建中变更时拒绝登记。构建记录原子写入忽略的 out/latest-build.json。安装调用同一 install.ps1 的本地包模式，启用 StopRunning 和 StartAfterInstall；没有独立解压、拷贝或激活流程。开发入口和正式入口均使用 DesktopShortcut Auto；首次询问，升级保留实际图标状态。见 [本地安装计划](../development/features/local-install-entry/plan.md)。

默认入口不含应用版本常量，未来 Linux/macOS 固定脚本遵循相同版本选择契约。没有稳定版时明确失败并提示指定版本，不自动使用预发布版。Version 支持带或不带 v 前缀；自定义 ReleaseTag 从清单推导应用版本，同时指定 Version 时检查二者一致。本地包或本地清单优先按其内容推导版本，不查询 Latest；只提供清单时从清单标签或 v<version> 获取包。清单包含仓库/标签元数据时必须与所选坐标一致。决策见 [固定安装入口](adr/003-stable-installer-entry.md)。

scripts/release.ps1 是维护者发布入口，要求 Windows PowerShell 7、Git 和已登录的 GitHub CLI。构建用 Go 和运行用 Node/npm 依赖由固定清单自动准备。平台注册表 packaging/release-targets.json 通过 builder/installer 路径声明扩展点，只启用 Windows x64；Linux/macOS 需实现原生构建和安装检查后接入。

发布顺序为依赖准备、平台构建、安装回归、附件摘要生成、GitHub 草稿上传、远端逐项校验、显式发布。默认 Draft；Prerelease 与 Stable 为显式模式。重试只接受同名同内容附件，拒绝覆盖公开版本。源码提交、源码摘要、平台支持边界与验证报告随发布保存。源文件变化后重新准备输出，避免混用旧附件。

普通安装用户无需 Git、Go、npm 或 PowerShell 7。前端静态资源内嵌，完整运行时和第三方许可随平台包分发。发布者登录只用于上传，公开安装下载无需 GitHub 账户。设计决策见 [安装配置分离](adr/002-release-and-install-boundary.md)。
## Windows 桌面入口与生命周期

固定安装根目录提供 AsterhandLauncher.exe，读取 installation.json 中当前版本 executable 和 data，以无控制台方式启动节点并等待健康检查。可执行文件必须位于该安装的 versions 目录，程序与数据目录独立，路径检查解析实际目录别名。已有节点复用；读取本机登录 token 前先核对 loopback health 的节点 ID 和进程身份，禁止 HTTP 重定向。CLI 的 status/open 使用相同只读探测，不初始化数据或争抢运行中节点的服务锁。

启动阶段按规范化数据目录使用命名互斥量串行化，等待锁与等待节点就绪各最多 20 秒。安装器与启动器先解析实际目录，再统一调用 Windows LCMapStringEx 的 invariant locale + LCMAP_LOWERCASE，以 UTF-8 编码求 SHA-256；不使用各语言运行时的大小写表，确保 Unicode 路径与目录别名使用同一锁。launch-state.json 保存早期子进程 PID、创建时间和程序路径，覆盖尚未创建服务锁的启动窗口；超时、未知身份或服务锁占用时保留现场并提示日志位置，不强杀或追加实例。节点 stdout/stderr 分别追加到数据目录的 startup.log/error.log，启动器错误追加到 launcher.log；无法安全使用数据目录时写入安装目录。标准输出不包含带登录 token 的地址。start.ps1 转调启动器并保留 -NoBrowser，原生入口支持 --no-browser，CLI 仍支持直接启动与诊断。

节点进程在 Windows 当前用户交互桌面拥有原生托盘；图标与桌面快捷方式选择独立。双击打开本机 Web，菜单显示节点名称及助手就绪/待配置/正在退出状态，提供打开 Web、打开日志目录和退出。Explorer 重建后重注册图标，DPI 变化时加载相应尺寸；Windows 可将托盘折叠到隐藏区域。托盘保留到服务收尾完成，关闭浏览器与锁屏不退出节点。

安装参数 -DesktopShortcut 接受 Auto、Create、Skip、Keep。Auto 首次交互安装询问并默认创建，非交互首次安装跳过；升级 Auto 与 Keep 按本安装实际存在的桌面快捷方式保留，用户手动删除后不会复活。Create 显式创建，Skip 只移除属于本安装的快捷方式。同名外部快捷方式保留；需要创建却遇到冲突时明确报错。快捷方式位于当前用户桌面 Known Folder，兼容重定向和 Unicode，目标为稳定启动器。

-LoginAtStartup 独立启用当前用户 Startup 快捷方式，升级保留已启用的入口；启动参数 --no-browser 避免登录时弹浏览器。桌面选择不改变登录后启动。移除对应 Startup 快捷方式即可关闭。所选旧包没有原生启动器时继续使用兼容 start.ps1，拒绝 DesktopShortcut Create，不生成不存在的启动器目标。安装默认不启动节点，不修改 Tailscale 登录或防火墙。

应用采用星核托举标识，托盘使用简化轮廓。透明 PNG 为图形源，构建工具生成多尺寸 ICO 并将应用图标嵌入节点和启动器。Windows API 使用现有 x/sys/windows，资源加工使用 Windows 自带 API，无额外运行时或 CGO。取舍见 [桌面入口 ADR](adr/004-desktop-launcher-and-tray.md)，验证见 [功能验证](../development/features/windows-desktop-lifecycle/validation.md)。

start.ps1 只等待短生命周期启动器进程完成，最多 45 秒；不用 Start-Process -Wait 等待包含后台节点的整个进程树。启动器成功返回后，节点继续运行，安装或启动脚本正常结束；超时保留节点现场并提示日志。

## 能力与依赖

| 能力 | 首版处理 |
|---|---|
| 基础节点 | 服务、Web、本地状态、身份、配对、诊断 |
| 完整助手节点 | 基础节点 + 已验证 OpenClaw + 必要运行时 + DeepSeek 配置 |
| 文件工具 | 首版随对应组件提供，限制授权目录 |
| Everything | 可选后续增量，非首版安装和验收前置 |
| Coding harness | 后置，不安装或要求任何 coding harness；运行时自身依赖单独验证 |
| 项目编译工具 | 后续按项目准备，独立于节点安装 |

当前发布目标为 Windows 完整助手节点；Linux/macOS 为后续平台。开发本项目的工具链另行记录，不能要求普通用户安装全部开发依赖。

## 用户流程

1. 粘贴 PowerShell 引导命令下载并执行固定 install.ps1；默认安装最新稳定 Release，或通过参数选择已发布版本。仅发布源码而没有稳定 Release 时，默认安装明确报错。
2. B 执行脚本，查看待安装版本和范围，自动补齐依赖。
3. 打开本地设置页，配置节点名、DeepSeek key、已验证模型、授权目录。
4. 运行小型模型和无副作用工具检查，确认助手就绪。
5. 启动 B，展示可达地址与一次性配对码。
6. A 已运行兼容版本，在 Web 添加 B 并完成信任建立。
7. A 选择 B，以自然语言创建文件；B 本地可直接使用助手。

后续只需启动节点并使用，配对信息保留。A、B 的 key 各自本地保存，配对不复制。用户可选择登录后自动启动。

## 可靠安装

- 使用版本锁定清单：组件、OS/架构、来源、版本、下载大小和摘要、许可证、安装方式。
- 下载校验失败立即停止；TLS 与摘要只能在清单来源可信时证明完整性，发布签名策略在首次发布前明确。
- 首选应用专属目录和用户权限安装；不静默升级用户已有的全局开发工具。
- 安装步骤记录可恢复状态；重复执行不重置身份、凭据或数据库，失败后可从已完成步骤继续。
- 激活前检查组件健康。包更新回退与数据库迁移分开处理，不能只换回二进制就宣称数据可回退。
- 网络不可用、代理、权限不足、架构不支持、空间不足应给出具体失败步骤和诊断路径。
- 系统包、管理员操作、防火墙、开机启动或重启需求单独说明；WSL 可选，不作为 Windows 完整节点的隐式前置。
- 修复动作可审查，只作用于明确归属本应用的资源；默认不删除任务数据、不重置 key。

## 发布门槛

同一 Release 包含脚本、平台包、版本/校验清单、说明与已知限制。精确 Windows / Linux 架构和最低版本以 [平台矩阵](../testing/platforms.md) 为准；未实测的平台明确标注。

源码仓库为 [jackgan90/Asterhand](https://github.com/jackgan90/Asterhand)，项目采用 [MIT License](../../LICENSE)。依赖再分发、引导脚本、数据路径、默认端口和证书规则由本文定义，精确版本见依赖清单。本地候选安装步骤见 quickstart.md；远端 Release 未发布时只使用本地包模式。

## 安装契约

Windows x64 包为完整自包含目录，PowerShell 5.1+ 校验 manifest schema/version、zip SHA-256 和大小，安全解压到 staging，检查 Asterhand/Node 版本后激活不可变版本目录。没有 Go/npm/Git 安装前置；第三方模块和许可随包保留。当前包未 Authenticode 签名，可信分发依赖官方仓库来源和同一 Release 清单，不宣称抵抗发布账户被攻陷。

默认程序目录 LocalAppData/Programs/Asterhand，数据目录 AppData/Asterhand。桌面快捷方式、固定启动器和 start.ps1 复用同一启动流程；命名互斥量协调启动，节点用 service.lock 防止双实例，已有运行节点直接打开。setup/start 初始化相同服务和设置页；doctor 可在节点运行时读取诊断，repair 重新发现同包运行时，缺文件时要求重装。

安装器通过 PowerShell SessionState 路径解析接口，将 PackagePath、ManifestPath、InstallDir、DataDir 在入口统一转换为当前 FileSystem 位置下的绝对路径；允许尚未创建的目标目录，拒绝其他 provider。不得直接使用单参数 .NET GetFullPath 解释用户相对路径，也不修改全进程 CurrentDirectory。缺省清单从已解析的本地包目录推导。专项 PS5.1 回归强制 PowerShell 位置与进程目录不同，使用真实包验证新装和重复安装。

发现只更新自动管理的运行时选项；显式 --runtime-options 对象在启动、repair 和升级后保持。旧无来源标记的完整历史包布局迁移到新包，其余非空旧选项保留为显式，规则由 adapter 决定。有效执行配置或候选身份变化使旧 probe 失效；安装升级必须验证“自动旧包路径迁移”与“显式路径保留”两个方向，凭据仍留在原数据目录。旧满容量任务可以启动进入待核对，不会因日志预算阻断 Web 启动。

重复安装保留数据与身份；未显式指定 DataDir 时沿用已有安装记录，首次使用标准目录；版本目录按包摘要区分。数据迁移目前仅 schema 1，遇到较新数据库拒绝打开。程序与数据目录不得互相包含，目录别名按实际路径判断，非应用已有程序目录拒绝写入。激活阶段持有安装锁，以及原数据目录和目标数据目录的启动互斥量与服务锁；默认在线节点拒绝激活。显式 StopRunning 在取得启动互斥量后验证进程程序位于本安装 versions 下、PID/创建时间/路径未变、loopback health 身份匹配，再读取本机登录 token 并通过现有认证及 CSRF 停止 API 请求正常退出，最多等待进程退出 30 秒，随后取得服务锁。HTTP 禁止代理与重定向；未知状态和超时失败，不强杀。稳定入口和 installation.json 通过临时文件替换更新；记录 desktop_shortcut 状态，但升级是否保留图标以实际快捷方式为准。

StartAfterInstall 在整个激活和清理成功、所有锁释放后调用 start.ps1，沿用原生启动器的健康检查与 Web 打开行为；与兼容 NoStart 参数互斥。停止本机在途任务及重启后的人工对账沿用任务域契约。取舍见 [显式安装生命周期](adr/005-explicit-install-lifecycle.md)。

Tailscale 缺失时引导官方 1.104.2 MSI，检查固定摘要及有效供应商签名，再请求管理员安装。安装流程到组件安装完成即结束。之后由用户打开 Tailscale 完成登录。防火墙脚本仅 TCP 17433、当前程序、Tailnet 地址范围；默认不会自行新增规则。-SkipTailscale 用于已有 LAN / 隔离安装试验；安装默认不启动，-NoStart 保留为兼容参数。

Windows 原生 npm 模块需要 Microsoft VC++ v14 x64 运行库。清单固定其版本、不可变供应商 URL、SHA-256 和许可；安装器检查 x64 注册表版本，复用符合要求的版本，缺失或较旧时通过 Microsoft 签名的官方安装器补齐。版本检查后，按 windows_native_modules 中的包内相对路径使用捆绑 Node 实际加载原生模块，失败时不写入激活入口。VC++ 与 Tailscale 系统安装均请求 Windows 管理员许可并禁止自动重启；重启要求明确提示。两者从供应商下载，不重新分发。

Linux install.sh 保留为实验基础节点源码，不进入当前 Release 附件。Linux/macOS 安装尚未实现正式发布接入。

## 依赖升级与候选验证

清单采用 schema 2：components 描述包管理器组件，bundle.targets 描述实际打包的 runtime/provider/model/credential_ref 组合，default_target 指定新节点默认选择。公共 bundle-check 遍历目标并交给各适配器检查，不构造固定厂商配置。准备与锁检查遍历组件，核对完整的直接依赖集合；未知包管理器或缺失/重复目标拒绝。首版组件安装技术仍为 npm + Node；将来其他安装技术需相应实现，而不是在公共脚本中增加厂商分支。

packaging/dependencies.json 是 Node、OpenClaw、模型插件、Go 构建器、Microsoft VC++ 运行库及 Tailscale 的唯一版本来源。Go 直接嵌入同一文件；构建和准备脚本直接读取；安装器从经过 SHA-256 校验的 bundle 读取系统依赖元数据，并执行 `asterhand bundle-check --directory ...`。该命令不打开节点数据、不读取凭据、不访问网络；核对内嵌清单摘要、组件存在性、Node 和各插件独立版本。重复使用已有版本目录也重新执行包检查与原生模块加载检查。

`prepare-dependencies.ps1` 只写新的隔离目录。默认按现有锁 npm ci；RefreshLock 根据明确版本重新解析候选 lock，并从 lock 提取来源/integrity 写候选清单。Node archive 必须匹配清单 SHA-256；npm 完整性由 npm ci 校验，生命周期脚本默认禁用。候选清单和 lock 经审查后才替换源码中的对应文件。支持新组件时应补该组件的包布局与验证，不把通用下载器当作兼容性保证。

`verify-dependencies.ps1` 输出机器可读报告：清单/锁一致性、错误清单拒绝、完整 Go tests/vet、插件契约、可选真实模型、可选 Windows 安装/升级。缺 LiveHome/LiveAddress、PackageDirectory 或 PreviousPackageDirectory 时相应检查为 skipped，upgrade_ready 为 false。传入真实节点会消耗模型额度并在授权目录创建唯一测试文件；节点凭据只由其进程读取。

构建将源码摘要写入程序和 release manifest。真实验证核对该摘要；安装测试在独立中文含空格目录运行 Windows PowerShell 5.1，覆盖首次/重复安装、重复启动、运行时 doctor、旧 flat 配置迁移以及身份/凭据字节保留。包激活与数据库回滚分别处理，不承诺自动回滚；停止与安装后启动必须显式选择。

完整命令及更新步骤见 [升级流程](../../packaging/upgrade.md)。验证报告、人工两机验收、提交、推送和发布分别记录。
