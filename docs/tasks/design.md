# 任务设计

下列语义由 internal/model、store、tasks、app 落实。当前数值与限制见文末实现契约，未实现扩展明确标注。

## 对象与事务

| 对象 | 必要信息 |
|---|---|
| Job | job_id、origin_node_id、request_id、owner_node_id、project_id、goal、state、start_before |
| Attempt | attempt_id、executor_kind、executor_instance_id、native_reference、state、时间与失败原因 |
| Event | job_id、单调 seq、attempt_id、type、payload、时间 |
| Interaction | interaction_id、job/attempt/instance/native_request 关联、类型、内容摘要、有效期、状态、回答者 |
| Artifact | artifact_id、owner、job/attempt、类型、受限本地位置、摘要与大小 |
| Outbox | 请求或回答的稳定 ID、目标、内容摘要、发送状态、下次重试时间 |

Job 与 runtime 会话、turn、run 分离。一个内部 turn 完成不直接表示 job 完成。

A 先保存提交请求，再向 B 发送。B 校验身份和范围后，在单事务内写入 job、去重记录和初始事件，提交成功才返回确认。状态变化和对应关键事件必须原子保存。

启动前保存 attempt，再调用执行适配器；保存 native_reference。外部执行和数据库不是一个事务，启动窗口崩溃必须对账。

## 幂等、排队和取消乱序

去重键为 `(已认证 origin_node_id, request_id)`；规范化摘要包含稳定业务内容和执行策略，排除传输重试元数据。请求字段与摘要定义见 [数据模型](../../internal/model/model.go) 和 [持久接收实现](../../internal/store/store.go)。

- 相同键与摘要返回同一任务；相同键不同摘要返回冲突。
- 未确认提交在 A 显示“送达待确认”，重试不换 owner。
- start_before 限制 B 最晚启动时间，超期任务不得执行。时间偏差与边界测试必须纳入契约。
- 取消可引用 origin/request，即使提交尚未到达也保存取消标记；迟到提交只能产生已取消记录，不能启动。
- 取消标记仅接受有权控制该请求的身份，不能让任意 peer 取消别人的任务。
- 终态后的人工重试创建新 job，关联 retry_of_job_id；首版 max_attempts=1，不自动重跑有副作用任务。

终态摘要和去重记录独立于详细事件保留；首版不自动删除必要去重记录。后续清理必须有明确旧请求拒绝规则。

## 状态语义

| 状态 | 含义 |
|---|---|
| accepted / queued | 已持久接收，尚未执行 |
| running | 已确认在执行 |
| waiting_input / waiting_approval | 等待相应交互，保留具体 interaction 列表 |
| cancel_requested | 已登记取消，尚未确认停止 |
| reconciliation_required | 真实执行状态不明，保留现场 |
| succeeded | 执行正常结束、约定机器检查通过、结果保存完成 |
| failed | 已确认失败并结束，记录验证失败或执行失败原因 |
| cancelled | 已确认不再执行，保留已经产生的变化 |

任务状态只是摘要；若多个交互并存，UI 以 interaction 列表为准。取消和恢复状态优先于等待摘要，不用单状态字段替代真实交互记录。

execution_outcome、verification_outcome、user_acceptance 分开记录。文件验收以实际读取的 UTF-8 字节为证据；没有机器判定标准的任务显示“执行完成，待人工验收”，不得将模型自述作为 succeeded 的充分依据。无机器验证的完成状态为 completed_unverified，见 [数据模型](../../internal/model/model.go)。

网络可达性是观察状态。已发布终态不反向变为 running；完成与取消竞争按 B 记录的执行事实和原子状态转换处理，不强改真实已完成结果。

## 事件与交互

关键事件先持久化再发布，文本输出可批量保存。按 job 单调 seq；快照返回水位，后续按 after_seq 补读。SSE 是通知通道，持久日志是补读来源。缺口返回明确游标过期及新快照。

事件包括接收、attempt 创建、执行启动、工具开始/结束、问答/审批、取消、产物、终态和对账。设分页、事件大小、日志总量与慢客户端背压，不要求每个 token 落盘。

回答先事务保存并登记待发送记录，然后交给执行器；区分待回答、已保存、已发送、已确认、已失效。重启后对账原请求，不重放旧批准。普通问答不等同于权限审批。

