# 多 Coding Harness 本地中介与统一接入管理（Milestone 1）验证说明

本文记录 [多 Coding Harness 中介计划](plan.md) Milestone 1 的长期可复现验证入口、前置条件、预期结果与平台/环境支持边界。原始测试日志保存于临时忽略目录，不提交至仓库。

## 目标与契约

- **功能名称**：`multi-harness-broker` Milestone 1（1 个 Antigravity 发起方 + 1 个只读 Codex `app-server` Worker + 1 个授权工作区 + 最小人工首次接入）。
- **关联权威文档**：
  - [功能计划](plan.md)、[M1 协议预检记录](preflight.md)
  - [执行器需求](../../../executors/requirements.md)、[执行器设计](../../../executors/design.md)、[执行器 ADR-008](../../../executors/adr/008-multi-harness-broker-and-native-adapters.md)
  - [自动化需求](../../../automation/requirements.md)、[自动化设计](../../../automation/design.md)、[日常操作覆盖清单](../../../automation/coverage.md)、[自动化 ADR-005](../../../automation/adr/005-harness-mcp-lifecycle-and-dynamic-toolsets.md)
  - [Web 需求](../../../web/requirements.md)、[Web 设计](../../../web/design.md)
- **关联测试与实现源码**：
  - 工具集过滤、纯文本回传、热切换通知与受限 `node.start`：[toolset_mcp_test.go](../../../../internal/automation/toolset_mcp_test.go)、[toolset_client_test.go](../../../../internal/automation/toolset_client_test.go)
  - 授权工作区、三开关、可撤销凭据与 Antigravity 单键 MCP 对账：[service_test.go](../../../../internal/harnesses/service_test.go)
  - Worker 状态机、最终正文组装、防串轮、三类撤权窗口、关联核对、超时预算、长正文分页与未支持审批/提问立即拒绝：[service_test.go](../../../../internal/workers/service_test.go)、[codex_adapter_test.go](../../../../internal/workers/codex_adapter_test.go)
  - 本机真实 Codex `app-server` 两轮持久会话实测：[codex_live_test.go](../../../../internal/workers/codex_live_test.go)
  - 节点级端到端自动化流、热切/在途收据与批处理撤权、崩溃收据修复与停机链：[automation_workers_test.go](../../../../internal/app/automation_workers_test.go)、[automation_coverage_test.go](../../../../internal/app/automation_coverage_test.go)
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
| 工具集过滤、热切换 `notifications/tools/list_changed` 与纯文本 MCP 客户端完整回传（`operations.new_id` / `operations.get` / `workers.wait`） | `go test -v ./internal/automation/...` | 仅读取 `content[].text` 即可获取 `operation_id`、`worker_id`、`turn_id` 与完整审查正文；热切换工具集收到 `list_changed` 通知；仅开 `collab` 的受管调用方执行本地 `node.start` 返回 `forbidden` |
| 免登记项目路径解析与确定性 `workspace_id`、敏感系统/凭据目录黑名单与路径穿越拦截、三开关正交策略、可撤销受限凭据与 Antigravity `mcp_config.json` 原子单键托管/冲突保护 | `go test -v ./internal/harnesses/...` | 任意常规本地目录无需登记即可解析稳定 `workspace_id`，盘符根/用户主目录/节点数据目录/系统与凭据目录返回 `forbidden`；仅维护 `mcpServers.asterhand` 单键，空 0 字节文件识别为 `not_configured`；同名非托管条目报 `conflict` 直至显式 `takeover` |
| 正文提取规则（`final_answer` 优先、混合无 `phase` 兜底、仅 `commentary` 与中断 `partial_answer` 不提升）、双项目同名 Worker 隔离、单 Worker `worker_busy` 与节点并发限流、迟到 `turn_id` 取消隔离 | `go test -v -run "TestSelectFinalAnswerRules|TestDualProjectAliasesBusyCapacityAndLateCancel" ./internal/workers/...` | 同别名在不同工作区互不冲突；跨工作区误读报 `workspace_mismatch`；活跃轮次阻塞普通 `BeginStop(false)` 而空闲常驻进程不阻塞 |
| 三类撤权窗口（发前预备 / `turn/start` 在途且迟到回执仅补审计不逆转 / 运行中触发 `InterruptTurn`）、重复提示词不误配与撤权后 `ReconcileTurn` 不晋升成功、无输出超时回收、长正文 UTF-8 边界分页与未支持审批/提问立即拒绝 | `go test -v -run "TestInFlightTurnStart|TestCorrelationReconciliation|TestStallTimeout|TestCodexSessionServerRequest|TestWindow1PreStart" ./internal/workers/...` | 未支持审批回 `{"decision":"cancel"}` 并以 `blocked_unsupported_approval` 收尾；未支持提问回 JSON-RPC `-32001` 并以 `blocked_unsupported_user_input` 收尾；无输出超时强杀进程并释放锁 |
| 节点级端到端流、热切“通用+协作 → 仅通用”、`operations.get` / `batches.get` 在途撤权复核、`read.many` 工具集拦截、崩溃收据从 Worker 域修复与 `App.RequestStop(false)` 停机链 | `go test -v -run "TestM1|TestAutomationCoverage" ./internal/app/...` | 热切后通用操作与通用收据仍可读、协作操作及协作收据拒绝；崩溃未完成收据经 `operations.get` 找回同一 `turn_id` 不二发；存在活跃 Worker 时 `RequestStop(false)` 返回 `false` |
| 本机真实 Codex `app-server` 两轮持久会话复审（含只读沙箱与 `-c mcp_servers.asterhand={command='disabled',enabled=false}`） | `$env:ASTERHAND_LIVE_CODEX="1"; go test -v -timeout 180s -run TestCodexAdapterLiveTwoTurnReview ./internal/workers/...` | 真实启动 `codex app-server`，Turn 1 指出缺陷标记 `BUG_ZERO`，修改临时目录文件后 Turn 2 复用同一原生 `threadId` 复审并回传 `FIXED_OK` |
| 全仓静态检查、测试、构建与覆盖/前端/国际化守卫 | `go vet ./...`、`go test ./...`、`go build ./cmd/asterhand`、`pwsh -NoProfile -File ./scripts/check-i18n.ps1 -Go <go.exe>`、`node ./scripts/verify-frontend-assets.mjs`、`node --test ./tests/automation-coverage.test.mjs` | 全部退出码 0；中英文词表与四层硬编码守卫 5/5 通过；所有 `/api/harnesses*` 与 `/api/workers*` 端点均具备显式覆盖分类 |
| **真机 Antigravity 端到端用户验收**（人工步骤） | 在本机启动 Asterhand → 打开 Web「设置 → Coding Harness」添加测试工作区（只读）、开启 Antigravity（协作）与 Codex（Worker）、一键接入 Antigravity → 在真实 Antigravity 对话中先后让 `codex-reviewer` 审计划/代码并在修改后同 Worker 复审，验证热关开关与停止/退出 | Antigravity 原对话直接引用 Codex 的具体审查意见并继续行动；修改后复审沿用原 Worker 身份；热关协作开关即时拦截；活跃审查时退出节点提示忙并受控收尾 |

## 支持边界与验收状态

- **已通过验证与本机用户验收（Windows x64，Milestone 1）**：上述全部 Go 单元/故障/节点级自动化测试、i18n/前端/覆盖守卫、本机真实 `codex-cli 0.171.0` `app-server` 两轮实测，以及**本机真实 Antigravity 对话 → Asterhand MCP（仅暴露 `core + collab` 14 个无点号工具且 `parameters` 完整）→ 只读 Codex（含模型型号与 `reasoning_effort` 指定及结果回传原对话）端到端用户验收**均已通过。
- **后续里程碑范围（当前未实现）**：四家自动发现、`cmd/asterhand-mcp` 独立无版本号转发器、Codex `workspace_write` 写入模式、Claude Code / DeepSeek Harness 适配器与人工审批卡片分别归属 Milestone 2—4，不计入本期验收范围。
- **未验证平台**：macOS 与 Linux 仅保留编译占位（`process_other.go`），在对应平台实测前不支持。
