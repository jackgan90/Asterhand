# 编程协作者轮次时间线与任务上下文计划

## 基本信息

- **功能名称**：`worker-turn-timeline`（编程协作者轮次时间线与任务上下文）
- **目标与用户价值**：
  1. 查看任意轮次（含已归档 Worker）时能看到发起方 A 当时提交的原始任务说明（prompt）和 Worker 的角色提示（`role_hint`），不再只有 Worker 的最终回答；
  2. 每个 Worker 显示协作总耗时（墙钟时长与累计轮次时长）和轮次统计，便于事后统计工作量；
  3. 轮次按真实先后顺序编号展示，长程协作（数十到上百轮）不截断、不失控，可分页回看、筛选异常轮次；
  4. 用"任务概要 + 纵向时间线 + 就地展开"取代当前的状态按钮墙，每轮一眼可见序号、状态、任务摘要、耗时和时间，并修正按钮内文字基线不齐的问题。
- **状态及实施授权范围**：已完成实现与工程验证，待用户验收。用户已确认计划与默认值（完整任务说明保留 7 天、默认展开运行中或最新一轮、首屏 20 轮、两种时长口径、导出另行立项），并授权实施领域文档、代码与工程测试。本地安装、用户验收、提交和推送需另行授权。
- **关联领域需求 ID**：
  - 执行器：`EXEC-HARNESS-11`（轮次任务说明留存、轮次序号与协作耗时汇总）；
  - 自动化：`AUTO-HARNESS-06`（`workers_turns` 分页列表、`workers_result` 读取任务说明、`workers_get` 汇总字段）；
  - Web：`WEB-HARNESS-05`（编程协作者轮次时间线）。
- **相关领域 requirements / design 链接**：
  - [执行器需求](../../../executors/requirements.md)、[执行器设计](../../../executors/design.md)
  - [自动化需求](../../../automation/requirements.md)、[自动化设计](../../../automation/design.md)、[CLI 与 MCP 使用指南](../../../automation/usage.md)、[自动化覆盖清单](../../../automation/coverage.md)
  - [Web 需求](../../../web/requirements.md)、[Web 设计](../../../web/design.md)
  - [多 Coding Harness 本地中介计划](../multi-harness-broker/plan.md)、[长程等待与精简回传优化计划](../harness-collab-usability/plan.md)
- **ADR**：预计不需要新增 ADR。所有决定都是在[执行器 ADR-008](../../../executors/adr/008-multi-harness-broker-and-native-adapters.md)框架内扩展存储与视图；单向升级 `DBSchemaVersion = 2` 与显式清理事务记录在执行器设计中。

## 现状问题

| 问题 | 根因 |
|---|---|
| 归档轮次看不到原始任务说明 | `workers.Turn` 无 prompt 字段；`Service.Send` 将 prompt 组装后仅传给运行协程，不落盘 |
| 看不到角色提示 | `Worker.RoleHint` 已持久化，但 Web 详情头部未渲染 |
| 没有总耗时 | 轮次有 `started_at`/`finished_at`，Worker 有 `created_at`/`closed_at`，但没有汇总；前端只拿到部分轮次，无法自行汇总 |
| 超过 10 轮的历史不可见 | `Service.Get` 固定调用 `ListTurnsByWorker(w.ID, 10)`，界面无"有更多"提示 |
| 轮次顺序不可靠 | 按 `updated_at DESC` 排序，对账或迟到更新会使旧轮次跳到前面；无序号 |
| 按钮内状态与 ID 不齐 | `.worker-turn-pill` 使用 `align-items: center`，不同字号的两段文本基线不一致；6 位随机 ID 尾缀对人无意义 |
| 交互模式不匹配 | 轮次是时间序列问答，当前却是标签式按钮墙：不点开不知内容、问答分离、规模增长后挤压详情区 |

## 范围

### 本次实现

