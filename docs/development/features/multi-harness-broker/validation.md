# 多 Coding Harness 本地中介与统一接入管理（Milestone 1—4.1）验证说明

本文记录 [多 Coding Harness 中介计划](plan.md) Milestone 1—4.1 的长期可复现验证入口、前置条件、预期结果与平台/环境支持边界。原始测试日志保存于临时忽略目录，不提交至仓库。

## 目标与契约

- **功能名称**：`multi-harness-broker` Milestone 1—4.1（多 Coding Harness 自动发现与托管 MCP、三家受控 Worker 适配器与长程编排、正交 `approval_mode` 与人机审批/提问桥、右下角单一交互浮窗及 Web 同步入口）。
- **关联权威文档**：
  - [功能计划](plan.md)、[协议预检记录](preflight.md)
  - [执行器需求](../../../executors/requirements.md)、[执行器设计](../../../executors/design.md)、[执行器 ADR-008](../../../executors/adr/008-multi-harness-broker-and-native-adapters.md)
  - [自动化需求](../../../automation/requirements.md)、[自动化设计](../../../automation/design.md)、[日常操作覆盖清单](../../../automation/coverage.md)、[自动化 ADR-005](../../../automation/adr/005-harness-mcp-lifecycle-and-dynamic-toolsets.md)
  - [安装需求](../../../installation/requirements.md)、[安装设计](../../../installation/design.md)
  - [Web 需求](../../../web/requirements.md)、[Web 设计](../../../web/design.md)
- **关联测试与实现源码**：
  - 稳定 MCP 转发入口（`asterhand-mcp.exe`）与安装清单：[main_test.go](../../../../cmd/asterhand-mcp/main_test.go)、[test-fast-local-install.ps1](../../../../scripts/test-fast-local-install.ps1)
  - 工具集过滤、纯文本回传、热切换通知与受限 `node.start`：[toolset_mcp_test.go](../../../../internal/automation/toolset_mcp_test.go)、[toolset_client_test.go](../../../../internal/automation/toolset_client_test.go)
  - 四家发现、8 种开关组合、可撤销凭据、Antigravity/Codex/Claude MCP 对账与冲突保护、稳定入口迁移及 `allow_workspace_write` 门禁：[service_test.go](../../../../internal/harnesses/service_test.go)
  - Worker 状态机、最终正文组装、防串轮、三类撤权窗口、关联核对、超时预算、长正文分页、未支持审批/提问立即拒绝与 `workspace_write` 规范化工作树写互斥/撤权/Git 快照：[service_test.go](../../../../internal/workers/service_test.go)、[codex_adapter_test.go](../../../../internal/workers/codex_adapter_test.go)、[claude_adapter_test.go](../../../../internal/workers/claude_adapter_test.go)、[deepseek_adapter_test.go](../../../../internal/workers/deepseek_adapter_test.go)
  - 本机真实三家协议集成测试（环境变量门控）：[codex_live_test.go](../../../../internal/workers/codex_live_test.go)、[claude_live_test.go](../../../../internal/workers/claude_live_test.go)、[deepseek_live_test.go](../../../../internal/workers/deepseek_live_test.go)
  - 节点级端到端自动化流、`harnesses.list/get` 门禁、热切/在途收据与批处理撤权、崩溃收据修复与停机链：[automation_workers_test.go](../../../../internal/app/automation_workers_test.go)、[automation_coverage_test.go](../../../../internal/app/automation_coverage_test.go)
  - 覆盖登记与前端/国际化守卫：[automation-coverage.test.mjs](../../../../tests/automation-coverage.test.mjs)、[check-i18n.ps1](../../../../scripts/check-i18n.ps1)、[verify-frontend-assets.mjs](../../../../scripts/verify-frontend-assets.mjs)

## 前置条件

- **支持环境**：Windows x64；使用仓库锁定的 Go 1.27.x 工具链与 Node.js（见 [测试指南](../../../../tests/README.md)）。
- **自动化回归测试**：全部默认 `go test` 与守卫脚本仅使用临时目录（`t.TempDir()`），**不读取或修改**用户真实 `%USERPROFILE%\.gemini\config\mcp_config.json`、`~/.codex/config.toml` 或任何第三方凭据。
- **本机真实 Codex `app-server` 集成测试**（可选环境变量触发）：
  - 本机已安装官方 `codex` CLI（支持 `codex app-server`，已验证 `0.171.0`）并由用户在 Codex 原生环境内完成自身登录或 API 配置；
  - 执行命令时显式设置环境变量 `ASTERHAND_LIVE_CODEX=1`（产生少量真实模型调用额度消耗）；未设置时测试标记 `SKIP`。
