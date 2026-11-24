# 多 Harness 协作长程等待、MCP 容错与精简回传优化计划

## 基本信息

- **功能名称**：`harness-collab-usability`（多 Harness 协作长程等待、MCP 容错与精简回传优化）
- **目标与用户价值**：
  1. 修复长文本结果截断时缺失精确 `next_cursor` 与超限丢弃定位字段的问题，使主控端无须盲猜字节偏移即可对齐 UTF-8 边界翻页读取；
  2. 修复 Asterhand 守护进程重启或升级瞬断期间 MCP 子进程误清空已注册工具列表的缺陷，并同步修复等待态（`waiting_approval` / `waiting_input`）在节点重启后的恢复对账遗漏；
  3. 默认精简 `workers_wait` 响应体积与双通道重复文本，消除高频轮询中的重复元数据消耗，同时保留可选 `verbose` 开关供按需取全；
  4. 提供位于安装根目录的稳定无版本号 CLI 转发入口（`asterhand-cli.exe`）与客户端侧阻塞长等待子命令（`wait`），配合 `capabilities.cli_path`、受限 `--caller` 与 `--args` 直传，支持后台 shell 零轮询上下文等待长轮次，并在 MCP 异常时提供完整 CLI 兜底；
  5. 将 MCP 写工具的 `operation_id` 调整为可选并在省略时自动生成与回传，消除每次写调用前先查 `operations_new_id` 的多余往返。
- **状态及实施授权范围**：
  - 当前已获授权同步领域文档并完成代码实现与工程测试验证；本地安装部署（`install-local`）、真机端到端验收、本地提交与推送均保留独立人工授权。
- **关联 Rxx / Cxx / 领域需求 ID**：
  - 自动化领域需求：`AUTO-13`、`AUTO-HARNESS-02`、`AUTO-HARNESS-03`、`AUTO-HARNESS-05`（[自动化需求](../../../automation/requirements.md)）；
  - 安装领域需求：`INST-33`、`INST-34`（[安装需求](../../../installation/requirements.md)）。
- **相关领域 requirements / design 链接**：
  - [自动化需求](../../../automation/requirements.md)、[自动化设计](../../../automation/design.md)、[CLI 与 MCP 使用指南](../../../automation/usage.md)
  - [安装需求](../../../installation/requirements.md)、[安装设计](../../../installation/design.md)
  - [多 Coding Harness 本地中介计划](../multi-harness-broker/plan.md)
- **ADR**：
  - [自动化 ADR-006：长等待 CLI、MCP 瞬断容错与可选自动操作身份](../../../automation/adr/006-long-wait-cli-and-mcp-usability.md)

---

## 范围

### 本次实现
1. **分页 `next_cursor` 与超限文本保底**：
   - `workers_wait` 与 `workers_wait_any` 在预览截断时直接返回对齐 UTF-8 字符边界的 `next_cursor`，并在 `next_step` 提示中显式带出 `cursor` 参数；
   - `formatMCPText` 超过 `48 KiB` 单响应文本上限时保留关键标识字段（`turn_id`、`worker_id`、`state`、`truncated`、`result_bytes`、`next_cursor`、`triggered_turn_id`、`error_code`）与续读指引。
2. **守护进程瞬断工具保持与等待态重启对账**：
   - `MCPHost` 区分瞬态连接不可达（`node_unavailable` / `unavailable` / `wait_timeout` / 传输失败）与显式鉴权/工具集拒绝（`forbidden` / `authorization_required` / `toolset_disabled`）：瞬断期间保持已注册工具清单不变，显式鉴权拒绝或删除凭据仍即时移除工具；启动首次拉取增加有界短重试（最多 3 次 × 500ms）；同版本或同 Schema 重启不触发多余工具列表刷新通知；
   - `internal/workers` 的 `ListNonTerminalTurns` 与 `PruneExpiredDetails` 补齐 `waiting_approval` 与 `waiting_input` 两种非终态，节点重启后统一转入 `reconciliation_required` 并清空已失效的挂起交互对象。
