# 聊天历史顺序与分页修复

状态：实现、工程验证及本机用户验收完成。涉及助手会话、OpenClaw 适配、桌面聊天展示及 CLI/MCP 历史读取。

## 范围与需求

对应 AST-CHAT-02、AST-CHAT-08、AST-TOOLS-04、WEB-TOOLS-03。修复工具调用形成空消息、工具活动固定堆在消息末尾，以及静止会话向前翻页错误重置。既有原生历史继续读取，不迁移或重写正文。

文本与工具调用按原生消息顺序及消息内内容顺序显示；工具状态变化更新原位置。隐藏推理、工具参数和工具输出不作为聊天正文公开。只有工具调用的记录直接显示工具项，不生成空的助手气泡。工具执行、权限与发送队列保持现有规则。

## 设计

- 原生适配器把可见文本段与工具调用转换为同一消息内有序的内容块；混合消息的文本段使用稳定的片段身份，完整文本读取保持同一段的边界。纯文本消息保留原身份。同一 transcript ID 的相邻 commentary/工具行合并；页首只剩工具行时，有界补读原消息，保留页边界的文字。
- 工具调用按稳定调用身份读取执行结果；原生结果及节点已有执行记录用于状态显示。状态刷新不移动工具项，缺少确认依据时明确显示未知。进度仍标为助手声明。
- 历史按原生 `nextOffset` 前进。`deltaCursor` 仅用于最新历史快照的比较，不要求较早页携带该字段。向前翻页在读取前后校验最新快照，实际变化时返回重置；缺少工具结果或没有可见文本不阻断偏移前进。
- 界面、CLI 与 MCP 的 `messages.list` 使用同一有界历史结果；`messages.get` 支持对应文本段的完整分片读取。保留当前正文缓存、消息数和响应体限制。

权威说明见[持续会话设计](../../../assistant/conversations.md)与[本机工具能力](../../../assistant/runtime-capabilities.md)。

## 验收与验证

- 文本、工具、文本、工具、最终答复按发生顺序显示；工具失败、完成和状态刷新保持位置，无空气泡和独立尾部工具列表。
- 超过一页的工具会话能够读取到最初用户消息，连续翻页无遗漏或重复；停止聊天后不反复提示历史变化。
- 真正追加消息、历史重置和切换会话仍按既有保护规则处理，迟到结果不会覆盖当前会话。
- 混合消息中的长文本完整读取后不吞掉、重排或重复工具项；大记录保持有界。
- 前端模型与 DOM 测试、Go 公共历史与状态补充测试、锁定 OpenClaw 加本地模型 fixture 的跨页回归、实际浏览器显示检查；同时检查国际化、自动化覆盖和文档链接。

## 状态

- 实现与工程验证：完成；Windows 固定 OpenClaw 与本地 SSE 模型验证跨页、32 次工具调用、文本段读取及重启恢复；前端模型/DOM、Go 历史与自动化读取、浏览器中英文和双主题显示检查通过。
- 用户验收：本机已通过，覆盖文字与工具按发生顺序显示、消除空助手气泡，以及向前翻页读取最初消息。

## 验证入口与边界

[历史适配测试](../../../../tests/chat-history.test.mjs)覆盖较早页缺少 `deltaCursor`、真实历史变化、隐藏记录推进偏移、同 ID 展示行合并、页边界补读和文本段的 UTF-8 完整读取。[有序渲染测试](../../../../tests/chat-timeline.test.mjs)与[聊天交互测试](../../../../tests/chat-ui.test.mjs)覆盖原位状态更新、队列快照、旧页阅读、语言切换和混合长文本。

[Go 状态补充测试](../../../../internal/executor/conversation_history_windows_test.go)检查会话/轮次/工具匹配、失败优先和响应预算；[公共读取测试](../../../../internal/app/automation_message_preview_test.go)通过共享自动化注册验证相同顺序、文本段全文和会话授权。原始工具入参、输出与私有日志不进入公开内容。

~~~powershell
node --test tests/chat-history.test.mjs tests/chat-timeline.test.mjs tests/chat-model.test.mjs tests/chat-ui.test.mjs
go test ./internal/app ./internal/executor ./internal/conversation/... -count=1
go vet ./internal/app ./internal/executor ./internal/conversation/...
pwsh -NoProfile -File scripts/check-i18n.ps1
~~~

[原生分页回归](../../../../internal/executor/conversation_history_integration_test.go)使用独立 state/workspace 与本地模型服务，先保存招呼，再交替产生文字和 32 次工具调用，完整读取所有页与文本段，重启后比较历史，断言只读过程没有模型请求。按[持续会话测试前置](../local-node-chat/validation.md#自动化入口与覆盖边界)设置固定运行时的 `ASTERHAND_CHAT_RUNTIME`、`ASTERHAND_CHAT_NODE` 与 `ASTERHAND_CHAT_ENTRY` 后执行：

~~~powershell
go test ./internal/executor -run TestNativeConversationToolTimelinePagination -count=1 -v -timeout=8m
~~~

浏览器检查复用快捷输入的实际 HTML/CSS 和 `chatPanel`，覆盖文字/工具混排、工具独占行、向前翻页、长文本扩展、中英文与深浅主题。本次修复的本机用户工作流已通过验收；跨设备、其他运行时及完整模型故障矩阵不在该验收范围内。

## 规模评估

新增适配分别放在 `history.mjs`（分页读取）、`timeline.mjs`（内容映射）和 `conversation_history.go`（状态补充），未新增超过 1000 行的手写文件。既有 `chatPanel` 仍是超过 100 行的界面控制函数，本次只调整其历史刷新条件与文本段定位；选择代次、发送队列和旧页保护共享其局部状态，单独抽出这些条件会增加跨模块状态传递，本次保留。词表生成文件按现有生成流程维护，不手动拆分。
