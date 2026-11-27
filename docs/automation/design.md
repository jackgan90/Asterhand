# 自动化公共操作设计

## 本地命令通知字段

`notify_on_completion` 使用默认开启且能区分缺省与 false 的领域类型，机器 schema 映射为 boolean。创建/校验缺省采用 true；patch 或旧版完整保存缺省保留现值；显式 null 拒绝。复制保留值，读输出包含有效布尔值。命令草稿保留同一字段；旧草稿替换缺字段时浏览器保留当前开关。运行接纳后根据快照决定通知，不重新读取注册配置。展示失败不改变公共运行结果；`settings.get.notifications` 提供 `state`、`pending` 和 `visible`，其中 `idle` 表示尚未首次启动宿主，`unsupported` 表示平台未实现。

状态：公共操作和三入口适配已实现，工程验证、独立审核及用户验收已完成，范围见[功能计划](../development/features/agent-automation/plan.md)。当前行为与差异见[覆盖清单](coverage.md)，需求见[自动化需求](requirements.md)。

## 已固定的基础参数

- 公共业务 API 为 `1`，与 MCP 协议版本独立。使用同一可执行文件的 `automation` 和 `mcp` 子命令；离线 `automation help/schema/new-id` 不打开可写节点。
- Windows 本机管道使用 Microsoft go-winio v0.6.2，DACL 限定当前用户和 SYSTEM，拒绝远端管道调用。客户端通过本机连接记录校验证书、节点 ID 和 TLS 1.3。
- MCP 使用官方 Go SDK `v1.7.0`，通过 stdio 运行；输入定义、严格解码、客户端和业务响应与 CLI 共用。依赖版本与校验记录在 `go.mod/go.sum`，许可证分别为 SDK 的 MIT 和 go-winio 的 MIT；不使用滚动版本。
- 普通写身份格式为 13 位 UTC Unix 毫秒、下划线和 32 位小写随机十六进制；`node.start` 使用同样格式并加 `start:` 前缀。启动器与在线操作分别拒绝另一命名空间，离线启动不用打开节点数据库也不会让相同 ID 被两个存储接纳。新身份允许过去 10 分钟、未来 30 秒；保留的旧收据先于时间检查返回。节点提供 `operations.new_id` 以使用节点时钟。
- 普通读取每主体 8 / 节点 32；写入每主体 4 / 节点 16；等待每主体 4 / 节点 16、最长 30 秒；停止/核对另保留每主体 4 / 节点 8。所有连接合并按认证主体计数。
- 操作收据保留 30 天，最多 100,000 条或 64 MiB，以先到者为准。每条接纳前为最多 8 KiB 的结果摘要预留容量；活动/未知记录不因过期清理。完整正文和图不进入收据。数据库版本提升为 3，旧数据库可升级，新版本数据库拒绝降级打开。
- 普通设置按两个已有保存对象分别修改：节点名称/已有模型使用节点配置修订，语言/主题/快捷键使用桌面偏好修订。单次 `settings.patch` 明确选择 `node` 或 `desktop`；跨对象组合使用逐项报告的批处理。这样每次保存仍只有一个原子文件发布点。
- 运行摘要由运行管理器独占 `command-runs/history.db`，最多 100,000 条或 64 MiB，接纳前每条预留 4 KiB；当前运行索引仍是唯一执行接纳依据。摘要至少保留 30 天，当前索引引用和未结束记录继续保留。详细数据保留 7 天、每命令最近 10 个已结束轮次、合计 2 GiB，先到者清理；节点全部详细数据统一限额 4 GiB，每份元数据按最大 4 MiB 预留，包含活动和历史。原有每实例 256 MiB、每轮 1 GiB 限制继续适用；容量不足先拒绝接纳，运行中输出超限按原日志故障流程停止。
- 日志文本按固定 WHATWG Encoding 表解码，Go 和界面使用同一份校验后的映射数据；UTF-8/UTF-16 及 GBK/GB18030、Big5 的随机字节、分帧和 BOM 行为有对照测试。表来源、生成器和 BSD 3-Clause 许可见[编码数据说明](../../internal/localcommands/encodingdata/README.md)。包含解码状态的日志游标由节点私有随机密钥签名，绑定主体、授权范围、对象、编码和筛选，跨节点或篡改后拒绝。

