# 执行器与工具设计

所有本地命令统一为[脚本图](script-graphs.md)，schema 4 只保存图、并发和超时属性，不迁移旧格式。运行管理器按命令归集多实例，各实例及其节点拥有独立身份、结果和日志；不进入任务引擎或模型会话。

[执行与恢复](managed-local-commands.md)定义实例选择、停止和恢复；[自动化设计](../automation/design.md#运行日志与保留)新增有界历史，替代下一轮立即清理全部旧记录的策略。

公共核心通过 CommandPolicy、ExecutionBackend 和 ExecutionClock 等[窄平台接口](managed-local-commands.md#平台边界与最小接口)注入系统能力；Windows shell、Job、令牌、句柄和路径等价规则留在首个 adapter。平台能力/当前环境检查决定是否可用，其他平台未满足受管清理等要求时明确 unsupported。公共核心不依赖快捷输入包或具体 WebView，后续平台沿用相同状态、日志、停止与恢复协议。

范围：OpenClaw adapter、公共配置和目录保护；未接入具体 coding harness。验证方法见 [公共流程契约](../development/features/provider-neutral-flows/validation.md)。

## 当前契约

[自动化操作](../automation/design.md#图编辑与人机并发)将命令管理、图预览/修改、启动、停止与日志接入相同领域服务。注册修改以命令修订防覆盖，全库唯一性仍串行检查；启动把业务请求与 run ID 随运行索引一起公布，窗口序号不再承担公共去重。结果/收据与大日志分别有界保留，过期拒绝重放，普通用户执行和既有图调度/恢复规则保留。`current.json` 仍是唯一执行接纳依据，`history.db` 只索引保留摘要与详情，不据其重启工作。脚本总槽位为 8，agent 同时最多占 6，人类可用全部空闲名额。

`Executor.Execute(ctx, Run) (Result,error)` 是实际最小接口。Run 绑定 attempt ID、mode、callback、一次性 token 和 goal；Result 提供 ok/status/final/error。启动、查询、事件、回答和恢复由任务服务实现，不为未支持的 native resume 提供空接口。

`Runtime` 接口嵌入 Executor，提供 Describe、Validate、Discover、Check 和 ProtectedPaths。静态工厂只登记实际实现的 adapter，当前为 OpenClaw；Selected 在每次 attempt 按当前配置解析 adapter。环境管理使用同一工厂发现和诊断，应用入口不直接实例化具体 agent。

按需准入增加本地配置能力检查：公共层校验选择和凭据，adapter 声明聊天与任务各自的本地安装条件，返回稳定 code/detail。检查读取固定数量的路径/包元数据，不启动 Node/OpenClaw，不递归校验依赖或请求模型；版本执行诊断仍由 Check 和实际 runtime 承担。能力 available 是“配置可尝试使用”，与手动 probe 通过分开。任务提交前使用同一判断，执行时重新校验必要配置；聊天使用同源本地规则且不依赖任务工具插件或授权目录。见 [按需配置决策](../assistant/adr/009-on-demand-model-readiness.md)。

将运行时专用选项放在 Config.runtime_options[runtime ID] JSON 中，由该适配器解析和校验；公共配置与 CLI 不声明厂商专用字段。CLI 用 --agent-runtime 和 --runtime-options（JSON 文件）选择及配置；旧 --node/--openclaw/--plugin/--provider 参数由此替代。历史 flat JSON 字段仅由兼容迁移模块读取，保存时移除，身份和凭据文件不改写。

适配器 ProtectedPaths 返回已知运行时、宿主、工具插件和模型插件保护目录。公共授权目录校验统一拒绝与这些目录及节点数据目录重叠，解析现有目录/祖先的符号链接与 junction 后再比较；目录声明或解析失败显式拒绝。更换运行时选项时对既有授权目录重新校验。

用 Config.runtime_options_explicit[runtime ID] 标记显式选项。CLI 的 --runtime-options 将整个选项对象标为显式，普通启动/repair 保留它；自动选项随包目录发现和升级。旧无标记配置仅当宿主、入口、工具插件和当前模型插件均匹配同一历史包布局时视为自动，其余非空配置保留为显式。新 CLI 即使指定另一个完整包也保持显式；旧格式无法区分人为填写的完整包布局与旧自动值，按此兼容规则处理。

能力声明区分 route/execute/probe、工具回调、普通问答、进程取消与原生事件/resume/审批。后三项当前明确 false；任务事件、持久问题及重启对账由 Asterhand 提供。`/api/settings` 返回能力描述与支持的模型 provider，服务端拒绝不支持的组合。

OpenClaw adapter 在启动前校验 attempt ID、模式、本机回调和凭据引用。stdout 采用已验证 CLI envelope：status 为 ok/error/timeout，且 ok 标志必须一致；未知或矛盾状态返回适配失败。输入通过 stdin 异步写入，取消能同时中断阻塞写入和进程树。失败日志、最终文本与错误字段对已知凭据脱敏。

契约 fixture 以真实子进程模拟协议边界，覆盖成功/错误/无效输出、取消和阻塞输入；真实 OpenClaw 检查另行执行。新增 agent 要实现同一工具桥接和能力契约，并运行相同类别的契约及真实模型验证，不假设 Hermes 与 OpenClaw 的 API 相同。

Windows 在写入目标前把 Node 加入 kill-on-close Job Object；取消和服务退出结束整棵子进程树。真实进程 fixture 验证后代终止。Linux 使用进程组，其产物仅交叉编译。每个 attempt 的 runtime state 留在节点数据目录用于现场核对。

OpenClaw CLI 固定 `agent exec --message-file - --json --code-mode direct`，每个 attempt 独立运行。有效执行预算 10 分钟，墙钟 2 小时；问答等待由 Go 回调承载。 stdout/stderr 各最多保存 1 MiB，不展示无限模型输出。

## 文件工具

Go `os.OpenRoot` 约束路径解析，拒绝绝对路径、穿越、NTFS 流、尾随点/空格和越界 junction。文件 create-only：`O_EXCL`，不覆盖已有内容。文本有效 UTF-8，最多 64 KiB；不自动加换行或 BOM，父目录须已经存在。

调用绑定 job/attempt/native tool-call ID，摘要相同的重试读取原操作结果，摘要不同返回冲突。写前取得普通事件预算并持久化 prepared 操作；写后 fsync、close、回读校验，再把完成操作与任务产物/事件同事务提交，包含大小、完整 SHA-256 和最多 8000 字节预览。旧分步提交留下的 completed 操作可按原证据幂等补齐任务产物；prepared 仍进入待核对，不根据同内容旧文件伪造成功。

普通问题持久化 interaction，绑定 attempt 与 native tool-call ID；回答后才释放等待回调。取消时等已接收回调退出再释放执行槽，防止上一个写入尚未结束就开始下一个任务。

工具层不提供权限审批工具或任意 shell。远端 peer 无本地人工处置权限。后续 Everything、VCS、coding harness 需单独实现并申明能力。

## 会话能力接口

持续会话使用与 `Executor.Execute` 并列的能力接口，产品语义由[会话设计](../assistant/conversations.md)维护，首版仅 OpenClaw 实现。原任务 mode、回调 token、文件工具与证据契约不变；纯聊天不伪造 job/attempt，也不取得任务工具 token。

公共层依赖创建/打开/列举、发送、观察/核对、历史/完整消息读取、停止及能力描述。adapter 负责 native session/run/message/receipt 映射，保证公共稳定身份和事件合并。不同 runtime 的取消、流式、历史、重启恢复分别验证，未知能力明确拒绝；不把 `Runtime` 扩成所有方法必选的通用执行大接口。

首个 adapter 使用受管 Gateway，生命周期、凭据注入、认证连接与状态目录由后端管理。UI 不知道 WebSocket RPC、配置路径或管理凭据。当前任务仍独立 `agent exec`；会话 state 与其 state 分开，禁止共享独占写入目录或接管用户已有 Gateway。

验收覆盖原生持久收据而非仅 HTTP/WS ACK、重复与迟到响应、cancel 与完成竞争、状态查询、历史代次/超长消息、无工具/管理命令旁路。实现前验证门槛与范围见[计划](../development/features/local-node-chat/plan.md)。本机工作区工具另按当前策略修订授权；类型化消息只展示执行事件，不授予能力。

## 可选聊天提示词适配

提示词组装独立于 harness；适配器声明 replace/append/unsupported，实际状态区分 pending/applied/degraded。应用失败继续原生聊天，状态只携带安全的枚举与版本。默认保留 harness 原生身份、语气与行为，只适配能力和入口冲突；append/applied 为正常成功。OpenClaw 私有 Gateway 加载应用自带的可信提示词插件，before_prompt_build 使用 appendSystemContext 保留原生组装，llm_input 核实补充规则与当前授权工具白名单。隔离工作区开启原生角色文件初始化并保留已有定制；不读取工程或个人 home 的设定。工具权限仍由有效运行配置约束。详见[会话设计](../assistant/conversations.md)与[功能计划](../development/features/chat-prompt-interaction/plan.md)。

## 持续会话原生工具与轮内并发调度

[本机工具能力设计](../assistant/runtime-capabilities.md)通过可选 WorkspaceRuntime 提供共享工作区服务与人工权限修改。公共层不绑定特定 harness 内部实现；OpenClaw adapter 生成十一项统一工具白名单（`read_file`、`write_file`、`edit_file`、`apply_file_patch`、`list_dir`、`search_files`、`memory_search`、`memory_get`、`progress_card`、`exec_command`、`process_control`），同步注入系统提示词补充与 `llm_input` 校验，并通过离线 SDK 预检测试拦截与核心工具/别名的重名冲突。

可信插件 `asterhand-workspace` 通过公开 `api.registerTool` 拥有全部十一项工具的执行包装，无需修补 `node_modules/openclaw`：文件与进度工具在 `execute` 内委托 `openclaw/plugin-sdk/agent-harness` 的 `createOpenClawCodingTools`（每次调用注入词法绑定 `callId` 与在途计数的独立 `SandboxFsBridge`，且仅对只读/等待动作绑定调用级 `AbortSignal` 而不中断 `'cancel'`/`'end'` 清理），记忆工具委托 `openclaw/plugin-sdk/memory-host-search` 的 `getActiveMemorySearchManager`，配置层通过 `tools.updatePlan: false` 与 `tools.deny`（加入 `"exec"`、`"process"`、`"code_execution"`、`"skills_read"`、`"memory-core"`）彻底屏蔽内置原始工具，工作区控制桥鉴权令牌通过 `${ASTERHAND_WORKSPACE_TOKEN}` 注入子进程环境而不落盘；`exec_command`（新增可选 `escalated`、`escalationReason` 与 `externalPaths`）与 `process_control` 均经受认证本地 HTTP 控制桥（`"exec/start"`、`"exec/approval_wait"`、`"exec/poll"` 走 `waitSlots`，`"exec/kill"` 走 `controlSlots`，`"exec/control"` 走 `ioSlots`）交由 Go `workspace.Service` 统一经纪调用 `internal/platform/sandbox`，禁止在持有模型 API key 的 Gateway Node 进程内直接 `spawn` 脚本子进程（其中 `"exec/start"`、`"exec/approval_wait"` 与 `"exec/poll"` 分片最长 `2.5` 秒并走独立 `waitSlots` 避免阻塞控制流或触发 `45s` 写超时）。`chatFingerprint` 仅哈希 `LocalToolPolicy.{Enabled,Revision}`，确保通过 `WorkspaceTrustRuntime.SetWorkspaceTrustPolicy` 切换 `standard`/`turbo` 或移除信任规则时毫秒热生效而不触发 Gateway 重启。原任务 `Executor.Execute`、任务 token 与 create-only 授权目录保持独立。

节点内共享资源调度器按 `(rootID, relPath)`、范围（`exact/children/subtree`）与 `S/X` 模式协调轮内并发，详见[细粒度并发设计](../development/features/workspace-tool-concurrency/design.md)与[ADR-007](adr/007-workspace-resource-scheduling.md)：
- **两阶段封存、同批盲写冲突拦截与公平队列**：`before_tool_call` 只按批内顺序登记调用身份与输入指纹，不阻塞等待锁；各工具 `execute` 在同一微任务链收齐最终参数并一次原子封存整批声明（同一批次内若已有前序修改命中同一路径，后续盲覆写 `write_file` 直接以 `conflicting_batch_write` 拒绝，避免静默吞掉前序修改；有序管线与跨批次覆写保持允许），再一次申请全部资源与容量名额（最多 8 个活动调用、4 个修改调用、1 个记忆索引同步、64 项排队），冲突资源按批内顺序公平等待，不相交资源并行执行。
- **句柄隔离、原子预算与排空收尾**：每次低层文件/目录操作均校验精确调用身份、已授予范围与允许的效果模式；Windows 纯读采用独立句柄与 `FILE_SHARE_READ`，修改保持相对父句柄打开、前像保存与排他新文件发布；容量采用原子预留账本，版本清理固定活跃前像引用。本地 HTTP 桥保留独立控制名额；取消、超时或进程退出必须等待全部在途子 I/O 排空并完成结果持久化（或转入 `unknown` 封锁新增修改）后才释放资源锁与轮次租约。
- **受限记忆枚举与代次同步**：记忆文件枚举限制在 `MEMORY.md`、`USER.md` 与 `memory/`，纳入 `memory_sources` 与 `memory_index` 资源域，使普通工作区修改可与 `memory_search` 并行，而同批先写记忆后搜索严格等待并命中最新索引代次。
- **受限脚本进程调度、四级停止与事后对账**：前台 `exec_command` 在同步等待窗（`<= min(timeoutMs, yieldMs)`）内持有 `DomainFS("", subtree, X)` + `DomainMemorySources("sources", exact, X)`，一旦超出 `yieldMs` 转入后台即持久落盘 `<journal>/es_*.json`（`"started"`）并交由独立 `execManager`（单轮最多 `8` 个并发会话）托管并释放 `DomainFS` 锁，放行同轮只读、控制与其他脚本会话，同时由调度器 `exec_active` 写屏障延迟并发受管文件修改直至脚本退出（`internal/workspace` 按命令策略评估 `exec_policy.go`、审批前快照与 TOCTOU 复验 `exec_verify.go`、无状态命令词法解析与执行目标提取 `exec_command_parse.go` 职责分层）；`process_control` 仅声明 `DomainExec("control", exact, S)` 实现后台轮询、日志与终止；取消（`CancelTurn` / `CancelCall`）、轮次收尾（`ReleaseTurn(turn, true)`）与进程退出（`RuntimeStopped`）在 `s.mu` 锁外并行终止进程树、有界排空、清理 `<journal>/sandbox-temp`（清除只读位）并执行 `reconcileAfterExec`（刷新 `publishGen`、巡检 `256 MiB / 10,000` 项上限并触发全轮超限终止、记录意外 reparse point 并保护已有文件恢复触发 `revision_conflict`）。

## 多 Coding Harness 本地中介与协作（Milestone 1—2 与 Milestone 3 适配器已通过用户验收；角色模板与 Milestone 4 规划中）

本节对应[多 Coding Harness 中介计划](../development/features/multi-harness-broker/plan.md)与 [ADR-008](adr/008-multi-harness-broker-and-native-adapters.md)，明确新增 `internal/harnesses`、`internal/workers` 与 `internal/automation` 的单一权威交界：

- **职责边界**：`internal/harnesses` 唯一拥有只读发现、按 `(harness × version × platform × access_mode)` 验证的能力矩阵、三开关配置、免登记项目路径规范化/敏感目录黑名单及外部注册期望/观察对账；`internal/workers` 唯一拥有按规范化路径派生的项目内 Worker 身份（`(workspace_id, alias)` 唯一）、单 Worker 活跃轮次门禁（并发返回 `worker_busy`）、原生会话/轮次三元组映射、解析后工作树根写互斥、长结果与日志有界存储及空闲进程回收（服务层按核心写入口 `service.go`、启停与常驻进程生命周期 `service_lifecycle.go`、单轮执行驱动 `service_runner.go`、对账与原子事务提交 `service_reconcile.go`、只读查询与等待 `service_query.go` 高内聚组织）；`internal/automation` 统一负责受限主体、全路径闸门、收据与等待预算。两者不修改现有 `Executor.Execute` 任务协议或 OpenClaw 持续会话路径。
- **官方原生双向协议与垂直闭环顺序**：`HarnessAdapter` 诚实声明防护级别（`os_sandbox_enforced` / `harness_tool_restricted` / `unsupported`）；Milestone 1—2 以纯 Go 直连 `codex app-server`（JSON-RPC 2.0 over stdio，基于 `generate-json-schema` 校验 `thread/start`、`thread/resume`、`thread/read`、`turn/start`、双 ID `turn/interrupt`、发前 `client_correlation_id` 关联匹配、按 `itemId`/`final_answer` 组装正文，并在首次建立线程时以 `NativeDefaultsCaptured` 持久化记录 `thread/start` 初始有效 `model`/`reasoningEffort`，含未显式配置强度时的原生自动空值；后续省略轮次按需在 `turn/start` 显式恢复，原生自动强度则保留当前协作模式与指令并通过 `collaborationMode.settings.reasoning_effort = null` 重置并同步本地缓存，避免 `turn/start` 覆盖延续到后续轮次）跑通只读与受控 `workspace_write` 并补齐四家发现、稳定转发入口与三开关矩阵；Milestone 3 已按同一接口接入 `ClaudeAdapter`（`claude -p --verbose --input-format stream-json --output-format stream-json --strict-mcp-config`，预绑定 `--session-id` + `--resume`，Windows 声明 `harness_tool_restricted`）与 `DeepSeekAdapter`（`dsh --profile acp` 标准 Agent Client Protocol v1 stdio JSON-RPC，`DSH_PERMISSION_MODE` + `mcpServers: []` 防递归，会话分别维护 ACP 原生默认配置、Worker 默认配置与当前会话激活配置，首次建立原生会话时将 `NativeDefaultsCaptured` 与原生默认模型/推理强度随 Worker 会话绑定持久化，跨空闲回收或重启执行 `session/resume` / `thread/resume` 时绝不以恢复出的上轮临时覆盖覆写或误回填初始默认值，每轮按“单轮覆盖 → Worker 默认 → 原生默认”独立解析模型与推理强度并仅在有效值变化时显式切换，确保单轮临时覆盖不延续到后续省略参数的轮次；对升级前已存在且未采集初始默认值的旧 Worker 仅凭其显式 Worker 默认配置生效、不从 `session/resume` / `thread/resume` 猜测回填，历史无法还原时需显式设置 Worker 默认值或新建 Worker，Windows 声明 `harness_tool_restricted`）；Antigravity 静态声明 `worker_supported = false`。
- **安全、预算、停机与恢复边界**：零第三方凭据读取或整文件备份；`workers_send` 为纯写立即返回 `turn_id`（`workers_create`、`workers_send` 与崩溃恢复将域内去重键 `OperationKey = Identity.NativeID()` 持久化在不可覆写的 SQL 索引列，同时在对外返回的 `Worker`/`Turn` 元数据保留原始公共 `operation_id` 供 `workers_get` 后直接用于 `operations_get` 并记录 `actor_id`，确保不同调用方使用相同裸 `operation_id` 互不碰撞，旧裸 ID 仅在领域记录自身携带可持久核验的 `actor_id`/`caller_id` 时复用，绝不依赖可被保留期清理的公共日志推算归属；接入 `reconcileAutomationOperation` 修复轮次落盘与公共收据间的崩溃窗口；派发前通过 `Store.SaveTurnAndWorker` 原子落盘初始 `WriteLockRoot`/`WriteLockKey` 与 Worker 归属；尚未发出 prompt 的前置启动失败（含三家适配器在写帧前即发现常驻会话已关闭或编码失败的 `ErrPreStartFailed`，经 `ForceKill` 确认进程停止后）直接记 `failed`（`adapter_start_failed`）并淘汰失效常驻句柄，不误锁 `reconciliation_required`（若 `ForceKill` 无法确认停止仍保留对账与写锁，写帧后丢失回执仍转 `turn_start_unacknowledged`）；每次接纳 `workspace_write` 轮次均在 `Service.mu` 临界区内实时重算新旧双方的 `WorktreeKey` 并校验双向目录包含关系以拦截建仓漂移与父子目录重叠写），等待走独立 `workers_wait` / `workers_wait_any`（上限 `30s`，返回 `elapsed_seconds`/`idle_seconds`/`stream_events`/`stream_bytes` 实时活跃遥测、结构化 `next_step` 引导、严格隔离思维链的有界 `commentaries` 中间反馈与 `after_commentary_seq`/`wake_on_commentary` 提前唤醒，及非成功终态下的 `after_vcs.changed_files` 提示），对支持原生运行中插话的 Harness（Codex `turn/steer`、Claude Code `stream-json` 运行中 `user` 帧；DeepSeek ACP v1 诚实标 `supports_steer: false`）开放 `workers_steer` 注入指令并记录 `steer` 日志（三家 Go 适配器均按“会话传输 `*_adapter.go` + 协议解析 `*_protocol.go`”高内聚拆分），同时施加节点并发上限（默认 4）、单轮墙钟时限（默认 30 分钟，单轮可覆盖至 60 分钟）与无事件超时（默认 10 分钟，由三家适配器全量流式 delta/chunk 心跳续期，并由同轮全部未解决 `request_id` 集合推导等待态、剩余队首进度文本与暂停计时，仅最后一项解决后才恢复 `running` 与计时），取消/超时/停机均经运行器统一裁决停止结果（`codexSession.close` 先执行 `proc.Kill()` 再以 `LocalClose`/`StopErr` 结束活跃通道，DeepSeek `readLoop` 本地断流在 `rpcReply.localClose` 上标记并在 `blockedCode` 分支同样透传 `LocalClose`；执行循环收到 `LocalClose` 或 `StopErr` 时一律按实例摘取失效常驻句柄，同时继续依据 `StopErr` 保留旧轮次写锁，避免对账或后续派发复用已关闭会话；`Service.Close` 超时时发 `signalForceKill` 让运行器执行 `ForceKill` 并先落盘再返回 `ErrProcessStopUnconfirmed`），`Kill()` 独立核验根进程退出与 Windows `JobObject` 终止/活跃进程清零（`QueryInformationJobObject(JobObjectBasicAccountingInformation)`），无法确认停止时转入 `reconciliation_required`（`process_stop_unconfirmed`）并持久化占锁范围与原因（`WriteLockRetained`/`WriteLockRoot`/`WriteLockKey`/`WriteLockReason`），服务启动接纳新任务前在 `markInterruptedOnStartup` 重建未解决的保留锁并保持派发时持久化的 `WriteLockKey` 不被嵌套建仓缩小（仅对缺失历史键的旧记录回填；中断的非终态写轮次亦保守保留锁）；所有状态迁移与自动解锁路径（含 `ReconcileTurn` 的撤权对账与 `ResolveTurn`）均通过 `commitTurnAndWorker`/`commitTurnAndWorkerLocked` 在单一 `Service.mu` 临界区与 SQLite 事务内校验 `cur.State == TurnReconcile` 与 `cur.ActiveTurnID == t.ID` 并同步完成常驻进程续期或摘取（锁外仅经 `closeDetachedResident` 关闭已摘取实例），常驻进程打开与登记经 `registerResidentLocked` 分配单调 `ThreadHandle.instanceID` 并在 `sameHandle` 中优先按实例 ID 比较（原始句柄回退 `reflect.Value.Comparable()`/`Equal` 防动态不可比较字段 panic），迟到对账绝不覆盖人工结论或覆盖/中断新轮次常驻进程；写库失败仅在运行器（`rt != nil`，含传入 `rt` 的 `turn_start_unacknowledged` 路径，仅当停止未确认时保留工作树写锁）首次持久化失败时记录 `pendingReconcile` 上下文并向上传播存储错误（已落盘为 `reconciliation_required` 的轮次在对账终态提交失败时直接返回错误并保留原对账记录与写锁范围不变），`CloseWorker` 在同一 `Service.mu` 临界区内重读 Worker、校验无活跃轮次及保留锁/待落盘对账并持久化 `closed` 后再于锁外回收已摘取进程，存储恢复后可由 `Cancel`/`ReconcileTurn`/`ResolveTurn` 经单临界区 `flushPendingReconcile`（以非终态且非对账态前置条件保护 Turn 与 Worker 整体提交，遇终态或已对账直接消费旧记录）在线完成落盘与解锁；终态保存（`finalizeTurn`/`finalizeRevoked`）合并至最新持久记录以保留增量 `Progress` 与 `LatestCommentarySeq`；异常终态自动淘汰常驻子进程防脏会话残留；`Workers` 接入 `App.RequestStop(confirmed)` 停机链，全部后台协程纳入 `WaitGroup` 并在全部退出后再关闭 SQLite `Store`（同时 `Store` 具备 `ErrStoreClosed` 守卫，防止超时场景下提前关库）；吊销整份凭据与仅关闭 `collab_caller_enabled`（保留剩余有效工具集并按当前权限过滤 `operations_get`）严格区分，撤销授权与普通 `Cancel` 均以可取消 `turnCtx` 贯穿 `OpenThread` 与 `StartTurn`，各适配器将编码、取会话锁与请求登记置于最终取消检查之前并在最后写入 stdin 前同步裁决，杜绝取消后仍写出任务帧；仅对 `completed` 成功轮次发布最终正文，中断/失败的 `partial_answer` 仅作部分输出；受管 Worker B 优先原生侧禁用 `asterhand` MCP 并辅以服务端 `max_delegation_depth = 1` 兜底；审批与人机协作层接入正交 `approval_mode`（`auto` 默认 / `ask_human` / `strict_deny`）、三家原生自动审单/宿主双向审批与提问桥（Go 侧在 `Service.mu` 同一临界区内仲裁人工决定、主控回复、取消与超时的唯一赢家并负责 `SessionAutoApprove` 授予与撤销持久化，人工授予先检查未取消且写库成功再 `claim`/`deliver`，写库失败保留原待处理请求，冲突/失败绝不残留免审；原生回调仅消费已裁定赢家结果不再回写；Codex 固定单次 `accept`/`scope:"turn"`，DeepSeek 严格按 ACP `kind` 精确选择 `allow_once`/`reject_once` 不升级 `allow_always`）、统一硬底线规则引擎、`request_id` 幂等人工仲裁器、含 `is_secret` 提问直达人工 UI + 遮蔽输入与秘密选项隔离绕开普通草稿 + 持久日志脱敏，以及 `workers_reply`（通过 `ReplyTurnWithReceipt` 支持可选 `request_id`，直接响应与持久收据摘要 `Outcome.Summary` 均精确保存目标 `request_id`/`escalate_at` 以支持同 ID 重放与 `operations_get` 补读、A 代答、读取后 30s 缓冲、`defer_seconds` 核实续期与附带 `note` 显式升级人类）；节点重启不自动续跑未终结轮次，统一转入人工可核对状态。
