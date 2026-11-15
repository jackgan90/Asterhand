# 聊天工具调用目标摘要展示

状态：实现、工程验证与本机用户验收已完成。涉及持续会话工作区工具、原生桥接投影、共享活动协议、快捷输入聊天界面及 CLI/MCP 历史读取。

## 基本信息

- 功能名称：`chat-tool-call-summaries`（聊天工具调用目标摘要展示）
- 目标与用户价值：在聊天时间线中于工具名旁展示简短关键目标（如目录、文件路径、检索关键词或记忆文件），控制在 1—2 行内；各工具通过自身定义白名单声明可展示参数，避免写入正文、`oldText`/`newText` 或补丁差异正文进入聊天展示。
- 关联需求：`AST-CHAT-02`、`AST-CHAT-08`、`AST-TOOLS-04`、`WEB-TOOLS-03`、`WEB-I18N-04`、`DEV-13—15`
- 领域权威文档：[助手需求](../../../assistant/requirements.md)、[本机工具能力](../../../assistant/runtime-capabilities.md)、[持续会话设计](../../../assistant/conversations.md)、[Web 需求](../../../web/requirements.md)、[Web 设计](../../../web/design.md)、[日常操作覆盖清单](../../../automation/coverage.md)

## 范围与展示规则

- 本次实现：
  - 命令定义驱动的工具展示规格（`toolDisplaySpecs`），绑定已注册工具 Schema 参数并兼容历史旧工具名（`read`、`write`、`edit`、`apply_patch`、`ls`）。
  - 在原生桥接历史投影、工作区运行时批次登记/封存与协议 `wire.ToolActivity` / `workspace.Operation` 上统一承载可选有界单行字段 `summary`。
  - 快捷输入持续聊天界面采用紧凑同行布局（方案 A：`<工具名>  <目标摘要> · <状态>`），短摘要单行展示，长摘要自动折行并最多保留 2 行，完整单行文本通过标题悬停提示可得。
- 明确非目标：
  - 不在聊天中展开完整入参、写入内容、编辑前后对比文本、补丁差异 hunk 或原始工具输出。
  - 不新增专门的工具详情弹窗或独立设置项；不更改已有原生工具并发调度、工作区权限开关与 64 KiB 分页 / 2 MiB 窗口容量契约。

| 工具名（含历史别名） | 声明展示参数 | 显式排除参数 | 摘要格式与示例 |
|---|---|---|---|
| `list_dir` (`ls`) | `path`（缺省为 `.`） | `limit`, `after` | `.` 或 `docs/assistant` |
| `read_file` (`read`) | `path`, `[offset, limit]` | `cursor`, `optional` | `notes/plan.md` 或 `notes/plan.md:20-69` |
| `write_file` (`write`) | `path` | `content` | `output/summary.md` |
| `edit_file` (`edit`) | `path` | `edits` (`oldText`, `newText`) | `src/config.json` |
| `apply_file_patch` (`apply_patch`) | `input`（仅解析补丁头路径，最多前 3 项 + 计数） | diff 正文与上下文行 | `a.md → b.md, c.md (+1)` |
| `search_files` | `query`, `path`（非根目录时） | `content`, `cursor` | `"TODO" · internal` |
| `memory_search` | `query`, `corpus`（非默认值 `memory` 时） | `maxResults`, `minScore` | `"周报偏好"` |
| `memory_get` | `path`, `[from, lines]` | `corpus` | `MEMORY.md:1-40` |
| `progress_card` | 无（`kind: "none"`） | `markdown`, `plan` | 空（沿用下方结构化步骤列表） |

## 设计与契约同步

