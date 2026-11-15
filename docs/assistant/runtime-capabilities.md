# 本机工具能力与运行时适配

当前实现：Windows 受限文件服务、受限工作区脚本执行沙箱与正式 OpenClaw 桥已接入；未设置过工具权限时默认开启，已有明确选择保持。人工可在“设置 → 助手”关闭或开启。原生宿主直写组合未通过 G3，现用实现及原因见[实施门槛](../development/features/local-runtime-tools/preflight.md)，第一阶段脚本执行与统一停止设计见[脚本执行计划](../development/features/workspace-script-execution/plan.md)。适用 R03/R04/R23/R27 与 AST-TOOLS、EXEC-TOOLS；实际验证范围分别见对应验证方案。

## 公共边界

Asterhand 负责目标节点、授权、工作区身份、资源预算、公共执行观察、结果引用与恢复；harness 负责原生模型工具循环、上下文和会话正文。Adapter 把公共约束映射到原生配置/工具/事件，必要时使用共享受限业务服务。安全边界不依赖模型遵守提示词。

不统一所有工具名称与参数，不强制原生工具绕行 MCP，不抽象通用 agent 操作系统。能力按实际需要扩展，未知能力默认关闭；模型 provider、harness 和执行环境是不同选择维度。

## 工作区与目录所有权

| 区域 | 所有者与规则 |
|---|---|
| 工作区 Workspace | 节点为本机默认助手分配稳定 ID，映射到现有持久 workspace；多会话共享文件和助手记忆；第一阶段脚本进程亦以此目录作为默认工作与写入根 |
| Runtime state | harness 的会话、索引及内部存储；模型文件工具不可读写，不同 harness 独立 |
| 管理与凭据 | agentd 的策略、配置、桥接程序、认证材料；工作区控制桥令牌通过 `${ASTERHAND_WORKSPACE_TOKEN}` 注入进程环境而不再明文写入 `openclaw.json`，脚本子进程仅继承 OS 与开发工具链显式白名单变量（拒绝未列入的宿主机变量、`ASTERHAND_*`/`OPENCLAW_*`/`DEEPSEEK_*`、带秘密后缀/关键词及含 `user:pass@` 的代理 URL）并启用工作区外规范化路径拦截（不持 `dwShareMode = 0` 文件锁以免自锁控制面与 CLI 停止链路） |
| 版本记录与活动元数据 | 节点受管目录，独立于可写 workspace；模型不能修改或清理 |
| 受管临时目录（`sandbox-temp`） | 位于 `<journal>/sandbox-temp`（独立于 `workspace` 与 `<runtime>/openclaw.json`）；为第一阶段沙箱脚本承接 `TEMP/TMP` 与包管理器缓存（`pip`/`uv`/`npm`/`go`），受独立 `256 MiB / 10,000` 项配额看门狗约束，并在打开服务、开启轮次与轮次结束收尾时安全清理（跳过 reparse point） |

沿用现有 conversations/runtime-<generation>/workspace 的物理目录，增加逻辑工作区身份；不迁移到用户 home、不将整个节点数据目录授权给模型、不把一次升级视为新工作区。凭据、安装或策略变化不改旧会话绑定。更换 harness 时创建新的运行时绑定与默认工作区，旧会话继续归原 adapter，历史迁移另定。

允许 workspace 内的原生人格、偏好、记忆和纯文本文件修改，并纳入版本记录。它们只影响模型上下文，不得改变执行策略、安装/加载代码、注册命令或读取管理目录。首批不自动发现或执行 skill；普通工作区文本可通过 read 读取。可信启动流程从固定运行时包复制缺失的七个原生模板并登记版本，不覆盖已有定制；模型不能读取安装目录。

## 能力描述与授权

公共描述包含工作区 ID、语义能力 ID 列表、supported、authorized、ready、unavailable_reason、policy_revision、busy、enforcement 和带单位的限制。第一阶段 enforcement 为 `restricted_service`（含文件受限服务与内核写沙箱，当底层 `SandboxBackend.Supported()` 为真时额外宣告 `scripts.execute` 并导出 `exec_timeout_seconds`、`exec_output_bytes`、`exec_sessions_per_turn`、`sandbox_temp_bytes` 上限）；取消与恢复遵循下文规则。ready 是本机工作区服务可以接收工具调用，不预先发模型测试；模型就绪仍查询既有会话能力，有效工具策略由每次原生模型输入核对。

