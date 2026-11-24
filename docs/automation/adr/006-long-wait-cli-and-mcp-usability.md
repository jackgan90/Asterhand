# ADR-006：长程阻塞等待 CLI、MCP 瞬断容错与可选自动操作身份

- 状态：accepted（设计与代码实现待本批工程验证；安装及真机验收独立推进）
- 日期：2026-11-24
- 所属领域与需求：自动化、CLI 与 MCP（[自动化需求](../requirements.md) `AUTO-13`、`AUTO-HARNESS-02`、`AUTO-HARNESS-03`、`AUTO-HARNESS-05`；[安装需求](../../installation/requirements.md) `INST-34`）
- 替代 / 被替代记录：扩展 [自动化 ADR-001](001-shared-operations.md) 与 [自动化 ADR-005](005-harness-mcp-lifecycle-and-dynamic-toolsets.md)，保留 Named Pipe + TLS 1.3 传输与公共业务 API 版本 1

## 背景

在外部 Coding Harness（如 Claude Code）通过 `asterhand-mcp` 长期编排其他受控 Worker（如单轮耗时 10–17 分钟的 Codex）的实际协作中，暴露出四类影响效率与可靠性的结构性摩擦：
1. **长轮次轮询上下文成本过高**：`workers_wait` 单次上限为 `30s`，长任务需要轮询二三十次；`workerWaitView` 又内嵌完整 `workers.Turn` 导致每次轮询重复回传 `baseline_vcs`、关联键及冗余 `next_step`；
2. **守护进程升级/重启瞬断导致 MCP 工具消失**：本地覆盖更新或重启节点时，`asterhand-mcp` 的轮询将瞬态 `node_unavailable` 等同于鉴权吊销，清空全部已注册工具并向客户端广播空工具集；
3. **写操作强制往返 `operations_new_id`**：每次 `workers_send`/`workers_steer`/`workers_reply` 必须先消耗一轮模型调用获取合法时间戳 ID；
4. **缺少安装根目录稳定 CLI 入口与阻塞等待原语**：版本化 `asterhand.exe` 位于版本号子目录且默认不在 `PATH`，调用方既无法稳定启动后台长等命令，也难以在 MCP 异常时直接切换等价 CLI 兜底。

## 决策

1. **保持 MCP `MaxWait = 30s`，以稳定入口 `asterhand-cli.exe wait` 承接长程阻塞等待**：
   - 不强行拉长服务端 `automation.MaxWait`、客户端 `40s` 超时或服务端 `45s` 写超时，避免撞上不同 MCP 宿主客户端 60–120s 的单工具调用硬超时，也避免阻塞对话主流程；
   - 在安装根目录与 `asterhand-mcp.exe` 同级交付纯控制台转发入口 `asterhand-cli.exe`（仅转发 `wait`、`automation`、`status`、`version`，自动校验 `installation.json` 并注入 `--home <Installation.Data>`），通过 `capabilities.cli_path` 与 MCP `Instructions` 显式告知路径，不修改用户系统 `PATH`；
   - 新增 `wait` 子命令（复用 `--caller` 受限身份与 `workers_wait`/`workers_wait_any` 30s 切片轮询，支持瞬断宽限重连、状态变更 `stderr` 单行输出与退出 `stdout` 单 JSON，总超时返回退出码 `8` 并携带最新快照）。
2. **`workers_wait` 默认返回紧凑视图，按需支持 `verbose`**：
   - 默认省略内嵌 `workers.Turn`，将 `progress`、`error_code`、`error_message`、终态 `after_vcs` 与 UTF-8 对齐的 `next_cursor` 提升至顶层，并消除 `formatMCPText` 中的重复 `next_step`；需要完整元数据时可传 `verbose: true` 或调用 `workers_get`。
3. **MCP 写工具 `operation_id` 改为可选自动生成（对齐 CLI），拒绝客户端自造任意格式**：
   - 保持 `operations.CheckFresh` 的 13 位 UTC 毫秒时间戳格式与 10 分钟接纳窗口不变，防止 30 天收据淘汰后重放旧身份；
   - MCP 写工具省略 `operation_id` 时由 `asterhand-mcp` 进程调用 `operations.NewIDFor` 生成合法身份并在成功及断连报错中一并回传，供后续 `operations_get` 对账与重试复用；显式传入仍严格校验。
4. **`MCPHost` 严格区分瞬态离线与显式鉴权拒绝**：
   - 仅当守护进程显式返回 `forbidden` / `authorization_required` / `toolset_disabled`（或本地 `.token` 凭据缺失失效）时移除受限工具；
   - 遇到节点重启导致的 `node_unavailable` 等瞬态离线时保留当前已注册工具清单（离线调用仍由客户端/服务端门禁安全报错），节点恢复后仅在工具/Schema 摘要或权限策略实际变化时发送 `tools/list_changed`。

## 备选与后果

- **备选 A：允许模型按自定义 `op_<uuid>` 自行构造 `operation_id`**：会破坏基于内嵌时间戳的 `AcceptanceWindow` 防重放语义，或迫使服务端改变收据保留/淘汰结构，予以否决。
- **备选 B：将 `workers_wait` 上限直接提升至 20 分钟并依赖 MCP Progress 通知保活**：各外部 MCP 客户端对长阻塞 tool call 的超时与保活策略不一致，且长工具会阻塞主会话交互能力，予以否决。
- **备选 C：自动修改 Windows 用户级 `PATH` 注册 `asterhand-cli.exe`**：修改全局环境变量侵入用户主机环境且易受旧安装残留影响；改由 `capabilities.cli_path` 与 MCP 指令传递精确路径更安全可控。
- **后果与限制**：省略 `operation_id` 的极端情况下，若 `asterhand-mcp` 自身在收到节点响应前异常退出，调用方未拿回生成 ID 时需通过 `workers_get` / `workers_list` 对账而非 `operations_get`；需要跨进程绝对确定性幂等时仍应先显式调 `operations_new_id`。

## 验证与关联

- 关联计划：[多 Harness 协作长程等待与精简回传优化计划](../../development/features/harness-collab-usability/plan.md)
- 关联文档：[自动化需求](../requirements.md) · [自动化设计](../design.md) · [CLI 与 MCP 使用指南](../usage.md) · [安装设计](../../installation/design.md)