## 组件和数据所有者

```text
Web HTTP / 快捷输入内部桥 ──────────────┐
CLI ─────┐                              ↓
         ├─ 共用客户端 ─ 本机通道 ─ 公共操作适配 ─ 现有领域服务
MCP stdio┘                                           ├─ 命令注册 / 图运行
                                                     ├─ 持续会话 / runtime
                                                     └─ 任务 / 节点 / 普通设置
```

节点进程继续独占自己的数据库、注册配置、运行管理器和会话 runtime。CLI 与 MCP 进程不重新打开可写数据库、不另起执行管理器、不直接改 harness 的内部文件。MCP 直接调用共用客户端，不启动 CLI 来解析人类文字输出。

公共操作适配负责可信调用上下文、schema、授权检查和统一响应；去重、原子提交及状态转换由拥有该数据的领域服务实现。已有 HTTP/IPC 可以保留自己的封装，不要求 UI 绕行 CLI/MCP。业务校验仍只有服务端一份权威实现；前端允许即时预检查和本地草稿，规则常量/schema 可生成，无法共享的交互判断用同组案例检查。

按职责组织操作定义、客户端和传输适配，不引入承包全部业务的 master service。CLI/MCP 的公共协议与现有 `internal/*/wire` 分开版本管理，内部桥的宿主代次、窗口序号和原生会话信息不作为 agent 参数。领域服务通过窄接口接入，公共框架不读取其内部锁、数据库表或平台句柄。

## 本机通道与授权

首个交付使用 MCP stdio。CLI/MCP 共用客户端，以受保护的命名管道和固定证书 TLS 连接原节点，逐请求核对 node ID、协议版本与实例代次。具体保护由 `internal/automation/pipe_windows.go`、`transport.go` 和 `access.go` 实现，测试与未验证环境见功能验证说明。既有窗口匿名管道保持私有，不让任意客户端模拟已认证宿主。

CLI/MCP 通过当前用户可读的节点登录文件派生专用于本机通道的凭据。节点连接记录只保存地址、节点身份和证书。使用同一节点的多个外部客户端共享该节点的普通操作，无需为每个 agent 创建主体。产品内认证、加密配置和配对入口仍由人类使用。

未来 Streamable HTTP、远端持续会话和远端脚本图需要各自的目标端授权及真实双机验证；现有配对凭据只继续拥有已经授予的任务能力。本期自动化可委托现有远端任务，不因此开放远端脚本或会话管理。

## 操作定义与机器响应

每个操作定义稳定名称、输入/输出 schema、机器错误码、授权类别、是否写入、是否可批处理、目标能力、预算和状态查询方式。公共定义生成或校验 CLI 帮助与 MCP 工具描述；业务规则仍由领域代码执行。命令、脚本图、运行与日志的操作说明及字段 `description` 显式暴露标识符正则、数值区间、`start`/`end` 字段互斥规则、脚本扩展名与引号规则、并发槽位上限及 Windows 非脱离 Job 执行语义，同时保持硬性校验集中于领域服务，确保 CLI 与 MCP 返回一致的业务错误码和收据，且 `commands.validate` 对未保存候选保留空 `id`/`revision`。工具按明确动作暴露，不用接收自由字符串的万能 `execute` 工具替代类型化能力。

请求包含明确目标、业务参数；写入另带操作身份，对已有对象修改带预期修订。`node_id` 表示助手节点，图内步骤使用 `step_id`，避免复用内部日志协议的 `node_id` 含义。名称用于搜索和选择，正式修改及执行使用稳定 ID。

响应包含协议版本、目标、请求身份、业务 ID、接纳事实、当前结果和查询方法。错误采用稳定 `code`、可选 `details`/字段路径/对象 ID，以及可本地化的 `message_key/args/message`；不根据语言文字决定重试。错误须区分无效参数、禁止访问、缺配置、不支持、对象冲突、容量不足、结果未知和记录过期。业务状态继续保留图运行、任务、聊天各自的含义，不强行压成同一个状态机。

