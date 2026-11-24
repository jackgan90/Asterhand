# 多 Coding Harness 中介层协议预检记录

本记录保存实施前针对本机锁定工具链的只读/隔离协议实测结论，作为 [multi-harness-broker 计划](plan.md)与各厂商适配器（`CodexAdapter`、`ClaudeAdapter`、`DeepSeekAdapter`）实现的权威协议依据。本文件不包含个人绝对路径、账户凭据或原始长日志。

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

## 5. Milestone 3 协议预检结论（Claude Code & DeepSeek Harness，Windows x64）

### 5.1 Claude Code（`claude 2.1.330`）`stream-json` 双向协议

| 预检项 | 实测方式 | 实测结论 | 对 `ClaudeAdapter` 的直接约束 |
|---|---|---|---|
| **持久多轮与双向控制** | `claude -p --verbose --input-format stream-json --output-format stream-json --no-chrome` 保持 stdin 打开，先发 `control_request(initialize)` 再连续发送多条 `{"type":"user","message":{"role":"user","content":"..."}}` | `initialize` 返回 `control_response(subtype:"success")`；单进程内连续多轮均正常产出 `system(init)` → `assistant` → `result(subtype:"success", session_id, result)`，保持同一 `session_id` | 复用持久子进程与空闲回收模型；支持 `control_request(subtype:"interrupt")` 精确中断进行中轮次 |
| **防递归禁用 `asterhand` MCP** | 固定传 `--strict-mcp-config --mcp-config '{"mcpServers":{}}'` | `system(init)` 返回 `mcp_servers: []`，即使本机用户配置中存在 `asterhand` 或其他 MCP server 也不会加载 | 作为受控 Worker 启动时强制携带空 `--strict-mcp-config` |
| **不要误用 `--restricted`** | 对比携带与不携带 `--restricted` | `--restricted` 会忽略用户设置文件（`~/.claude/settings.json`），导致自定义认证/代理设置失效并报错 `Not logged in · Please run /login` | **不得使用 `--restricted`**；使用正常认证链路配合 `--strict-mcp-config`、`--tools` 与 `--permission-mode` 控制能力边界 |
| **只读 vs 工作区可写边界** | `read_only`：`--tools Read,Glob,Grep --permission-mode dontAsk --permission-prompts none`；`workspace_write`：`--tools Read,Glob,Grep,Edit,Write,Bash --permission-mode acceptEdits --permission-prompts host` | `read_only` 下模型无任何写文件/命令执行工具，尝试创建文件未落盘；`workspace_write` 下工作区内 `Write` 直接成功，工作区外路径写入被拒绝（记录于 `permission_denials`，文件未创建）；支持 `--effort`（`low`/`medium`/`high`/`xhigh`/`max`）、`--model` 与 `--append-system-prompt` | Windows 下按实测如实声明 `harness_tool_restricted`（应用层工具与权限限制 + `OrdinaryToken` 非提权进程）；收到 `control_request(subtype:"can_use_tool")` 时立即回复 `deny + interrupt:true` 收敛为 `blocked_unsupported_approval`（若为 `AskUserQuestion` 则为 `blocked_unsupported_user_input`） |
| **预绑定会话与跨进程 `--resume`** | 首轮传 `--session-id <uuid>`；新进程传 `--resume <uuid>`；以及传入不存在的随机 UUID | 首轮直接采用预分配 UUID 作为 `session_id`；新进程 `--resume` 准确记住上一轮上下文；无效 UUID 返回 `is_error:true`、`subtype:"error_during_execution"` 及 stderr `No conversation found with session ID: ...` | 首轮可用预分配 UUID 提前落盘 `NativeSessionID`；恢复失败精确映射为 `native_session_unrecoverable`（绝不静默新建会话） |

### 5.2 DeepSeek Harness（`dsh.cmd 0.2.2-rc.5`）ACP（Agent Client Protocol）协议

