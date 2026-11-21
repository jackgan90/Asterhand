# 自动化、CLI 与 MCP 需求

AUTO-NTF-01：`commands.create/get/list/validate/patch/clone` 与命令草稿支持布尔字段 `notify_on_completion`，创建默认开启、编辑缺省保留、显式 false 保持关闭，null 和错误类型拒绝。UI、CLI、MCP 共用注册、运行接纳快照和终态保存规则；卡片关闭和动画是展示行为，业务结果通过 `runs.get/wait`、`logs.read` 等价读取。`settings.get.notifications` 返回展示可用性和当前队列数量。

状态：公共操作与 Windows CLI/MCP 已实现，工程验证、独立审核及用户验收已完成。本文为持续有效的需求，实施范围见[统一自动化计划](../development/features/agent-automation/plan.md)，验证方法与专项环境边界见[验证说明](../development/features/agent-automation/validation.md)。

## 目标与覆盖边界

在本机当前用户已有访问权的范围内，外部 agent 能通过 CLI 或 MCP 完成 Asterhand 的全部日常业务操作，并能批量处理。覆盖比较业务结果和可观察信息，不比较鼠标、键盘或窗口操作次数。同一业务操作由人类、CLI、MCP 调用时使用相同的领域规则；参数、呈现和组合方式可以不同。

“日常操作”包含命令和脚本图管理、执行与日志、持续会话、现有本机/远端任务、节点查询、普通设置及日常启动/停止。认证、凭据、证书、加密配置、配对和授权范围管理保留人工操作。当前尚未实现的远程持续聊天、远程脚本图执行、内置助手调用自动化工具、交互终端和定时调度不因本模块而自动成为已支持能力。

## 功能需求

| ID | 要求 |
|---|---|
| AUTO-01 | 日常业务结果与可读取信息覆盖现有 UI；每个人类入口在[覆盖清单](coverage.md)登记公共操作、组合、等价读取或人工例外，不能只统计 HTTP 路由 |
| AUTO-02 | CLI、MCP 与人类入口复用节点内的领域服务；权限、校验、资源限制、执行、停止和恢复只有一份业务实现 |
| AUTO-03 | 每个可委托公共操作同时提供 CLI 和 MCP；工具/命令描述、输入输出 schema、机器错误码与协议版本有统一定义 |
| AUTO-04 | 发现当前节点身份、版本、支持能力、调用者可用操作、当前就绪条件及资源上限；发现和普通查询不暗中调用模型 |
| AUTO-05 | 命令支持分页搜索、单条读取、新建、复制配置、修改、删除；保留名称唯一、脚本路径/参数规则及运行中禁止修改的现有约束 |
| AUTO-06 | 图支持整图提交及按稳定步骤 ID 的批量局部修改，覆盖节点、边、顺序、坐标、脚本属性、并行数和超时；一次修改统一校验并原子保存，支持只校验/预览 |
| AUTO-07 | 执行固定命令修订，返回精确 run ID；支持逐实例状态、等待、停止和显式实例集合的批量停止；相同启动意图不产生第二次运行 |
| AUTO-08 | 读取保留期内的运行结果、步骤状态和日志，支持实例/步骤/流筛选、编码、分页、增量和完整性信息；文本可直接使用，原始字节作为可选格式 |
| AUTO-09 | 复用会话服务创建/列举/读取会话、发送文本、读取队列/收据/轮次、等待和停止回复；正文权威、内存队列与持久接收的区别保持不变 |
| AUTO-10 | 覆盖现有本机/远端任务提交、查询、事件、产物证据、普通问答、取消及状态核对；保留 owner、attempt 和请求身份，不将模型回复当执行证据 |
| AUTO-11 | 覆盖设备状态、普通名称/语言/主题/快捷键、受支持平台的桌面图标显示开关、按原配置重试快捷输入宿主与热键、已有有效模型选择、诊断与启动/停止；普通设置只能修改声明的字段，不连带改变凭据、监听安全或授权目录 |
| AUTO-12 | 提供多个 ID 查询、图批量编辑和有界批量执行；逐项返回成功、失败、未执行或未知及对应身份，不暗示跨操作副作用可以统一回滚 |

## 可靠性与权限需求