- **单一声明规格与校验**：共享模块 [tool-display.mjs](../../../../internal/executor/chatbridge/tool-display.mjs) 定义 9 项工具的 `display` 白名单规则与 `summarizeToolCall` 提取器（兼容对象与 JSON 字符串形式的原生参数）；插件 [tools.mjs](../../../../internal/executor/workspaceplugin/tools.mjs) 在每个工具定义上直接挂载对应 `display` 属性。构建时通过 `go:embed` 将该共享模块同步部署至 `bridge/` 与 `workspace-plugin/`，并由 [SDK 预检测试](../../../../internal/executor/workspace_sdk_preflight_test.go) 校验全部工具具备 `display` 规则且引用的参数均真实存在于导出的 JSON Schema 中。
- **有界归一化与隐私隔离**：工作区内路径统一转换为正斜杠相对路径，越界绝对路径仅保留末段文件名，不暴露宿主目录前缀；控制字符与换行归一化为空格，`summary` 限制在 96 显示宽度单位（160 rune、240 UTF-8 字节）以内，超长路径保留首尾文件名。未在工具 `display` 中声明的字段一律不读取。
- **协议与 CLI/MCP 一致性**：[wire.ToolActivity](../../../../internal/conversation/wire/activities.go) 与 [workspace.Operation](../../../../internal/workspace/types.go) 增加可选 `summary` 字段；工作区直调操作复用 `ToolDescriptor` 缺省目标摘要，历史读取页超 64 KiB 预算时先保留基础状态，必要时按预算裁剪；UI、`messages.list`、`turn_activities.list/get` 与 `workspace_operations.get` 共享同一 `summary` 结构。
- **前端紧凑展示与无损刷新**：[chat-activities.mjs](../../../../internal/quickinput/assets/chat-activities.mjs) 与 [chat.css](../../../../internal/quickinput/assets/chat.css) 将工具标题分为工具名、可选目标摘要块（仅存在摘要时显示）与本地化状态标签，CSS 限制 `.tool-activity-head` 最多 2 行并保持 Starcore / Frost 双主题及高对比度可读性；状态刷新与语言切换仅原位更新标题文本，保持正文与公式缓存不变。

## 规模与拆分评估

- 所有受影响手写文件（`service.go`、`descriptor.go`、`descriptor_registry.go`、`call_prepare.go`、`transport.go`、`timeline.mjs`、`projection.mjs`、`tool-display.mjs`、`tools.mjs`、`batch.mjs`、`chat-activities.mjs`、`chat.css` 等）均保持在 1000 物理行以内，新增与修改函数均控制在 100 物理行以内，未触发大文件拆分。

## 验证与维护

- 前端与桥接回归（覆盖 9 工具与历史别名摘要提取、未声明秘密与正文排除、原位更新、中英切换与双主题）：

~~~powershell
node --test tests/chat-history.test.mjs tests/chat-timeline.test.mjs tests/chat-model.test.mjs tests/chat-ui.test.mjs tests/chat-receipt.test.mjs tests/workspace-plugin.test.mjs
~~~

- Go 单元、契约、SDK 预检与国际化全栈门禁：

~~~powershell
go test ./internal/workspace ./internal/conversation/... ./internal/executor ./internal/quickinput/... ./internal/app ./internal/i18n -count=1
go vet ./...
pwsh -NoProfile -File scripts/check-i18n.ps1 -Go go
~~~

- 锁定 OpenClaw + Node 运行时回归（设置 `ASTERHAND_CHAT_RUNTIME=1`、`ASTERHAND_CHAT_NODE` 与 `ASTERHAND_CHAT_ENTRY` 后运行）：

~~~powershell
go test ./internal/executor -run "^(TestWorkspacePluginSDKPreflight|TestNativeConversationToolTimelinePagination|TestNativeWorkspaceConcurrency|TestNativeWorkspaceTools)$" -count=1 -v -timeout=8m
~~~

- 验收重点：
  1. 已有历史会话与新触发的对话工具调用均显示 `<工具名> <目标摘要> · <状态>`，`edit_file` / `write_file` 不出现代码或文本正文，`apply_file_patch` 只列涉及文件，`memory_get` 显示目标记忆文件与行范围，`progress_card` 不重复拼摘要。
  2. 长路径自动折行且最多占 2 行，悬停显示完整单行摘要；中英切换与星核/霜蓝主题切换后状态译文和对比度正常。
  3. 仅在 Windows 目标环境中进行本机验证；跨节点、macOS/Linux 与真实双机未在此项单独扩展。
