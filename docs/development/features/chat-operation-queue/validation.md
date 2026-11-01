# 聊天操作队列验证

## 自动化覆盖

使用依赖清单锁定的 Go、Node.js 与 OpenClaw，在 Windows 上运行：

```powershell
go test ./...
go vet ./...
go build ./...
$uiTests = Get-ChildItem -LiteralPath tests -Filter '*.test.mjs' -File
node --test $uiTests.FullName
node scripts/verify-frontend-assets.mjs
```

| 验证目标 | 源码入口 |
|---|---|
| 冷启动先登记、跨会话 FIFO、创建依赖、单生成槽 | [节点队列测试](../../../../internal/conversation/queue_test.go) |
| 队满保留、操作 ID 去重、停止回复后继续派发、配置/退出准入 | [节点队列测试](../../../../internal/conversation/queue_test.go) |
| 明确失败自动离队、近期失败恢复、元数据失败只重试记录、失败后新消息正常发送 | [失败处理测试](../../../../internal/conversation/queue_failures_test.go) |
| 未派发正文不写入目录，重启不续发；未知派发只核对，确认停止后释放调度并保留未知事实 | [节点队列测试](../../../../internal/conversation/queue_test.go) |
| 最大转义文本逐条恢复不超过 128 KiB IPC 帧 | [IPC 队列测试](../../../../internal/quickinput/hostipc/queue_test.go) |
| 入队回执丢失、宿主重建和新编辑修订保护 | [宿主回执测试](../../../../internal/quickinput/chat_test.go) |
| 队列修订单调、重建恢复正文、轮询间完成补读、不重放发送 | [队列视图测试](../../../../tests/chat-queue.test.mjs) |
| 新建期间连续发送、共用气泡与等待提示、失败只显示异常、停止回复无全局暂停、切换不等待历史 | [面板交互测试](../../../../tests/chat-ui.test.mjs) |
| 初始化、排队、派发和生成保持相同回复动画，隐藏与异常停止动画 | [回复状态测试](../../../../tests/chat-status.test.mjs) |

## 固定原生运行时

先按[依赖维护流程](../../../../packaging/upgrade.md)准备固定依赖。`ASTERHAND_CHAT_NODE` 和 `ASTERHAND_CHAT_ENTRY` 分别设置为该依赖目录中的 Node 可执行文件与 OpenClaw 入口绝对路径：

```powershell
$env:ASTERHAND_CHAT_RUNTIME = '1'
go test ./internal/executor -run '^TestNativeConversation(QueuedStartupAndHistory|StopReplyContinuesQueue)$' -count=1 -v -timeout 5m
```

[集成测试](../../../../internal/executor/conversation_queue_integration_test.go)在隔离目录启动固定 OpenClaw，用本机受控模型服务完成三条排队消息和两个原生会话。检查启动前节点托管确认、顺序执行、上下文连续性、接收和完成回执、原生历史数量。模型选择为测试适配，队列、启动所有权、桥接、会话与历史使用生产实现；不使用用户凭据、不产生外部模型费用。普通 `go test` 跳过此显式测试。

[停止回复专项](../../../../internal/executor/conversation_stop_queue_integration_test.go)阻塞第一轮模型输出，在确认接收后请求停止，验证第二条已提交消息自动执行；原生历史保留两条用户消息、第二轮正常回复，模型调用计数不重复。

真实 Windows WebView2 验证设置 `ASTERHAND_CHAT_GATE=1`，运行 `go test ./internal/quickinput -run '^TestNativeChat(SendIntent|PanelWorkflow|SendStability)$' -count=1 -timeout=150s -v`。[发送语义专项](../../../../internal/quickinput/surface_send_intent_windows_test.go)使用[受控传输夹具](../../../../tests/fixtures/chat-send-intent.js)验证真实 DOM、按钮、气泡错误和停止后的新发送；设置 `ASTERHAND_COMPOSER_CAPTURE` 可保存深浅主题截图。传输为测试替身，真实原生调用由上述 OpenClaw 专项单独验证。

## 桌面验收边界

正常聊天、启动期间发送和停止回复后的后续发送已通过本机用户验收。以下步骤作为后续回归清单；故障注入、宿主重建和配置竞争仍按对应场景单独验证：

1. 退出后快速重启，在运行时启动期间连续发送两条消息；立即出现普通消息气泡和“正在回复”，与运行时已连接后的发送体验一致。全程不显示待发数量、准备阶段、取回编辑或取消发送按钮，后续按顺序回复。
2. 创建新对话后立即发送，再切回旧对话发送；消息始终属于提交时的会话，旧历史迟到不切换目标。
3. 在隔离节点模拟真实启动失败，确认对应消息显示“消息未送达”，没有撤回、取回、继续发送按钮；近期失败正文可在宿主重建后恢复。
4. 准备和排队期间不显示停止按钮；接收后的模型轮次显示“停止回复”。点击后当前回复停止、用户消息保留，后续消息自动继续，新发送正常处理。
5. 隐藏和重新显示、重建 UI 宿主后检查待发消息；主进程退出确认明确说明丢弃未发送内容。
6. 待发队列非空时修改模型配置被拒绝，主题仍可修改；明确失败离队后可修复配置，主动发送新消息不会重发之前失败的消息。

未知派发故障测试使用隔离节点，确认应用只对账，不自动重新提交。在线服务异常、实体 WebView2 焦点/输入法、多屏及快速退出重启的专项覆盖独立于日常聊天验收。