当前语义能力包括文件读取/变更/发现、受限工作区脚本执行（`scripts.execute`）、记忆检索、进度、产物读取和版本恢复；复合工具必须满足它全部可能副作用的授权。apply_patch 的移动/删除不能被描述为仅写入。工具缺失、原生策略不可落实或版本不匹配时拒绝该能力，基础聊天保持可用。

策略由受信任节点设置保存。新安装、缺失工具设置的旧配置，以及此前自动保存但没有权限修订的默认关闭配置，都按默认开启处理；已有非空权限修订的明确开启/关闭保持原值。默认策略带稳定修订，人工更改产生新修订；普通设置保存和重启不得覆盖明确选择。默认开启覆盖受限工作区工具与第一阶段受限工作区脚本沙箱；未来第二阶段跨工作区提权与持久化信任规则仍保持显式关闭与独立审批演进，见[默认工具权限决策](adr/011-default-assistant-tools.md)与[ADR-AST-012](adr/012-workspace-script-execution-boundary.md)。CLI/MCP 只能读取能力状态，不能更改内置助手权限。普通恢复是已有授权范围的业务操作，不是权限变更。

每轮固定策略修订。人工更改权限先取得会话配置门禁；有活动、排队、创建中的会话操作或公共文件修改时返回 busy。用户先停止或等待收尾，再重试权限更改；成功前不声称已撤权。保存新修订前关闭旧 Gateway，后续轮次才应用新策略。未知文件操作仍需核对，重新启用权限不会解除该阻塞。

## 首个 OpenClaw 映射

- 持久 Gateway 与 chat.send 路径保持，agents.defaults.workspace 显式指定受管工作区。
- 由可信插件 `asterhand-workspace` 注册十一项无冲突工具：`read_file`、`write_file`、`edit_file`、`apply_file_patch`、`list_dir`、`search_files`、`memory_search`、`memory_get`、`progress_card`、`exec_command`、`process_control`；前九项继续委托公开 `openclaw/plugin-sdk/agent-harness` 与 `openclaw/plugin-sdk/memory-host-search`，无需修改 `node_modules/openclaw`；`exec_command` 与 `process_control` 经认证控制桥（`"exec/start"`、`"exec/poll"` 走 `waitSlots`，`"exec/kill"` 走 `controlSlots`，`"exec/control"` 走 `ioSlots`，令牌通过 `${ASTERHAND_WORKSPACE_TOKEN}` 注入环境，不写入 `openclaw.json`）委托 Go 服务在内核沙箱中执行，不在 Gateway Node 进程内直接 `spawn`。配置层显式开启 `tools.fs.workspaceOnly` 与 `tools.exec.applyPatch.workspaceOnly`，设 `tools.updatePlan: false`，并在 `tools.deny` 禁用内置 `"exec"`、`"process"`、`"code_execution"`、`"skills_read"` 与 `"memory-core"` 原始注册；会话权限覆盖无法解除工作区边界。
- 文件工具采用不绑定特定根目录前缀的通用名称，默认相对路径作用于受管工作区根，服务端资源键按 `(rootID, relPath)` 建模，为后续扩展额外授权目录保留统一工具面。
- 不直接启用 coding/full 全集或 group:plugins，不自动接入用户 MCP/个人配置。原生工具最终 schema 与插件归属由离线预检测试与每次 `llm_input` 钩子双重核对。
- 原生文件实现通过每调用词法绑定的独立 `SandboxFsBridge` 配合服务端资源调度器处理并发、版本记录、原子预算和禁止的路径形式（每调用 `AbortSignal` 仅绑定只读/等待调用，不附加至 `'register'`/`'seal'`/`'cancel'`/`'end'`）；`files_windows.go` 在 `.new` 同卷发布后恢复父目录继承 ACL（失败时标记该操作待核对）；`search_files` 与 `memory_search` 传播每次调用 `AbortSignal`，配合服务端基于 `context.Context` 与 `schedEntry` 取消标志的协作中止检查。
- 前台 `exec_command` 在等待切片窗（`<= min(timeoutMs, yieldMs)`）内声明 `DomainFS("", subtree, X)` + `DomainMemorySources("sources", exact, X)`（`Modifying: true`），与同批文件/记忆工具严格串行；一旦超出 `yieldMs` 转入后台先持久落盘 `<journal>/es_*.json`（`"started"`）并交由独立 `execManager` 托管，当前前台调用正常 `End()` 释放 `DomainFS` 锁，使同轮后续 `read_file`/`list_dir`/`search_files`/`process_control` 及多达 8 个并发脚本会话正常放行，同时 `scheduler` 通过 `exec_active` 屏障有序延迟后台脚本运行期内的受管文件修改（`write_file`/`edit_file`/`apply_file_patch`）直至脚本退出唤醒；`process_control` 仅声明 `DomainExec("control", exact, S)`（`Modifying: false`）。
- promptplugin 将当前有效十一项工具注入补充规则并在 `llm_input` 核对集合与策略修订。可选人格补充失败仍可聊天；权限配置失败必须关闭工具，二者不可共用宽松降级。
- 只启用已审核的 `memory-core` 作为记忆槽位与提示词段落来源，关键词检索；文件枚举严格限制在 `MEMORY.md`、`USER.md` 与 `memory/`，不扫描整个工作区、会话或 home。显式关闭 dreaming、cron、heartbeat、后台学习和 compaction memory flush。
- 显式 `memory_search` 持有 `memory_sources S` 与 `memory_index X`，等待前序记忆写入收尾后在受监督调用身份与排空计数保护下同步刷新本地索引（`manager.sync({force: true})`）再执行查询，禁用脱离工具上下文的后台监听唤醒；普通非记忆工作区文件读写可与其并行。
- `progress_card` 持有当前轮次进度资源 `X`，只表达模型声明的进度，与文件及记忆工具并行，不替代工具结果或成功证据。
- G1—G5 与 S0 公开 SDK 预检通过后锁定正式字段和接口；以上键名是 2026.10.7 实测映射，升级时由离线预检测试拦截漂移。

