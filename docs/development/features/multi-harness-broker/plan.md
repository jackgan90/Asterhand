# 多 Coding Harness 本地中介与统一接入管理计划

## 基本信息

- **功能名称**：`multi-harness-broker`（多 Coding Harness 本地中介层、自动发现与统一 MCP 管控）
- **目标与用户价值**：
  1. 允许用户继续留在习惯的原生 Coding Harness（如 Antigravity）中完成日常开发，直接通过自然对话把子任务（如审 Plan、审 Code、跨模型实现与复核）派发给本机其他已安装的 Coding Harness（Codex、Claude Code、DeepSeek Harness），并将审查结论可靠回传至发起方原对话继续推进；
  2. 为被调方提供**绑定授权工作区的稳定逻辑身份**（例如同项目内的 `codex-reviewer` 先审 Plan、再审 Code），在不强制跨 Harness 共享私有聊天库的前提下保持跨轮上下文连续；
  3. 由 Asterhand 统一发现本机已安装的 Coding Harness、托管其 MCP 注册/反注册与对账，并按**三项正交开关**控制每个 Harness 的工具暴露程度和参与角色，实现“最小必要工具暴露、升级不断连、一键启用/禁用即时生效”。
- **状态及实施授权范围**：
  - **当前状态**：`Milestone 1—2 已完成实现、工程验证及本机端到端用户验收；Milestone 3—4 保持规划态，待后续独立授权后进入实现`。
  - **授权边界**：Milestone 1—2 的代码、自动化测试、本机真实协议实测、用户真机验收与文档同步已完成；Milestone 3—4 实现、推送和 Release 发布各自独立授权。
  - **预检与验证文档**：[协议预检记录](preflight.md)、[验证说明](validation.md)。
- **关联 Rxx / Cxx / 领域需求 ID**：
  - 原始与收敛需求：`R10`、`R19`、`R20`、`R26`、`R27`、`R28`；扩展原 `C06`（见 [ADR-002](../../../overview/adr/002-mvp-scope.md) 后置范围的新立项增量）；
  - 领域需求：`EXEC-HARNESS-01`—`10`（[执行器需求](../../../executors/requirements.md)）、`AUTO-HARNESS-01`—`05`（[自动化需求](../../../automation/requirements.md)）、`INST-33`（[安装需求](../../../installation/requirements.md)）、`WEB-HARNESS-01`—`04`（[Web 需求](../../../web/requirements.md)）。
- **相关领域 requirements / design 链接**：
  - [执行器需求](../../../executors/requirements.md)、[执行器设计](../../../executors/design.md)
  - [自动化需求](../../../automation/requirements.md)、[自动化设计](../../../automation/design.md)、[日常操作覆盖清单](../../../automation/coverage.md)
  - [安装需求](../../../installation/requirements.md)、[安装设计](../../../installation/design.md)
  - [Web 需求](../../../web/requirements.md)、[Web 设计](../../../web/design.md)
  - [后续能力方向](../../../overview/future.md)、[开发路线图](../../roadmap.md)
- **关联 ADR**：
  - [执行器 ADR-008：多 Coding Harness 本地中介层与官方原生协议适配](../../../executors/adr/008-multi-harness-broker-and-native-adapters.md)
  - [自动化 ADR-005：Coding Harness 统一 MCP 托管与三开关动态工具集](../../../automation/adr/005-harness-mcp-lifecycle-and-dynamic-toolsets.md)

---

## 合规红线、信任模型与职责边界

### 1. 官方 ToS 与零侵入认证原则
- **仅调用官方未修改本体**：Asterhand 仅作为本地进程托管与官方协议转译层，不内嵌、不逆向、不替代任何第三方 Coding Harness 的运行时或模型通信。
- **零第三方凭据接触**：严禁读取、解析、复制、备份或代理任何第三方 Harness 的本地 OAuth 凭据、API 密钥或外部 MCP 配置中的认证头。计费方式（API Key 或订阅）完全由用户在对应原生 Harness 内自行管理。
- **发起方 (A) 与被控方 (B) 角色分离**：
  - **Antigravity**：静态锁定为**仅发起方（A）**（`worker_supported = false`），通过原生 MCP Client 调用 Asterhand，不将消费者订阅版 Antigravity 作为被控 Worker 循环调用；
  - **Codex / Claude Code / DeepSeek Harness**：支持作为 A 和 B，全部采用官方公开长期协议与 CLI 管理接口，不做临时过渡封装。

### 2. 诚实的信任边界（同 Windows 用户前提下的策略执行）
- **同账户边界声明**：在本机 Windows 同一当前用户账户下，外部 Harness 与 Asterhand 运行于同一 OS 用户空间；Asterhand **不宣称**能以纯应用层令牌阻断同一 Windows 账户下的恶意进程伪造连接或越权读盘。三开关与受管入口的真实保证范围是：**面向合作型外部 Harness 的托管入口策略、工具集隔离、防模型误调用、防意外递归与可撤销主体验证**。
- **统一的受限主体（`Principal`）与全路径闸门**：
  - 每个受 Asterhand 托管接入的 Harness 绑定可撤销的独立本机调用凭证与受限 `Principal`（不再仅靠未校验的 `--caller` 字符串复用全权 `localPrincipal()`）；
  - 该 `Principal` 的有效工具集与权限策略在 **`tools/list`、单次 `Dispatch`、`batches.submit` 逐项子调用、`operations.get` 收据作用域以及 `Client.Call` 中的本地特殊操作（如 `node.start`）** 上强制一致生效；
  - 既有的无 `--caller` 兼容 CLI/MCP 入口明确界定为“本机当前用户显式全量自动化入口”，不与受管 Harness 的三开关策略混用。