CLI 支持 JSON 标准输入/文件（`--input`）及单行参数（`--args`）、可选受管调用者身份（`--caller`）、稳定 JSON 输出、明确退出码（含长等待总超时退出码 `8`）和有界等待；其中客户端侧 `wait` 阻塞子命令通过循环短 `workers_wait` / `workers_wait_any`（单切片上限 `30s`）并在守护进程重启瞬断宽限内自动重试，解决跨轮次长任务无需反复耗费模型上下文轮询的需求；stdout 只输出所选结果格式，诊断与状态变更单行进度写 stderr。MCP 使用同一输入/输出定义，返回结构化结果及简洁文本，业务错误按 MCP 工具错误表达。MCP JSON-RPC 请求 ID 与业务操作 ID 分开，重新连接时保持业务身份。资源读取可作为便利入口，但所有必需读取能力同时有工具，不能依赖客户端实现可选资源订阅。

节点离线时返回 `node_unavailable`，不把一次查询或重试隐式变为启动命令。显式 `node.start` 复用安装定位与启动器；只在原安装/用户权限下运行，不修改配置或触发安装。显式停止复用节点忙状态和统一收尾，接纳响应后断连以原节点实例消失为核对依据，不自动重发到新的节点实例。

## 请求身份、过期与可恢复接纳

写请求在第一次可能丢失响应前生成稳定操作身份，包含不可变的生成时间和随机部分；CLI 与 MCP 均支持在省略 `operation_id` 时由本机适配进程按当前 UTC 毫秒时钟自动生成合法身份，并在成功结果、收据以及断连 `result_unknown` 报错中回传该 `operation_id` 供核对或原样重试；显式调用 `operations_new_id`（或传 `--operation-id`）仍作为强跨进程幂等场景的推荐入口，使用节点握手时间校准。主体、目标、完整身份和规范化业务参数摘要一起参与核对；正文、字段省略/默认值和预期修订的规范化规则由各操作定义固定。显示语言、等待时长等观察参数不改变业务意图。

处理顺序为认证授权、查找旧收据、检查同 ID 的摘要、核验新请求时效与对象修订、接纳并产生副作用。重复请求不能因为当前 busy、修订已变或模型配置已变而失去原收据；授权撤销后也不能借重放取得数据。

新请求接纳窗口为生成后 10 分钟，未来时钟偏差最多 30 秒；只要收据仍保留，同 ID 可以在窗口外查询原结果。收据已不存在且身份超出新接纳窗口时返回 `operation_expired`，绝不作为新操作执行。修改时间就是新的意图，SDK 不自动替换；结果未知必须继续核对或由调用者显式决定下一步。格式和窗口由 `internal/operations` 统一检查。

图启动把操作身份、摘要和主体引用写入运行准备记录，随当前运行索引一起公布；索引公布后才可启动脚本，并返回其中固定的 run ID。进程启动与磁盘提交不能原子完成，崩溃窗口按现有执行身份核对，保持未知或中断，不据此重跑。公共收据索引可以重建，不能形成另一个独立的接纳决定。

配置修改、创建和删除必须有等价的可恢复提交记录：旧状态/预期修订、操作身份、目标版本及结果关联在原子发布点可核对。若收据与业务数据不在同一事务，使用持久意图和业务发布标记，恢复后先核对，收据补齐前阻止该对象下一次写入；不能在两个独立写入之间返回普通失败后允许重做。命令目录、节点配置和桌面偏好分别保存最后一次发布标记，在允许后一次保存前补齐原收据；无需把已有数据迁入新数据库。

聊天沿用已有 operation、摘要和原生收据；公共身份映射须持久且一对一，不能重新生成底层发送 ID。任务复用 request/answer/cancel 的去重记录和原 owner，不改变已有 peer 摘要字段。重复的相同正文配不同操作身份仍是两次显式发送。

## 图编辑与人机并发