1. **轮次任务说明留存**（执行器）
   - 新增独立明细表 `turn_prompts(turn_id PRIMARY KEY, body, byte_len, updated_at)`，在 `Service.Send` 接纳轮次的同一持久化步骤保存发起方原始 `prompt`（已有上限 128 KiB）。不保存服务端拼接的附件、Git 引用和输出契约说明，这些已有独立字段或可由轮次元数据还原。
   - `Turn` 新增 `prompt_preview`（首段文本，按 UTF-8 边界截取不超过 240 字节，折叠空白）和 `prompt_bytes`。预览随 8 KiB 轮次收据保存。
   - 保留策略与结果正文一致：完整任务说明计入 `MaxTotalDetailBytes` 并随 `DetailRetention`（7 天）清理；`prompt_preview` 随轮次摘要保留 `SummaryRetention`（30 天）。
   - 本功能上线前的历史轮次没有任务说明，界面显示"该轮次创建时未记录任务说明"。
2. **轮次序号与稳定排序**（执行器）
   - `Turn` 新增 `turn_seq`：同一 Worker 内从 1 递增，在接纳轮次的 Worker 临界区内分配（`Worker` 新增持久字段 `last_turn_seq`），与轮次落盘同步提交。
   - 历史轮次无 `turn_seq` 时，读取时按 `started_at`（相同时以 `turn_id`）排名推导，并标注为推导值；不回写历史记录。
   - 列表排序改为按 `started_at` 与 `turn_id`，不再按 `updated_at`。
3. **协作耗时汇总**（执行器）
   - `workers_get` 新增 `summary`：`turn_count`、各状态计数、`first_started_at`、`last_finished_at`、`wall_seconds`（`created_at` 到 `closed_at` 或当前时间）、`turns_seconds`（各轮 `finished_at - started_at` 之和，运行中轮次计到当前时间，含等待审批和等待回答的时间）。
   - 汇总只覆盖保留期内的轮次；若有轮次已被清理，返回 `summary_partial: true`，界面标注"仅统计保留期内轮次"。
4. **轮次分页列表与任务说明读取**（自动化公共操作）
   - 新增公共只读操作 `workers_turns`（CLI、MCP、`/api/workers/turns` 共用 `internal/workers.Service`），参数为 `worker_id`、`limit`（1..50，默认 20）、`cursor`（上一页返回的不透明 `next_cursor`，取更早轮次）、`filter`（`all` | `attention`，后者只返回失败、取消、中断、待对账、受阻、等待审批/回答的轮次）。每项返回紧凑轮次视图：`turn_id`、`turn_seq`、`state`、`prompt_preview`、`started_at`、`finished_at`、`elapsed_seconds`、`result_bytes`、工作树变更文件数，另返回 `next_cursor` 和 `has_more`。归属 `collab` 工具集，权限作用域同 `workers_get`。
   - `workers_result` 新增可选参数 `part`：`answer`（默认，行为不变）| `prompt`。复用现有 46 KiB 分页、UTF-8 对齐 `next_cursor` 与 `complete` 语义读取完整任务说明。
   - `workers_get` 保持返回最近 10 轮以兼容现有调用方，新增 `turns_has_more` 并提示改用 `workers_turns` 翻页；`role_hint` 与新增字段纳入现有响应预算裁剪规则（`details_omitted`）。
5. **Web 轮次时间线**（前端）
   - **任务概要区**（Worker 头部下方）：角色提示（默认显示前两行，可展开）；轮次数、累计轮次时长、墙钟时长、异常轮次数。
   - **时间线**：纵向列表，最新轮次在上，每行一轮，固定列为：`#序号`、状态点（正常为绿点不写文字；异常状态显示文字并高亮）、任务摘要（单行省略）、耗时、开始时间。完整 `turn_id` 移入展开详情并支持复制。
   - **就地展开**：点击行展开该轮详情，依次为任务说明（超过 160 px 高度时在块内滚动，超过 32 KiB 时提供“加载剩余内容”）、结果正文（同样支持续读）、工作树快照、执行日志（终态轮次默认折叠，运行中轮次默认展开）。默认展开正在运行的轮次，没有则展开最新一轮；可同时展开多轮。
   - **长程浏览**：首屏加载 20 轮，底部"加载更早的轮次"按游标追加；提供"全部 / 只看异常"切换。实时轮询与 `/api/stream` 事件只刷新已加载轮次和顶部新增轮次，不重置已展开状态和滚动位置。
   - 修正行内文本对齐（统一基线与等宽数字列），新增文案同步 `zh-CN` 与英文词表。
   - 轮次渲染从 [workers-panel.mjs](../../../../web/workers-panel.mjs) 迁到新模块 `web/workers-timeline.mjs`，面板模块只负责 Worker 选择、轮询与数据装载。