- **受管 Worker B 的双层防递归隔离**：
  - **第一层（启动侧隔离）**：Asterhand 启动 Worker B 子进程时，通过该 Harness 的原生参数/隔离配置显式禁用 `asterhand` MCP（如 Codex 传入 `-c mcp_servers.asterhand.enabled=false`；Claude Code 通过受控 MCP 配置排除 `asterhand`；DeepSeek Harness 通过 profile/patch 排除），且不向 B 注入任何 Asterhand 自动化登录凭证；
  - **第二层（服务端兜底）**：受管上下文附加 `max_delegation_depth = 1` 门禁，禁止 B 再次调用 `workers.*` 写操作或访问 `general` 通用自动化工具集。
- **人工独占边界**：外部 MCP 注册/反注册、三开关、路径覆盖、授权委派工作区与最高访问档位配置，以及 Milestone 4 的高危权限审批，全部保留为人类专用入口（`AUTO-19`、`AUTO-20`），严禁暴露给发起方 A 的模型自动修改或代批。

### 3. 领域职责交界（单一权威来源）

| 模块 | 唯一权威职责 | 明确不承担 |
|---|---|---|
| `internal/harnesses` | 发现结果、按 `(harness × version × platform × access_mode)` 验证的能力矩阵、三开关与授权工作区配置、外部 MCP 期望/观察状态与可重试对账 | 不管理 Worker 轮次状态机、不直接执行 MCP 传输闸门 |
| `internal/workers` | 授权工作区内的 Worker 身份、原生会话映射、单 Worker 活跃轮次门禁、规范化工作树写互斥、取消、恢复、长结果与日志持久化及空闲进程回收 | 不改写外部 Harness 配置、不定义通用自动化主体 |
| `internal/automation` | 统一调用主体（`Principal`）、工具集可见性、单调用/批处理/收据/`node.start` 闸门、幂等收据、等待槽位与面向模型的 MCP 文本+结构化响应封装 | 不解释厂商原生协议、不持有 Worker 长正文存储 |
| 各厂商 `HarnessAdapter` | 子进程启动（`OrdinaryToken` + 具名 `JobObject`）、官方双向协议翻译、绑定原生 `(threadId, turnId)` 的中断与会话恢复 | 不仲裁三开关权限、不绕过工作区授权 |

---

## 核心架构设计

### 1. 面向发起方模型的 MCP 结果回传契约（核心闭环保障）

现有 `internal/automation/mcp.go` 的 `mcpResult` 仅在 `Content` 文本中返回 `"Completed"` 或收据状态，业务数据全部放在 `StructuredContent`；同时写操作前必须先调用 `operations.new_id`、回执丢失后需调用 `operations.get`，这两项也仅把结果放在 `Data`。为确保只消费 `content[].text` 或同时消费 `structuredContent` 的客户端（包括 Antigravity）都能稳定完成“生成 ID → 创建 → 派发 → 断线找回 → 取正文”全闭环：

- **全链路 `core + collab` 文本回传覆盖**：将 `operations.new_id`、`operations.get`、`capabilities` 以及全部 `harnesses.*`、`workers.*` 纳入双通道契约——除保留完整 `structuredContent` 外，`content[].text` 必须输出有界、面向模型的结构化文本摘要：
  - `operations.new_id`：文本直接包含生成的 `operation_id`（及适用操作名）；
  - `operations.get`：文本直接包含原 `operation_id`、接纳状态、关联的 `worker_id` / `turn_id` 或错误码；
  - `capabilities`：文本包含当前受限主体可用的工具集与关键预算上限；
  - `workers.*` 四类状态回传：
    1. **运行中（`running`）**：包含 `worker_id`、`turn_id`、`session_epoch`、已耗时、最新阶段进度及明确后续动作指引（“轮次尚未结束，请继续调用 `workers.wait(turn_id=...)`”）；
    2. **已完成（`completed`）**：包含 `worker_id`、`turn_id`、访问模式、结果来源标记（`answer_source`）与工作树观察证据摘要，以及**完整审查/执行正文**（若超出单次响应预算则内嵌首段正文、`truncated: true`、`result_bytes` 及 `workers.result(turn_id=..., cursor=...)` 续读指引）；
    3. **阻塞/未支持交互（`blocked_unsupported_approval` / `blocked_unsupported_user_input`）**：明确说明被控 Harness 请求了当前阶段尚未接入的审批或提问、轮次已有界终止并释放锁，附具体原因供 A 转告用户；
    4. **失败/中断/待核对（`failed` / `cancelled` / `interrupted` / `reconciliation_required`）**：包含机器错误码、可读诊断与 `workers.logs(turn_id=...)` 排障指引。
- **Codex 最终正文组装与防重复拼接规则**：
  - 适配器按原生 `(threadId, turnId, itemId)` 幂等归集条目：`commentary`、工具调用与流式 `delta` 归入运行进度与结构化日志（`workers.logs`），绝不与最终正文重复拼接；
  - 最终结果正文（`workers.result`）**仅对 `status == "completed"` 的成功轮次**执行候选选择并发布，按严格优先级：
    1. 优先按原生条目顺序组装本轮全部 `phase == "final_answer"` 的文本段（`answer_source: "final_answer"`、`answer_available: true`）；
    2. 若无 `final_answer`，则**排除已知 `commentary` 与工具条目**，从剩余的无 `phase` 或 `phase == "partial_answer"` 助手消息中取**最后一条**作为最终正文（`answer_source: "fallback_last_candidate_assistant_message"`、`answer_available: true`）；
    3. 若无任何有效候选（如整轮仅有 `commentary`），显式返回 `answer_available: false` 与 `answer_source: "none"`，不伪造空正文；
  - 对 `interrupted`、`cancelled`、`cancelled_authorization_revoked`、`failed` 等**非成功终态**，**绝不执行最终正文选择**，也不会把中断前已收到的 `partial_answer` 提升为完整审查结论；已有片段仅作为明确标示的 `partial_output`（`answer_available: false`）保留于 `workers.logs` / 部分输出字段供诊断查看；
  - 对**可恢复的非终态 `reconciliation_required`（待核对）**：待核对期间同样不发布最终正文（`answer_available: false`）。恢复流程严格区分两种情形——① 普通回执丢失（未落盘撤权标记）：仅凭唯一 `client_correlation_id` 匹配到唯一原生轮次且确认其为 `completed` 时，才可恢复为 `completed` 并发布最终正文；② 撤权标记已落盘：迟到原生记录或回执仅补充审计执行事实，绝不可将该轮对外恢复/发布为 `completed`；
  - 按原生 `itemId` 去重，确保重连补读绝不重复拼接流式片段。