| 预检项 | 实测方式 | 实测结论 | 对 `DeepSeekAdapter` 的直接约束 |
|---|---|---|---|
| **官方协议选型（`acp` vs `headless` vs `sdk`）** | 对比 `--dump-default-config` 与隔离实测 | `headless` 默认禁用 `deepseek-account` 与 `credentials`；`acp`（`dsh --profile acp`）完整保留账号/凭据插件并暴露标准 **Agent Client Protocol v1**（stdio 换行分隔 JSON-RPC 2.0），握手返回 `agentCapabilities.sessionCapabilities: {close, list, resume}` | 锁定 `dsh --profile acp` 作为 `DeepSeekAdapter` 唯一协议，无需逆向内部私有 RPC |
| **会话创建、配置与跨进程恢复** | `initialize` → `session/new({cwd, mcpServers:[]})` → `session/set_config_option` → 跨进程 `session/resume` → `session/close` | `session/new` 返回 `sessionId` 与 `configOptions`（含实时 `model` 列表及 `reasoning_effort`: `off`/`low`/`high`/`max`）；`session/set_config_option` 支持动态切换模型与推理档位；新进程 `session/resume` 准确恢复上下文；无效 ID 返回 JSON-RPC `-32602: Invalid params: session is not resumable: ...`；`session/close` 返回 `{}` | 模型名按 `configOptions` 实时匹配（同时兼容完整 tuple JSON 与短模型名）；无效会话精确映射 `native_session_unrecoverable` |
| **流式事件与精确取消** | `session/prompt({sessionId, prompt:[{type:"text", text}]})` + 运行中发通知 `session/cancel({sessionId})` | 持续流式推送 `session/update`（`agent_thought_chunk`、`tool_call`、`tool_call_update`、`agent_message_chunk`、`usage_update`），结束返回 `{"stopReason":"end_turn"}`；收到 `session/cancel` 后立即收敛返回 `{"stopReason":"cancelled"}` | `agent_thought_chunk` / `tool_call*` 归入实时进度与日志，`agent_message_chunk` 累积为 `final_answer`；取消映射 `session/cancel` |
| **只读 / 可写与审批拦截边界** | 通过环境变量 `DSH_PERMISSION_MODE` 切换 `read-only` 与 `workspace-write`，测试区内/区外文件写入及 `pwsh` 写盘 | `read-only` 下 `write` 拒绝（`file access denied under read-only mode`），`pwsh` 写盘未创建文件；`workspace-write` 下区内写入直接成功、区外写入被拒；当模型尝试申请提权（`workspace-write` 或 `danger-full-access`）时服务端发出 JSON-RPC 请求 `session/request_permission`（选项 `allow-once` / `reject-once`），客户端回复 `{outcome:{outcome:"cancelled"}}` 即可阻断 | Windows 下 `pwsh` 沙箱受目录 ACL 影响（部分目录会触发 `0xC0000142`），文件工具依赖 `fs-sandbox`，因此诚实声明 `harness_tool_restricted`；收到 `session/request_permission` 时在 M3 阶段回复 cancelled 并触发 `session/cancel` 确定性收敛为 `blocked_unsupported_approval` |
| **防递归与发起方托管边界** | 检查 `dsh --profile acp --dump-default-config` 与 `session/new` 入参 | `acp` 默认配置不挂载外部用户 MCP server，且 `session/new` / `session/resume` 显式传入 `mcpServers: []`；同时 `desktop` profile 仍由 Electron 独占、无官方独立 `mcp add/remove` CLI | 作为 Worker 启动时强制 `mcpServers: []` 且零 Asterhand 凭据注入；`deepseek-harness` 在 M3 开放 `worker_managed = true`，`caller_managed` 继续诚实保持 `false` |

## 6. Milestone 4 协议预检结论（审批模式分档、自动审单与 Windows 交互浮窗置顶）

### 6.1 三家原生自动审单（Auto-Approve）与审批回调边界实测（Windows x64）