### 明确非目标

- 不对结果正文或任务说明做 Markdown 渲染。前端当前没有 Markdown 渲染器，引入依赖需单独评估；本次保持等宽预格式文本并支持自动换行。
- 不提供协作记录导出（Markdown/CSV）和跨 Worker 工时报表；有需要可在本功能之后单独立项。
- 不提供任务说明全文搜索。
- 不延长现有保留期，不回填历史轮次的任务说明。
- 不改变轮次执行、审批、对账、取消语义和既有公共操作的默认行为。

### 依赖和待验证假设

- **单向数据库 schema 升级（`DBSchemaVersion = 2`）**：新建库直接创建完整 `v2` 表（含 `turns` 时间线列、`turn_prompts` 与 `worker_history_pruned`）与索引 `(worker_id, started_at DESC, turn_seq DESC, turn_id DESC)`；旧 `v1` 库首次打开时一次性执行建列、从轮次 JSON 回填起止时间并写入 `user_version = 2`，后续常规启动零迁移开销（不支持向旧版本二进制降级回滚，旧程序遇到更高版本库按既有 `err.store.schema_newer` 拒绝启动）。Web 面板每 2 秒轮询一次，采用索引列而非运行时 JSON 解析以保证亚毫秒级分页与汇总。30 天摘要过期清理（`DeleteExpiredTerminalTurns`）在同一显式事务内按主键删除关联明细并累加 `worker_history_pruned`，不使用隐式数据库触发器。
- `Worker.RoleHint` 只在创建时写入，生命周期内不可修改，因此不需要在轮次上保存角色提示快照。
- 已确认的默认值：完整任务说明保留 7 天（与结果正文一致）；默认展开运行中轮次，没有则展开最新一轮；首屏 20 轮。

### 验收用例及预期可见结果

1. 新建 Worker 并发送一轮任务，完成后在编程协作者中展开该轮：可见完整任务说明、结果正文、工作树快照和日志；归档后再次查看内容不变。
2. 有 `role_hint` 的 Worker 在任务概要区显示角色提示，长文本可折叠/展开。
3. Worker 头部显示轮次数、累计轮次时长和墙钟时长；累计值与各轮耗时之和一致；运行中的轮次实时计入。
4. 对同一 Worker 连续发送 60 轮：时间线显示 `#60` 到 `#41`，加载更早后依次出现 `#40` 到 `#1`，序号连续、无遗漏、无重复；对旧轮次执行对账后，它的位置不变。
5. "只看异常"仅显示失败、取消、中断、待对账、受阻和等待中的轮次。
6. CLI 与 MCP 调用 `workers_turns` 翻页的结果与 Web 一致；`workers_result(part="prompt")` 可完整读回 128 KiB 中文任务说明（跨页 UTF-8 边界正确）。
7. 本功能上线前产生的历史轮次显示推导序号和"未记录任务说明"，不报错。
8. 完整任务说明超过保留期被清理后，展开详情显示摘要和"完整内容已按保留策略清理"。
9. 行内序号、状态、摘要、耗时、时间垂直对齐，中英文界面均无溢出。

## 实施与文档同步

- **实施步骤**：
  1. 用户确认本计划及待确认默认值。
  2. 更新领域文档与覆盖清单（见下），登记 `workers_turns` 和 `workers_result.part`。
  3. 执行器：新增 `internal/workers/store_turns.go`（`turn_prompts` 读写与清理、分页查询、汇总查询）和 `internal/workers/service_turns.go`（`ListTurns`、`Summary`、任务说明分页读取）；在 `service.go` 接纳路径保存任务说明并分配 `turn_seq`；`types.go` 增加字段。
  4. 自动化：在 `internal/app` 注册 `workers_turns`、扩展 `workers_result` 与 `workers_get` 视图和响应预算；更新 `contracts/automation-coverage.json`、机器 schema 与 MCP 文本输出。
  5. Web：`/api/workers/get` 返回 Worker、汇总与时间线首页，新增 `/api/workers/turns`（翻页与筛选）和 `/api/workers/result`（`part=answer|prompt` 续读）；新增 `web/workers-timeline.mjs` 并纳入 `web/embed.go`；更新 `workers.css` 与 i18n 词表。
  6. 补充测试并运行验证，检查文档链接。