- **原对话接续与断线补读边界**：
  - 首版采用**原对话内主动轮询模式**：由发起方 A 调用 `workers.send` 立即拿到 `turn_id`，再在原对话内循环调用 `workers.wait` 取回终态结果；
  - 若 A 因超时、断连或模型策略提前结束本轮，任务仍由 Asterhand 持久化托管；用户只需在原对话提示继续查看，A 即可经 `operations.get` / `workers.list` / `workers.get` / `workers.result` 无损补读已落盘正文。首版**不宣称**支持从服务端反向唤醒 Antigravity 已结束的对话轮次。

### 2. 自动项目隔离、敏感路径底线、三层身份与防串轮规则

#### 2.1 免登记直通项目目录与敏感路径拦截（用户决策：方案 A）
- **日常顺畅优先（免逐项目登记）**：用户无需在 Asterhand 中手工逐一登记或维护项目白名单；在任意本地项目中，发起方直接传入当前项目的 `workspace_root` 即可协作。Asterhand 自动对路径做规范化（解析符号链接/junction 与 Windows 大小写）并确定性派生稳定 `workspace_id`，确保同项目重启后身份一致、多项目同名别名（例如项目 X 与项目 Y 各自独立的 `codex-reviewer`）天然隔离不串台。
- **内置敏感路径黑名单底线**：底层拒绝明显危险目录作为 Worker 工作区，包括：文件系统根目录（如 `C:\`、`/`）、用户主目录本身（`%USERPROFILE%`）、系统临时目录根本身、Asterhand 节点自身数据目录、`Windows` / `Program Files` / `ProgramData`、`AppData`（`Temp` 下的独立子目录除外）以及 `.ssh` / `.aws` / `.gnupg` / `.kube` / `.azure` / `.config/gcloud` / `.gemini` / `.codex` / `.claude` / `.dsh` 等凭据目录。
- **跨项目防误控与访问档位边界**：
  - Worker、唯一别名（`(workspace_id, alias)`）、轮次、结果与日志均绑定规范化派生的 `workspace_id`；`artifact_paths` 严格约束在该工作区根内（拒绝越界与外部 junction）；
  - `workers.get`、`workers.send`、`workers.cancel`、`workers.close` 等按 `worker_id` 发起的操作支持传入预期 `workspace_id` 做防误控核验，目标不匹配时拒绝返回 `workspace_mismatch`；
  - Milestone 1 节点默认仅开放 `read_only` 访问档位（`workspace_write` 在 Milestone 2 开放）；鉴于本机 Codex `readOnly` 沙箱策略不含 `readableRoots` 字段，**不宣称** `read_only` 模式在 Windows 上能构成目录级只读 chroot，其保障的是“禁止写盘/改文件”与“普通用户非提权 `OrdinaryToken` 运行”。

#### 2.2 三层身份、关联对账与单 Worker 严格防串轮
- **`worker_id`（逻辑角色）**：持久化 `workspace_id`、`alias`、`harness_id`、`access_mode`、`state` 与当前 `session_epoch`。
- **`native_session_id`（底层原生会话）**：按 `(worker_id, session_epoch)` 绑定原生会话 ID（如 Codex `threadId`、Claude `session_id`）。若原生会话因过期、损坏或版本变更无法恢复，**绝不静默开新会话冒充连续上下文**，而是返回 `native_session_unrecoverable`，需显式触发 `reset`（推进 `session_epoch`）或新建 Worker。
- **`turn_id`（单次派发轮次）**：一对一绑定幂等 `operation_id`、`session_epoch`、发前持久化的唯一关联键 `client_correlation_id`（由 `turn_id` 派生）与 `native_turn_id`（如 Codex `turnId`）：
  - **单 Worker 严格单活跃轮次**：同一 `worker_id` 无论 `read_only` 还是 `workspace_write`，同时最多允许 1 个活跃轮次；并发第二次 `workers.send` 立即返回 `worker_busy`；
  - **精确取消与防迟到串轮**：`workers.cancel` **必填 `turn_id`**，取消、事件流与终态提交均核对 `(worker_id, session_epoch, turn_id, native_turn_id)`（例如 Codex `turn/interrupt` 同时传原生 `threadId` 与 `turnId`），迟到取消或旧轮次晚到事件绝不影响后续新轮次；
  - **送达回执丢失的精确关联对账**：发出 `turn/start` 前先持久化 `client_correlation_id` 并在协议支持时随请求透传（如 Codex 0.171.0 的 `turn/start.clientUserMessageId`，经 M1 预检验证其与历史 `userMessage.clientId` 在重启后的持久唯一性）。若回执丢失导致 `native_turn_id` 未知，该轮进入 `reconciliation_required` 并阻塞该 Worker 接纳新轮次：**仅当在原生历史分页中通过唯一关联键匹配到唯一轮次**时才补写 `native_turn_id` 与结果，**严禁按提示词正文相等猜测匹配**；无法唯一确认时保持待核对，并提供按 Asterhand `turn_id` 的人工核对关闭入口（记录处置原因与证据，不伪造成功）。

### 3. 纯写 `send`、独立 `wait` 与完整运行/存储预算（对齐现有超时与幂等）

- **`workers.send` 为纯写接纳操作（不含 `wait_seconds`）**：
  - 参数包含 `worker_id`（及可选核验 `workspace_id`）、`prompt`、可选单轮覆盖参数（`model`、`reasoning_effort`，缺省继承 Worker 创建时默认值）及可选结构化交接信封（`artifact_paths`、`git_ref`、`diff_base`、`output_contract`）；
  - 在校验授权、节点并发上限、单 Worker 活跃锁与写互斥锁后，可恢复地登记轮次（含 `client_correlation_id`）并立即返回 `turn_id`、`session_epoch` 与接纳收据，完全避开 40s 客户端超时（`internal/automation/client.go`）与 45s 服务端写超时（`internal/automation/transport.go`），不污染 `operations.Hash(value)` 幂等摘要。
- **`workers.wait` 为独立有界观察操作**：
  - 单次等待上限严格对齐现有 `automation.MaxWait = 30s`，复用独立 `waitSlots`（不占用 `writeSlots`）；客户端断开或超时仅释放观察等待，不取消已接纳轮次。
- **活跃轮次运行预算、节点停机协同与撤销语义**：
  - **节点级活跃轮次上限**：默认同时最多 `4` 个活跃轮次，常驻适配器进程上限默认 `4` 个（超限时优先回收最久空闲且无活跃轮次的进程，全忙则返回 `worker_capacity` 拒绝新派发）；
  - **单轮总墙钟时限**：默认 `15 分钟`（可配上限），计时不因输出或轮询重置；
  - **无事件/无输出超时（Stall Timeout）**：默认 `5 分钟`（可配），防止子进程存活但不再发事件时无限占用 Worker；
  - **超时与失联处置**：触发超时或取消时，先发原生中断（如 `turn/interrupt` 带双 ID），有界等待（默认 `10s`）未退出则强制终止 Windows 具名 `JobObject` 进程树；**必须确认进程/轮次已停止后再释放 Worker 活跃锁与工作区写锁**；若执行停止无法证明则保留 `reconciliation_required` 锁存，不提前放行新轮次；`workers.cancel` 走独立控制通道（`Control: true`），不被 `wait` 或日志读取阻塞；
  - **纳入节点停机原子链与关闭顺序**：`Workers.BeginStop(confirmed, next)` 接入 `App.RequestStop(confirmed)` 原子检查链（与 `Commands`、`Chat`、`Engine` 同链）。存在活跃 Worker 轮次时，`confirmed = false`（托盘普通退出或普通 `node.stop`）返回忙状态；仅存在无活跃轮次的空闲常驻 `app-server` 不算忙并直接优雅回收。`confirmed = true` 确认退出后先关闭新 Worker/轮次接纳，在 `App.Close()` 关闭 SQLite 之前停止所有活跃 Worker、落盘终态或待核对状态并回收进程树，最后关库；
  - **精细区分三类撤权对后续调用与在途轮次的影响**：
    - **吊销整份调用凭据**（或同时关闭 `general_tools_enabled` 与 `collab_caller_enabled`）：旧凭据全部立即失效，拒绝一切后续读取、派发与控制请求；此前已接纳且归节点持有的在途轮次继续由节点按既定规则收尾（M1 可经最小人工接入区的活跃轮次停止按钮或本机 CLI 处置，M2 扩展至独立 Web「Coding Workers」页面）；
    - **仅关闭 `collab_caller_enabled`（但 `general_tools_enabled` 仍开启）**：不吊销整份连接凭据，而是按剩余开关实时重算有效权限（保留 `core + general`，触发 `notifications/tools/list_changed`），立即拒绝 `workers.*` 协作调用，且 **`operations.get` 按原操作所属工具集与当前有效权限过滤收据**（不可读取已撤销协作工具集的旧收据）；已接纳在途轮次同样由节点按既定规则收尾；
    - **撤销授权工作区（含下调工作区最高档位）或关闭被控方 B 的 `collab_worker_enabled`**：覆盖三个精确窗口——① 已接纳落盘但尚未发起原生 `turn/start` 的预备轮次直接拒绝启动并记 `cancelled_authorization_revoked`；② **`turn/start` 已发出但 `native_turn_id` 尚未返回**时，先持久化撤权标记并阻止新派发，在短有界窗内若拿到匹配的 `native_turn_id` 则精确中断，否则直接有界回收该受管 `JobObject` 进程树，**迟到到达的 `turn/start` 回执仅作关联审计补充、严禁将已撤权轮次恢复为 `running`**；③ 已运行中的原生轮次发双 ID `turn/interrupt` 并在超时后回收 `JobObject`。三类窗口均在确认停止后释放锁，无法确认停止则保留 `reconciliation_required` 锁存；
  - **领域收据崩溃恢复**：将 `workers.create`、`workers.send`、`workers.cancel`、`workers.close` 接入 `internal/app/automation_recovery.go` 的 `reconcileAutomationOperation`（或同 SQLite 事务发布关联），确保“Worker 轮次已落盘、公共自动化收据尚未完成”窗口重启后，按原 `operation_id` 调用 `operations.get` 仍可修复收据并找回原 `turn_id`，重复 `send` 绝不触发第二次原生派发。
- **长正文、日志与空闲进程预算**（对齐 `MaxResponse = 64 KiB` 与 `operations.ResultLimit = 8 KiB`）：
  - `operations.Receipt` 仅存轻量接纳/终态摘要（`<= 8 KiB`），绝不塞入长审查正文；
  - 完整结果正文与结构化运行日志独立持久化于 `internal/workers` 有界存储：终态摘要保留 30 天，正文与详细日志保留 7 天并设单轮输出上限与节点总容量预算（活动及待核对记录不淘汰）；
  - Milestone 1 同步交付分页 **`workers.result`**（按 `turn_id` + 游标有界读取完整正文，明确标示 `complete` / `truncated` / `expired`）与 **`workers.logs`**（按 `turn_id` + 游标读取进度、工具活动与失败诊断，与 Web 实时日志同源）；
  - 空闲常驻适配器进程默认 `10 分钟`无活跃轮次自动优雅回收，保留 SQLite 中的 `worker_id ↔ native_session_id` 绑定，下次 `workers.send` 按需拉起并 `resume`；Worker 关闭归档后在保留期内仍可读历史结果与日志。

### 4. 按 `(Harness × 版本 × 平台 × 访问模式)` 诚实声明能力与并发保护

- **不作跨 Harness 泛化“硬只读”承诺**：每个适配器按 `(harness, version, platform, access_mode)` 实测并声明实际防护等级：
  - `os_sandbox_enforced`（例如 Codex 在实测通过其只读沙箱与审批策略配置后）；
  - `harness_tool_restricted`（例如 Claude Code 在原生 Windows 无 OS 级 shell 沙箱的前提下，仅通过原生禁写/只读工具策略限制，明确告知其为应用层约束）；
  - `unsupported`（若某版本/平台无法可靠落实请求的档位，在 `workers.send` / `workers.create` 前直接拒绝返回 `unsupported_access_mode`）。
- **统一进程基线**：所有受管 Worker 子进程均使用非提权 `userprocess.OrdinaryToken()` + Windows 具名 kill-on-close `JobObject` 运行，声明受限环境变量与受控扩展/MCP 边界。
- **Milestone 4 审批桥上线前的确定性收敛**：若在审批桥未启用期间收到原生审批请求或业务提问，适配器立即按协议拒绝/中断该请求，有界结束轮次、释放 Worker 活跃锁与工作区写锁，并返回 `blocked_unsupported_approval` 或 `blocked_unsupported_user_input`（附脱敏请求摘要），绝不挂死等待墙钟超时。
- **工作区写锁与 Git 观察证据**：
  - 写互斥锁绑定到解析 junction/symlink 并规范化后的 **Git worktree 根**（非 Git 目录则为规范化目录根），避免通过仓库根、子目录、大小写变体或 junction 绕过互斥；
  - 派发前后采集工作树观察快照（涵盖 staged、unstaged、untracked；非 Git 目录返回 `vcs: "none"`），在结果中明确区分基线是否已脏、并如实标注“窗口内变化可能包含用户或原生 IDE 的外部并发修改”；只读审查参数明确区分读取实时工作树还是固定 `git_ref` / `diff_base`。

### 5. 本机 Harness 发现、三开关正交模型与可重试 MCP 托管对账

#### 5.1 本机发现器（只读、零凭据接触）
探测顺序：人工路径覆盖 > `PATH`（Windows 正确解析 `.exe` / `.cmd` / npm shim）> 合规安装目录（如 `%LOCALAPPDATA%\Programs\...`、`%LOCALAPPDATA%\agy\bin`、`%APPDATA%\npm`、`%USERPROFILE%\.local\bin`）与标准卸载注册表 > 短超时只读 `--version`。

#### 5.2 三开关正交配置与 8 种组合预期行为
每个 Harness 维护三项独立布尔设置（修订号保护）：`general_tools_enabled`（Asterhand 通用自动化工具集）、`collab_caller_enabled`（协作发起方 A）、`collab_worker_enabled`（协作被控方 B；Antigravity 静态锁定为 `false`）。外部 MCP 期望注册条件为 `mcp_desired = general_tools_enabled || collab_caller_enabled`。

| `general_tools_enabled` | `collab_caller_enabled` | `collab_worker_enabled` | 外部 MCP 期望状态 | 暴露工具集 | 预期行为 |
|---|---|---|---|---|---|
| 关 | 关 | 关 | 反注册 / 未注册 | 无 | 完全脱离 Asterhand：既不挂载 MCP，也不可被派发 |
| 关 | 关 | 开 | 反注册 / 未注册 | 无 | 纯 Worker：零工具上下文污染，仅接受其他开启 A 的 Harness 派发 |
| 开 | 关 | 关 | 已注册 | `core + general` | 仅可调用 Asterhand 通用能力，完全不参与多 Harness 协作 |
| 开 | 关 | 开 | 已注册 | `core + general` | 可用 Asterhand 通用能力 + 可作为 B 被派发，不可主动发起派发 |
| 关 | 开 | 关 | 已注册 | `core + collab` | **Antigravity 推荐配置**：精简上下文，专注调度其他 Harness |
| 关 | 开 | 开 | 已注册 | `core + collab` | 纯多 Harness 双向协作者，不暴露通用自动化工具 |
| 开 | 开 | 关 | 已注册 | `core + general + collab` | 全能力主控端（通用自动化 + 调度其他 Harness） |
| 开 | 开 | 开 | 已注册 | `core + general + collab` | 全能力双向节点；被 Asterhand 作为 B 启动时在原生侧禁用 `asterhand` MCP |

#### 5.3 期望状态 vs 观察状态对账与无凭据恢复元数据
- 分别保存每个 Harness 的 **`desired_state`**（三开关与托管意图）与 **`observed_state`**（最近探测到的外部配置状态、版本、对账时间与失败诊断），支持幂等重试对账；
- **关闭权限先在服务端立即生效**：即使外部 CLI 写入/反注册暂时失败，服务端闸门已按新策略立即拒绝对应调用，并发送 `notifications/tools/list_changed` 通知已连接客户端刷新工具清单；
- **最小侵入写入与冲突保护**：
  - Codex 优先 `codex mcp add/remove`；Claude Code 优先 `claude mcp add --scope user/remove`；Antigravity 仅原子操作 `%USERPROFILE%\.gemini\config\mcp_config.json` 的 `mcpServers.asterhand` 单键；DeepSeek Harness 实测确认格式后仅维护单一条目；
  - **严禁备份整份外部配置文件**（避免把用户其他 MCP server 里的认证头或 OAuth 密钥复制到 Asterhand 数据目录），仅在节点侧保存 Asterhand 托管条目的恢复元数据；
  - 检测到同名但归属不匹配的外部条目时标为 `conflict`，拒绝静默覆盖或删除，需人类显式确认接管。

#### 5.4 稳定无版本号 MCP 转发入口（含精确数据目录传递）
- 正式安装根目录随 `AsterhandLauncher.exe` 同级交付纯控制台轻量二进制 `asterhand-mcp.exe`（`cmd/asterhand-mcp`）；
- 启动时复用 `internal/launch.ReadInstallation` 校验同目录 `installation.json` 的 `Executable` 与 `Data`，在未显式指定 `--home` 时自动向活动版本 `asterhand.exe mcp` 透传 **`--home <Installation.Data>`** 与调用方参数，确保自定义数据目录安装与版本覆盖升级均连接到正确节点实例、零断连。

---

## 分阶段实施顺序（垂直闭环前置）

为尽早用真实 Antigravity 对话验证最关键的端到端模型回传与跨轮复审契约，避免先写大量外围配置与界面再返工接口，按 4 个里程碑推进：

### Milestone 1：最小垂直闭环（1 个 Antigravity + 1 个只读 Codex `app-server` Worker + 1 个授权工作区 + 最小人工首次接入）
- **实现范围**：
  - `internal/automation`：引入工具集分类（`core` / `general` / `collab`）、受管 Harness 可撤销受限主体（`Principal`）与全路径闸门（覆盖 `tools/list`、`Dispatch`、`batches.submit`、`operations.get`、`node.start`）、以及面向发起方模型的 `mcpResult` 有界文本 + 结构化响应契约（**覆盖 `operations.new_id`、`operations.get`、`capabilities` 与全部 `workers.*`**）；
  - `internal/harnesses`（M1 最小人工接入骨架）：提供 Web 最小人工入口——从全新空节点即可绑定/移除授权工作区（M1 限定 `read_only`）、启用/停用 Antigravity 发起方与只读 Codex、签发/撤销 Antigravity 受限身份，支持将本次安装接入 Antigravity 的 `mcp_config.json`（含冲突检测与手工引导），并附带 M1 极简活跃轮次查看与一键停止/核对操作（满足撤权后的人工收尾需要，完整多项目实时日志观测页留在 M2）；
  - `internal/workers`：SQLite 持久化（三层身份、发前持久化 `client_correlation_id`、单 Worker 活跃锁、规范化工作树锁、节点级并发与墙钟/无输出超时预算、接入 `App.RequestStop`/`App.Close` 停机链与 `reconcileAutomationOperation` 收据核对、三类撤权窗口处置、长正文与日志有界存储、空闲进程回收、精确关联对账与人工处置入口）、完整 `workers.create/list/get/send/wait/result/logs/cancel/close`（CLI + MCP 同 schema 与覆盖登记）；
  - `CodexAdapter`：纯 Go 直连 `codex app-server`（基于本机 `0.171.0` `generate-json-schema` 校验 `initialize`、`thread/start`、`thread/resume`、`thread/read`、`turn/start`、`turn/interrupt`、原生只读沙箱、按 `(threadId, turnId, itemId)` 与仅 `completed` 候选优先级组装正文、启动禁用 `asterhand` MCP、未支持审批/提问立即有界拒绝收尾）。
- **硬性真机与故障验收**：
  1. **全新节点最小人工接入与精细撤权**：从空节点数据目录出发，通过 Web 最小人工入口绑定授权工作区、启用 Antigravity 发起方与只读 Codex、建立受限连接；确认守护进程识别到受限 `Principal`；分别验证①吊销整份凭据后旧连接全部失效而在途轮次可由节点收尾并经 M1 最小入口/CLI 停止，②同一条已连接 MCP 会话从“通用 + 协作”热切为“仅通用”后通用查询继续成功、`workers.send` 拒绝，且 `operations.get` 与 `batches.get` 逐项收据在**查询已经开始、返回前才关闭协作开关**的竞态下仍按原操作的最新权限复核并拒绝旧协作收据（同一连接仍可读通用收据），③在“发前预备”、“`turn/start` 已发但延迟回执（验证迟到回执仅补审计、不逆转已撤权状态）”与“原生运行中”三个窗口撤销工作区/B 授权时均受控停止并释放锁；
  2. **纯文本通道、混合/中断正文组装、两类待核对恢复与原对话真实引用**：先用仅转发 `content[].text` 的测试客户端跑通 `operations.new_id` → `workers.create` → `workers.send` → 丢弃首次响应 → `operations.get` 找回 `turn_id` → `workers.wait` → `workers.result`；用确定性协议样例覆盖混合阶段（`commentary` + 无 `phase` 最终答复）、仅 `commentary` 无答复、“先输出 `partial_answer` 后中断（不提升为完成审查结论）”，以及同一组待核对恢复对比样例（普通丢回执凭唯一关联键恢复 `completed` vs 撤权标记已落盘时迟到完成仅补事实、不发布为成功）；再在真实 Antigravity 对话中派发只读 `codex-reviewer`，验证 Antigravity 原对话准确引用 Codex 指出的具体问题并据此继续行动；
  3. **防串轮、两级崩溃对账、超时预算与节点停机**：验证双发返回 `worker_busy`、迟到旧 `turn_id` 取消不影响新轮次、故障注入“轮次落盘后且公共收据完成前重启”仍可按原 `operation_id` 找回同一轮次且重复 `send` 不二发、连续两轮相同提示词且第二轮原生回执丢失时绝不误返第一轮结果、“子进程存活但不再发事件”超时回收 Job 并释放锁、“多 Worker 并发超节点上限”限流且 `workers.cancel` 可进入；仅保留 1 个活跃 Worker 时普通 `node.stop` 返回 `busy`，确认退出后先拒新 `send` 再停 Worker 落盘最后关库，仅留空闲常驻 `app-server` 时直接回收退出；
  4. **跨轮连续复审与只读边界实测**：修改代码后在同一 Antigravity 对话让同一个 `codex-reviewer` 复审，验证 `thread/resume` 生效且重连补读不重复拼接流式片段；实测核对 Codex `read_only` 在本机 Windows 下的真实写保护与工作区外读取行为并如实记录能力边界。

### Milestone 2：四家 Harness 自动发现 + 稳定 `asterhand-mcp.exe` + 三开关全矩阵托管 + 首次开放 Codex `workspace_write`
- **实现范围**：
  - 完整 `internal/harnesses` 四家发现器（Antigravity / Codex / Claude Code / DeepSeek Harness，含不在 `PATH` 中的桌面版与手工路径覆盖）；
  - `cmd/asterhand-mcp` 稳定入口（复用 `ReadInstallation` 透传 `--home <Data>`）及构建/快速安装/升级流水线集成；
  - 四家 MCP 期望/观察状态对账、官方 CLI / 原子单键注册反注册（零整文件凭据备份）、冲突保护、8 种开关组合全覆盖及 `notifications/tools/list_changed` 动态工具刷新；
  - `harnesses.list` / `harnesses.get` 公共只读操作；
  - Web「设置 → Coding Harness」（四家发现、多授权工作区与最高档位、三开关、MCP 对账与冲突接管）及 Web「Coding Workers」（活跃/历史 Worker 列表、实时日志流、一键强杀与待核对人工处置），全部纳入中英双语 i18n 词表与守卫检查；
  - **首次开放并验证 Codex 普通 `workspace_write`**：对人类显式开放 `workspace_write` 的授权工作区启用 Codex 受控写入、规范化工作树写互斥与前后工作树/Git 观察快照。
- **验收用例**：
  1. 自动识别本机四家已安装 Harness；两项目各建同名 `codex-reviewer` 互不冲突，并用 X 项目连接验证对 Y 项目轮次的核验/读取/停止规则；
  2. 逐项验证 8 种开关组合（含仅开通用能力不参与协作、仅开协作不暴露通用工具、热关开关服务端即时拦截、手工同名条目冲突保护）；
  3. **Codex 真实 `workspace_write` 验收**：修改授权测试仓库中的指定文件 → 返回含基线脏状态与外部并发标注的真实差异快照 → 并发第二个写轮次被规范化工作树写互斥拒绝 → 取消后确认停止并释放锁，可再次正常派发；
  4. 经 `install-local.cmd` 覆盖升级后，验证新连接走新版本、旧连接重连恢复、在途/已终结轮次可按原 `turn_id` 补读，自定义 `--home` 安装连接正确节点。

### Milestone 3：补齐 Claude Code & DeepSeek Harness 适配器 + Worker 角色模板
- **实现范围**：
  - 按同一 `HarnessAdapter` 契约接入 `ClaudeAdapter`（`claude -p --output-format stream-json --input-format stream-json --resume`，如实声明 Windows 下 `harness_tool_restricted` 等级与 `OrdinaryToken` 边界）与 `DeepSeekAdapter`（预检锁定桌面包内 CLI 的 `sdk`/`acp` 协议）；
  - Web 新增常用 Worker 角色模板配置（预设别名、默认 Harness、默认访问档位、角色提示词），与对话即时创建共存。

### Milestone 4：人机高危审批、业务提问流转与 Git Worktree 并行写隔离
- **实现范围**：
  - 打通 `waiting_approval`（复用 Asterhand 桌面通知与原位人工确认卡片，严禁由 A 代批）与 `waiting_input`（先回传 A，可经 `workers.reply` 回答或升级人类）；
  - 支持 `isolated_worktree` 访问模式（自动创建/回收独立 Git worktree 分支并返回 diff 摘要）；
  - 跨设备 Tailscale Worker 委派另行立项评估安全边界。

---

## 明确非目标

- 不打造替代 Antigravity / Codex / Claude Code 原生 UI 的重型多 Agent 聊天客户端；
- 不读取、提取、备份、同步或代管任何第三方 Harness 的 OAuth/API 凭据；
- 不支持消费者订阅版 Antigravity 作为被控 Worker（B）；
- 首版不承诺从服务端反向唤醒 Antigravity 已结束的对话轮次；
- 不实现自动跨 Harness 全量聊天同步、通用多 Agent DAG 编排或自动合并/推送 Git 分支；
- 首期仅支持 Windows x64 完整实测；macOS / Linux 仅保留窄平台接口，实测前声明不支持。

---

## 依赖、待验证项与实施前预检

1. **已核实事实**：
   - 本机已安装 `codex-cli 0.171.0`（含 `codex mcp`、`codex app-server generate-json-schema`，`turn/interrupt` 要求同时传 `threadId` 与 `turnId`）、`claude 2.1.284`（含 `claude mcp`）、`agy 1.5.13`（`%USERPROFILE%\.gemini\config\mcp_config.json`）、以及桌面版 DeepSeek Harness（`%LOCALAPPDATA%\Programs\DeepSeek Harness`、`%USERPROFILE%\.dsh`）；
   - 现有 `internal/automation` 硬参数已核对：客户端超时 `40s`、服务端写超时 `45s`、`MaxWait = 30s`、`MaxResponse = 64 KiB`、`operations.ResultLimit = 8 KiB`、`Installation` 含 `Executable` 与 `Data`。
2. **Milestone 1 实现前必做预检**：
   - 在本机导出并归档 `codex-cli 0.171.0` 的 `codex app-server generate-json-schema` 关键方法签名（`initialize`、`thread/start`、`thread/resume`、`thread/read`、`turn/start`、`turn/interrupt`、审批请求通知、只读沙箱参数），并实测验证 `turn/start.clientUserMessageId` 与历史 `userMessage.clientId` 在重启后的持久唯一匹配能力，以及 `commentary` / `partial_answer` / `final_answer` 条目形状；
   - 核验锁定版 `github.com/modelcontextprotocol/go-sdk v1.7.0` 中 `mcp.Server` 动态增删工具与 `notifications/tools/list_changed` 的精确 API；
   - 验证真实 Antigravity 客户端读取 `mcpResult` 中 `content[].text` 与 `structuredContent` 的实际行为。
3. **Milestone 2/3 实现前预检**：
   - 核验本机 DeepSeek Harness 桌面包内 CLI 可执行路径、MCP 配置格式及 `sdk`/`acp` 稳定性；
   - 核验 Claude Code 在 Windows 下的 `stream-json` 双向控制协议及受控禁用 `asterhand` MCP 的启动参数。

---

## 实施与文档同步

- **已同步的规划文档**：
  - [执行器需求](../../../executors/requirements.md)、[执行器设计](../../../executors/design.md)、[执行器 ADR-008](../../../executors/adr/008-multi-harness-broker-and-native-adapters.md)；
  - [自动化需求](../../../automation/requirements.md)、[自动化设计](../../../automation/design.md)、[日常操作覆盖清单](../../../automation/coverage.md)、[自动化 ADR-005](../../../automation/adr/005-harness-mcp-lifecycle-and-dynamic-toolsets.md)；
  - [安装需求](../../../installation/requirements.md)、[安装设计](../../../installation/design.md)；
  - [Web 需求](../../../web/requirements.md)、[Web 设计](../../../web/design.md)；
  - [开发路线图](../../roadmap.md)、[文档导航](../../../README.md)。
- **已同步的实现与文档（Milestone 1—2）**：
  - `contracts/automation-coverage.json` 已登记全部 `harnesses.list/get`、`workers.*` 公共操作与 `/api/harnesses*`、`/api/workers/*` 端点分类，并通过 `automation_coverage_test.go` 与 `tests/automation-coverage.test.mjs`；
  - `cmd/asterhand-mcp`（稳定无版本号 `asterhand-mcp.exe`）已纳入 `scripts/build-application.ps1`、`install.ps1` 与 `scripts/test-fast-local-install.ps1`；
  - 运行期可见文案与错误键已同步至 `internal/i18n/locales/zh-CN.json` / `en-US.json` 及生成词表，通过 `pwsh -NoProfile -File ./scripts/check-i18n.ps1` 与 `node ./scripts/verify-frontend-assets.mjs`；
  - 已补充 [协议预检记录](preflight.md) 与 [验证说明](validation.md)。

---

## 规模与拆分评估

- 遵循 [代码风格](../../code-style.md) 单文件 ≤ 1000 物理行、单函数 ≤ 100 物理行软约束；
- Milestone 1—2 全部新增与修改的手写 Go / JS / CSS 文件均控制在 1000 行以内：其中 `cmd/asterhand-mcp` 为独立入口（`main.go` 58 行）；`internal/workers` 按职责拆分为 `types.go`、`store.go`、`service.go`、`service_runner.go`、`service_query.go`、`codex_adapter.go`、`vcs.go`、`process_windows.go`（最大 621 行）；`internal/harnesses` 按职责拆分为 `types.go`、`path.go`、`discover.go`、`exec_windows.go`、`exec_other.go`、`mcp_antigravity.go`、`mcp_codex.go`、`mcp_claude.go`、`service.go`（最大 549 行）；前端拆分独立模块 `web/harnesses-settings.mjs`（395 行）、`web/workers-panel.mjs`（380 行）与独立样式 `web/workers.css`（47 行，保持 `web/style.css` 894 行不超 1000 行）；
- 仅生成的 `web/i18n-catalog.mjs`（由 `scripts/sync-i18n.mjs` 生成）与既有扁平双语词典 `internal/i18n/locales/zh-CN.json` / `en-US.json`（各 1083 行，在原有结构上增量追加 M1/M2 词条）超过 1000 行；扁平词典拆分会增加单一词表校验与同步复杂度且降低检索可读性，按软约束保留现状。

---

## 验证与维护

- **分层测试覆盖**：
  1. Go 单元与假子进程契约测试：双发 `worker_busy`、迟到 `turn_id` 取消、`turn/start` 断连对账、旧轮次事件晚到、`operation_id` 幂等、长正文分页与过期、未支持审批/提问立即有界拒绝、受限 `Principal` 覆盖 `tools/list`/单调用/批处理/收据/`node.start`、8 种开关组合与冲突保护、Windows junction/大小写工作树锁、非 Git 目录与脏工作区快照；
  2. 守卫测试：`go vet ./...`、`automation_coverage_test.go`、`tests/automation-coverage.test.mjs`、`pwsh -NoProfile -File ./scripts/check-i18n.ps1`、`node ./scripts/verify-frontend-assets.mjs`；
  3. 本地安装升级回归（Milestone 2）：`install-local.cmd` 验证 `asterhand-mcp.exe` 透传 `--home <Data>`、新旧连接与跨升级补读；
  4. 本机真实设备验收：严格按各里程碑真机场景验证，不以 fixture 代替真实模型与真机 Antigravity 验收。
- **平台边界**：本轮仅完整实现与验证 Windows x64；macOS / Linux 保持未实现/未验证声明。
