# 日常操作覆盖清单

下列操作通过同一注册定义提供 CLI 与 MCP。机器可检查的对应关系在[覆盖登记](../../contracts/automation-coverage.json)，输入输出以 `asterhand automation schema` 为准；人工例外和纯展示等价仍需逐项审查。实现与实测状态见[验证说明](../development/features/agent-automation/validation.md)。

## 覆盖判定

- 业务操作：产生相同合法业务结果并能读取同等信息。
- 操作组合：agent 可以一次提交人类分多次完成的操作；保留明确的失败和原子性范围。
- 展示等价：窗口、滚动、菜单等动作由结构化读取、参数或 agent 自有草稿提供等价能力，不要求接管人类窗口。
- 人工例外：涉及认证、秘密或权限管理；必须给出具体原因和人工入口，不能用“设置”整类排除普通操作。

## 命令、图与日志

| 人类操作 / 已有入口 | 公共操作或等价行为 | 必须保留的规则 |
|---|---|---|
| Web 命令列表、快捷输入 `/` 搜索主名/别名 | `commands.list/search/get`，分页摘要及单条完整定义 | 名称/别名搜索排序一致；完整命令不塞进列表；缺失脚本不隐藏 |
| 新建、改名、别名、描述、删除 | `commands.create/patch/delete`；新增 `clone` | 唯一性、对象修订、运行锁；删除不删除外部脚本 |
| Web“管理”内多选、全选全部命令、批量删除（管理切换仅控制显示） | `commands.list` 遍历分页固定 ID/修订；`batches.submit` 组合 `commands.delete`，`batches.get` 查逐项结果（CLI/MCP 同 schema） | 每批最多 32 项，可顺序分块；对象修订、运行锁与逐项收据；部分成功，不回滚；未知结果先核对原身份 |
| 添加/删除/重命名步骤、选择脚本、参数、工作目录、步骤超时 | 整图替换或一次 `commands.patch` 中的步骤操作 | 稳定步骤 ID、参数空项、平台路径规则；允许缺失文件配置 |
| 连接/断开依赖、调整步骤顺序、并行数 | 同一图修改中的边/顺序/属性操作 | 最终图校验一次，中间不完整图不写入正式配置 |
| 拖动布局、命令并发开关、整图超时、执行结束后通知 | 显式坐标、自动布局及命令属性修改，通知字段为 `notify_on_completion` | 布局不改变依赖；保留已有坐标和执行限制；通知字段缺省与显式 false 区分 |
| 图撤销/重做、保存、放弃修改 | 自有候选快照/修改序列、`validate`、显式保存 | 可回到任一未保存草稿；恢复已保存内容需新写入及修订检查；不操作他人的撤销栈 |
| 续改未保存的图、普通表单、快捷聊天和任务回答 | `drafts.list/get/update/commit` | 按业务目标和窗口 ID 操作，检查原修订，复用已有保存/发送，见[编辑器接口](draft-handoff.md) |
| 脚本文件选择窗口 | 直接设置本机脚本绝对路径，运行时复核可用性 | 无需弹窗；不扩大为任意目录浏览、脚本正文写入或上传 |
| 快捷列表点击/Enter 启动 | `commands.run` | 同一配置修订、普通用户执行、返回 run ID、稳定请求去重 |
| 当前轮实例、最近结果、步骤状态 | `runs.list/get/wait` | 实例身份、接纳顺序、真实退出/收尾；新增保留历史明确容量 |
| 停止单次、停止当前全部 | `runs.stop` / 显式 run ID 集合 | 不误停新实例；停止某步骤所属实例仍是停止整图 |
| 日志全部/运行中/自选、多分屏 | 多实例/步骤过滤及批量状态读取 | 不只过滤当前页；每个游标独立，完整性可见 |
| 更早/后续/回到最新、跟随、编码、复制日志 | `logs.read/tail/wait`，文本/原始字节、编码及游标 | 跨帧/页解码一致，空页可推进，旧记录过期可区分 |
| 打开日志或命令配置窗口 | 可选 `desktop.open` 固定对象入口；业务访问用上述接口 | 不返回登录 URL/token，不因读取抢焦点；无桌面仅此展示操作不可用 |

## 聊天、任务与节点

