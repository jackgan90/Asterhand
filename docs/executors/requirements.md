# 执行器与工具需求

## 本地快捷命令

自动化要求（已接入公共操作）：EXEC-AUTO-01 扩展本机命令的受授权 agent 管理和执行入口，三入口共用注册、路径校验、运行锁和执行监督；EXEC-AUTO-02 要求跨客户端可恢复的启动身份、精确 run ID、对象修订及结果/收据独立保留；EXEC-AUTO-03 要求完整图/批量局部编辑和可直接读取的有界日志文本。范围见[自动化需求](../automation/requirements.md)。下表原有手动入口和当前轮规则按[统一操作 ADR](../automation/adr/001-shared-operations.md)扩展为本机授权调用及有界历史，普通用户执行和停机清理不变；远程和内置模型启动仍后置。

| ID | 要求 |
|---|---|
| EXEC-CMD-01 | 本机用户通过快捷输入显式启动预注册 Windows bat/cmd/ps1；与任务槽、模型和远端执行隔离 |
| EXEC-CMD-02 | 注册包含主名、别名、描述、图、allow_concurrent 与整图超时；脚本启动信息属于节点；名称唯一，不自动合并配置 |
| EXEC-CMD-03 | schema 4 原子保存、修订校验；旧格式不迁移、不自动清空；删除注册保留脚本 |
| EXEC-CMD-04 | 所有命令普通用户受管执行，节点退出停止，UI 关闭不停止；未知投递不自动重试 |
| EXEC-CMD-05 | 缺失项保留并可定位修复，执行前复核；路径、参数、权限、IPC 与认证由应用保证，脚本自身行为和依赖由用户负责 |
| EXEC-CMD-17 | 按显式集合批量删除，逐项校验对象修订与运行占用并返回可核对结果；部分失败不回滚成功项，不删除脚本文件 |
| EXEC-CMD-18 | 命令级 notify_on_completion 默认开启，当前格式缺失字段的旧命令按开启处理；显式关闭、修订保护、复制与 UI/CLI/MCP/草稿一致 |
| EXEC-CMD-19 | 整次运行完成保存后发布一次通知事实，按接纳时配置决定是否提醒；不因步骤退出、历史读取或重启核对而重复提醒，展示故障不影响执行 |

当前功能契约见[本地命令设计](local-commands.md)，实施状态见[功能计划](../development/features/local-quick-commands/plan.md)。

## 本地命令执行与日志

以下为所有本地命令的执行要求，当前有效设计见[统一命令计划](../development/features/unified-command-runs/plan.md)。

| ID | 要求 |
|---|---|
| EXEC-CMD-06 | 命令只有脚本图；取消托管/非托管、单脚本/图与终端类型；非交互执行，交互仅预留可选接口 |
| EXEC-CMD-07 | allow_concurrent 默认 false；按命令 ID 限制重叠，允许时每实例独立；接纳到收尾占用受控，具体上限见执行设计 |
| EXEC-CMD-08 | 持续保存各实例 stdout/stderr；最近按接纳顺序，其他当前轮日志到下一轮接纳才清理；容量超限可见失败，不静默截断 |
| EXEC-CMD-09 | 自然成功/失败按入口脚本退出码；执行完毕、执行出错和执行中断持久保存，正在执行/正在停止仅为活动阶段，stderr 内容不直接决定结果 |
| EXEC-CMD-10 | 按 run ID 强制停止；停止全部显式携带当时的实例集合，不影响后接纳者；重复停止幂等 |
| EXEC-CMD-11 | 托管注册可选整次执行超时，默认无；到期停止受管进程树并记失败/超时，日志保留；计时不因输出、界面或请求生命周期重置 |
| EXEC-CMD-12 | 节点退出统一停止托管执行，UI 关闭/重建不影响执行；异常退出恢复保留可确认原因和已落盘日志，结果不明标中断且说明，禁止自动重跑 |
| EXEC-CMD-13 | 任一实例未收尾禁止修改或删除；删除清理全部命令记录，保留脚本；持久清理意图保障恢复 |
| EXEC-CMD-14 | 完成、停止、超时和退出由同一状态机仲裁；进程树清理与日志收尾完成前不释放占用，启动/停止身份、恢复和存储故障不能产生双执行或虚假终态 |
| EXEC-CMD-15 | 公共命令/状态/日志契约与平台分离，脚本路径策略、执行监督、计时和必要文件操作通过窄接口注入；Windows 为首个实现，其他平台缺少必需能力时明确拒绝，不降级为非托管或伪报停止成功 |
| EXEC-CMD-16 | 查看日志先显示独立窗口，页面异步加载；关闭后有界缓存并暂停刷新，失败及时反馈且可显式重建，窗口或宿主恢复不改变命令执行 |