交互绑定 attempt、executor_instance、native_request、操作摘要和有效期。跨节点人工身份的证明见 [节点设计](../node/design.md)。

## 取消与恢复

启动成功但 native ID 未保存、工具副作用完成但结果未保存、答案保存但未发出等窗口都需故障注入。

恢复检查本地 attempt、进程实例、runtime 会话、工具执行凭据与工作区。PID 单独不足以证明进程身份。不能确认时保留占用与现场，进入待核对，不自动再启动。

对账可确认已完成、失败或取消，也可请求人工处置；人工处置必须记录理由和证据，不伪造执行确认。resume 能力需要适配器实测，首版不承诺 B 重启后无缝续跑。

取消调用返回已登记；执行器确认停止才发布 cancelled。存在未确认子进程时进入待核对。取消不回滚已经完成的文件或外部副作用。

## 预算与持久化

队列期限、有效执行时长、交互等待期限、事件容量分开配置。等待用户不消耗有效执行时长，但受交互期限限制；超时走取消流程。当前预算与持久化策略见下文实现契约；尚未提供指数退避与自动容量回收。

SQLite 数据库存本地，不跨设备同步活跃文件。迁移应可诊断、可备份，磁盘满或提交失败时不得返回持久接收成功。备份恢复必须防止旧身份和旧 job 在第二台机器同时执行，首版不提供热克隆节点。

## 实现契约

- SQLite WAL + synchronous=FULL；schema 1，拒绝较新 schema。Accept 原子写 job/dedup/event，Change 原子更新任务与事件。attempt 内嵌于 job；max_attempts=1，不提供自动 resume 或独立多 attempt 表。
- 所有 Web 输入先形成 route job；execute job 才拥有文件工具。route 的 succeeded 表示提交已持久保存，UI 展示对应执行任务；执行 job 的 succeeded 要求文件回读证据，无文件证据的模型回复为 completed_unverified。
- 每节点一个全局执行槽；reconciliation_required 阻塞后续启动。重启时所有非 queued、非终态执行进入待核对；本机填写证据关闭后状态为 failed / unknown_manually_closed。
- 一个同步 interaction；普通问题 10 分钟有效，回答事务保存后交给原回调。旧 attempt 和第二个不同回答拒绝；远端答案先存 A 的 answer-outbox。当前没有审批或并行交互 UI。
- 启动期限前端默认 1 小时、服务端最大 24 小时；执行预算 10 分钟（不含问答等待），墙钟 2 小时。每秒轮询 outbox，失败保持原目标；尚无指数退避和自动容量清理，适用于个人低任务量候选。
- 普通事件预算 4096，另留 16 条控制事件，总硬上限 4112；每事件 64 KiB、分页 200 条不变。预留只用于既有文件/提交证据、已发出问答的交付、取消、结束、恢复、人工处置。新文件写入和委派必须先取得普通事件；重复控制请求保持幂等，不追加日志。兼容旧 Seq=4096 的执行现场，不删除既有事件。SSE 每 2 秒通知刷新，无自动游标过期窗口。
- 文件成功证据和人工接受分开；user_acceptance 始终保留 pending，用户验收独立登记，不从文件写入成功推断。当前没有自动设置 accepted 的界面。

## 状态与证据边界

问答请求和答案交付在 Store 事务内再次检查当前 attempt、interaction、状态与取消上下文。取消和问答按持久事务顺序裁决，已登记取消不能被后到更新覆盖；收尾也重新检查取消状态。活动 attempt 注册与取消查找同步，避免“已 running、尚未登记 cancel 函数”的空窗。

控制空间按当前单槽、单个同步回调、单 attempt 的状态机有界：越过普通预算时至多一个已登记操作（文件/提交证据 1 条或问答交付 2 条），取消 1 条，结束或恢复 1 条，再加人工处置 1 条。16 条预留保留余量，重复取消、答案及文件证据不新增事件；普通预算已满时工具返回预算错误并取消运行上下文。总上限检查仍强制，未开放无限控制日志。

文件回读后，operation=completed 与任务产物/事件在同一事务提交。旧节点可能留下 completed operation 而未登记 job artifact，重试及恢复通过原操作证据补齐，保持幂等；prepared 仍须人工核对。结束状态提交失败传回执行循环，节点停止服务并保留现场，重启先恢复，不继续调度下一项任务。