| ID | 要求 |
|---|---|
| AUTO-13 | 写操作使用稳定操作身份与规范化参数摘要；身份作用域包含可信调用主体和目标节点，相同身份不同内容拒绝；未知结果先查原请求，不自动换 ID/目标重做 |
| AUTO-14 | 接纳与业务对象的关联在副作用前可恢复地记录；启动回执丢失、进程重启、记录清理和请求过期均不得导致重复执行；不承诺外部副作用 exactly-once |
| AUTO-15 | 配置修改使用对象修订；同对象旧修订拒绝覆盖，不同命令可独立修改；跨对象约束仍由领域服务串行检查 |
| AUTO-16 | 长操作的接纳、执行、保存和观察分开；CLI/MCP 断开或取消等待不取消已接纳工作，显式停止仍需核对实际终态；节点退出沿用现有收尾 |
| AUTO-17 | 结果摘要、请求收据和大日志分别有明确、有限的保留策略；过期可诊断，运行中及恢复证据不能被正常淘汰，容量不足在新副作用之前拒绝 |
| AUTO-18 | 列表、响应、日志扫描、等待连接、队列及批处理均有字节/数量/时间预算；保留现有领域槽位，并为人类操作及停止请求留出服务能力 |
| AUTO-19 | 本机自动化按当前 Windows 用户和指定节点建立可信连接；认证、密钥、加密、配对等敏感操作留在原人工入口。 |
| AUTO-20 | 普通业务问答及有依据的状态处置可以委托，记录实际 agent 主体；需要新增权限或人工验收声明时返回需要人类处理，不伪造人工批准 |
| AUTO-21 | 普通操作保持人类原有页面和交互；历史、日志及错误有与 UI 相同的业务含义。 |
| AUTO-22 | 操作元数据可追踪主体、入口、目标、对象、请求和结果；默认不额外持久化聊天正文、完整脚本输出、敏感参数或凭据 |
| AUTO-23 | 旧版 UI、当前任务 peer 协议、运行数据、安装和正常退出按兼容策略验证；未知协议/schema 明确拒绝，不静默改写较新数据 |
| AUTO-24 | 新增/变更功能同时维护覆盖清单、公共操作、两种 agent 入口及等价行为测试；全部本期清单完成前不得声称日常能力已全覆盖 |
| AUTO-25 | 外部 agent 可直接读、改并提交打开窗口内未保存的普通草稿；按目标和修订定位，在途状态和敏感字段不被误提交。 |

## 多 Coding Harness 托管与动态工具集（Milestone 1—2 已交付并通过本机用户验收，Milestone 3—4 规划中）

以下需求对应[多 Coding Harness 中介计划](../development/features/multi-harness-broker/plan.md)与 [ADR-005](adr/005-harness-mcp-lifecycle-and-dynamic-toolsets.md)：Milestone 1—2 的工具集裁剪、全仓下划线规范名、`harnesses_list/get`、稳定 `asterhand-mcp.exe`、守护进程升级零断连热同步及 Antigravity/Codex/Claude 托管对账均已实现并通过本机用户验收：

| ID | 要求 |
|---|---|
| AUTO-HARNESS-01 | 每个已发现/配置的 Coding Harness 独立维护三项正交开关：`general_tools_enabled`、`collab_caller_enabled`、`collab_worker_enabled`，全部 8 种组合均有确定预期行为；分别维护期望状态与观察状态以支持可重试对账，外部 MCP 仅在 `general_tools_enabled || collab_caller_enabled` 为真时注册 |
| AUTO-HARNESS-02 | MCP 公共操作按 `core`、`general`、`collab` 工具集分组；每个受管 Harness 绑定可撤销的受限主体与有效工具集，统一作用于 `tools/list`、`Dispatch`、`batches.submit` 子项、`operations.get` 收据作用域及客户端本地 `node.start`，支持 `notifications/tools/list_changed` 刷新；无 `--caller` 兼容入口明确定位为本机全量入口 |
| AUTO-HARNESS-03 | `harnesses.list/get` 与 `workers.create/list/get/send/wait/result/logs/cancel/close` 统一提供 CLI 与 MCP、共用机器 schema 与幂等操作收据，并随对应里程碑实现同步登记至 `contracts/automation-coverage.json`；`workers.send` 为纯写接纳、`workers.wait` 上限对齐 `30s`、`workers.result/logs` 支持长正文与日志有界分页 |
| AUTO-HARNESS-04 | 外部 Harness 的 MCP 注册/反注册、冲突接管、路径覆盖、授权工作区与三开关变更按 `AUTO-19` 保留为 Web 专用人工例外；写入优先官方 CLI 或单键原子修改，严禁整文件备份外部含凭据配置，发现非托管同名条目时拒绝静默覆盖 |
| AUTO-HARNESS-05 | MCP 工具响应在保留 `structuredContent` 的同时，须对 `core` 底座（`operations.new_id`、`operations.get`、`capabilities`）与全部 `harnesses.*`、`workers.*` 向 `content[].text` 输出面向发起方模型的有界结构化文本（包含 `operation_id`、`worker_id`、`turn_id`、状态、审查正文或分页/轮询指引），确保仅读取文本通道的客户端从首次生成 ID 到断线找回与读正文全链路不断链 |

总体业务架构见[全局设计](../overview/design.md)，公共操作与生命周期见[自动化设计](design.md)，治理要求见[开发治理](../development/requirements.md)。