详细契约见[托管命令设计](managed-local-commands.md)，实施与验收见[托管命令计划](../development/features/managed-local-commands/plan.md)。

## 本地脚本图

| ID | 要求 |
|---|---|
| EXEC-GRAPH-01 | 命令统一为静态图，最小开始→脚本→结束；不迁移旧配置 |
| EXEC-GRAPH-02 | 唯一开始/结束、无环且所有节点位于开始到结束路径；服务端验证身份、边和容量 |
| EXEC-GRAPH-03 | 全部前置成功才调度；图内与本机节点并发有界，同命令实例并发受 allow_concurrent 控制 |
| EXEC-GRAPH-04 | 任一失败禁止新启动、停止活跃节点，清理确认和日志收尾后结束；错误策略独立于实际结果 |
| EXEC-GRAPH-05 | 整图与节点执行身份和结果分离；配置固定，节点超时与整图超时独立 |
| EXEC-GRAPH-06 | 每实例持久日志标记节点且独立解码，当前轮多实例完整读取；使用新存储格式，不承担旧格式兼容 |
| EXEC-GRAPH-07 | 崩溃恢复核对所有节点身份，不自动重跑，清理未知继续占用；窗口不拥有执行 |
| EXEC-GRAPH-08 | 连线仅表达依赖，无隐式数据传递、回滚、重试或常驻服务就绪语义；不增加远端/模型启动能力 |
| EXEC-GRAPH-09 | 日志支持按脚本节点独立读取历史、尾页和前后分页；校验运行/节点身份，读取量及响应有界，保留每实例合并读取与节点分页，不兼容旧持久格式 |

详细行为见[脚本图设计](script-graphs.md)。

## 持久任务执行需求

状态：已确认边界；实现契约见本领域 design.md，平台边界见测试文档。关联 R10、R11、R14、C03、C06。

| ID | 要求 |
|---|---|
| EXEC-01 | 任务服务依赖窄执行器契约，不绑定某个 coding harness 的会话类型 |
| EXEC-02 | 首版真实执行由 OpenClaw 调用受限本机文件工具完成 |
| EXEC-03 | 能力显式声明：事件、问答、审批、取消、查询、恢复及断连行为 |
| EXEC-04 | 任务执行必须关联 job/attempt，返回真实工具结果与产物；持续会话轮次及工作区工具按独立会话契约管理 |
| EXEC-05 | 首版不安装、不认证、不实现具体 coding harness adapter |
| EXEC-06 | FakeExecutor 支持确定性故障脚本，仅供自动化验证 |
| EXEC-07 | Agent runtime 由工厂选择，adapter 自己管理执行、配置校验、安装布局发现和诊断；未知 runtime 不回退 |
| EXEC-08 | 适配器必须通过结果、失败、取消、工具模式和凭据边界契约；新增运行时不修改任务和节点协议 |
| EXEC-09 | 公共配置/CLI 不声明提供者专用字段；运行时选项由适配器校验并声明全部已知受保护目录，公共授权检查拒绝重叠和目录别名绕过 |
| EXEC-10 | 功能配置检查归 executor/adapter，采用有界本地读取，不调用模型或启动运行时；聊天与任务工具条件分别判断，真实执行继续校验运行时和工具契约 |

