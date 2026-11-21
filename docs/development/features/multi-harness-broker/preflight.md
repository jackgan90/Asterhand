# 多 Coding Harness 中介层 M1 协议预检记录

本记录保存 Milestone 1 实施前针对本机锁定工具链与 `codex-cli 0.171.0` `app-server` 协议的只读/隔离实测结论，作为 [multi-harness-broker 计划](plan.md)与 `CodexAdapter` 实现的权威协议依据。本文件不包含个人绝对路径、账户凭据或原始长日志。

## 1. 预检环境与版本

- 平台：Windows x64
- Codex CLI：`codex-cli 0.171.0`（支持 `codex app-server` 与 `codex app-server generate-json-schema`）
- Asterhand MCP SDK：`github.com/modelcontextprotocol/go-sdk v1.7.0`

## 2. `codex app-server` 协议契约核对结论（基于 `generate-json-schema` + 隔离实测）

### 2.1 握手与方法清单
- 传输：stdio 换行分隔 JSON-RPC 2.0（无需 `Content-Length` 帧头）。
- 握手：`initialize`（`params: {"clientInfo": {"name": "...", "version": "..."}}`）→ 收到响应后发送通知 `initialized`（无 `id`）。
- 关键 Client→Server 方法：
  - `thread/start`：`params` 含 `cwd`、`sandbox`（`"read-only"` | `"workspace-write"` | `"danger-full-access"`）、`approvalPolicy`（`"never"` | `"on-request"` | `"untrusted"` | granular）；返回 `thread.id`、`sandbox`、`approvalPolicy`。
  - `thread/resume`：`params` 含 `threadId`、`cwd`、`sandbox`、`approvalPolicy`。
  - `thread/turns/list`：`params` 含 `threadId`、`itemsView`（`"full"` | `"summary"` | `"notLoaded"`）、`limit`、`cursor`、`sortDirection`；**响应字段为 `data`（轮次数组）、`nextCursor`、`backwardsCursor`**（非 `turns`）。
  - `thread/items/list`：支持按 `threadId` + `turnId` 分页读取条目。
  - `turn/start`：`params` 含 `threadId`、`cwd`、`approvalPolicy`、`sandboxPolicy`（只读为 `{"type": "readOnly", "networkAccess": false}`；写入为 `{"type": "workspaceWrite", "writableRoots": [...]}`）、`clientUserMessageId`、`input`（`[{"type": "text", "text": "..."}]`）；返回 `turn.id`、`turn.status`。
  - `turn/interrupt`：`params` **必填 `threadId` 与 `turnId`**（双 ID 精确中断）。
- 关键 Server→Client 通知：
  - `turn/started`、`turn/completed`（`turn.status` 枚举：`"completed"` | `"interrupted"` | `"failed"` | `"inProgress"`）
  - `item/started`、`item/completed`、`item/agentMessage/delta`
  - `item.type == "userMessage"` 含 `id`、`clientId`、`content`
  - `item.type == "agentMessage"` 含 `id`、`text`、`phase`（`"commentary"` | `"partial_answer"` | `"final_answer"` | `null`）
- 关键 Server→Client 请求（需客户端回包，否则轮次挂起）：
  - 审批类：`item/commandExecution/requestApproval`、`item/fileChange/requestApproval`、`item/permissions/requestApproval`、`execCommandApproval`、`applyPatchApproval`（其中命令与文件变更审批响应均支持 `{"decision": "cancel"}` 立即拒绝并中断轮次）；
  - 交互/提问类：`item/tool/requestUserInput`、`mcpServer/elicitation/request`、`item/tool/call`（M1 审批/提问桥上线前立即返回 JSON-RPC 错误或取消响应，并随后发双 ID `turn/interrupt` 保证有界结束）。

### 2.2 三项关键实测结果

| 预检项 | 实测方式 | 实测结果 | 对实现的直接约束 |
|---|---|---|---|
| **禁用受管 Worker 内 `asterhand` MCP** | 启动 `codex app-server -c mcp_servers.asterhand.enabled=false` vs `-c mcp_servers.asterhand={command="disabled",enabled=false}` | 仅传 `.enabled=false` 在 `config.toml` 无 `asterhand` 条目时会直接退出报错 `Error: invalid transport in mcp_servers.asterhand`；传入 `mcp_servers.asterhand={command="disabled",enabled=false}` 在有无既有条目下均正常启动 | `CodexAdapter` 固定传入 `-c mcp_servers.asterhand={command="disabled",enabled=false}`，避免未配置 Codex MCP 时启动崩溃 |
| **`clientUserMessageId` 跨进程重启持久匹配** | `turn/start` 携带 `clientUserMessageId`，轮次完成后结束进程，重启全新 `codex app-server` 调用 `thread/turns/list(itemsView="full")` | 重启后返回 `data[0].items` 中 `userMessage.clientId` 精确保留原 `clientUserMessageId`，且与 `turn.id` 及 `agentMessage(phase="final_answer")` 一并持久可读 | M1 丢失 `turn/start` 回执后的对账机制可用 `client_correlation_id` ↔ `userMessage.clientId` 做唯一精确匹配 |
| **Windows 原生 `read-only` 写拦截与 `phase` 行为** | 隔离临时工作区下以 `sandbox="read-only"`、`approvalPolicy="never"`、`sandboxPolicy={"type":"readOnly","networkAccess":false}` 要求 Codex 创建文件 | PowerShell 写盘返回 Access Denied，文件未创建；同一轮先输出 `phase="commentary"`，随后输出 `phase="final_answer"`；`ReadOnlySandboxPolicy` 无 `readableRoots` 字段 | Codex 0.171.0 在 Windows 上的 `read_only` 写保护实测有效（能力可标为 `os_sandbox_enforced` 写保护），但仍不宣称对工作区外可读文件做目录级只读 chroot |

