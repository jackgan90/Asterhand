# ADR-008：多 Coding Harness 本地中介层与官方原生协议适配

- 状态：accepted（Milestone 1 已实现并完成工程验证与本机真机 Antigravity → Codex 用户验收；Milestone 2—4 规划待实施）
- 日期：2026-11-21
- 所属领域与需求：执行器与工具（[执行器需求](../requirements.md) `EXEC-HARNESS-01`—`10`、关联 `R10`、`R19`、`R20`）
- 替代 / 被替代记录：延续并扩展 [执行器 ADR-001](001-harness-neutral.md) 与 [全局 ADR-002](../../overview/adr/002-mvp-scope.md)（不改变既有 OpenClaw 任务与持续聊天契约）

## 背景

用户希望继续留在原生 Coding Harness（如 Antigravity）内完成主要开发，通过本机常驻的 Asterhand 将审查或编码子任务以稳定身份派发给其他已安装 Harness（Codex、Claude Code、DeepSeek Harness）并可靠取回结果。方案须兼顾：
1. 各家官方 ToS 与零第三方凭据侵入；
2. 跨项目并发隔离、单 Worker 防串轮、长审查正文存储预算与不同平台沙箱能力的诚实声明；
3. 与 Asterhand 现有 `tasks`、`conversations` 和 `automation` 的职责解耦及超时/幂等契约兼容。

## 决策

1. **采用 Broker Plane 并明确四方单一权威边界**：
   - `internal/harnesses` 拥有发现、能力矩阵、三开关/授权工作区配置与外部注册对账；
   - `internal/workers` 拥有授权工作区内的 Worker 身份、原生会话映射、单 Worker 活跃轮次门禁、规范化工作树写互斥、取消、恢复、长结果与日志存储及空闲进程回收；
   - `internal/automation` 统一拥有调用主体、工具可见性、全路径闸门、收据与等待预算；
   - 各厂商 `HarnessAdapter` 仅负责受管启动（`OrdinaryToken` + 具名 `JobObject`）、协议翻译、绑定原生轮次的中断与恢复。
2. **合规与零凭据侵入边界**：
   - Antigravity 静态限定仅作为发起方 A（`worker_supported = false`）；
   - Codex、Claude Code、DeepSeek Harness 均仅调用未修改官方本地本体，严禁读取、复制或整文件备份任何第三方认证凭据。
3. **直连长期官方双向协议，不做一次性过渡实现**：
   - Codex 直接对接 `codex app-server`（JSON-RPC 2.0 over stdio，配合 `generate-json-schema` 校验契约）；
   - Claude Code 对接 `claude -p --output-format stream-json --input-format stream-json --resume`；
   - DeepSeek Harness 在 Milestone 3 实施前实测桌面包内 CLI 的 `sdk` / `acp` 后锁定单一协议。
4. **免逐项目登记的自动工作区隔离、敏感路径拦截、精确关联对账与防串轮**：
   - 不要求用户在 Asterhand 中手工逐项目登记委派目录（避免多项目高频增删带来的使用摩擦）；任意常规本地项目目录直通使用，由 Asterhand 对规范化路径确定性派生稳定 `workspace_id` 并内置拦截盘符根、用户主目录、Asterhand 数据目录、系统目录及常见凭据目录；Worker、`(workspace_id, alias)`、`artifact_paths` 边界与控制核验均绑定该 `workspace_id`；明确区分此边界不宣称 Codex `readOnly` 在 Windows 上构成目录级只读 chroot；
   - 单 Worker 同时最多 1 个活跃轮次（并发返回 `worker_busy`）；发送前持久化唯一 `client_correlation_id`（优先配合原生关联字段如 Codex `clientUserMessageId` / `userMessage.clientId` 做重启后唯一匹配，禁止按正文猜测匹配，未唯一确认则保留待核对与人工处置入口）；`workers.cancel` 必填 `turn_id` 并以原生 `(threadId, turnId)` 中断；原生会话不可恢复时明确报错而不静默新开上下文；
   - Codex 适配器按 `(threadId, turnId, itemId)` 去重归集，`commentary`/工具流归入 `workers.logs`；**仅对 `status == "completed"` 的成功轮次**执行最终正文选择（优先 `final_answer`；缺失时排除 `commentary` 与工具条目后取最后一条无 `phase` 或 `partial_answer` 助手消息；无候选则显式返回 `answer_available: false`）；非成功终态的 `partial_answer` 仅作显式部分输出保留；`reconciliation_required` 为可恢复待核对非终态（待核对期间不发布最终正文；普通丢回执凭唯一关联证据可恢复为 `completed`，已落盘撤权标记时迟到记录仅补事实、不可恢复为成功）。