Web 批量删除是公共 `commands.delete` 的有界组合：同源适配 `POST /api/local-commands/delete-batch` 接收 `items: [{command_id, expected_revision, operation_id}]`（1—32 个不同命令、不同操作身份），完整检查批次形状后逐项派发，返回同顺序 `results: Response[]`。沿用人类主体及领域收据，不增加通用人类代理权限。CLI/MCP 使用现有 `batches.submit`（`on_error: continue`）组合相同操作及 `batches.get` 核对；全选由 `commands.list` 遍历同目录修订的所有页后固定集合，无隐式服务端 delete-all。跨项允许部分成功，运行占用或修订冲突按原机器错误返回，未知项保留原操作身份核对。

图仍属于命令，不新增第二套独立图存储。`commands.get` 返回完整可编辑配置、命令修订与能力；`commands.create/clone/patch/delete/validate` 使用统一模型。复制只复制配置并生成新命令/步骤身份，不复制脚本文件、运行记录或权限。新建和复制支持单次输入包含名称、图和元数据，避免必须先创建无效中间对象。

`patch` 按稳定 ID 描述添加/删除/修改步骤、连接/断开边、调整顺序/坐标和命令属性，保持参数数组及空参数原义。先对副本应用整个修改，再归一化和检查图、名称及容量，最后一次提交；任何错误都不部分保存。`validate` 接收 `{command_id?, candidate}` 并返回归一化候选或领域校验错误，不写配置、不检查模型、不运行脚本。保存允许尚不存在的脚本；运行前沿用领域路径可用性复核。调用者可以比较候选和原配置得到差异。

单命令使用自身修订，列表另有库修订。不同命令的有效修改可相继成功；名称唯一性、全库容量和运行锁在注册服务内原子检查。运行前再核验命令修订，修改与启动竞争只能有一个合法顺序。现有 HTTP 的库修订请求在迁移期间保留旧语义，由同一服务适配，不能直接把旧 token 当命令修订。

原编辑器继续持有未保存输入、选择和撤销历史。外部调用通过[编辑器内部接口](draft-handoff.md)读取、修改或提交具体窗口的普通草稿；接口不创建可见控件。

## 运行、日志与保留

`commands.run` 返回明确接纳事实及 run ID；`runs.get/list/wait/stop` 指向具体实例。停止集合固定于提交时，迟到请求不停止新增实例。图、聊天和任务的既有槽位保持：脚本步骤共享 8 个名额；单命令最多 8 个活跃实例、节点最多 32 个，聊天一个生成槽、任务一个执行槽；达到上限明确拒绝，不在客户端无界排队。

观察请求有独立 context；等待超时、MCP 请求取消和客户端退出仅释放观察资源。已接纳运行由节点持有。接纳前可以取消且必须返回确定拒绝；接纳点附近取消但结果无法证明时，返回/保留未知并可按操作 ID 查。显式停止保留原领域的“请求停止”和“确认结束”区别。

图运行增加有限历史。终态摘要和写请求收据保留 30 天，单节点最多 100,000 条或 64 MiB；活动和未核对记录不按期限删除。保留承诺期内容量满时拒绝新写入，不提前清除去重证据。过期清理只处理终态且可安全清理的对象；长期未核对记录达到预算时要求诊断处置。摘要不复制最大 4 MiB 图快照，完整配置快照跟随详细运行记录另计预算。

详细运行记录及日志按 7 天、每命令最近 10 个已结束轮次、节点合计 2 GiB 三者先到者淘汰；活动轮次和恢复证据不参与淘汰。当前轮既有每实例 256 MiB、每命令轮次 1 GiB 及超限停止语义不被替换。节点活动日志与详细元数据合计上限为 4 GiB；接纳前预留每实例 4 MiB 的元数据空间，输出按实际帧字节原子计数。32 个最大活动实例的元数据预留为 128 MiB，摘要另行按 4 KiB 预留；超过预算先拒绝新接纳，运行中输出失败按原日志故障路径停止。

