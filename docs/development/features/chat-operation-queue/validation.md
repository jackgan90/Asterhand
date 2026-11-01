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
| 队满保留、操作 ID 去重、取消与准备竞争、暂停/继续、配置/退出准入 | [节点队列测试](../../../../internal/conversation/queue_test.go) |
| 未派发正文不写入目录，重启不续发；未知派发只核对，移除后仍保留未知事实 | [节点队列测试](../../../../internal/conversation/queue_test.go) |
| 最大转义文本逐条恢复不超过 128 KiB IPC 帧 | [IPC 队列测试](../../../../internal/quickinput/hostipc/queue_test.go) |
| 入队回执丢失、宿主重建和新编辑修订保护 | [宿主回执测试](../../../../internal/quickinput/chat_test.go) |
| 队列修订单调、重建恢复正文、轮询间完成补读、不重放发送 | [队列视图测试](../../../../tests/chat-queue.test.mjs) |
| 新建期间连续发送、正常阶段共用气泡与等待提示、失败后才出现恢复入口、切换不等待历史 | [面板交互测试](../../../../tests/chat-ui.test.mjs) |
| 初始化、排队、派发和生成保持相同回复动画，隐藏与异常停止动画 | [回复状态测试](../../../../tests/chat-status.test.mjs) |

## 固定原生运行时

先按[依赖维护流程](../../../../packaging/upgrade.md)准备固定依赖。`ASTERHAND_CHAT_NODE` 和 `ASTERHAND_CHAT_ENTRY` 分别设置为该依赖目录中的 Node 可执行文件与 OpenClaw 入口绝对路径：

```powershell
$env:ASTERHAND_CHAT_RUNTIME = '1'
go test ./internal/executor -run '^TestNativeConversationQueuedStartupAndHistory$' -count=1 -v -timeout 5m
```

[集成测试](../../../../internal/executor/conversation_queue_integration_test.go)在隔离目录启动固定 OpenClaw，用本机受控模型服务完成三条排队消息和两个原生会话。检查启动前节点托管确认、顺序执行、上下文连续性、接收和完成回执、原生历史数量。模型选择为测试适配，队列、启动所有权、桥接、会话与历史使用生产实现；不使用用户凭据、不产生外部模型费用。普通 `go test` 跳过此显式测试。

## 桌面验收边界

本机安装后的正常聊天和启动期间发送体验已通过用户验收。以下步骤用于后续回归；故障注入、宿主重建和配置竞争仍按对应场景单独验证：

1. 退出后快速重启，在运行时启动期间连续发送两条消息；立即出现普通消息气泡和“正在回复”，与运行时已连接后的发送体验一致。全程不显示待发数量、准备阶段、取回编辑或取消发送按钮，后续按顺序回复。
2. 创建新对话后立即发送，再切回旧对话发送；消息始终属于提交时的会话，旧历史迟到不切换目标。
3. 在隔离节点模拟真实启动失败，确认等待动画结束并显示异常提示，失败后才出现必要恢复入口；取回原文时不覆盖输入框中的新草稿。
4. 点击停止，确认当前轮次停止后后续消息仍暂停，点击“继续发送”才继续。
5. 隐藏和重新显示、重建 UI 宿主后检查待发消息；主进程退出确认明确说明丢弃未发送内容。
6. 队列非空时修改模型配置被拒绝，主题仍可修改；启动失败时可取回/取消消息，取消失败创建后可修复配置。

未知派发故障测试使用隔离节点，确认应用只对账，不自动重新提交。在线服务异常、实体 WebView2 焦点/输入法、多屏及快速退出重启的专项覆盖独立于日常聊天验收。