3. **精简 `workers_wait` 返回体与 MCP 双通道投影**：
   - `workerWaitView` 默认不内嵌整份 `workers.Turn`，将 `progress`、`error_code`、`error_message` 与仅终态输出的 `after_vcs` 提升至顶层；新增可选入参 `verbose`（默认 `false`）按需包含完整 `turn`；
   - `formatMCPText` 避免重复输出 `next_step`，`appendChangedFilesNote` 优先读取顶层 `after_vcs`。
4. **稳定无版本号 CLI 转发入口与阻塞长等待 `wait` 子命令**：
   - 新增 `cmd/asterhand-cli`（构建并发布 `asterhand-cli.exe` 至安装根目录），复用 `launch.ReadInstallation` 校验同目录 `installation.json`，仅转发安全 CLI 子命令（`wait`、`automation`、`status`、`version`）并在未显式传入时注入 `--home <Installation.Data>`，不改写用户全局 `PATH`；
   - 新增 `asterhand wait <turn_id...>` 子命令：支持单/多轮次阻塞等待、任意顺序 flag 与位置参数、`--caller` 受限主体、节点重启瞬断宽限自动重连、`--wake-on-commentary`、状态变化单行输出至 `stderr`、退出时向 `stdout` 输出单个紧凑 JSON（总超时返回退出码 `8` 并携带最新快照）；
   - `asterhand automation` 新增 `--caller` 与 `--args`，与 `wait` 一并作为 MCP 异常时的等价 CLI 兜底；`capabilities` 返回 `cli_path` 并在 MCP `Instructions` 与 `workers_send` 引导中提示。
5. **MCP 写工具 `operation_id` 可选自动生成**：
   - `InputSchema` 保留 `operation_id` 字段定义及正则校验，移出必填列表；省略时由 `asterhand-mcp` 进程在发送前使用合法毫秒时间戳生成并随成功及错误响应原样返回；显式传入仍执行严格格式与防重放检验，并保留 `operations_new_id` 供预生成重放场景使用。

### 明确非目标
- 不改变 MCP 单次 `workers_wait` / `workers_wait_any` 的 `30s` 上限（规避外部客户端工具调用硬超时）；
- 不修改 `web/workers-panel.mjs`（999 物理行）及 Web 界面样式；
- 不在本批实现 Token 用量采集、Worker 隔离记忆开关、非仓库上下文附件、JSON Schema `output_contract` 校验、`isolated_worktree` 并行写或评审议题状态机。

### 依赖和待验证假设
- 依赖现有 `launch.ReadInstallation`、`internal/operations` 时间窗口检验与 `internal/automation` 命名管道 TLS 1.3 客户端；
- 本次构建与测试验证覆盖 Windows Go 单元/集成测试与本地安装脚本模拟检查；安装至本地程序目录后的真实 Claude Code + Codex 跨轮端到端验收需另行授权执行。

### 验收用例及预期可见结果
1. 长答案被 `workers_wait` / `workers_wait_any` 截断时，直接拿返回的 `next_cursor` 调用 `workers_result` 即可无报错读全（含中文多字节边界）；`48 KiB` 文本超限时仍能从回包直接读取 `turn_id` 与 `next_cursor`；
2. 守护进程短暂重启期间已连接 MCP 会话不清空工具列表，同 Schema 重启不发无效变更通知；撤销凭据或关闭权限开关立即移除受限工具；
3. `workers_wait` 默认响应不再重复携带整份 `Turn`、`baseline_vcs` 与重复 `next_step`，`verbose=true` 时完整保留 `turn`；
4. 直接调用 `workers_send` 无需先调用 `operations_new_id`，成功和断连未知结果均返回本次自动生成的 `operation_id`；
5. 通过 `capabilities.cli_path` 获取稳定 `asterhand-cli.exe` 路径后，后台运行 `asterhand-cli.exe wait <turn_id> --caller <id>` 可跨越 10 分钟以上任务在终态或需处理交互时退出并返回紧凑 JSON，期间重启守护进程不断线退出；超过 `--timeout` 以退出码 `8` 退出并保留最新快照。