Hermes 的 read_file/write_file/patch/search_files 可映射相同语义，但只写路径保护不足以满足首批读写双向限制。DeepSeek Harness 的 fs、sandbox、session、interaction 等接口可分别适配；自定义 harness 同样必须证明执行约束。第一批只用异名 fixture 验证边界，不宣称支持其他产品。

## 文件边界、预算与版本

路径验证覆盖绝对/相对路径规范化、祖先与最终文件、junction/symlink、硬链接、UNC/device namespace、NTFS ADS、大小写和尾随点/空格。先检查再普通打开存在竞态，必须使用根目录句柄或经过验证的原生安全 I/O，并测试检查后替换链接。首批不允许 workspace 根本身或父链指向未授权对象；操作前校验真实身份。

首批初始预算（实现时集中定义带单位的领域限制；变更须同步验证）：

| 项目 | 上限或策略 |
|---|---|
| 单个可变更文本文件 | 2 MiB UTF-8；超过拒绝修改，读取可有界分页 |
| 一次补丁/批操作 | 最多 32 个目标、总新旧内容 16 MiB；前像不完整即不修改 |
| 文件发现/内容搜索 | 每页 100 条、64 KiB 响应；每次最多扫描 10,000 项/64 MiB 内容、5 秒；达到上限返回截断与继续依据 |
| 工作区托管数据 | 256 MiB、10,000 文件及目录；新增父目录也计数；禁止增加已超限的用量，允许读取、删除与缩小文件 |
| 版本存储 | 1 GiB；最近 30 天且每文件最近 20 个版本，先达到者触发清理；活动/未知记录固定保留 |
| 轮次 | 沿用现有 10 分钟预算，新增最多 100 次工具调用；达到上限停止新调用并收尾 |
| 活动元数据 | 每调用摘要最多 8 KiB，总额 64 MiB；记录按 30 天清理，未收尾固定保留；不足时拒绝新工具轮次 |

搜索对大目录使用可推进的游标/扫描预算，regex 必须有线性时间实现或显式超时，忽略内部索引、版本和秘密区域。文件结果和预览保持当前公共 IPC 响应上限；大文件通过有界片段读取，不扩大桥的单帧上限。