- **真机 Antigravity 对话用户验收**：
  - 启动 Asterhand 节点后，在 Web「设置 → Coding Harness」打开 Antigravity 发起方开关（「允许调用其他 Harness」，自动同步外部配置）与 Codex 被控开关（「允许被其他 Harness 调用」），无需手工登记项目目录，直接在任意本地项目打开 Antigravity 对话即可完成跨轮复审验证。

## 验证方法

| 行为与契约 | 仓库测试入口或复现命令 | 预期可观察结果 |
|---|---|---|
| 工具集过滤、热切换 `notifications/tools/list_changed`、守护进程升级重启（`instance_id` 变化）经 `GET /v1/definitions` 零断连热同步最新工具与 Schema，以及纯文本 MCP 客户端完整回传（`operations_new_id` / `operations_get` / `workers_wait`） | `go test -v ./internal/automation/...` | 仅读取 `content[].text` 即可获取 `operation_id`、`worker_id`、`turn_id` 与完整审查正文；热切换或守护进程升级均收到 `list_changed` 并自动拉取新工具/新 Schema 无需重连；仅开 `collab` 的受管调用方执行本地 `node_start` 返回 `forbidden` |
| 免登记项目路径解析与确定性 `workspace_id`、敏感系统/凭据目录黑名单与路径穿越拦截、三开关正交策略、可撤销受限凭据与 Antigravity `mcp_config.json` 原子单键托管/冲突保护 | `go test -v ./internal/harnesses/...` | 任意常规本地目录无需登记即可解析稳定 `workspace_id`，盘符根/用户主目录/节点数据目录/系统与凭据目录返回 `forbidden`；仅维护 `mcpServers.asterhand` 单键，空 0 字节文件识别为 `not_configured`；同名非托管条目报 `conflict` 直至显式 `takeover` |
| 正文提取规则（`final_answer` 优先、混合无 `phase` 兜底、仅 `commentary` 与中断 `partial_answer` 不提升）、双项目同名 Worker 隔离、单 Worker `worker_busy` 与节点并发限流、迟到 `turn_id` 取消隔离 | `go test -v -run "TestSelectFinalAnswerRules|TestDualProjectAliasesBusyCapacityAndLateCancel" ./internal/workers/...` | 同别名在不同工作区互不冲突；跨工作区误读报 `workspace_mismatch`；活跃轮次阻塞普通 `BeginStop(false)` 而空闲常驻进程不阻塞 |
| 三类撤权窗口（发前预备 / `turn/start` 在途且迟到回执仅补审计不逆转 / 运行中触发 `InterruptTurn`）、重复提示词不误配与撤权后 `ReconcileTurn` 不晋升成功、无输出超时回收、长正文 UTF-8 边界分页与未支持审批/提问立即拒绝 | `go test -v -run "TestInFlightTurnStart|TestCorrelationReconciliation|TestStallTimeout|TestCodexSessionServerRequest|TestWindow1PreStart" ./internal/workers/...` | 未支持审批回 `{"decision":"cancel"}` 并以 `blocked_unsupported_approval` 收尾；未支持提问回 JSON-RPC `-32001` 并以 `blocked_unsupported_user_input` 收尾；无输出超时强杀进程并释放锁 |
| 节点级端到端流、`harnesses.list/get` 工具集门禁与零凭据泄露、`workspace_write` 策略拦截、热切“通用+协作 → 仅通用”、`operations.get` / `batches.get` 在途撤权复核、`read.many` 工具集拦截、崩溃收据从 Worker 域修复与 `App.RequestStop(false)` 停机链 | `go test -v -run "TestM1|TestM2|TestAutomationCoverage" ./internal/app/...` | `harnesses.list/get` 仅在 `collab` 开启时可用且不泄露 token/hash；`workspace_write` 在开启 `allow_workspace_write` 前被拒、开启后放行；热切与停机链保持一致 |
| 稳定无版本号 `asterhand-mcp.exe` 转发入口（透传 `--home <Data>`、显式 `--home` 优先、非法路径仅写 stderr）与本地快速安装清单校验 | `go test -v ./cmd/asterhand-mcp/...`、`pwsh -NoProfile -File ./scripts/test-fast-local-install.ps1` | 缺省自动补齐 `installation.json` 中的 `--home <Data>`；失败时 stdout 零字节；本地安装清单包含 `asterhand-mcp.exe` |
| 8 种开关组合全矩阵、Codex/Claude 官方 CLI MCP 对账与非托管冲突保护、升级至 `asterhand-mcp.exe` 的无冲突自动迁移、`allow_workspace_write` 单独撤权 | `go test -v ./internal/harnesses/...`、`go test -v -run TestWorkspaceWriteLockingCancelRevocationAndVCS ./internal/workers/...` | 8 组合行为与预期完全一致；同 Git 仓库根目录与子目录共享规范化工作树写互斥锁，取消后释放锁；关闭 `allow_workspace_write` 仅中止写轮次而不影响只读轮次 |
| 本机真实 Codex `app-server` 两轮只读复审与 `workspace_write` 真实写入/脏基线快照实测 | `$env:ASTERHAND_LIVE_CODEX="1"; go test -v -timeout 180s -run "TestCodexAdapterLive" ./internal/workers/...` | 只读两轮复用同 `threadId` 回传 `BUG_ZERO` → `FIXED_OK`；`workspace_write` 真实修改临时仓库目标文件且不碰基线脏文件，返回 `dirty_baseline=true`、`external_changes_possible=true` 与变更文件清单 |
| `ClaudeAdapter`（`stream-json`）与 `DeepSeekAdapter`（`dsh --profile acp`）假进程契约测试及本机真实三段实测（只读 / 跨进程恢复 / `workspace_write`） | `go test -v -run "TestClaudeAdapter|TestDeepSeekAdapter" ./internal/workers/...`；真实联调：`$env:ASTERHAND_LIVE_CLAUDE="1"; $env:ASTERHAND_LIVE_DEEPSEEK="1"; go test -v -timeout 240s -run "TestClaudeAdapterLive|TestDeepSeekAdapterLive" ./internal/workers/...` | 假进程覆盖防递归参数/空 `mcpServers`、模型切换、审批/提问有界拦截、中断不晋升 `partial_answer` 及无效会话 `native_session_unrecoverable`；本机真实 `claude 2.1.330` 与 `dsh 0.2.2-rc.5` 均跑通只读读取、跨进程 `--resume`/`session/resume` 与受控 `workspace_write` 写盘 |
| 全仓静态检查、测试、构建与覆盖/前端/国际化守卫 | `go vet ./...`、`go test ./...`、`go build ./cmd/asterhand`、`go build ./cmd/asterhand-mcp`、`pwsh -NoProfile -File ./scripts/check-i18n.ps1 -Go <go.exe>`、`node ./scripts/verify-frontend-assets.mjs`、`node --test ./tests/automation-coverage.test.mjs` | 全部退出码 0；中英文词表与四层硬编码守卫 5/5 通过；所有 `/api/harnesses*` 与 `/api/workers/*` 端点均具备显式覆盖分类 |
| **真机用户验收（Milestone 2 人工步骤）** | 通过 `install-local.cmd` 覆盖升级后，核对安装根目录 `asterhand-mcp.exe` 与外部 MCP 自动迁移；在 Web「设置 → Coding Harness」查看四家发现、版本与路径编辑，验证 Codex/Claude 开关同步与 Codex「允许修改项目文件」开关；在顶栏「Workers」面板实时查看日志与工作树变更并测试停止/归档 | 升级后外部工具经 `asterhand-mcp.exe` 无断连恢复；四家正确识别；受控写入返回真实工作树快照且并发写互斥生效；Workers 面板实时显示日志与变更 |
| **M4.1 审批模式分档、硬底线拦截、暂停超时账本、同步会话免审、业务提问流转（`workers_reply`）、右下角交互浮窗防闪跳/防双击透传/通知避让及 Web SSE 同步** | `go test -v -run "TestApprovalMode|TestHardRedline|TestInteractionBridge|TestCodexClaudeDeepSeekInteraction|TestFinalizeTurnPreservesConcurrentRevokeAndStrictDenyMode|TestDecideAllowSessionPersistsSynchronouslyBeforeReturning" ./internal/workers/...`、`go test -v -run "TestPeersAPIProbesMultiplePeersConcurrently|TestM2" ./internal/app/...`、`go test -v -run "TestNotificationStackAvoidsWorkerActionPopup|TestWorkerActionRevealVerifiesTopmostOrder" ./internal/quickinput/...`、`node --test ./tests/worker-action.test.mjs ./tests/workers-panel.test.mjs ./tests/app-lifecycle.test.mjs ./tests/automation-coverage.test.mjs` | Codex/Claude 默认 `auto` 审单、DeepSeek 默认优雅回落 `ask_human`；等待态暂停 `StallTimeout`/`WallClockTimeout`；`deny` 仅拒单次不杀轮次；`allow_session` 同步持久化免审态、对安全命令静默放行但硬底线仍强制弹窗并禁用会话免审；`workers_wait` 文本与结构化 `next_step` 均告知 A 勿取消等待审批轮次；A 可经 `workers_reply` 回答 `waiting_input` 或超时自动升级人类；右下角浮窗两阶段显影无闪跳、400ms 跨项防双击透传、Z 序晋升置顶且与通知零重叠；Web 通过 `/api/stream` `workers` 事件即刻同步且错误不断轮询、离线对端并发探活不阻塞首屏 |
| **真机用户验收（Milestone 4.1 人工步骤）** | 通过 `install-local.cmd` 更新并重启服务后：① 创建默认 `workspace_write` 的 Codex/Claude Worker 验证原生 `auto` 审单与审计日志；② 创建 `approval_mode="ask_human"` 与 `strict_deny` 的三家 Worker 分别触发工作区内外读写与红线命令，实测右下角单一交互浮窗置顶（普通应用不遮挡）、弹出不抢焦点、`< 1/N >` 队列切换、收起/展开与长命令折叠无闪跳、快速双击不跨项透传、`拒绝本次` 不杀轮次、`本会话免审` 对安全命令静默放行而对敏感/高危仍强拦、与本地命令完成通知同屏无重叠；③ 触发三家业务提问验证主控 A 经 `workers_reply` 代答及升级人类后点击弹窗输入中文 IME 回答；④ 验证 Web「编程协作者」顶部待处理队列与桌面浮窗即时同步、一键撤销「本会话免审」、多并发等待与重启恢复 | 四项场景在真机 Windows 上全量闭环通过，审批/提问交互无焦点劫持、无遮挡、无闪跳、无跨项误触与重复提交 |

