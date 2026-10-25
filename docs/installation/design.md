# 安装与环境设计

范围：Windows 安装、启动、诊断与构建；使用 schema 2 包描述。验证方法见 [安装路径](../development/features/installer-path-resolution/plan.md) 与 [公共包契约](../development/features/provider-neutral-flows/validation.md)。

## 两层引导

`install.ps1`（Windows）和 `install.sh`（Linux）只负责识别平台、取得对应 Asterhand 程序、校验后调用环境准备入口。

计划命令：`asterhand setup` 初始化和准备环境；`asterhand doctor` 检查环境、认证、网络和能力；`asterhand repair` 修复已识别的问题。环境管理逻辑由 Go 实现，CLI 与 Web 复用，避免脚本分叉。

正常运行节点不安装 Go。前端静态资源内嵌发布，用户不需要为 Web 编译前端。OpenClaw 可能需要的运行时依赖由版本清单描述，不能在未验证前承诺全栈为一个二进制。

## 能力与依赖

| 能力 | 首版处理 |
|---|---|
| 基础节点 | 服务、Web、本地状态、身份、配对、诊断 |
| 完整助手节点 | 基础节点 + 已验证 OpenClaw + 必要运行时 + DeepSeek 配置 |
| 文件工具 | 首版随对应组件提供，限制授权目录 |
| Everything | 可选后续增量，非首版安装和验收前置 |
| Coding harness | 后置，不安装或要求任何 coding harness；运行时自身依赖单独验证 |
| 项目编译工具 | 后续按项目准备，独立于节点安装 |

低配 Linux 可选择基础节点。两台 Windows 选择完整助手节点。开发本项目的工具链另行记录，不能要求普通用户安装全部开发依赖。

## 用户流程

1. 从指定 GitHub Release 下载 install.ps1；仅发布源码还不能执行安装；正式 Release 可用前不提供虚构安装下载地址。
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

默认程序目录 LocalAppData/Programs/Asterhand，数据目录 AppData/Asterhand。start.ps1 隐藏启动、确认 health，同数据目录以文件锁防止双实例；已有运行节点直接打开。setup/start 初始化相同服务和设置页；doctor 可在节点运行时读取诊断，repair 重新发现同包运行时，缺文件时要求重装。

安装器通过 PowerShell SessionState 路径解析接口，将 PackagePath、ManifestPath、InstallDir、DataDir 在入口统一转换为当前 FileSystem 位置下的绝对路径；允许尚未创建的目标目录，拒绝其他 provider。不得直接使用单参数 .NET GetFullPath 解释用户相对路径，也不修改全进程 CurrentDirectory。缺省清单从已解析的本地包目录推导。专项 PS5.1 回归强制 PowerShell 位置与进程目录不同，使用真实包验证新装和重复安装。

发现只更新自动管理的运行时选项；显式 --runtime-options 对象在启动、repair 和升级后保持。旧无来源标记的完整历史包布局迁移到新包，其余非空旧选项保留为显式，规则由 adapter 决定。有效执行配置或候选身份变化使旧 probe 失效；安装升级必须验证“自动旧包路径迁移”与“显式路径保留”两个方向，凭据仍留在原数据目录。旧满容量任务可以启动进入待核对，不会因日志预算阻断 Web 启动。

重复安装保留数据与身份；版本目录按包摘要区分。数据迁移目前仅 schema 1，遇到较新数据库拒绝打开。可选登录后启动用当前用户 Startup 快捷方式。程序/数据必须独立，非应用已有程序目录拒绝写入。

Tailscale 缺失时引导官方 1.104.2 MSI，检查固定摘要及有效供应商签名，再请求管理员安装。登录由 Tailscale 自身完成。防火墙脚本仅 TCP 17433、当前程序、Tailnet 地址范围；默认不会自行新增规则。-SkipTailscale 用于已有 LAN / 隔离安装试验，-NoStart 只安装。

Linux install.sh 为实验基础节点入口，需要 curl、sha256sum、tar、python3，尚无真实 Linux 安装证据，不作为完整助手发布承诺。

## 依赖升级与候选验证

清单采用 schema 2：components 描述包管理器组件，bundle.targets 描述实际打包的 runtime/provider/model/credential_ref 组合，default_target 指定新节点默认选择。公共 bundle-check 遍历目标并交给各适配器检查，不构造固定厂商配置。准备与锁检查遍历组件，核对完整的直接依赖集合；未知包管理器或缺失/重复目标拒绝。首版组件安装技术仍为 npm + Node；将来其他安装技术需相应实现，而不是在公共脚本中增加厂商分支。

packaging/dependencies.json 是 Node、OpenClaw、模型插件、Go 构建器及 Tailscale 的唯一版本来源。Go 直接嵌入同一文件；构建和准备脚本直接读取；安装器从已经过 SHA-256 校验的 bundle 读取 Tailscale 元数据，并执行 `asterhand bundle-check --directory ...`。该命令不打开节点数据、不读取凭据、不访问网络；核对内嵌清单摘要、组件存在性、Node 和各插件独立版本。重复使用已有版本目录也重新检查。

`prepare-dependencies.ps1` 只写新的隔离目录。默认按现有锁 npm ci；RefreshLock 根据明确版本重新解析候选 lock，并从 lock 提取来源/integrity 写候选清单。Node archive 必须匹配清单 SHA-256；npm 完整性由 npm ci 校验，生命周期脚本默认禁用。候选清单和 lock 经审查后才替换源码中的对应文件。支持新组件时应补该组件的包布局与验证，不把通用下载器当作兼容性保证。

`verify-dependencies.ps1` 输出机器可读报告：清单/锁一致性、错误清单拒绝、完整 Go tests/vet、插件契约、可选真实模型、可选 Windows 安装/升级。缺 LiveHome/LiveAddress、PackageDirectory 或 PreviousPackageDirectory 时相应检查为 skipped，upgrade_ready 为 false。传入真实节点会消耗模型额度并在授权目录创建唯一测试文件；节点凭据只由其进程读取。

构建将源码摘要写入程序和 release manifest。真实验证核对该摘要；安装测试在独立中文含空格目录运行 Windows PowerShell 5.1，覆盖首次/重复安装、重复启动、运行时 doctor、旧 flat 配置迁移以及身份/凭据字节保留。包激活与数据库回滚是不同问题，不承诺自动回滚，不升级或重启用户运行中的节点。

完整命令及更新步骤见 [升级流程](../../packaging/upgrade.md)。验证报告、人工两机验收、提交、推送和发布分别记录。