| Harness / 版本 | 实测方式 | 实测结论 | 对 M4.1 实现的直接约束 |
|---|---|---|---|
| **Codex (`codex-cli 0.171.0`)** | 导出 `TurnStartParams` / `TurnSteerParams` / `CommandExecutionRequestApprovalResponse` / `ToolRequestUserInputParams` / `AsyncUserInputQuestion` Schema，并在隔离目录对比审批组合及 `request_user_input_async` 提问链路 | ① `ApprovalsReviewer` 枚举为 `"user"` / `"auto_review"`（兼容 `"guardian_subagent"`）；默认不传时若本机启用了 Guardian，Codex 会经 `item/autoApprovalReview` 静默批准并写入工作区外文件（`approvals_count = 0`）；② 显式 `approvalsReviewer: "user"` + `workspace-write + on-request` 稳定触发 `item/commandExecution/requestApproval`，回复 `{"decision":"decline"}` 后仅拒绝该命令、轮次优雅继续；③ `danger-full-access + on-request` 不发审批回调（`approvals_count = 0`）；④ `danger-full-access + untrusted + approvalsReviewer: "user"` 稳定触发 `item/commandExecution/requestApproval`（带完整 `command`/`cwd`）；⑤ 默认业务提问走 `request_user_input_async`，以 `item/completed`（`type:"agentMessage", delivery:"async", questions:[{title,options}]`）发问后进入 `sleep`，向其发送 `turn/steer({threadId, expectedTurnId, input})` 即可 1ms 唤醒并带答案续跑 | `approval_mode="auto"` 默认用 `on-request + approvalsReviewer:"auto_review"` 并记录 `item/autoApprovalReview`/`guardianWarning` 日志；`approval_mode="ask_human"` 必须显式传 `approvalsReviewer:"user"` 防止 Guardian 截胡；人类授权“本会话免审”后切 `danger-full-access + untrusted + approvalsReviewer:"user"` 经 Asterhand Go 硬底线过滤后静默放行；识别 `AgentMessageThreadItem.questions` 转入 `waiting_input` 并用 `turn/steer` 回传回答 |
| **Claude Code (`claude 2.1.330`)** | 检查 `claude --help` / `claude auto-mode --help`，并在隔离目录实测 `--permission-mode auto`、`acceptEdits`、`--permission-prompt-tool stdio` 与 `AskUserQuestion` | ① `--permission-mode auto` 内置 Auto Mode 分类器开箱可用；② 外部 `stream-json` 模式**必须传 `--permission-prompt-tool stdio`** 才会把权限审批与 `AskUserQuestion` 经 stdio `control_request(can_use_tool)` 路由给宿主，否则既不发 `can_use_tool` 也会隐藏 `AskUserQuestion`；③ `ask_human`/`strict_deny` 不得将 `Write`/`Edit`/`Bash(python *)` 放入 `--allowed-tools`（否则会预放行区外写盘），而应依赖 `acceptEdits` + `can_use_tool`；④ 回复 `AskUserQuestion` 使用 `{"behavior":"allow","updatedInput":{...,"answers":{...}}}` 即可续跑 | 所有模式统一携带 `--permission-prompt-tool stdio` 并在 `--tools` 中包含 `AskUserQuestion`；`auto` 传 `--permission-mode auto`（仅 `approval_mode=="auto"` 时按 `system(init)` 校验实际生效态，不覆写 `strict_deny`）；`ask_human`/`strict_deny` 仅预放行只读 `Read,Glob,Grep,Bash(git status/diff/log)`，区外 `Write` 与其余命令全走 `can_use_tool` 审批桥 |
| **DeepSeek Harness (`dsh 0.2.2-rc.5`)** | 核对 `dsh --help`、`--dump-default-config` 并抓取 `session/update(tool_call)` 与 `session/request_permission` 原始 JSON | ① 仅有 `read-only` / `workspace-write` / `danger-full-access` 三档，无内置审单子代理且 ACP 无提问 RPC；② `session/request_permission` 仅携带 `toolCall.toolCallId` 而不重复 `rawInput`（完整 `command` / `justification` / `description` 在先到的 `tool_call` 通知中） | `harnesses_list/get` 如实声明 `supports_auto_approval: false`，`auto` 回落 `ask_human`；`DeepSeekAdapter` 按 `toolCallId` 缓存 `tool_call.rawInput` 供 `session/request_permission` 关联解析（并兜底解析 `pwsh:` 标题），确保区外与硬底线命令 100% 命中 `EvaluateInteractionRisk` |