每次修改在同一 workspace 修改租约内：校验范围/修订 → 预留容量 → 持久化调用身份及前像/不存在标记 → 原生工具执行 → 核对实际文件状态 → 记录逐文件后像摘要和结果。成功证据来自工具执行与受管文件核对，不从模型文字提取。快照由服务直接复制字节，不运行 Git、PowerShell 或用户脚本。

准入只固定已有祖先并记录缺失的父目录，不创建目录；全部目标校验和初始日志持久化后，实际写入才创建、验证并固定新父目录。日志中的 new_parent_directories 列出准入时缺失且可能在部分完成后保留的目录；文件恢复不递归删除它们。已知的新内容按增量预留，原生 edit/apply_patch 的保守长度用于单批上限；实际字节增长在每次发布前复核，覆盖中途的临时增长。删除和不增加用量的覆盖不因工作区已满而被拒绝，仍须满足前像与版本存储限制。

workspace 租约只协调节点内入口；外部程序仍可同时改动普通文件。前像、预期修订与实际覆盖/删除必须由同一文件身份上的原子条件修改，或与原生工具配合的已验证排他访问保证一致：条件修改在提交副作用时比较已持久前像对应的修订；排他访问从读取前像持续到修改完成，期间阻止外部原地写入、删除或替换。不存在目标使用不可覆盖的条件创建，移动同时校验源与目标。仅等待 before hook、再次计算摘要或无条件原子替换文件都不足以通过。该规则同样覆盖公共 mutate、restore 和记忆写入；多目标可逐项提交，但每项都须在真实修改时满足条件。

G3 必须在真实 Windows 上证明所选原生实现能在该保护下正常写入，外部竞争得到明确冲突/拒绝；用共享模式把原生工具自身一并挡住不算通过。锁获取受操作预算限制，取消/失败按实际收尾释放资源。无法配合的写入能力保持不可用，按计划重新评估范围，不自行重写原生工具。

Windows 服务对修改调用从读取前像到发布结果持续持有目标及父目录排他句柄（`share = 0`），阻止原地写入、替换和父目录改名；对纯读调用采用独立句柄、`FILE_SHARE_READ`（仍拒绝外部写入与删除）及 `ReadAt` 定位读取，允许同文件内部纯读并发且禁止跨调用借用句柄。修改写入私有新文件，将旧文件移入受管暂存目录，再以不可覆盖的句柄重命名发布；提交前检查目标是否仍为空。Windows 允许外部进程为已打开文件新建硬链接，新文件发布保证该别名仍指向未修改的旧内容。原目标已存在硬链接或 reparse point 时拒绝读取和修改。服务支持受管目录中的长路径及 Unicode，拒绝调用方传入 device namespace。该实现只约束文件 I/O，不提供脚本执行隔离。

已有父目录即使被外部程序原地设为 junction，也不能让后续 I/O 跟随新目标：每级子项通过 NtCreateFile 相对已固定父句柄打开，发布通过 NtSetInformationFile 相对目标父句柄重命名，不重新解析整条目标绝对路径。遇到无法解析的目录变化时拒绝操作，无法确认的结果保留 unknown；私有暂存目录由节点拥有。

### 工作区脚本沙箱、敏感目录保护与跨平台一致契约

受限脚本执行通过跨平台 `internal/platform/sandbox` 接口封装平台差异，保持上层工具语义、调度、取消与对账体验一致：