---

## 实施与文档同步

- **实施步骤**：
  1. 同步编写本功能计划、ADR-006 与自动化/安装领域文档；
  2. 更新 `internal/workers` 的 `WaitAnyItem`、`buildWaitAnyItems`、非终态恢复查询与清理 SQL；
  3. 更新 `internal/app` 的 `workerWaitView`、`workerWaitNextStep` 与 `capabilities.cli_path`；
  4. 更新 `internal/automation` 的 `mcp.go`、`mcp_text.go`、`cli.go` 并新增 `cli_wait.go`；
  5. 接入 `cmd/asterhand/main.go`、`cmd/asterhand-cli` 与 `scripts/build-application.ps1`、`install.ps1`、`scripts/test-fast-local-install.ps1`；
  6. 补充并运行 Go、前端守卫与本地安装测试，核对文档链接完整性。
- **必须更新的领域文档及实质内容**：
  - [自动化需求](../../../automation/requirements.md)：同步 `AUTO-13` 可选自动生成写操作身份与 `AUTO-HARNESS-03`/`05` 长等待 CLI、精简等待视图、游标与瞬断保持要求；
  - [自动化设计](../../../automation/design.md) & [CLI 与 MCP 使用指南](../../../automation/usage.md)：同步稳定 CLI 转发入口、`wait` 命令、退出码 `8`、`cli_path`、紧凑视图、MCP 自动 `operation_id` 与下划线规范名示例；
  - [安装需求](../../../installation/requirements.md)（`INST-34`）& [安装设计](../../../installation/design.md)：同步 `asterhand-cli.exe` 分发与安装清单；
  - [自动化 ADR-006](../../../automation/adr/006-long-wait-cli-and-mcp-usability.md)、[ADR 索引](../../../automation/adr/README.md)、[开发路线图](../../roadmap.md)、[文档导航](../../../README.md)、[多 Coding Harness 中介计划](../multi-harness-broker/plan.md)。
- **覆盖清单影响评估**：
  - 本功能未新增公共业务领域操作（仅扩展既有 `capabilities` 返回字段 `cli_path` 与 `workers_wait` 可选 `verbose` 参数，并新增客户端侧复合观察命令 `wait` 复用既有 `workers_wait`/`workers_wait_any`），`contracts/automation-coverage.json` 操作集合保持一致；`TestAutomationCoverage` 继续通过。

---

## 规模与拆分评估

- 所有新建与修改源码文件均远低于 1000 物理行上限（`cli_wait.go` 与 `cmd/asterhand-cli/main.go` 为独立职责模块，其余改写均控制原文件体量内），所有函数保持单一职责与 100 行以内；
- 本次不修改当前处于 999 物理行的 `web/workers-panel.mjs`，未触发前端拆分前提。

---

## 验证与维护

- **仓库测试入口与复现命令**（需先将锁定版 Go 工具链加入 `PATH`）：
  - `go vet ./...`
  - `go test -count=1 ./internal/harnesses ./internal/workers ./internal/app ./internal/automation ./internal/quickinput ./internal/quickinput/hostipc ./internal/notifications ./cmd/asterhand ./cmd/asterhand-mcp ./cmd/asterhand-cli`
  - `go build` 校验 `asterhand.exe`、`asterhand-mcp.exe`、`asterhand-cli.exe`
  - `node ./scripts/verify-frontend-assets.mjs`、`node --test ./tests/*.test.mjs`、`pwsh -NoProfile -File ./scripts/check-i18n.ps1`、`pwsh -NoProfile -File ./scripts/test-fast-local-install.ps1`
- **当前边界与后续工作**：
  - 仅覆盖 Windows 正式发布布局与 Go 单元测试，真实本机安装升级及 Claude Code + Codex 真机端到端验收需用户单独授权执行；
  - Token 用量可观测性与 Worker 隔离记忆开关作为下一批独立体验优化评估，不混入本次变更。