后续候选包括 OpenCode、Claude Code、Codex、Antigravity；它们不是首版依赖，也不假定存在等价 API。首个具体适配器由后续场景选择。

实际接口为 Execute(ctx, Run)，查询/问答/取消/恢复由任务服务和工具回调提供。文本 create-only、64 KiB、os.OpenRoot、操作日志与回读摘要已实现；未支持的 native resume/审批不声明为可用。

## 会话运行时适配

当前源码已接入，验收边界见[本机持续聊天计划](../development/features/local-node-chat/plan.md)；不改变当前 Execute 契约。

| ID | 要求 |
|---|---|
| EXEC-CHAT-01 | 持续会话能力与一次性任务执行接口并列；创建/发送/历史/事件/状态核对/停止按产品语义适配，厂商 RPC、ID 与游标保持私有或不透明 |
| EXEC-CHAT-02 | 首个聊天 adapter 复用锁定 OpenClaw/provider，在独立 state 下管理 Gateway；默认允许受限工作区工具白名单，人工关闭后使用纯文本模式；关闭代码、skill 执行、管理命令及不受控插件/渠道，验证最终有效能力与拒绝路径 |
| EXEC-CHAT-03 | 新 harness 以能力及契约测试接入；可靠收据、历史和恢复不足时明确不支持；不使用空实现、静默 provider/runtime 回退或未验证持久化保证 |

- EXEC-PROMPT-01：会话适配器保留原生设定，优先使用配置与局部补充解决能力冲突，声明完整覆盖、补充或不支持并报告经确认的应用状态；append 已确认即为正常成功。OpenClaw 保留原生提示词与隔离工作区角色文件，通过受支持钩子补充当前入口约束，失败不得单独阻断聊天。后续 harness 遵循相同原则，工具定义以实际暴露集合为准。

## 持续会话工具执行

以下要求约束工具开启时的文件、第一阶段受限工作区脚本执行工具及第二阶段受控提权脚本与极速免审执行；未设置权限时默认开启（默认按需询问模式），保留用户明确关闭后的纯文本模式。文件并发、第一阶段脚本沙箱及第二阶段受控提权边界分别见[工作区工具并发计划](../development/features/workspace-tool-concurrency/plan.md)、[第一阶段计划](../development/features/workspace-script-execution/plan.md)与[受控提权计划](../development/features/workspace-script-escalation/plan.md)。