| 平台 | 隔离机制 | 写限制与管道 | 敏感目录读写边界（平台分层事实） | 进程树终止 |
|---|---|---|---|---|
| Windows（已实现） | `CreateRestrictedToken(DISABLE_MAX_PRIVILEGE \| LUA_TOKEN \| WRITE_RESTRICTED)` + Medium IL + `S-1-9-*` 能力 SID `S_sandbox` + `S-1-5-33` + `LogonSID` + `S-1-1-0 (Everyone)` + `TokenDefaultDacl` | 内核仅放开 `workspace` 与 `<journal>/sandbox-temp` 的 `(A;OICI;0x001301BF;;;<S_sandbox>)`（`Modify`，无 `WRITE_DAC`/`WRITE_OWNER`）；`cmd > nul`/Python 3.14/PowerShell/pwsh/Git/Node 正常工作（宿主机显式授 `Everyone:Modify` 目录例外） | 环境变量剥离 + 桥令牌不落盘 + 工作区外与常见用户凭据目录（`.ssh`/`.aws`/`.gnupg`/`.kube` 等）内核禁写与启动前规范化路径拦截（`sensitive_path_denied`）；**Windows 零 UAC 同用户不阻止内核读或 DPAPI 解封**（保留用户工具链可用性） | `PROC_THREAD_ATTRIBUTE_JOB_LIST` + Kill-on-close Job Object（`TerminateJobObject`） |
| macOS（契约已对齐，未实现/未验证） | 预期 `sandbox-exec` 动态 Seatbelt SBPL | 预期 `(deny file-write*)` + 仅允许工作区/`sandbox-temp`/`/dev/null` 等 | 预期内核级 `(deny file-read* file-write* ...)`；SIP 与运行时签名兼容性待二期实机验证 | 预期 `-pgid` `SIGKILL` |
| Linux（契约已对齐，未实现/未验证） | 预期 Bubblewrap (`bwrap`) user/mount namespace | 预期工作区与 `sandbox-temp` `--bind` 读写，其余 `--ro-bind` | 预期 `--tmpfs` 覆盖遮蔽自管凭据与用户敏感凭据目录；Ubuntu 24.04+ AppArmor userns 限制待二期实机验证 | 预期 `--die-with-parent` + `-pgid` `SIGKILL` |

第一阶段默认允许出站网络，单次脚本最长执行 `600` 秒（默认 `120` 秒）、同步转后台阈值最多 `30` 秒（默认 `10` 秒，单次 HTTP 轮询等待切片上限 `2.5` 秒）、每流（stdout/stderr）保留上限 `256 KiB`（支持 UTF-8 / UTF-16LE / 中文 Windows GB18030 流式解码），单轮次最多同时运行 `8` 个并发脚本会话（进程退出或被杀即立即归还名额，受单轮 `100` 次总工具调用预算约束），`<journal>/sandbox-temp` 共享 `256 MiB / 10,000` 项上限并由 `2s` 看门狗监护，全部与对话 `turn` 生命周期绑定。Windows 清理环境中默认注入 `GIT_SSL_BACKEND=openssl` 以保障 Git HTTPS 正常执行；系统 Schannel/SSPI 工具（原生 `curl.exe` 报 `SEC_E_NO_CREDENTIALS` / `0x8009030E`，或 PowerShell `Invoke-WebRequest` 报 `"The underlying connection was closed"`）由服务端识别并返回 `tls_hint: "windows_schannel_restricted"`，引导改用 Python/Node HTTPS。

脚本结束后由服务端统一执行 `reconcileAfterExec`：推进 `publishGen` 并重算工作区磁盘占用与文件计数（忽略且记录非预期 reparse point 为 `unsafe_links_detected`）；若超出 `256 MiB` 或 `10,000` 项上限则锁存 `overQuota` 状态阻塞后续工具写入增长直至精简，且 `workspace_versions.restore` 在携带脚本前旧文件修订时必触发 `revision_conflict` 而不会静默覆盖脚本成果。

多文件补丁可能部分完成；版本记录覆盖所有预计目标，记录每项实际状态，不承诺整批事务。异常中断不自动回滚；标记待核对，在确认原进程停止和文件状态前锁住修改入口。恢复也是新的显式操作：携带稳定操作身份、版本 ID 和当前文件修订，校验后备份当前版本，再写回前像或恢复“不存在”。外部改动造成摘要冲突即拒绝；恢复重复请求返回原收据，身份异内容冲突。恢复失败也保留逐项结果。

## 会话、执行事件与恢复

首批同一 workspace 所有获工具授权的轮次串行（包括可能写记忆的轮次），搜索/产物读取和纯历史查询不占修改槽。默认工具会话在整轮与收尾期间持有租约，不根据模型尚未调用 write 就提前允许另一个工具轮次。现有入队与容量规则保持，停止和核对留独立通道。