### 6.2 Windows 右下角交互浮窗置顶与焦点策略核对（参考历史修复 `d6e4b7b`）
- 历史修复 `d6e4b7b` 已在 [notification_surface_windows.go](../../../../internal/quickinput/notification_surface_windows.go) 证实：Windows 上隐藏窗口直接调 `SetWindowPos(HWND_TOPMOST)` + `ShowWindow(SW_SHOWNOACTIVATE)` 可能静默不晋升 `WS_EX_TOPMOST`；必须先在隐藏态重置 `HWND_BOTTOM`，再调用 `SetWindowPos(HWND_TOPMOST, SWP_NOSIZE|SWP_NOMOVE|SWP_NOACTIVATE|SWP_SHOWWINDOW)` 并以 `IsWindowVisible && WS_EX_TOPMOST` 复核，后续移动仅传 `SWP_NOACTIVATE|SWP_NOZORDER`。
- 与旧通知不同，M4 交互浮窗在「协作者提问」模式下需支持自由文本与中文 IME 输入，不能挂永久 `WS_EX_NOACTIVATE`（否则鼠标点击也无法获焦打字）；应复用上述 Z 序晋升与校验逻辑，同时弹出时使用 `SWP_NOACTIVATE` 防抢焦点，用户主动点击浮窗内部后正常激活输入。

## 7. 双 Agent 实时协作增强预检结论（运行中 `steer` 插话与中间 `commentary` 反馈）

### 7.1 三家运行中 `steer` 插话协议实测（Windows x64）

| Harness / 版本 | 实测方式 | 实测结论 | 对 `workers_steer` 的直接约束 |
|---|---|---|---|
| **Codex (`codex-cli 0.171.0`)** | 复用已验证的 `turn/steer({threadId, expectedTurnId, input})` | 原生支持向运行中的 `turnId` 注入补充消息；`expectedTurnId` 不匹配（轮次已结束）时返回明确错误 | `supports_steer: true`，经 `CodexAdapter.SteerTurn` 直发 `turn/steer` |
| **Claude Code (`claude 2.1.330`)** | 隔离临时目录以 `--input-format stream-json --output-format stream-json` 启动首轮多步读取任务，在首次 `tool_use` 触发时立即向 stdin 写入第二条 `{"type":"user","message":{"role":"user","content":"..."}}` 并观察 5 秒收尾；另对照进入最终文本生成阶段后的尾段注入 | 尚有后续 `tool_use` 边界时，Claude 在下一工具边界无缝采纳注入指令并包含在同轮终稿中，且全过程仅产出 1 个 `result`（`resultsSeen = 1`）；若模型已进入无剩余工具调用的最终文本生成阶段，`claude -p` 结束当前 `result` 不再纳入该帧（也不污染后续新轮次） | `supports_steer: true`，经 `ClaudeAdapter.SteerTurn` 在持有 `s.active` 且匹配 `nativeTurnID` 时写入 `user` 帧；`workers_steer` 返回 `delivery_mode: "tool_boundary"` 与明确 `next_step`，提示主控在尾段收尾场景下核对终稿并在需要时跟进 `workers_send` |
| **DeepSeek Harness (`dsh 0.2.2-rc.5` ACP v1)** | 对本机 `dsh --profile acp` 执行 `initialize` 并探测 `session/steer` 与 `_session/steering` | `initialize` 返回 `sessionCapabilities: {close, list, resume}` 无 steering 扩展；两种方法均返回 `-32601 Method not found` | `supports_steer: false`，`workers_steer` 返回结构化 `unsupported_steer` 并引导等待本轮结束后改用 `workers_send`，严禁暗中改为取消重跑 |

### 7.2 三家中间 `commentary` 与思维链隔离核对
- **Codex**：仅取 `item.type == "agentMessage"` 且 `phase == "commentary"` 的面向用户文本（`item.type == "reasoning"` 不进入 `commentary`）。
- **Claude Code**：仅取 `assistant` 消息中的 `content.type == "text"` 块（`content.type == "thinking"` 仅作 `Thinking...` 进度）。
- **DeepSeek Harness**：仅取工具调用前由 `agent_message_chunk` 冲刷出的可见旁白（`agent_thought_chunk` 仅作 `Thinking...` 进度）。