| 人类操作 / 已有入口 | 公共操作或等价行为 | 必须保留的规则 |
|---|---|---|
| 新对话、历史会话列表、切换会话 | `conversations.create/list/get` 与显式 conversation ID | 目标/角色/runtime 绑定不变，不改变人类当前选择 |
| 普通文本发送、命令样式“作为文本发送” | `conversations.send`，显式纯文本模式 | 后者复用 UI 的文本转义语义，不把 `/` 当 runtime 管理命令 |
| 查看等待/未送达/待确认/失败、停止回复 | `turns.get/wait/stop`，按原 operation 查询 | queued/accepted 分开；只停止指定已接收轮次；后续队列继续 |
| 历史分页、完整正文、复制 Markdown/代码/公式 | `messages.list/get`，有界正文片段 | 按发生顺序返回文本/工具块（含工具定义声明的有界目标摘要 `summary`）；混合消息的文本段 ID 支持完整分片读取；注明截断/未保存，不泄露完整原始入参、输出或隐藏推理 |
| 打开面板观察连接、主动连接/重试 | `conversations.connection/connect` | 普通查询不发模型请求；恢复不重发消息 |
| 当前节点/目标节点选择、能力和在线状态 | `nodes.list/get/capabilities`，正式调用显式目标 | 返回更新时间，离线不替换目标，不把名称当身份 |
| Web 提交本机/远端文件任务或问答 | `tasks.submit` | 沿用 route/execute 和项目授权；不绕过原节点解释/提交规则 |
| 任务列表、详情、进度、事件、结果/产物证据 | `tasks.list/get/events` | owner 与 outbox 对应，缓存新鲜度、序列与水位保留；不承诺 UI 未有的任意文件下载 |
| 回答任务问题、取消任务、核对关闭 | `tasks.answer/cancel/reconcile/resolve` | 准确 attempt/interaction；代理处置记录主体和证据，人工授权/验收另行处理 |
| 节点名称、语言、主题、快捷键启用/修改/恢复默认 | `settings.get/patch`，热键检查 | 普通字段白名单、修订保护、语言不触发模型或重建会话 |
| 录制快捷键 | 直接提供规范化按键组合并检查冲突 | 产生相同持久设置；无需模拟实体按键 |
| 快捷输入设置中的“重试”（宿主/热键恢复） | `desktop.retry`，复用控制器的显式恢复 | 按原配置恢复，不重置设置或自动启用；无交互桌面明确不可用；与设置、自动恢复和退出串行处理，失败返回实际状态 |
| Web 设置“通用”页“显示桌面图标”即时开关 | `settings.get`（含 `desktop_shortcut`）+ `settings.desktop_shortcut` | 与 Web 共用 `ShortcutManager`，以当前用户桌面 `Asterhand.lnk` 实际归属为唯一事实源；受启动锁保护，未安装态与外部冲突保留原文件并明确报错 |
| 在已配置模型间选择、查看诊断、显式模型工具测试 | `models.list/select`、`diagnostics.inspect/run` | 已有授权凭据、busy 保护；显式模型测试说明额度影响 |
| 启动器启动、托盘/Web 退出、状态/关于 | `node.start/stop/status/version` | 复用安装定位与统一收尾；忙时停止须显式表达停止活跃工作/丢弃待发内容的意图 |

## 人工例外与纯展示操作

| 操作 | 分类与原因 | Agent 可以取得的信息 |
|---|---|---|
| 初次登录、凭据和节点信任管理 | 人工例外 | 原设置入口处理，不通过普通自动化工具导出秘密 |
| 模型 key 新建/替换/删除、凭据导入导出、证书/加密设置 | 人工例外：秘密与信任管理 | 配置是否齐全及脱敏诊断 |
| 配对码、配对/撤销、授权目录/项目范围修改、监听和信任配置 | 人工例外：改变执行范围或对外访问 | 已授权节点/项目与可达性，不擅自扩大范围 |
| 人工批准新增权限、声明用户已验收 | 人工例外：需真实人工身份 | 待处理原因及状态；普通业务问答不因此排除 |
| 窗口隐藏/展开/拖动/最小化、画布缩放/全屏、菜单与任务栏切换 | 展示等价：不改变业务对象 | 独立读取目标对象和完整内容；持久图坐标仍在业务覆盖内 |
| 文本输入、光标、IME、选择/复制 | 展示等价：结构化文本和快照 | 保留人工输入行为；正在组字时等待组合结束再操作。 |

安装、升级、Tailscale 登录及系统依赖管理不属于日常自动化首期；普通启动/停止已经纳入。新增人类能力必须先分类；不能把已有业务操作重新命名为纯展示来规避覆盖。

## 验证与扩展

### 命令完成通知

[本地命令完成通知](../development/features/local-command-completion-notifications/plan.md)通过 `notify_on_completion` 接入命令创建、读取、校验、局部修改、复制以及命令草稿的 CLI/MCP schema；新建与现有缺失字段默认 true，局部更新省略字段保持原值，显式 false 保留，null 拒绝。通知卡片、动画、关闭和排队为展示操作，运行结果与日志通过 `runs.list/get/wait`、`logs.read` 等价读取；`settings.get.notifications` 读取展示可用性及队列数量。专项测试与平台边界见[验证说明](../development/features/local-command-completion-notifications/validation.md)。