公共事件包含 workspace/conversation/turn/call 身份、策略修订、单调顺序、状态、简短目标摘要（`summary`，由工具定义显式声明可展示的参数白名单并归一化为有界单行文本）、结果/产物引用。不公开原生 token、内部提示词、隐藏推理、完整工具入参（如写入正文、编辑前/后文本、补丁差异块）或秘密。工具状态为 started/queued/completed/failed/cancelled/unknown（工作区操作记录保持 `State = "started"` 并通过 `Phase` 细分排队与运行，会话活动与历史视图将 `PhaseQueued` 映射为 `queued`）；轮次终态依据原生明确终态及工具收尾，不能凭出现 assistant 消息判完成。

会话正文和原生工具正文仍归 harness。若原生历史不能稳定回读活动，节点只存有界活动元数据、结果引用及操作日志，和文件版本日志同域管理；不复制完整 transcript。UI 实时事件丢失后用游标/快照补读；历史中保留部分完成与未知标记。无法还原完整输出时给摘要/可读取产物和明确限制，不伪造连续事件。

聊天中的文字和工具调用按原生可见历史及消息内的内容顺序显示。工具项属于原消息；只有工具的消息直接显示工具项，隐藏推理和没有可见内容的记录不生成空助手气泡。各工具按自身定义声明的 `display` 规则仅从目标参数生成有界单行 `summary`（历史旧名按对应新工具映射，工作区路径归一化为相对路径并剔除越界前缀，缺省或无合适目标时可为空）。调用 ID 由原生 run 与 call ID 固定生成，工具结果按同一轮次匹配；节点操作记录只有在会话、轮次和工具名都一致时才补充状态、缺失时的缺省摘要与声明进度，不追加或移动工具项。原生明确失败不被操作记录中的完成覆盖；没有对应结果或记录时显示未知。状态刷新只更新原节点，正文和公式缓存保持。

历史中的工具元数据也计入 64 KiB 页预算及界面 2 MiB 窗口预算；补充操作记录时为外层响应预留 4 KiB，预算不足先省略进度细节（分页仍超限时再裁剪可选 `summary`），保留调用身份和状态。历史内容不包含完整工具入参、未声明字段、原始工具输出、文件前像、操作诊断或隐藏推理。`messages.list` 读取会话内可见的有序文本/工具块，混合消息的文本段 ID 可交给 `messages.get`；完整活动、文件和恢复信息仍通过原有受工作区授权控制的接口读取。顺序与分页场景见[聊天历史顺序与分页](../development/features/chat-history-timeline/plan.md)，工具目标摘要规格见[聊天工具调用目标摘要](../development/features/chat-tool-call-summaries/plan.md)。

停止针对明确 turn；已经修改的文件保持，停止不等于撤销。所有工具统一遵循四级中断契约：
1. **排队与等待项**：`CancelTurn` / `CancelCall` 立即关闭准入并以 `cancelled` 退出等待队列；
2. **原子单文件修改（`write_file`/`edit_file`/`apply_file_patch`）**：保持当前文件临时写与重命名原子边界，关闭同批剩余目标继续进入；
3. **长目录搜索与记忆索引检索（`search_files`/`memory_search`）**：将 per-call `AbortSignal` 传至 `files.mjs` 与 `memory.mjs`（覆盖 `search_files` 及 `memory_search` 发起的全部 `stat`/`search`/`read` 桥接调用与 `assertCurrent()` 检查；OpenClaw 原生 `manager.sync` 内核 SQLite 同步步间以取消检查尽快退出），Go `searchInternal` 监听 `ctx.Err()` 与 `schedEntry` 取消标志实现毫秒级中止；
4. **脚本进程与 Turn 内后台会话（`exec_command`/`process_control`）**：所有脚本会话均归属 `execManager` 与当前 `turn`，并在转后台时以 `<journal>/es_*.json` 持久跟踪直至异步终态落盘；单独工具取消（`CancelCall`）、用户停止（`CancelTurn`）、模型结束轮次（`ReleaseTurn(turn, true)`，`stopped=false` 时直接返回 `cleanup_pending` 且不误杀在途脚本）或节点退出（`RuntimeStopped`）时，严格在 `s.mu` 锁外并行终止全部活跃进程树（`TerminateJobObject` / `-pgid`）、执行有界管道排空（`<= 1500ms`）、清除只读属性后清理 `sandbox-temp` 并完成工作区对账；全部进程确已退出且排空后即使对账/落盘出错亦置 `s.blocked = true` 并释放 `s.turn` 返回 `nil`，避免状态观察永久阻塞在 `"running"`。