- **必须更新的领域文档及实质内容**：
  - [执行器需求](../../../executors/requirements.md)：新增 `EXEC-HARNESS-11`，包括任务说明留存与保留期、序号分配与历史推导、耗时汇总口径。
  - [执行器设计](../../../executors/design.md)：`turn_prompts` 存储、序号临界区、按 `started_at` 排序、`DBSchemaVersion = 2` 单向迁移与显式过期清理事务。
  - [自动化需求](../../../automation/requirements.md)、[自动化设计](../../../automation/design.md)、[CLI 与 MCP 使用指南](../../../automation/usage.md)：`workers_turns` 参数、游标与筛选，`workers_result.part`，`workers_get.summary`、`turns_has_more`。
  - [自动化覆盖清单](../../../automation/coverage.md)：Web「编程协作者」的时间线、任务说明与汇总对应 `workers_turns`、`workers_result`、`workers_get`；展开、折叠和滚动属展示等价。
  - [Web 需求](../../../web/requirements.md)：新增 `WEB-HARNESS-05`；[Web 设计](../../../web/design.md)：时间线结构、模块职责、增量刷新与状态保持。
  - [开发路线图](../../roadmap.md)、[文档导航](../../../README.md)：登记本功能。
- **契约与用户说明变化**：新增一个只读公共操作和一个可选参数，均向后兼容；`workers_get` 默认返回结构只增不减。无配置、安装或权限模型变化。
- **自动化覆盖**：`workers_turns` 与 `workers_result.part` 同步提供 CLI 和 MCP，共用 schema、错误码与作用域检查；`TestAutomationCoverage` 必须通过，并补充 CLI 和 MCP 结果等价测试。没有人工例外。

## 规模与拆分评估

- 交付后相关文件行数：`internal/workers/service_query.go` 981 行（`Get` 与 `populateTurnActivity` 复用公共辅助函数，较改动前减少 10 行），`internal/workers/store.go` 838 行（轮次与 Worker 写入改为复用 `upsertTurnExec` / `upsertWorkerExec`），`internal/workers/store_retention.go` 484 行，新增 `store_turns.go` 545 行、`service_turns.go` 371 行、`internal/app/automation_workers_turns.go` 118 行；`internal/app/automation_workers.go` 783 行，`internal/app/workers_api.go` 293 行；前端 `web/workers-panel.mjs` 由 716 行降为 484 行，新增 `web/workers-timeline.mjs` 936 行，`tests/workers-panel.test.mjs` 906 行，新增 `tests/workers-timeline.test.mjs` 955 行。所有文件低于 1000 行。
- 把轮次渲染从 `workers-panel.mjs` 迁到 `workers-timeline.mjs` 属于本功能的职责划分（面板负责 Worker 选择、轮询与数据装载，时间线负责轮次列表、展开详情与增量 DOM 更新）；`workers-panel.mjs` 未超限，不属于超限文件拆分。
- 函数规模例外：`createTurnTimeline` 与既有 `workersPanel` 一样采用闭包控制器工厂，用于让轮次列表、查询纪元、展开状态与详情版本缓存共享私有状态；其内部各子函数（`renderItem`、`bodyContent`、`applyFirstPage`、`loadDetail` 等）均不超过 100 行。拆成多个模块需要把可变状态暴露为共享对象，可读性更差，因此保留。Go 新增生产函数均不超过 100 行；测试函数 `TestTurnTimelineSequencePagingPromptAndSummary` 与 `TestTurnTimelineAuditRegressions` 按端到端场景顺序断言，拆分会重复多轮数据库准备步骤，因此保留。

## 验证与维护

