# 自动化、CLI 与 MCP 验证说明

状态：实现、工程验证、独立审核及用户验收已完成。已执行的工程检查覆盖 Go/Node 回归、Windows 构建、CLI/MCP 实际进程、Edge 页面联调与隐藏 WebView2 宿主。关联 AUTO-01—25 和[覆盖清单](../../../automation/coverage.md)，专项环境边界见下文。

## 环境与入口

首个目标为 Windows x64；工具链和固定运行时来自[依赖清单](../../../../packaging/dependencies.json)，常规命令见[测试指南](../../../../tests/README.md)与[脚本指南](../../../../scripts/README.md)。本机通道验证需要普通用户与管理员启动节点、错误用户/错误节点及节点重启场景。只操作隔离临时数据目录、无副作用脚本和测试会话，不复用真实个人配置或凭据。

以下矩阵定义应验证的行为；具体已接入测试见后面的源码索引。Go/Node 测试采用隔离目录和受控运行时，不使用个人数据或在线模型；故障注入不能替代真实停电/双机验收。Markdown 链接、覆盖关系及 `git diff --check` 也属于交付检查。

`go test ./...`、`go vet ./...` 和 `node --test tests/*.test.mjs` 为基础检查；公开文案变动运行 `pwsh -NoProfile -File ./scripts/check-i18n.ps1`。新增 CLI/MCP 测试必须实际启动发布构建中的客户端/适配进程并调用隔离节点，不能只直接调用 Go 服务方法。实际进程验证命令见[测试指南](../../../../tests/README.md#自动化climcp)。

## 行为与故障矩阵

| 用例 | 验证方法 / 已有源码入口 | 预期结果 |
|---|---|---|
| AUTO-V01 定义与覆盖 | 新增公共操作定义/CLI/MCP/覆盖清单的交叉检查；人工核对全部 UI 行 | 漏一个业务入口或 schema 漂移会失败；敏感和展示例外有理由 |
| AUTO-V02 本机身份 | 新增真实通道握手：错误用户、错误 node ID、伪同名服务、错误凭据和多连接 | 访问拒绝且无身份/队列泄露；重连不能绕过额度；管理员节点不使脚本提权 |
| AUTO-V03 图等价 | 基于[注册测试](../../../../internal/localcommands/registry_test.go)、[图测试](../../../../internal/localcommands/graph_test.go)，分别用 UI、CLI、MCP 创建和修改同一候选 | 归一化配置、错误字段、稳定 ID、参数空项、最终图一致；批量修改无部分保存 |
| AUTO-V04 并发编辑 | 两客户端改不同命令、同命令旧修订、同名新建、修改与启动竞争；[前端管理测试](../../../../tests/local-command-management.test.mjs) | 不同对象可成功，同对象冲突不覆盖；人类草稿/历史保留 |
| AUTO-V05 启动去重 | 在准备记录、索引公布、进程创建、回执发送前后注入中断；[并发运行测试](../../../../internal/localcommands/concurrent_runs_test.go) | 一个操作只关联一个 run；未知先核对；不同内容同 ID 冲突 |
| AUTO-V06 清理与过期 | 下一轮启动、删除命令、保留期/容量淘汰、收据过期、新请求时钟偏差 | 已接纳收据仍可查，旧 ID 不重新执行；过期/缺日志可见；活动恢复证据不被清理 |
| AUTO-V07 停止与观察 | 等待超时/断开、MCP 取消等待、显式停止、完成竞争、停止旧集合后新启动 | 观察结束不停止运行；旧停止不误伤新实例，终态仍由执行事实决定 |
| AUTO-V08 日志与规模 | [编码测试](../../../../internal/localcommands/log_encoding_test.go)、[节点日志测试](../../../../internal/localcommands/node_logs_test.go)、[前端图日志测试](../../../../tests/script-graphs.test.mjs)；新增三入口文本比较 | 中文/四种编码、跨帧/页、BOM、无换行、非法字节一致；游标推进、不漏帧且响应有界 |
| AUTO-V09 聊天收据 | [队列测试](../../../../internal/conversation/queue_test.go)、[恢复集成](../../../../internal/executor/conversation_recovery_integration_test.go)；新增公共客户端断开/重试 | queued 仅内存托管；accepted 以原生证据为准；未知不重发，正文不进入公共收据 |
| AUTO-V10 人机聊天 | UI/CLI/MCP 交错创建、发送、历史、长正文、斜杠纯文本、切换及停止 | 当前人类选择/草稿不变；显式目标不漂移；不会切换当前窗口的会话 |
| AUTO-V11 任务回归 | [双节点集成](../../../../internal/app/integration_test.go)、[准入](../../../../internal/app/admission_test.go)及新增 CLI/MCP 场景 | owner/outbox/attempt、去重/取消/问答/事件一致；处置不冒充执行成功或人类批准 |
| AUTO-V12 设置和敏感例外 | 新增普通字段 patch、模型 busy、错误字段/批处理绕过、授权撤销；[语言 API 测试](../../../../internal/app/locale_api_test.go) | 普通修改不影响秘密/权限；语言不触发模型；敏感操作要求人类 |
| AUTO-V13 生命周期 | 节点离线查询、显式启动、忙时拒绝停止、明确停止、重启新实例、MCP 进程退出 | 不因查询隐式启动；复用启动器和统一收尾；客户端退出不杀节点工作 |
| AUTO-V14 批次部分完成 | 同批校验错、容量满、未知回执、节点崩溃、重复批次与逐项重试 | 每项身份/状态可查；未分派不自动续发；无隐式整体回滚或未知重做 |
| AUTO-V15 资源和公平性 | 小/最大图、100/10,000 命令、最大活动运行、长历史、多 MCP 客户端及持续读取 | 输出/内存/扫描/等待有界；列表不回传全库；停止与人类交互保留容量 |
| AUTO-V16 升级兼容 | 旧 UI 库修订、新对象修订、旧运行记录、较新 schema 回退、普通/快速安装 | 已有数据与能力保留；格式不支持明确拒绝；包中实际 CLI/MCP 能连接同版节点 |
| AUTO-V17 快捷输入显式恢复 | 基于[控制器测试](../../../../internal/quickinput/controller_test.go)，通过 UI/CLI/MCP 重试宿主启动失败和已消失的热键冲突；覆盖功能关闭、无交互桌面、损坏配置、清理失败、健康状态、重复请求及与设置/自动恢复/退出并发 | 沿用原配置，保持关闭意图；恢复结果及失败状态一致，不重复创建宿主/注册热键；普通状态查询不触发恢复，不修改人类草稿或会话 |

测试输入用固定种子/检查点控制故障，不用随机断网或随意 sleep 代替关键窗口。运行 ID、状态、文件副作用计数、日志字节/文本和目录内容分别断言，不把界面成功提示作为唯一证据。

AUTO-V18：在原窗口输入未保存的图、聊天、任务或设置，从外部客户端定位窗口、读取、更新和提交；检查原窗口变化、旧 ID、旧修订、输入法、并发提交、丢响应和敏感字段隔离。

## 性能验证

记录同一环境下 UI 现有基线与接入后的本地操作延迟、序列化字节、峰值内存、CPU、客户端往返次数及排队时长；模型网络延迟独立计量。重点检查分页是否仍扫描/序列化整库、批量是否省去逐步骤往返、长等待是否产生无界 goroutine/轮询、日志筛选是否保留扫描预算。

容量边界检查必须结合最大合法图、32 个活跃实例、摘要/完整记录的实际尺寸和现有 UI 预算；没有测量依据的建议值不作为已验证性能承诺。压力持续期间验证停止、状态核对和人类保存都能获得服务，限流错误有明确重试条件但不自动重放未知写入。

## 真实产品验收与支持边界

本功能已通过用户验收。工程自动检查采用隔离数据和受控运行时，未单独记录真实在线模型、两台实体 Windows、安装升级、实体 IME/多显示器及跨 Windows 用户权限矩阵的逐项实测结果。以下用例保留为这些环境的专项回归方法。

- 普通用户 Windows：用 UI 建图，CLI 批量修改，MCP 启动/读日志/停止，UI 查看同一结果；关闭自动化客户端后脚本继续，节点退出后按既有规则停止。
- 锁定 OpenClaw + 真实模型：通过 CLI/MCP 发送和继续会话，UI 查看原生历史，停止明确轮次并验证后续消息；凭据由人工专用表单配置。模型额度使用单列，不由常规测试触发。
- 两台 Windows：A 的 CLI/MCP 提交现有远端任务给 B，A 退出后 B 继续，重连读取同一任务/事件并处理普通问答与取消；此用例不证明远程持续聊天或脚本图执行。
- 新装/升级和普通/管理员节点：验证本机通道权限、组件分发、版本错误和数据保留；其他 OS、无交互服务会话和未验证客户端只列支持状态，不借交叉编译或 SDK 声明判定通过。

实际执行分别标记 passed、failed、skipped、blocked；尚未开始的用例为 not_run。原始报告保存于实际构建输出下的独立验证目录；长期文档只保留方法与支持范围，发布证据随版本发布物关联。

## 已接入的自动检查

| 范围 | 可复现源码 | 覆盖与边界 |
|---|---|---|
| 定义和入口登记 | [Go 注册守卫](../../../../internal/app/automation_coverage_test.go)、[UI 守卫](../../../../tests/automation-coverage.test.mjs) | 比较实际操作、输出 schema、Web 端点和原生闭合动作；不证明动态 UI 语义 |
| CLI/MCP 实际进程 | [传输集成](../../../../internal/app/automation_transport_windows_test.go) | 受保护管道、同版 SDK stdio、CLI 创建/MCP 读取与重放、默认本机连接；用 ASTERHAND_AUTOMATION_BINARY 指向当前构建产物启用真实应用客户端 |
| 可靠身份与容量 | [注册调度](../../../../internal/automation/registry_test.go)、[存储](../../../../internal/store/operations_test.go)、[启动器](../../../../internal/launch/automation_windows_test.go) | 严格 JSON、同 ID 冲突、断开观察、重启/过期、未知容量保护、旧启动只探测不重复拉起 |
| 图与保留日志 | [历史](../../../../internal/localcommands/run_history_test.go)、[公开日志](../../../../internal/app/automation_logs_windows_test.go)、[公平性](../../../../internal/localcommands/graph_slots_test.go)、[编码](../../../../internal/localcommands/log_text_test.go) | 原运行权威、保留清理、容量先预留；真实隔离脚本稀疏流/控制字符/中文/EOF 与游标；固定表跨语言对照 |
| 会话和任务适配 | [会话](../../../../internal/app/automation_conversations_test.go)、[任务](../../../../internal/app/automation_tasks_test.go)、[旧任务处置](../../../../internal/tasks/automation_test.go) | 原有人类会话服务创建后 agent 发送/读取，不重发；隔离双节点任务回答、actor/outbox/事件水位；不冒充真实在线模型或两台设备 |
| 未保存编辑 | [应用](../../../../internal/app/automation_live_editors_test.go)、[编辑器](../../../../tests/live-editors.test.mjs)、[图历史](../../../../tests/graph-editor.test.mjs) | 在原窗口按修订编辑和保存；设置 `ASTERHAND_NATIVE_TEST_PACKAGE` 为当前构建的应用目录后，[真实宿主测试](../../../../internal/quickinput/live_editors_windows_test.go)从隐藏 WebView2 读写草稿并检查父进程快照；实体 IME 另行验收 |
| 批量与大对象 | [组合](../../../../internal/app/automation_composition_test.go)、[分片](../../../../internal/app/automation_object_test.go) | 全批权限预检查、逐项身份、停止/继续、重启不续发；100 项读预算、大对象无损分片/修订拒绝 |

## 预算依据

运行详细元数据单实例预留 4 MiB，32 个活动实例最多预留 128 MiB；摘要每条预留 4 KiB，公共收据每条预留 8 KiB 加固定元数据，在副作用前检查总容量。控制响应 64 KiB，日志文本片段最多 16 KiB 且 JSON 转义后最多 24 KiB，签名游标最多 24 KiB；大对象 JSON 分片为 8 KiB。相关边界以历史、存储、公开日志和分片测试断言。

并发测试验证八个共享脚本槽位、独立控制并发和批次四路分派。这些数量/字节边界已经在代码中强制执行；长时间混合负载的 UI 延迟、CPU、峰值内存及 10,000 命令扫描性能仍须按上面的性能方法测量，不把容量上限当作响应时间承诺。