超时、Gateway 重启、节点崩溃后用原 call/run 身份核对；证据不足保留 unknown，用户可在确认原执行已停止后显式处置，不自动重放原工具或整个轮次。该处置仅解除阻塞并记录证据，不制造成功或隐式执行恢复。

确认原进程树已停止后，即使终态日志落盘失败，也释放该轮的运行租约；操作保持 unknown 并阻止后续修改。存储恢复后允许用户核对，不以残留 busy 阻止恢复，也不把落盘失败报告为成功。

## 产物与 UI/CLI/MCP

产物引用绑定节点、workspace、会话/轮次和创建时文件修订；路径只是内部定位信息。每次读取重新验证授权、真实文件身份和修订；已删除/改动返回明确 stale/not_found，可另取当前版本。模型输出的 Markdown 路径不能自动取得宿主文件下载权限。

首批日常界面仅在设置中提供“允许助手调用工具”开关，聊天保留工具活动与进度。文件目录/搜索、读取/下载、版本列表、显式恢复和未知操作核对保留在公共 CLI/MCP；不提供独立文件管理对话框。界面按普通用户使用频率安排，不为展示技术能力而增加低频控件。普通聊天澄清保持，不新增等待式 ask_user 工具。

统一 Web 的“设置 → 助手”通过专用人工 API 保存工具权限；点击即保存，不并入模型设置事务，保存成功后从下一轮生效，已有对话保留历史。保存结果未知时重新读取权限，未确认前不显示成功；加载、保存和错误均提供简短状态。活动状态变更推进单调序号，客户端按调用 ID 替换已有记录。公共恢复/核对继续使用稳定操作身份及收据，丢失响应时由调用方按同一身份核对或重试。文件公共读取每段最多 32 KiB（JSON 中为 base64），目录/搜索每页也受 32 KiB 限制，以满足公共响应 64 KiB 上限；单文件读取与变更均限 2 MiB。

CLI/MCP 与 UI 复用同一服务、目标约束、分页和错误语义；文件读写业务可以通过受限工作区公共操作调用，并复用快照和租约，不能另开一条直接修改 runtime state 的通道。内置助手仅获得该工作区的窄服务身份，不继承外部当前用户自动化客户端的全量权力。

## 验证与演进

文件工具实现门槛和场景见[本机工具验证方案](../development/features/local-runtime-tools/validation.md)，第一阶段脚本沙箱、秘密保护、四级停止与原生集成验证见[脚本执行验证方案](../development/features/workspace-script-execution/validation.md)。后续第二阶段审批提权、macOS/Linux 沙箱后端、媒体、委派与后台任务分别按独立计划扩展能力。旧任务路径、本机快捷命令与外部自动化的现有授权保持原契约。

[工作区工具细粒度并发计划](../development/features/workspace-tool-concurrency/plan.md)已在保留同工作区工具轮次串行的前提下实现轮内不冲突资源的并行执行：每项工具通过受信任描述声明资源范围，整批原子封存（同批内对同一已修改路径的后续盲覆写 `write_file` 以 `conflicting_batch_write` 拦截，其余冲突管线按批内顺序串行）后按序公平授予并在每次低层访问前校验身份与效果；记忆检索显式声明源文件与索引代次，取消等待在途子 I/O 排空。在此底座上，[工作区脚本与命令执行计划（第一阶段）](../development/features/workspace-script-execution/plan.md)进一步扩展 `DomainExec`、`execManager` 前台/后台生命周期管理与四级停止协同。公共文件入口共用同一调度器，操作记录可选暴露 `phase` 与 `wait_reason`，文件版本、原子预算和外部程序竞争保护完整保留。详细架构与验证入口见[资源调度设计](../development/features/workspace-tool-concurrency/design.md)与[并发验证方案](../development/features/workspace-tool-concurrency/validation.md)。