5. **纯写 `send` + 独立 `wait` + 活跃运行/停机协同与精细撤权语义**：
   - `workers.send` 仅做可恢复接纳并立即返回 `turn_id`（并接入 `reconcileAutomationOperation` 修复“轮次已落盘、公共收据未完成”窗口），等待统一走 `workers.wait`（上限 `30s`）；
   - 强制实施节点级活跃轮次上限（默认 4）、单轮墙钟时限（默认 15 分钟）与无事件超时（默认 5 分钟），超时先原生中断再回收 Windows `JobObject`，确认停止后才释放互斥锁；
   - `Workers` 接入 `App.RequestStop(confirmed)` 原子停机链并在 `App.Close()` 关库前落盘收尾（空闲常驻进程不阻塞普通退出）；吊销整份凭据使旧凭据全失效、仅关闭 `collab_caller_enabled` 按剩余开关重算权限（保留 `core + general` 并按当前权限过滤 `operations.get` 收据）且在途轮次由节点收尾；撤销工作区或 B 授权覆盖“发前预备”、“`turn/start` 已发未回执（迟到回执不逆转已撤权状态）”与“运行中”三窗口，均受控停止并在确认后释放锁；
   - 长审查正文与日志独立于 8 KiB 收据落盘，首版即提供 `workers.result` 与 `workers.logs` 分页读取，并设定空闲常驻进程回收策略。
6. **诚实能力分级与审批桥前确定性收敛**：
   - 按 `(harness × version × platform × access_mode)` 实测声明 `os_sandbox_enforced`、`harness_tool_restricted` 或 `unsupported`，不做跨 Harness 泛化“硬只读”承诺；
   - 审批桥上线前收到原生审批或业务提问时，立即有界拒绝并释放轮次与写锁，返回明确状态码。
7. **垂直闭环前置与写入验收归属**：
   - Milestone 1 包含最小人工首次接入路径并先跑通“1 个 Antigravity + 1 个只读 Codex `app-server` Worker + 1 个授权工作区”的真机闭环；Milestone 2 补齐四家发现、稳定转发入口、三开关全矩阵并首次开放且真机验收 Codex 普通 `workspace_write`。

## 备选与后果

- **备选 A：统一全走 ACP 或一期先用 `codex exec` 过渡**：会丧失原生审批/精确中断能力或产生二期返工，予以否决。
- **备选 B：`workers.send` 内置 `wait_seconds` 一次返回**：与 Asterhand 既有 40s 客户端超时、45s 服务端写超时和全参数幂等摘要直接冲突，改为纯写 `send` + 独立 `wait` 更稳健。
- **后果与限制**：首版采用原对话内主动轮询（不宣称反向唤醒已结束的 Antigravity 对话）；工作区前后 Git 快照诚实标注外部并发修改可能性而不伪称排他归因。

## 验证与关联

- 关联计划：[多 Coding Harness 中介计划](../../development/features/multi-harness-broker/plan.md)
- 关联文档：[执行器需求](../requirements.md) · [执行器设计](../design.md) · [自动化 ADR-005](../../automation/adr/005-harness-mcp-lifecycle-and-dynamic-toolsets.md)
- 待验证项：Milestone 1 前核对本机 `codex-cli 0.171.0` `generate-json-schema` 及真机 Antigravity 引用 Codex 审查意见的三步验收。