## 3. MCP Go SDK (`v1.7.0`) 动态工具集核对

- 锁定版本 `github.com/modelcontextprotocol/go-sdk v1.7.0` 的 `mcp.Server.AddTool` 与 `mcp.Server.RemoveTools` 均会自动向已连接会话发出 `notifications/tools/list_changed`；
- 配合守护进程侧按受限 `Principal` 实时执行的调用闸门，可同时满足“支持热刷新客户端工具列表自动同步”与“不支持热刷新客户端在调用入口即时被拒”。

## 4. 仍待真机端到端验证的项与 Milestone 2 预检结论

- **已实测确认（全仓统一 `[a-zA-Z0-9_-]` 下划线操作名）**：Antigravity 客户端在为懒加载 MCP 工具生成本地 JSON schema 时，对所有名称含 `.` 的工具（哪怕 `inputSchema` 与 `capabilities` 完全一致，如 `node_version` / `models_list` 或全字段带 `description` 的 `commands_list`）会将 `"parameters"` 置为 `null`，仅不含 `.` 的工具保留完整 `parameters`。因此全仓所有公共自动化操作（CLI、HTTP、直连 MCP 与受管 MCP）已统一使用下划线规范名（如 `workers_create`、`harnesses_list`、`operations_new_id`），取消点号↔下划线双轨转换，并由 `TestAutomationCoverage` 从源头强制校验 `[a-zA-Z0-9_-]+`。
- **Milestone 2 本机三家外部 CLI / MCP 管理接口实测结论（Windows x64）**：
  - **Codex CLI（`codex-cli 0.171.0`）**：支持 `codex mcp add <name> -- <cmd> <args...>`、`codex mcp remove <name>`、`codex mcp get <name> --json`。隔离临时 `CODEX_HOME` 实测确认：`codex mcp get asterhand --json` 将 `command` 与 `args` 嵌套在 `transport: {type: "stdio", command, args}` 对象下，且未找到条目时以退出码 1 在 stderr 输出 `Error: No MCP server named ... found.`；同时 stderr 可能附带 `WARNING:` 行，解析时必须仅消费 stdout JSON。
  - **Claude Code（`claude 2.1.284`）**：支持 `claude mcp add --scope user <name> -- <cmd> <args...>` 与 `claude mcp remove --scope user <name>`，用户级条目位于 `%USERPROFILE%\.claude.json` 顶层 `mcpServers.<name>`；Asterhand 仅只读检查 `mcpServers.asterhand` 单键并调用官方 CLI 增删，绝不整文件备份或接触其他条目的凭据。
  - **DeepSeek Harness（`0.2.2-rc.5` 桌面版）**：默认安装于 `%LOCALAPPDATA%\Programs\DeepSeek Harness\resources\runtime\cli\bin\dsh.cmd`（不在全局 `PATH`），`dsh -V` 返回 `0.2.2-rc.5`。实测确认其 `desktop` profile 明确返回 `error: profile "desktop" is managed exclusively by the Electron application` 且未提供官方独立 `mcp add/remove` CLI。因此 Milestone 2 诚实落地 DeepSeek Harness 的自动发现、版本探测与手工路径覆盖，保持 `caller_managed = false`（不逆向或强改 Electron 独占配置），留待 Milestone 3 协议预检与适配。
  - **Codex `workspace_write` 真实写保护实测**：在临时 Git 仓库中启用 `sandbox="workspace-write"`、`sandboxPolicy={"type":"workspaceWrite","writableRoots":[repoDir]}`、`approvalPolicy="never"` 后，真实 Codex 正常修改目标文件且不触碰基线脏文件，前后 `CaptureVCSSnapshot` 准确返回 `dirty_baseline=true`、`external_changes_possible=true` 与变更文件清单。
- 真实 Antigravity 对话中消费 `mcpResult` 双通道输出（`content[].text` + `structuredContent`）、工具集裁剪隔离、指定 `model` / `reasoning_effort` 与回传 Codex 只读分析结论的 Milestone 1 真机验收已通过，详见 [validation.md](validation.md)。