覆盖依据来自[Web 需求](../web/requirements.md)、[会话设计](../assistant/conversations.md)、[命令设计](../executors/local-commands.md)、[脚本图设计](../executors/script-graphs.md)、[任务设计](../tasks/design.md)和[安装设计](../installation/design.md)。[注册入口](../../internal/app/automation.go)按领域组织公共操作，[CLI](../../internal/automation/cli.go)与[MCP](../../internal/automation/mcp.go)消费同一组定义；行为测试按[覆盖登记](../../contracts/automation-coverage.json)关联。[Go 守卫](../../internal/app/automation_coverage_test.go)对比实际注册与登记、检查输出 schema，[UI 守卫](../../tests/automation-coverage.test.mjs)检查 Web 端点和原生闭合动作的新入口。这些静态检查不能证明动态 UI 的业务语义，reviewer 仍须逐行核对本表。

本期验收要求上述本机日常操作及现有远端任务全部覆盖。跨节点持续聊天、远程图操作另建 feature；内置助手工具按下节的独立工作区身份接入，沿用同一操作语义并扩展目标能力；它们不作为本期覆盖率的分母，也不得被宣传为已交付。

## 本机持续会话工具能力

以下操作已注册并同步机器 schema、contracts/automation-coverage.json 与行为测试。它们使用 workspaces scope；活动与产物还检查 conversations scope。外部客户端权限与内置助手权限分开，启用/撤销只能使用“设置 → 助手”的人工开关。日常 Web 不展示文件、搜索、版本和恢复面板，相关公共 CLI/MCP 操作完整保留。实现和验证边界见[本机工具计划](../development/features/local-runtime-tools/plan.md)。

| 人类操作 | 公共操作 / CLI 与 MCP 等价 | 规则 |
|---|---|---|
| 查看工作区与有效能力 | workspaces.get、conversations.capabilities | 只读，区分支持/授权/就绪/约束，不暴露凭据或管理路径 |
| 浏览目录、搜索文件/内容、读取文件 | workspace_files.list/search/read | 同一工作区根、分页/预算、真实身份与权限检查 |
| 聊天中创建/修改/移动/删除文件 | conversations.send + 工具结果读取；直接批操作通过 workspace_files.mutate | 复用修改租约、前像、逐项结果、操作身份/修订；不接受任意宿主路径或命令字符串 |
| 查看工具活动与进度 | 聊天顺序用 messages.list；完整活动用 turn_activities.list/get（及 workspace_operations.get），加 turns.get/wait/stop | 聊天与活动记录包含由工具定义生成的有界目标摘要 `summary`、工具名、状态和声明进度；完整活动仍检查工作区与会话授权；单调游标、真实终态、未知结果核对，不暴露完整正文入参或隐藏推理 |
| 查看/预览/下载产物 | artifacts.list/get/read | 同一授权 ID 和版本校验，有界分片，不允许任意路径下载 |
| 查看历史版本并恢复 | workspace_versions.list/get/restore | 修订冲突、操作去重、恢复前快照；无隐式回滚 |
| 处置确认停止的未知工具轮次 | workspace_operations.get/resolve | 记录核对依据；不能借处置伪造成功或重放修改 |
| 开启/撤销工具权限 | 人工例外；专用设置入口 | 改变执行范围；CLI/MCP 只读状态，模型不能批准自身权限 |

内置助手仅获已授权工作区的窄调用上下文；外部当前用户 CLI/MCP 原有能力保持，不将 commands.run、node 管理或全量自动化凭据注入内置模型。活动展示的展开/折叠为纯展示，等价读取是 turn_activities；文件恢复是业务操作，必须三入口一致。

### 工具轮内并发与活动阶段读取

[工作区工具细粒度并发](../development/features/workspace-tool-concurrency/plan.md)不增加独立调度设置页。`workspace_files.list/search/read`、`artifacts.read` 与内置助手工具共用同一资源调度器，与不冲突的模型工具并发执行并在目标冲突时公平等待；`workspace_files.mutate`、`workspace_versions.restore` 保留现有轮次门禁，进入后同样按目标登记资源与调用身份。`workspace.Operation` 同步暴露可选 `phase` 与 `wait_reason` 并接入 CLI/MCP 机器 schema，`turn_activities.list/get`、`workspace_operations.get` 提供等价等待与排空收尾信息，聊天消息时间线保持原生工具顺序。

### 本机工作区受限脚本与命令执行（第一阶段）