查询明确返回详细记录/日志是否保留、保留截止或容量淘汰事实及 `log_complete`；缺失/过期与“成功但无输出”分开。删除命令清理配置和其可删除日志，但留下不含脚本内容的最小接纳/结果收据，旧请求不能复活被删除命令。安装升级保留可核对记录，回退遇到较新 schema 明确拒绝写入。

日志公共服务在现有原始帧和解码上下文之上提供文本页，保留 stdout/stderr、步骤、时间、游标和编码。支持现有 UTF-8、GBK/GB18030、Big5、UTF-16 LE；跨帧、跨页、BOM、无换行和非法字节规则由一套用例固定。Go 与 UI 共用固定编码表及逐字节对照用例。公共日志保留块和流，UI 的行拼接只负责显示；文本分片额外限制 JSON 转义后大小，不能因控制字符膨胀超过响应预算。

## 会话、任务与普通设置

会话操作固定 node/role/conversation/operation，调用已有 `conversation.Service`，不代理任意 Gateway RPC。发送使用与 UI 相同的 prepare/enqueue 和核对规则；`queued` 仍只是有界 RAM 托管，原生 accepted 才表示持久接收。节点重启不自动重发尚未接收的正文，自动化收据不成为新的正文存储。停止回复只针对明确已接收轮次，不撤回用户消息，也不暂停后续派发。

队列、活跃会话、事件和收据按调用者授权过滤；现有服务每次附带的整节点队列摘要不能直接透传给权限更窄的客户端。公共层保留返回 `busy` 的必要信息，但不泄露其他会话正文或 ID。内置助手继续保持现有禁工具策略；将来接入工具须另定义递归预算及向自身发送的行为。

任务查询保留 owner、outbox、job 和 attempt。普通回答、取消和关闭未知结果分别调用原有任务服务。关闭需给出原因，事件保留实际调用来源；关闭不代表执行成功或人工验收。

普通设置只允许名称、语言、主题、快捷键和现有凭据已支持的有效模型选择，沿用运行中模型配置保护。运行时路径、凭据引用、授权目录、网络监听/信任配置不从普通 patch 修改。只读诊断与显式模型/工具诊断分开，后一项可能消耗模型额度并声明影响，不由发现或健康查询自动触发。纯界面偏好在无桌面时可读取；需要实际注册热键或展示窗口时明确返回桌面不可用，不妨碍命令/会话/任务操作。

`desktop.retry` 属于日常编辑能力，复用快捷输入控制器的显式 Retry，重置自动恢复次数限制并按已保存配置准备宿主、注册原热键，不写配置或恢复默认值。与设置修改、自动恢复和退出共用控制器串行规则；已有健康宿主和热键可直接复用，不强制重建。存在交互桌面但功能关闭时保持关闭，无交互桌面、配置损坏、旧资源清理失败、热键冲突或节点退出中则返回相应失败及实际状态，不另起第二个宿主。它是显式恢复操作，不由状态读取触发；沿用公共写操作身份和收据，重复查询原请求不额外恢复。

`settings.get` 同时返回 `desktop_shortcut`（`ShortcutStatus`：包含 `supported`、`available`、`enabled`、`conflict` 与状态提示）；可批处理写操作 `settings.desktop_shortcut`（输入 `{enabled}`）复用 `internal/launch.ShortcutManager`，按当前用户桌面快捷方式实际归属创建、更新或移除 `Asterhand.lnk`，保留未安装态与外部冲突保护，不改写 `desktop-settings.json` 修订或 `installation.json`。执行前写入 `operations.State` 预期开关意图，进程在提交后、收据完成前中断时，由 `operations.get` 核对磁盘快捷方式实际归属是否与已记录意图一致再完成收据。

## 批处理、分页与公平性

批量读取最多 100 个 ID；列表默认 50 项、上限 200，默认摘要不携带整图或正文。图修改单请求上限沿用现有 1 MiB，并继续受 64 节点/512 边等领域限制；普通控制响应目标不超过 64 KiB，显式完整命令或校验候选图响应允许独立的 1 MiB 预算。超过对应响应预算时使用有界分页/片段或明确拒绝，禁止截断字段后当作完整配置。日志页保持现有原始字节和扫描预算，文本膨胀后仍需分页。大整数和字节偏移使用不会丢精度的协议表示，游标绑定对象、修订、筛选和编码，不按页码猜测。

