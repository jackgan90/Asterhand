# 多 Coding Harness 本地中介与统一接入管理（Milestone 1—2）验证说明

本文记录 [多 Coding Harness 中介计划](plan.md) Milestone 1—2 的长期可复现验证入口、前置条件、预期结果与平台/环境支持边界。原始测试日志保存于临时忽略目录，不提交至仓库。

## 目标与契约

- **功能名称**：`multi-harness-broker` Milestone 1（Antigravity → 只读 Codex 垂直闭环）与 Milestone 2（四家发现、稳定 `asterhand-mcp.exe`、Codex/Claude 官方 CLI 托管与 8 组合矩阵、公共 `harnesses.list/get`、Web「Coding Workers」观测页与首次开放 Codex `workspace_write`）。
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
  - Worker 状态机、最终正文组装、防串轮、三类撤权窗口、关联核对、超时预算、长正文分页、未支持审批/提问立即拒绝与 `workspace_write` 规范化工作树写互斥/撤权/Git 快照：[service_test.go](../../../../internal/workers/service_test.go)、[codex_adapter_test.go](../../../../internal/workers/codex_adapter_test.go)
  - 本机真实 Codex `app-server` 两轮只读复审与 `workspace_write` 实测：[codex_live_test.go](../../../../internal/workers/codex_live_test.go)
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
| 全仓静态检查、测试、构建与覆盖/前端/国际化守卫 | `go vet ./...`、`go test ./...`、`go build ./cmd/asterhand`、`go build ./cmd/asterhand-mcp`、`pwsh -NoProfile -File ./scripts/check-i18n.ps1 -Go <go.exe>`、`node ./scripts/verify-frontend-assets.mjs`、`node --test ./tests/automation-coverage.test.mjs` | 全部退出码 0；中英文词表与四层硬编码守卫 5/5 通过；所有 `/api/harnesses*` 与 `/api/workers/*` 端点均具备显式覆盖分类 |
| **真机用户验收（Milestone 2 人工步骤）** | 通过 `install-local.cmd` 覆盖升级后，核对安装根目录 `asterhand-mcp.exe` 与外部 MCP 自动迁移；在 Web「设置 → Coding Harness」查看四家发现、版本与路径编辑，验证 Codex/Claude 开关同步与 Codex「允许修改项目文件」开关；在顶栏「Workers」面板实时查看日志与工作树变更并测试停止/归档 | 升级后外部工具经 `asterhand-mcp.exe` 无断连恢复；四家正确识别；受控写入返回真实工作树快照且并发写互斥生效；Workers 面板实时显示日志与变更 |

## 支持边界与验收状态

- **已通过工程验证与本机用户验收（Windows x64，Milestone 1—2）**：Milestone 1—2 全部 Go 单元/故障/节点级自动化测试、i18n/前端/覆盖守卫、本机真实 `codex-cli 0.171.0` 两轮只读与 `workspace_write` 实测，以及真机 `install-local.cmd` 升级、四家发现、稳定 `asterhand-mcp.exe` 迁移、零断连热同步最新工具与 Schema、全仓下划线操作名与严格边界校验、Web「Coding Workers」实时结构化日志观测页、黑盒端到端测试（`read_only` / `workspace_write` 沙箱、父级越界拦截、多轮延续、幂等重放与冲突、`worker_busy` 写互斥、取消恢复与分页）均已通过用户验收。
- **后续里程碑范围（当前未实现）**：Claude Code / DeepSeek Harness 受控 Worker 适配器、Worker 角色模板与高危审批卡片 / `isolated_worktree` 分别归属 Milestone 3—4，不计入本期验收范围。
- **未验证平台**：macOS 与 Linux 仅保留编译占位（`process_other.go`、`exec_other.go`），在对应平台实测前不支持。