- **仓库测试入口**（需先将锁定版 Go 1.27.3 工具链加入 `PATH`）：
  - `go vet ./...`
  - `go test -count=1 ./internal/workers ./internal/app ./internal/automation ./internal/i18n ./cmd/asterhand ./cmd/asterhand-mcp ./cmd/asterhand-cli`
  - `$ui = Get-ChildItem -LiteralPath tests -Filter '*.test.mjs' -File; node --test $ui.FullName`
  - `node ./scripts/verify-frontend-assets.mjs`、`pwsh -NoProfile -File ./scripts/check-i18n.ps1`
- **新增测试与覆盖点**：
  - [turn_timeline_test.go](../../../../internal/workers/turn_timeline_test.go)：25 轮连续序号、按开始时间稳定排序（旧轮次迟到更新不跳位）、三页游标翻页无遗漏无重复、113 KiB 中文任务说明的预览与 UTF-8 对齐分页、非法游标与筛选报错、汇总时长口径（已结束、待对账统一冻结至 `updated_at`）与 `partial`（含无序号旧轮次被清理后的 `partial` 与单调递增 `NextTurnSeq`）、30 天摘要过期清理显式删除关联 `turn_prompts` 并释放详情预算、历史无序号轮次在异常筛选下的单次覆盖索引全局排名、VCS 超过 32 个变更文件与暂存/未暂存/未跟踪折叠目录及旧 `v1` 截断样本遗漏折叠目录的下限标识、旧 `v1` 库一次性迁移至 `v2` 并拒绝更高未知版本，以及单 Worker 500 轮时首页与三元组深页游标的查询计划均定位 `idx_turns_worker_started` 且无额外排序。
  - [automation_workers_turns_test.go](../../../../internal/app/automation_workers_turns_test.go) 与 [cli_wait_test.go](../../../../internal/automation/cli_wait_test.go)：`workers_turns` 公共操作翻页与 `next_step` 携带显式 `workspace_id`/`filter`、单条轮次携带 50 KiB 超长 `error_code` 时裁剪非身份字段并标注 `error_code_truncated` 以保持单页可读、MCP 文本超限兜底保留当前请求 `cursor` 且不放大 `limit`、换筛选复用游标返回 `invalid_cursor`、`workers_result(part="prompt")` 常规分页与超预算兜底均保留 `part="prompt"`、Web `/api/workers/logs` 预取失败轮次留存的 `partial` 部分输出并支持续读、`workers_get.summary`、MCP 文本投影，以及超预算时缩页并给出可续读游标。
  - [workers-panel.test.mjs](../../../../tests/workers-panel.test.mjs) 与 [workers-timeline.test.mjs](../../../../tests/workers-timeline.test.mjs)：任务概要、编号行与任务说明、跨 Worker 切换失败重试与并发轮询零串页、关闭期间新增超 20 轮重开断档重置游标、完成轮次超过 100 条日志持续续读至末页后停轮询、筛选切换丢弃在途旧轮询与跨筛选旧行/游标、同筛选迟到初始首页不回退已加载旧页、增量日志刷新保留在途与已续读正文分页、正文续读与增量日志过期通知任意先后交叠（含迟到末页补齐全文与迟到中间页收起续读按钮）的状态合并、闭窗切换语言重开即时重本地化、空闲轮询 `wall_seconds` 跳秒保持关闭按钮焦点、0 轮 Worker 首轮立即完成时默认展开、失败轮次部分输出续读与长任务说明滚动位置保持、500 轮线性 ID 读取与 20 个大文本展开轮次无变化零 JSON 重序列化；原有原子切换、日志原位追加与交互队列测试继续通过。
- **工程验证结果**：在 Windows 本机以上述命令执行，`go vet` 通过；7 个 Go 包测试通过；Node 全部 315 个测试通过；前端资源校验与国际化守卫通过。500 轮查询测试的首页、深页与汇总合计耗时在亚毫秒级。
- **前置环境和未覆盖平台**：仅覆盖 Windows 本机 Go 与 Node 测试。界面视觉效果（对齐、折叠、长列表滚动）以及真实 Claude Code、Codex、DeepSeek Harness 多轮协作的端到端表现，需在本地安装后由用户验收。
- **当前能力边界与后续工作**：Markdown 渲染、协作记录导出、跨 Worker 工时统计和任务说明搜索作为后续独立功能评估。