首期批量变更/启动每批最多 32 项，单客户端并发分派最多 4；不绕过领域槽位。批次固定条目顺序、逐项身份和摘要，支持遇错停止后续分派或继续；已经接纳的条目不撤销。批次接纳必须可查询，进程重启后未分派项明确标记为未执行，不自动续发；客户端重试先查原条目，再决定是否重新提交确定未执行的项目。未知项不得按失败重做。单图修改是原子的，跨图/设置/执行组合无整体事务承诺。

日志/历史/列表查询、长等待和新写入分别限流；等待单次最多 30 秒，单客户端最多 4 个长等待，节点总等待并发为 16。控制请求保留独立容量，停止和核对不会被日志读取占满。节点共享限制按认证主体统计，不能通过多开 MCP 连接绕过；UI 与 agent 同时使用时需验证交互响应及执行名额的有界公平性。

## 版本、安装与验证

发现响应声明公共 API 版本、逐项能力和预算，MCP 协议版本与 Asterhand 业务版本独立。未知主版本拒绝，新增可选字段按双方声明处理；不静默忽略影响副作用的未知参数。CLI/MCP 随同一应用构建分发，版本不兼容时给出可诊断错误，帮助/版本命令不要求节点在线。

使用[官方 Go SDK v1.7.0](https://github.com/modelcontextprotocol/go-sdk/tree/v1.7.0)处理 stdio 协议协商和工具调用。固定模块及传递依赖见 [go.mod](../../go.mod) / [go.sum](../../go.sum)，校验使用 `go mod verify`；构建脚本收集每个模块的 LICENSE/NOTICE，额外分发 WHATWG 编码表许可。实际兼容证据是同版 SDK 客户端连接构建出的 MCP 进程；其他客户端需独立验证，不能把 SDK 支持列表当作产品实测。

验收状态、验证方法与专项环境边界见[验证说明](../development/features/agent-automation/validation.md)。操作覆盖登记与构建守卫见[开发治理设计](../development/design.md)。

## 多 Harness 工具集裁剪与稳定 MCP 入口（Milestone 1—2 已实现并通过本机用户验收）

本节对应[多 Coding Harness 中介计划](../development/features/multi-harness-broker/plan.md)与 [ADR-005](adr/005-harness-mcp-lifecycle-and-dynamic-toolsets.md)：

- **工具集分层与受限主体**：在 `automation.Definition` 上增加工具集标签（`core`、`general`、`collab`）。`core` 仅保留底座操作（`capabilities`、`operations_new_id`、`operations_get`）；`batches_get` 与 `batches_submit` 成对归属 `general`，避免纯 `collab` 模式下露出孤立的批处理读取工具。每个受管 Harness 建立可撤销的受限调用身份与独立 `Principal`，其有效权限统一作用于 `tools/list`、`Dispatch`、`batches_submit` 子项、`operations_get` 与 `batches_get` 逐项收据查询（返回前均按原操作所属工具集与最新权限复核，防止查询期间撤权泄漏旧收据）与客户端本地 `node_start`（安装版经 `launch.StartOperation` 按在线校验后的受管主体 `"harness:<caller_id>"` 持久化启动收据并在获取启动锁前后复核 `general` 工具集与 `execute` 角色；若启动前最后一次权限校验失败且尚未调用 `launchLocked`，在锁内原子撤销本次新建的 `prepared` 收据并透传原始错误以允许恢复后按原 ID 重试，撤销写盘失败时单独报持久化异常；实际派发 `launchLocked` 后的失败/断连继续保留 `prepared` 收据并返回 `result_unknown` 供对账；受管调用方经 `operations_get` 补读 `start:` 前缀收据时在读取前及命中后返回前两次复核同一主体与 `general`/`read` 权限，其他主体不可认领，校验时保留 `node_unavailable` 等传输错误分类且对缺失或 `null` 的 `roles`/`toolsets` 报 `invalid_response`，普通非 `start:` 收据零额外开销直查守护进程）；吊销整份凭据与仅关闭单个工具集开关（按剩余开关实时重算权限）严格区分；长驻 MCP 子进程在每次调用与定期轮询时自动与磁盘凭据同步（删除即失效，重签自动恢复）；无 `--caller` 的既有直连入口明确定位为当前 Windows 用户显式全量本机入口，同时在文档中诚实声明同 Windows 账户下的策略保证边界。
- **统一操作名规范（全量 `[a-zA-Z0-9_-]`，禁止 `.`）**：经实测，Antigravity 懒加载 MCP Schema 缓存对名称含 `.` 的工具会将 `parameters` 置为 `null`，且 OpenAI / Anthropic 等主流模型工具注册表均要求工具名仅包含 `[a-zA-Z0-9_-]`。全仓所有公共自动化操作（CLI、HTTP、无 `--caller` 直连 MCP 与受管 Harness MCP）统一直接使用下划线规范名（如 `workers_create`、`harnesses_list`、`operations_new_id`），彻底移除边界点号↔下划线双轨转换，使 CLI、MCP、Schema 描述与 `capabilities` 100% 一致；由 [automation_coverage_test.go](../../internal/app/automation_coverage_test.go) 从源头强制守卫全部操作名合法字符集；所有公开写/读入参 struct 字段均须附带清晰的 `description` tag（及适用时的 `enum` tag）。
- **双通道模型可读响应与预算公开**：`mcpResult` 对 `operations_new_id`、`operations_get`、`capabilities` 及全部 `harnesses_*`、`workers_*` 在保留 `structuredContent` 的同时，向 `content[].text` 投影有界、面向模型的结构化文本（避免与内嵌 `next_step` 重复拼接，并在超过 `48 KiB` 单响应文本上限时保留 `turn_id`、`worker_id`、`state`、`truncated`、`result_bytes`、`next_cursor`、`triggered_turn_id`、`error_code` 等最小定位字段及精确 `cursor` 指引）；`capabilities` 公开通用自动化限额、Worker 运行预算（`worker_wall_clock_seconds`、`worker_max_wall_clock_seconds`、`worker_stall_seconds`、`worker_max_active_turns`）及当前安装可用 CLI 路径 `cli_path`；`workers_send` 为纯写立即返回 `turn_id`（支持可选 `wall_clock_seconds` 30..3600s，不混入 `wait_seconds`，避开 40s/45s 超时与幂等摘要冲突；`workers_create/send/cancel/close` 接入 `reconcileAutomationOperation` 修复领域落盘后、公共收据完成前的崩溃窗口），等待走独立 `workers_wait`（默认返回紧凑视图，不嵌套整份 `Turn`，将 `progress`、`error_code`、`error_message`、终态 `after_vcs` 与 UTF-8 对齐的 `next_cursor` 提升至顶层，按需可传 `verbose: true` 取回完整 `turn`；整份响应同样按序列化后 JSON 字节数实施 `46 KiB` 预算，优先按序交付完整 `commentaries` 并返回 `delivered_commentary_seq` / `commentaries_truncated` 分页续读游标（若超大 `pending_interaction` 导致单页连首条完整旁白都装不下，必要时在本次 `workers_wait` 视图内压缩其重字段并保留 `request_id` 与全文查阅指引，确保分页游标必然前进且不改写人工审批界面所见完整命令；运行中或等待态存在未读分页旁白时，`next_step` 显式附带 `wake_on_commentary=true` 且服务端立即唤醒返回已缓冲条目），再用剩余预算按 UTF-8 边界填充 `answer` 预览与精确 `next_cursor`（含无余量时的 `"0"`）以继续通过 `workers_result` 读全，同时兼顾 `64 KiB` 公共上限与 `48 KiB` MCP 文本上限，避免已完成轮次显式续读多条 HTML 旁白与答案预览叠加超限）或多轮次批量 `workers_wait_any`（按整份响应序列化后字节数实施 `46 KiB` 预算，优先保留 `triggered_turn_id` 与各轮身份、状态、错误码、挂起交互等核心元数据——极端元数据或超长 `pending_interaction` 命令/路径/问题超限时渐进压缩 `after_vcs`/长消息及批量视图内 `pending_interaction` 重字段，同时保留 `request_id`、`kind`、处理方标志、有界摘要并附 `details_truncated: true` 与单轮查阅指引 `inspect_hint`，不影响人工审批界面与单轮 `workers_get`/`workers_wait` 查看完整命令——再公平水填分配已完成轮次最高单轮 `4 KiB` 的 `answer_preview` 与进行中轮次的 `commentaries`，余量再分配已完成轮次的归档 `commentaries`，使整份响应同时稳妥落在 `64 KiB` 公共响应上限与 `48 KiB` MCP 文本上限内，绝不因多轮长预览或大审批命令触发 `response_too_large` 丢失状态；预览截断时按实际交付的 UTF-8 字节数返回精确对齐的 `next_cursor`，无预览余量时返回 `"0"` 以供 `workers_result` 无缝读全；旁白因预算被省略或截短时区分最新生成序号 `latest_commentary_seq` 与已完整交付续读序号 `delivered_commentary_seq` 并置 `commentaries_truncated: true`（被截短条目同步标 `truncated: true`），调用方将 `delivered_commentary_seq` 传入 `workers_wait(after_commentary_seq=...)` 即可完整取回被截尾内容（`workers_wait` 通过 `OptionalInt64` 区分省略与显式 `0`：省略返回默认最近 2 条，显式 `0` 返回缓冲区中序号 `> 0` 的全部条目，避免续读前新增旁白导致第 1 条被尾部截断漏掉）；单次等待上限保持 `30s`，长任务通过后台调用 `asterhand-cli.exe wait <turn_id...>` 阻塞监听）；长审查正文与日志独立于 `8 KiB` 收据存储，通过 `workers_result` 与 `workers_logs`（支持正序 `after_seq` 与尾部 `tail=true`）按 `64 KiB` 响应预算分页读取；`ServerOptions.Instructions` 按当前开启的工具集动态拼接极简概要（每工具集一句，包含 `cli_path` 长等待/兜底提示）。
- **稳定入口、零断连热同步与多 Harness 托管对账**：正式安装根目录交付纯控制台转发二进制 `cmd/asterhand-mcp`（`asterhand-mcp.exe`）与 `cmd/asterhand-cli`（`asterhand-cli.exe`），均复用 `launch.ReadInstallation` 校验并自动向活动版本 `asterhand.exe` 透传 `--home <Installation.Data>`；守护进程通过受鉴权本地管道端点 `GET /v1/definitions` 提供当前工具定义与 Schema，长驻 `MCPHost` 严格区分节点重启瞬态离线（`node_unavailable` 等，保持已注册工具清单、启动时短有界重试、恢复后同 Schema 零 `list_changed` 刷新）与显式鉴权拒绝（`forbidden` / `authorization_required` / `toolset_disabled` 或本地凭据失效，立即清空受限工具）；检测到守护进程 `instance_id`、`policy_revision` 或调用方工具集/角色权限变化时自动拉取最新 `/v1/definitions` 并原位增删/替换工具、发出 `notifications/tools/list_changed`，仅在成功取得最新定义后再推进已同步版本记录（拉取瞬时失败时仍先按新权限立即下线已撤销工具，并在下次轮询自动重试），无需断开或重启已打开的外部客户端；`internal/harnesses` 在正式安装态自动为 Antigravity（原子单键 `mcp_config.json`）、Codex（官方 `codex mcp add/remove/get --json`）与 Claude Code（`~/.claude.json` 单键观察 + `claude mcp add/remove --scope user`）注册稳定入口并兼容迁移早期版本路径，DeepSeek Harness 按预检结论保留只读发现与路径覆盖；所有托管维护期望/观察状态对账与冲突保护且严禁整文件备份外部含凭据配置；关闭权限先在服务端闸门即时生效并发出 `notifications/tools/list_changed`。
