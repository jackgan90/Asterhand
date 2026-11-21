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

## 4. 仍待真机端到端验证的项

- **已实测确认（Antigravity 懒加载 MCP Schema 命名兼容）**：Antigravity 客户端在为懒加载 MCP 工具生成本地 JSON schema 时，对所有名称含 `.` 的工具（哪怕 `inputSchema` 与 `capabilities` 完全一致，如 `node.version` / `models.list` 或全字段带 `description` 的 `commands.list`）会将 `"parameters"` 置为 `null`，仅不含 `.` 的 `capabilities` 保留完整 `parameters`。因此 `MCPHost` 在受管 Coding Harness 调用方（`--caller` 非空）边界统一将对外 MCP 工具名中的 `.` 映射为 `_`（如 `workers_create`、`operations_new_id`），同时自动兼容反解 `operation` 参数并保留内部/CLI/HTTP 的规范点号名与零冲突测试守卫。
- 真实 Antigravity 对话中消费 `mcpResult` 双通道输出（`content[].text` + `structuredContent`）、工具集裁剪隔离、指定 `model` / `reasoning_effort` 与回传 Codex 只读分析结论的真机验收已通过，详见 [validation.md](validation.md)。
