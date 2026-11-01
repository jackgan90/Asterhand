# 聊天连接状态

## 范围与契约

关联 [Web 需求 WEB-CHAT-15](../../../web/requirements.md) 及[会话设计](../../../assistant/conversations.md)。对话 ID 后固定显示连接中、已连接、无法连接；分别使用黄色、绿色、红色文字。失败原因留给后续独立诊断界面，本次不展示原因，也不新增诊断 UI。

展开聊天时开始连接本机 Agent，复用已有共享启动。已连接表示配置齐全且 Agent 握手成功；模型凭据只在实际聊天时验证，不为标签发送模型请求。缺少凭据、启动失败、断线或实际聊天暴露的模型失败统一显示无法连接。重连归入连接中，排队、回复和停止回复不改变连接语义。

## 实现与验证

- 公共会话协议增加封闭的连接及只读连接状态操作；不传运行时地址、凭据或异常详情。
- 状态读取只查看配置、进程和桥内连接快照，不触发模型、创建会话或重发消息；启动使用原共享生命周期。
- 前端状态独立于发送按钮和回复动画；展开且可见时定时刷新，隐藏/折叠/销毁停止，恢复时重新读取。
- 自动测试覆盖冷启动、配置缺失、断线/重连、实际失败后恢复，以及标签、选择切换、隐藏和无模型调用。实际桌面颜色和安装体验由用户验收。
- 验证命令：`go test ./internal/conversation/... ./internal/executor ./internal/quickinput/...`；`node --test tests/chat-connection.test.mjs tests/chat-ui.test.mjs`。

状态：实现、工程验证及本机桌面用户验收通过。验收覆盖对话 ID 后的三态提示及正常聊天体验；故障注入与跨环境专项继续按下述验证入口执行。

### 验证入口与边界

- [运行时连接测试](../../../../internal/executor/conversation_connection_test.go) 覆盖共享启动、配置变化、缺少凭据、失联和失败后只读轮询不重启。
- [桥状态测试](../../../../tests/chat-connection.test.mjs) 和[界面测试](../../../../tests/chat-ui.test.mjs) 覆盖三种文字、空会话、迟到回复、可见性和消息状态隔离。
- [原生连接测试](../../../../internal/executor/conversation_connection_integration_test.go) 使用固定 Node/OpenClaw 和本机 HTTP 模型模拟服务；连接与快照读取的模型调用计数必须为零，用户发送收到 401 后必须显示 unavailable，后续状态读取不再次请求模型。设置 `ASTERHAND_CHAT_RUNTIME=1`、`ASTERHAND_CHAT_NODE=<固定 node 可执行文件>`、`ASTERHAND_CHAT_ENTRY=<固定 openclaw.mjs>`，运行 `go test ./internal/executor -run '^TestNativeConversationConnectionWithoutModelProbe$' -count=1 -v -timeout 5m`。
- 相关 Go 回归、前端回归、静态检查、Windows 构建和文档链接检查通过。原生测试验证本机可控 401 和零额外模型请求，不使用真实在线模型凭据。
- 人工验收：安装当前源码后，在两种主题中检查对话 ID 后的文字、颜色和窄窗口布局；分别打开空会话、等待冷启动、缺少凭据时打开和正常发送。隐藏后恢复状态应更新，停止回复和切换会话不得变成第四种连接状态。

## 规模评估

连接生命周期、前端状态视图按职责新增窄模块；修改文件均保持 1000 行以内，无超限拆分。
