# 原生提示词保留验证

关联[实施计划](plan.md)、[会话设计](../../../assistant/conversations.md)及[决策](../../../assistant/adr/007-preserve-native-harness-prompts.md)。验证区分原生提示词与工具的实际请求、模型行为和安装后用户体验。

## 入口与前提

从仓库根目录使用[锁定依赖](../../../../packaging/dependencies.json)执行。Windows 原生集成需要可创建本地进程、监听 loopback 和准备运行时临时目录；普通 Go 回归的目录别名用例需要 junction 能力。原生测试只访问临时节点和本地 HTTP 模型夹具，不读取密钥或修改已安装节点。

~~~powershell
go test ./... -count=1 -timeout=5m
go vet ./...
$uiTests = Get-ChildItem -LiteralPath tests -Filter '*.test.mjs' -File
node --test $uiTests.FullName
$env:ASTERHAND_CHAT_RUNTIME = '1'
$env:ASTERHAND_CHAT_NODE = '<locked-node.exe>'
$env:ASTERHAND_CHAT_ENTRY = '<locked-openclaw-directory>\openclaw.mjs'
go test ./internal/executor -run '^TestNativeConversation(Bridge|PromptModes)$' -count=1 -v -timeout=6m
~~~

## 断言

| 源码 | 验证内容 |
|---|---|
| [产品计划](../../../../internal/prompt/plan_test.go) | v2 差异规则不强制重命名/统一语气，作用范围与有效能力明确；模式与实际应用状态继续分离 |
| [适配配置](../../../../internal/executor/conversation_prompt_test.go) | 默认 append，原生 bootstrap 开启但工作区仍隔离，工具/工具搜索/Code Mode 继续关闭 |
| [插件](../../../../tests/chat-prompt.test.mjs) | 默认补充不返回 systemPrompt；实际输入确认、元数据限额与观察失效；append 成功无警告，旧 replace/v1 兼容可识别 |
| [原生两轮](../../../../internal/executor/conversation_integration_test.go) | 原生核心与默认 SOUL 内容进入实际模型请求；下一轮加载自定义 IDENTITY/SOUL；产品补充同时生效且工具数为零；两轮上下文保留、重启不覆盖定制角色文件 |
| [原生降级](../../../../internal/executor/conversation_prompt_integration_test.go) | unsupported、策略失效与 append 都保留原生设定并完成聊天，模式与应用状态准确，无工具泄漏 |

上游 [系统提示词组装](https://docs.openclaw.ai/concepts/system-prompt)和[提示词钩子](https://docs.openclaw.ai/plugins/hooks/prompt-and-session)提供机制说明；锁定包内同名文档与实际请求测试决定本版本支持范围。工具定义来自运行时策略，提示词只描述能力，不构成授权。

## 验证边界

本地模型夹具证明实际输入内容、工具隔离和生命周期，不证明所有真实模型都会遵循补充规则。真实模型可以复用[两轮验证](../../../../internal/executor/conversation_live_test.go)，须按既有 opt-in 和次数预算执行；当轮状态应为 append/applied。真实发送、历史查询、新对话、展开/收起已通过[日常聊天用户验收](../local-node-chat/validation.md#用户验收状态)；角色表达和长期对话专项仍按各自证据单列。

每次验证记录命令、版本、源码指纹、状态与原始日志，保存在忽略目录或版本发布物中。原生默认模板由锁定 OpenClaw 初始化，升级时重新验证关键段落和定制文件保留，不把旧运行结果作为新版本证据。