| ID | 要求 |
|---|---|
| EXEC-TOOLS-01 | 公共层按语义能力、支持/授权/就绪和执行约束表达；adapter 映射原生工具，不能落实的限制明确不可用 |
| EXEC-TOOLS-02 | 文件读写均限制于真实工作区根，覆盖 Windows 别名/链接和竞态；配置、凭据及运行时 state 不授予模型文件工具，工作区控制桥令牌通过环境注入且不写入 Gateway JSON |
| EXEC-TOOLS-03 | 所有文件修改入口执行前持久保存完整前像并预留预算，具备外部并发保护（含同卷发布后恢复工作区继承 ACL）；第一阶段脚本进程仅能在工作区与受管 `<journal>/sandbox-temp` 内由内核放开 `Modify` 写入，第二阶段 Tier 2 以普通用户 `OrdinaryToken` + 具名 `JobObject` 在声明外部目录或原生 HTTPS 环境下执行，退出后均触发配额重算、reparse point 检测与文件修订冲突保护 |
| EXEC-TOOLS-04 | 同工作区工具轮次与恢复串行，轮次内按资源声明调度并发与等待（前台 `exec_command` 在等待切片窗持有工作区写排他，超出 `yieldMs` 持久化 `<journal>/es_*.json` 转由 `execManager` 后台托管后释放 `DomainFS` 锁放行只读、控制与多脚本并发，并通过 `exec_active` 写屏障延迟并发受管文件修改直至脚本退出，且待确认提权票据通过 `approval_pending` 写屏障保护引用脚本不被并发修改，`process_control` 使用独立控制域）；取消（`CancelCall`/`CancelTurn`）、轮次结束与崩溃均在锁外并行强制终止并排空 Turn 内全部在途/后台脚本及可取消搜索扫描，不自动重放未知副作用 |
| EXEC-TOOLS-05 | 文件大小、搜索扫描、工具调用、脚本时长/输出流/轮内会话数、`sandbox-temp` 磁盘配额、版本和事件存储有界，达到上限明确强杀该轮全部活动脚本并截断/锁存，不静默丢失活跃证据；`"exec/start"`、`"exec/approval_wait"` 与 `"exec/poll"` 分片通过独立 `waitSlots`（24 槽）阻塞等待（单次切片 `<= 2.5s`），`"exec/kill"` 走 `controlSlots`（12 槽），`"exec/control"`（`status`/`log`/`list`）走 `ioSlots`（12 槽），避免耗尽通道或触发写超时 |
| EXEC-TOOLS-06 | 工具白名单（11 项统一工具）、prompt hook、原生权限覆盖和插件来源一起验证；离线预检拦截核心工具重名与归属漂移；Gateway 原生 `exec`/`process`/`code_execution` 保持拒绝；受控提权（`escalated`、`escalationReason`、`externalPaths`）必须经由 Go 服务端评估、分片审批或 `turbo` 临时放行执行，模型无权自批或绕过 UAC/硬底线拦截 |
| EXEC-TOOLS-07 | 提权策略独立保存于受限节点配置的 `ExecutionMode`（`standard`/`turbo`）、`TrustRevision` 与 `TrustRules`：按“系统硬底线 → 用户禁止 → 用户每次询问 → 会话/持久允许 → 模式兜底”五层执行，保守词法解析阻止复合连接符或裸通用解释器取得持久自动放行，修改模式或移除信任项目须在不重启 Gateway 的前提下毫秒热生效并同步清除被撤销/覆盖的会话级缓存 |
| EXEC-TOOLS-08 | 每次待确认审批票据设置 `5 分钟`最长等待上限、绑定工作目录/外部目录/可执行身份与引用工作区脚本 SHA-256 摘要，启动前复核任何篡改均返回 `approval_tampered`；用户拒绝返回非阻塞 `denied_by_user` 且不锁存 `reconciliation_required` |
| EXEC-SCHED-01 | 保留同工作区工具轮次串行，在轮次内允许无冲突工具同时执行；公共文件入口使用相同资源规则 |
| EXEC-SCHED-02 | 工具通过受信任描述登记效果、资源范围、兼容模式、容量和期限；未知工具或缺少声明时拒绝执行 |
| EXEC-SCHED-03 | 原生调用、文件桥、版本记录、取消和结果使用同一调用身份；跨调用、旧代次和身份缺失访问拒绝 |
| EXEC-SCHED-04 | 资源和执行名额一次取得，冲突等待有界且可取消；同资源按登记顺序公平执行，不阻塞不相关资源 |
| EXEC-SCHED-05 | 每次实际 I/O 前验证资源授权；未声明的访问在副作用前拒绝并记录，不能只检查工具注册信息 |
| EXEC-SCHED-06 | 保持 Windows 路径、句柄和外部修改保护；前像、容量预留、发布与逐文件收尾在并发下仍正确 |
| EXEC-SCHED-07 | 取消和超时不提前释放仍有实际工作的资源；未知副作用沿用显式核对，禁止自动重放 |
| EXEC-SCHED-08 | 记忆源文件、索引同步和索引代次有明确资源声明；搜索能看到此前已完成的记忆修改 |
| EXEC-SCHED-09 | 等待不占文件执行名额或耗尽传输通道；停止、收尾、核对和状态读取始终可进入 |
| EXEC-SCHED-10 | 每个工具有独立预期效果、组合测试和故障测试；漏声明、错误兼容模式和提前释放必须被测试发现 |
| EXEC-SCHED-11 | 并行完成顺序不改变聊天正文中的工具顺序；活动状态、分页与 CLI/MCP 可核对同一调用 |
| EXEC-SCHED-12 | 用真实 Windows 与锁定原生 Gateway 证明实际重叠、互斥和取消；性能结论附工作量与配置 |