[工作区脚本与命令执行计划（第一阶段）](../development/features/workspace-script-execution/plan.md)由内置助手在既有“允许助手调用工具”权限控制下调用 `exec_command` 与 `process_control`，不引入新的人类独立对话框或设置开关，也不开放通过 CLI/MCP 绕过会话权限任意执行外部模型沙箱脚本的入口（人类本机预注册脚本继续由 `commands.*` 管理）。第一阶段新增的 `scripts.execute` 能力声明、`exec_timeout_seconds`/`exec_output_bytes`/`exec_sessions_per_turn`/`sandbox_temp_bytes` 限制以及 `exec_command`/`process_control` 工具活动摘要和收据均直接通过既有公共操作（`workspaces.get`、`conversations.capabilities`、`turn_activities.list/get`、`workspace_operations.get`、`messages.list`）保持 UI/CLI/MCP 一致读取；开启/撤销权限继续按人工例外规则保留在“设置 → 助手”。

### 受控提权确认、极速免审与信任清单管理（第二阶段）

[受控提权脚本与外部目录执行计划（第二阶段）](../development/features/workspace-script-escalation/plan.md)扩展人工例外边界如下：
- **只读与状态一致性（UI/CLI/MCP 等价读取）**：`workspaces.get` / `conversations.capabilities` 返回只读 `execution_mode`（`standard` | `turbo`）、`trust_revision`、`trust_rule_count` 及用户可读的 `allowed_items`（`{id, kind, label}`），`turn_activities.list/get`、`workspace_operations.get` 与 `messages.list` 暴露 `awaiting_approval` 状态、`approval` 脱敏元视图与 `approval_mode` 审计结果；
- **人工例外（不开放模型或普通 CLI/MCP 写入，按 `AUTO-19`/`AUTO-20` 保留）**：原生对话动作 `approve_tool`（在聊天内联卡片批准或拒绝待确认票据）与 `/api/workspace/permission` 的 `execution_mode` / `remove_rule_id` 修改均登记为 `"human"` 人工例外，防止模型或未受人类直接确认的脚本扩大执行边界或自行切入无审批模式。

## 多 Coding Harness 发现、托管接入与协作

对应[多 Coding Harness 中介计划](../development/features/multi-harness-broker/plan.md)与[验证说明](../development/features/multi-harness-broker/validation.md)：全仓公共自动化操作名已统一采用 `[a-zA-Z0-9_-]` 下划线规范名（取消点号与边界映射）；Milestone 1—2 的全部 `harnesses_list/get`、`workers_*` 公共操作以及 `/api/harnesses*`、`/api/workers/*` 端点分类已落地并同步登记至 [contracts/automation-coverage.json](../../contracts/automation-coverage.json)；`batches_get` 与 `batches_submit` 成对归属 `general` 工具集；Milestone 4 的人工审批入口随对应里程碑交付：

| 人类操作 / 入口 | 公共操作（CLI 与 MCP 等价） | 分类与规则 |
|---|---|---|
| 创建、查询、纯写派发、有界等待（含多轮次 `workers_wait_any`）、分页与尾部读正文/日志、精确取消、关闭与人工核对协作 Worker | `workers_create/list/get/send/wait/wait_any/result/logs/cancel/close/resolve` | 归属 `collab` 工具集；写操作统一使用 `operation_id` 幂等收据、单 Worker 活跃锁与规范化工作树写互斥；`cancel` 必填 `turn_id` |
| Web「Coding Workers」面板查看各项目 Worker、轮次、工作树快照与实时日志、停止/核对/归档（`/api/workers/*`） | `workers_list/get/wait/result/logs/cancel/close/resolve`（Milestone 1—2 已实现） | 业务状态、日志分页、工作树快照、取消、归档与核对关闭完全等价；界面滚动/筛选/展开属展示等价 |
| 修改 Harness 权限开关（含 `allow_workspace_write`）、手工路径覆盖、执行外部 MCP 配置接管/重新接入 | 无（Web「设置 → Coding Harness」`/api/harnesses*` 人工专用入口，Milestone 1—2 已实现） | **人工例外**（`AUTO-19`）：修改本机外部工具全局配置与协作权限边界，禁止开放给模型自动写入；日常任意常规本地项目免逐项目登记直通，敏感系统与凭据目录由服务端内置拦截 |
| 查看全部已发现的 Coding Harness、版本、能力矩阵、三开关与期望/观察 MCP 托管状态 | `harnesses_list`、`harnesses_get`（Milestone 2 已实现） | 归属 `collab` 工具集，公共只读查询，不含凭据或内部 token |
| 批准或拒绝被控 Harness B 的高危权限请求（Milestone 4 待实现） | 无（Asterhand 原位人工确认卡片/桌面入口） | **人工例外**（`AUTO-20`）：绝不开放给发起方 A 的模型代批 |
