# 提示词与聊天交互验证

关联[实施计划](plan.md)、[会话设计](../../../assistant/conversations.md)和[可选提示词 ADR](../../../assistant/adr/006-optional-conversation-prompt-policy.md)。源码、工程验证、真实模型和实体桌面验收分别记录。

## 环境与命令

从仓库根目录执行，按[依赖清单](../../../../packaging/dependencies.json)准备锁定 Go、Node、OpenClaw/provider；原生桌面需要 Windows x64 交互桌面及 WebView2。普通回归不调用模型，产物写入独立忽略目录。

~~~powershell
go test ./... -count=1 -timeout=5m
go vet ./...
$uiTests = Get-ChildItem -LiteralPath tests -Filter '*.test.mjs' -File
node --test $uiTests.FullName
$env:ASTERHAND_CHAT_RUNTIME = '1'
$env:ASTERHAND_CHAT_NODE = '<locked-node.exe>'
$env:ASTERHAND_CHAT_ENTRY = '<locked-openclaw-directory>\openclaw.mjs'
go test ./internal/executor -run '^TestNativeConversation' -count=1 -v -timeout=6m
$env:ASTERHAND_CHAT_GATE = '1'
go test ./internal/quickinput -run '^TestNativeChatPanelWorkflow$' -count=1 -v -timeout=3m
~~~

测试以临时节点、原生 state/workspace 和本地 HTTP 模型夹具启动，不修改已安装节点或第三方源码。插件 source capture 使用真实 Windows 文件系统能力；深目录用例核对扩展长度 TEMP/TMP，不通过缩短失败路径宣称修复；缺少权限或桌面条件记为 blocked/skipped，不能替换为模拟通过。

## 自动化覆盖

| 来源 | 验证目的 |
|---|---|
| [通用提示词测试](../../../../internal/prompt/plan_test.go)、[会话提示词状态](../../../../internal/conversation/prompt_test.go) | 能力事实/模型事实与模板组装、版本变化、replace/append/unsupported、pending/applied/degraded、重启后已确认状态与终态未确认降级 |
| [适配器测试](../../../../internal/executor/conversation_prompt_test.go)、[插件测试](../../../../tests/chat-prompt.test.mjs) | 独立受控策略文件、配置白名单与失效、实际输入确认、run/session/instance 隔离、策略被修改或只读观察查询失败时撤销当前确认，插件元数据最多保留 16 轮 |
| [原生 Gateway](../../../../internal/executor/conversation_integration_test.go)、[模式测试](../../../../internal/executor/conversation_prompt_integration_test.go) | 实际出站原生系统提示词、SOUL/IDENTITY 与产品补充、最终零工具、两轮上下文、无 key 重启；不支持、策略失效和 append 都继续聊天；短正文与隐藏长字段不出现全文按钮 |
| [UI 状态](../../../../tests/chat-ui.test.mjs)、[消息模型](../../../../tests/chat-model.test.mjs) | 冷连接/创建/延迟发送立即出现气泡并清空输入区，明确失败恢复未修改草稿、未知送达保持核对、新编辑优先于迟到确认/失败，稳定 DOM 身份、重复正文按 ID 区分、面板与气泡共用状态新旧规则，迟到历史不倒退终态或节点忙状态、历史合并与有界缓存 |
| [编辑器桥接](../../../../tests/quick-input-bridge.test.mjs) | 发送后可见草稿为空，主进程快照仍持有待确认正文；确认释放副本，失败按操作恢复，再次输入相同正文也递增修订，IME 与超限编辑保持原有保护 |
| [按键测试](../../../../tests/quick-input.test.mjs)、[真实 WebView2](../../../../internal/quickinput/surface_chat_windows_test.go) | Enter/Ctrl+Enter 发送、Shift+Enter 默认换行、IME 组合/结束保护、长按防重发、Esc 隐藏；生产页面内合成按键与桥接工作流；收起态首次发送自动展开，回执未释放时输入区已为空且主进程恢复副本完整 |
| [正文投影](../../../../tests/chat-model.test.mjs) | 非展示字段不冒充正文截断，多文本块与边界歧义按需探测，Unicode/长正文/超限保持分片及窗口预算 |

原生适配器既观察出站模型请求，也核对每轮应用元数据；支持声明、插件回调执行与实际提示词应用是不同检查点。当前默认采用补充模式，验证原生核心/角色文件与完整差异规则同时存在且工具列表为空；角色保留与重启场景见[原生提示词验证](../native-harness-prompt-preservation/validation.md)。replace 只保留兼容能力。

## 真实模型与人工验收

真实模型遵循[持续聊天验证](../local-node-chat/validation.md)的显式 opt-in、凭据隔离与次数预算，运行 TestRealConversationTwoTurns：最多提交两轮，每轮 45 秒，验证实际输入确认 applied/append、上下文口令及移除临时 key 后重启历史。凭据只从指定测试节点读取，不输出密钥，不更改源节点。

实体中文 IME 必须人工验证组合选词、刚结束组合时 Enter、Shift+Enter 多行和 Esc。合成 KeyboardEvent 验证程序分支，不能替代真实输入法产生的事件顺序。人工同时核对短正文无全文按钮、长正文可完整读取、点击发送立即显示气泡并清空输入、失败恢复未修改草稿、成功无多余标签、跨应用焦点与复制。

多屏/DPI/RDP、实际安装升级、并行真实文件任务和既有隐藏 CPU 预算沿用前功能的独立验收与未通过状态；本追加功能不将这些项目标记通过。保存实际命令、版本、源码指纹、每层结果及原始日志；失败排查记录与最终通过记录同时保留。