范围、预算、原生可行性门槛和后续 harness 接入见[能力设计](../assistant/runtime-capabilities.md)；轮内资源调度的设计与测试入口见[工作区工具细粒度并发计划](../development/features/workspace-tool-concurrency/plan.md)。

## 多 Coding Harness 本地中介与协作（已完成实现与本机用户验收）

以下需求对应[多 Coding Harness 中介计划](../development/features/multi-harness-broker/plan.md)与 [ADR-008](adr/008-multi-harness-broker-and-native-adapters.md)：Milestone 1—2、Milestone 3 第 1—2 步、Milestone 4.1 与双 Agent 实时协作增强（`workers_steer`、`commentaries`/`wake_on_commentary` 与 `waiting_input` 认领续期）均已完成实现、工程验证及本机用户验收；后续按序推进 Milestone 4.2（`isolated_worktree`）与角色模板，不改变既有 OpenClaw 任务与持续会话契约。

| ID | 要求 |
|---|---|
| EXEC-HARNESS-01 | 多 Coding Harness 协作作为独立于现有文件任务与内置持续聊天的领域服务运行，不占原任务槽或聊天生成槽 |
| EXEC-HARNESS-02 | 自动发现本机已安装的受支持 Coding Harness（支持人工路径覆盖、`PATH`、合规安装目录/注册表与只读版本检查），绝不读取、复制或代理任何第三方 OAuth/API 凭据文件 |
| EXEC-HARNESS-03 | 区分发起方（A）与被控方（B）能力边界：Antigravity 静态限定仅支持作为 A，不支持作为受管 Worker B；Codex、Claude Code、DeepSeek Harness 仅调用未修改的官方本体 |
| EXEC-HARNESS-04 | 被控方适配器直接采用各家官方长期双向协议（Codex `codex app-server`、Claude Code `stream-json` 双向协议、DeepSeek Harness 实测锁定的 `sdk`/`acp`），不做一次性过渡实现，并如实声明能力矩阵 |
| EXEC-HARNESS-05 | 为被调 Worker 维护绑定规范化授权工作区的三层稳定身份（`(workspace_id, alias)` 唯一 `worker_id`、`session_epoch`/`native_session_id`、映射发前持久 `client_correlation_id` / 原生轮次与幂等 `operation_id` 的 `turn_id`）；单 Worker 同时最多 1 个活跃轮次（并发返回 `worker_busy`），`cancel`/事件/终态均核对三元组，回执丢失仅凭唯一持久关联键匹配（禁止按正文猜测），原生会话不可恢复时明确报错；按 `(threadId, turnId, itemId)` 幂等归集，仅对 `completed` 成功轮次按“优先 `final_answer` → 排除 `commentary`/工具后取最后一条无 `phase` 或 `partial_answer` 助手消息 → 无候选显式标不可用”发布最终正文，中断/失败轮次的 `partial_answer` 仅作部分输出不冒充完成结论 |
| EXEC-HARNESS-06 | 任意常规本地项目目录免逐项目登记即可协作，由服务端对规范化路径派生稳定 `workspace_id` 并内置拦截盘符根、用户主目录、节点数据目录、系统及常见凭据目录（`artifact_paths` 严格约束在对应项目根内，不宣称 Codex `readOnly` 在 Windows 上构成目录级只读 chroot）；按 `(harness × version × platform × access_mode)` 实测并声明 `os_sandbox_enforced`、`harness_tool_restricted` 或 `unsupported`；`workspace_write` 在 Milestone 2 首次开放并按解析后的工作树根互斥，附带诚实标注外部并发可能性的工作树/Git 快照（涵盖 staged/unstaged/untracked 及非 Git 目录）；关闭全部发起方开关或吊销凭据使旧连接全失效、仅关闭 `collab_caller_enabled` 保留剩余有效工具集并过滤旧协作收据；关闭被控方 B 的 `collab_worker_enabled` 覆盖发前、`turn/start` 待回执（迟到回执不逆转已撤权状态）与运行中三窗口并受控停止 |
| EXEC-HARNESS-07 | `workers_send` 为纯写接纳立即返回 `turn_id`（接入领域收据恢复，不混合等待参数），等待统一走 `workers_wait` / `workers_wait_any`（上限 `30s`，返回实时流式活跃遥测、结构化 `next_step` 与严格隔离思维链的面向用户 `commentaries` 中间反馈，并支持 `after_commentary_seq` / `wake_on_commentary` 事件驱动提前唤醒）；对声明 `supports_steer: true` 的 Harness 提供 `workers_steer` 在 `running` 轮次内注入补充指令（不支持时显式返回 `unsupported_steer`，不暗中取消重跑）；强制节点级活跃轮次上限（默认 4）、单轮墙钟时限（默认 30 分钟）与无事件超时（默认 10 分钟，由全量流式 delta/chunk 心跳刷新），超时先原生中断再同步回收 `JobObject`，确认停止后释放锁并淘汰异常终态常驻进程；接入 `App.RequestStop` 原子停机判断并在关库前完成收尾（空闲常驻进程不阻普通退出）；长审查正文与运行日志独立于 8 KiB 收据有界落盘并同步提供分页 `workers_result` 与 `workers_logs`；所有受管子进程以非提权 `OrdinaryToken` + 具名 kill-on-close `JobObject` 运行，支持空闲进程回收与重启对账 |
| EXEC-HARNESS-08 | 受管启动的 Worker B 优先通过原生参数禁用其加载 `asterhand` MCP，并辅以服务端 `max_delegation_depth = 1` 与屏蔽 `general` 工具集兜底 |
| EXEC-HARNESS-09 | 可写 Worker 支持正交 `approval_mode`（`auto` 默认 / `ask_human` / `strict_deny`；`read_only` 始终自动拒绝提权仅放行业务提问）：支持原生辅助审单的 Harness（Codex `approvalsReviewer="auto_review"`、Claude Code `--permission-mode auto --permission-prompt-tool stdio`）默认启用官方自动审批，不支持者（DeepSeek Harness）诚实声明 `supports_auto_approval: false` 并回落 `ask_human`；进入宿主审批桥的高危权限请求（`waiting_approval`）必须经 Asterhand 统一硬底线过滤并直接呈交人类本人确认（支持单次允许、会话免审但仍拦硬底线、仅拒单次不杀轮次、中止整轮），严禁暴露给发起方 A 的模型代批或自行开启会话免审；业务提问（`waiting_input`）优先回传发起方 A 经 `workers_reply` 回答，支持“未读 15s 兜底 → A 读取后 30s 反应缓冲 → `defer_seconds`（30..300s）显式续期核实 → 附 `note` 显式升级或超时自动兜底转人工”两阶段流转（Web 保持透明可见并允许人类随时抢答）；等待人工审批/提问期间暂停 `StallTimeout` 与执行墙钟计时 |
| EXEC-HARNESS-10 | 明确同 Windows 用户账户下的诚实信任边界，区分面向合作型 Harness 的托管策略与 OS 账户级隔离 |