## 支持边界与验收状态

- **已通过工程验证与本机用户验收（Windows x64，Milestone 1—2）**：Milestone 1—2 全部 Go 单元/故障/节点级自动化测试、i18n/前端/覆盖守卫、本机真实 `codex-cli 0.171.0` 两轮只读与 `workspace_write` 实测，以及真机 `install-local.cmd` 升级、四家发现、稳定 `asterhand-mcp.exe` 迁移、零断连热同步最新工具与 Schema、全仓下划线操作名与严格边界校验、Web「Coding Workers」实时结构化日志观测页、黑盒端到端测试（`read_only` / `workspace_write` 沙箱、父级越界拦截、多轮延续、幂等重放与冲突、`worker_busy` 写互斥、取消恢复与分页）均已通过用户验收。
- **已通过工程验证与本机用户验收（Windows x64，Milestone 3 第 1—2 步）**：`ClaudeAdapter`（`claude 2.1.330` `stream-json`）与 `DeepSeekAdapter`（`dsh 0.2.2-rc.5` ACP v1）已完成协议预检、假进程契约测试、本机真实 `TestClaudeAdapterLive` / `TestDeepSeekAdapterLive` 联调验证，并通过多轮真实长程三路并行开发验收（涵盖独立隔离目录双轮大型工程构建、`workers_wait_any` 并发监听、`wall_clock_seconds` 3600s 预算、`workers_logs(tail=true)`、DeepSeek Windows ACL 只读预警与传输中断无损续跑、Claude 受控测试命令闭环与流式心跳、`worktree_busy` 写锁提示及中文 UI「编程智能体 / 编程协作者」）。
- **已通过工程验证与本机用户验收（Windows x64，Milestone 4.1）**：正交 `approval_mode`（`auto` 默认 / `ask_human` / `strict_deny`）、三家原生自动审单与双向审批/提问协议接入（Codex `auto_review`/`user` + 异步提问 `turn/steer`、Claude `--permission-prompt-tool stdio` + `AskUserQuestion`、DeepSeek `session/request_permission` + 缓存 `tool_call` 参数校验）、统一硬底线拦截、等待态暂停 `StallTimeout`/`WallClockTimeout`、`workers_reply` 与超时自动升级、`workers_wait` 结构化 `next_step`、右下角单一置顶交互浮窗（含 Z 序校验、本地命令通知避让、两阶段显影无闪跳与 400ms 跨项防误触）、Web「编程协作者」SSE 实时同步/撤销免审与非阻塞并发对端探活、以及并发等待/取消/重启清理均已通过全量自动化回归与真机用户验收。
- **后续范围（当前未实现）**：Milestone 4.2（`isolated_worktree` 并行写隔离）与后置的 Milestone 3 第 3 步（Worker 角色模板）待 M4.1 验收后推进。
- **未验证平台**：macOS 与 Linux 仅保留编译占位（`process_other.go`、`exec_other.go`），在对应平台实测前不支持。
