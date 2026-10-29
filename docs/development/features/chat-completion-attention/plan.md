# 聊天发送稳定性与后台完成提醒

状态：发送回执适配与界面稳定性修复、后台完成提醒实现、相关工程验证及独立复审完成；本机用户验收通过。

## 范围与需求

修复发送期间临时提示插入消息区导致的布局跳动，并为后台聊天窗口增加回复完成后的任务栏提醒。关联 WEB-CHAT-06、WEB-CHAT-10、WEB-CHAT-13、INST-29；涉及 Web 展示、会话完成观察和 Windows 宿主。

正常发送继续立即显示本地消息，持久历史按稳定身份接替；保留拒绝、未知送达和运行错误信息。聊天界面移除提示词注入/应用状态，包括对应 DOM、文案和展示逻辑；底层继续观察与保存状态，未来 Web 诊断展示单独规划。

窗口被其他应用遮挡或最小化时，确认回复 completed 后请求 Windows 原生任务栏高亮闪烁，持续到窗口激活。前台完成不闪烁；取消、失败、unknown、重复核对与读取已完成历史不触发。显式隐藏没有任务栏入口，因此停止提醒且不自动重新显示。托盘图标、系统通知、声音、自定义图标逐帧动画、跨重启未读状态均不在范围内。

## 设计

- 聊天不展示提示词注入状态；常规发送/运行信息使用单行固定高度状态区，长文本省略且可通过标题查看。
- 从接受用户的发送操作开始，发送确认、正常准备和模型生成统一显示带动态点的“正在回复”；不显示持久接收、连接或准备等内部阶段。发送期间迟到的上一轮历史及新会话创建不得覆盖当前等待提示；当前轮次明确完成后清空。送达未知、拒绝、失败和保存异常继续提供需要处理的信息。
- OpenClaw 的成功发送响应可能先返回运行身份而没有历史消息序号。适配器在返回送达结果前，按原轮次身份只读查询原生持久接收记录或用户消息；确认后返回 accepted，避免将正常接收误报为 unknown 并在气泡内插入核对提示。查询失败或没有匹配证据仍保留 unknown，不重发消息，不以 started 文案推断持久接收。
- 会话服务已有的后台核对负责检测 accepted/completed 转换，并通过本地回调通知应用装配层。读取旧历史不补发提醒，不新增轮询或消息正文存储；保存状态独立呈现，运行完成可先于保存确认。
- 应用将完成通知交给快捷输入 Controller，发送仅父进程允许的 `reply_completed` IPC 消息到当前宿主。消息不携带正文、凭据、原生窗口参数或可执行操作。
- 原生 UI 线程检查窗口可见性和系统前台窗口；后台可见窗口（含最小化）调用 `FlashWindowEx` 的任务栏持续提醒模式。激活、显式隐藏及销毁停止。提醒不唤醒已暂停的 WebView、不恢复窗口、不抢焦点，节奏由 Windows 控制。
- 提醒属于当前宿主生命周期；宿主关闭、重建期间无可见窗口时不缓存或重放历史提醒。

## 验收与验证

| ID | 条件 | 结果与验证 |
|---|---|---|
| CA01 | 发送、等待接收、提示词待确认/降级/应用、首个回复前 | 本地消息只显示一次；各提示词状态均无聊天提示；状态区/消息视口高度稳定。前端阶段测试及真实 WebView2 布局测量 |
| CA02 | 后台可见或最小化时完成 | 任务栏持续高亮闪烁；窗口不激活、不恢复。服务回调、IPC 及原生调用测试，实体桌面核验视觉 |
| CA03 | 前台完成，已完成历史，重复核对，失败/取消/未知 | 无新提醒，回调只在完成转换触发 |
| CA04 | 提醒后激活、Esc/同键隐藏、关闭或宿主退出 | 停止闪烁，无残留窗口；后续另一轮可再次提醒 |
| CA05 | 窗口隐藏/最小化，前端暂停轮询 | 服务仍可观察完成，通知不依赖前端计时器，不重放提交 |
| CA06 | OpenClaw 成功发送但未返回消息序号，模型尚未输出 | 用锁定运行时和暂停响应的本地模型服务验证：同轮次持久回执可确认接收，历史尚未出现用户消息也不显示虚假的核对提示；无证据或读失败仍为 unknown |

执行相关前端、conversation/quickinput/hostipc/app 测试与构建，使用独立 quality-audit 检查影响链并复审必要修复。桌面测试使用 `ASTERHAND_CHAT_GATE=1` 和现有非提权测试入口；具体构建命令见[此前桌面体验验证](../chat-desktop-experience/validation.md)。实体任务栏呼吸效果受 Windows 主题、动画和任务栏设置影响，自动化的原生调用检查与用户视觉验收分别记录。日志放忽略输出目录，长期文档保留可复现测试入口。

### 可复现入口

- [完成转换测试](../../../../internal/conversation/completion_test.go)：前端暂停、失败/取消/未知、历史读取和保存失败重试。
- [真实运行时发送回执测试](../../../../internal/executor/conversation_send_test.go)：设置 `ASTERHAND_CHAT_RUNTIME=1`、`ASTERHAND_CHAT_NODE` 和 `ASTERHAND_CHAT_ENTRY` 指向锁定 Node/OpenClaw，运行 `go test ./internal/executor -run '^TestNativeConversationSendBeforeReply$' -count=1 -timeout=120s -v`；本地模拟模型暂停正文输出，无需真实模型凭据。可设置 `ASTERHAND_CHAT_EVIDENCE` 为短路径临时目录保存隔离运行时证据。
- [父进程投递测试](../../../../internal/quickinput/completion_test.go)、[协议测试](../../../../internal/quickinput/hostipc/protocol_test.go)：仅父方向、无载荷、队列满与关闭。
- [Windows 提醒及布局测试](../../../../internal/quickinput/surface_attention_windows_test.go)与[发送夹具](../../../../tests/fixtures/chat-send-stability.js)：真实生产 DOM/CSS 的五阶段高度、单气泡、后台/最小化提醒与激活/隐藏清理。

使用符合依赖清单的 Go/Node，在仓库根目录运行：

```powershell
go test ./internal/conversation ./internal/quickinput/... ./internal/app -count=1 -timeout=120s
$env:ASTERHAND_CHAT_GATE = "1"
go test ./internal/quickinput -run "^TestNativeChat(SendStability|CompletionAttention)$" -count=1 -timeout=150s -v
Remove-Item Env:ASTERHAND_CHAT_GATE
```

普通测试中的桌面场景默认 skipped；原生场景需要交互桌面和安装好的 WebView2。真实前台完成、物理任务栏点击和实际动画属于手动验收范围，自动化调用检查不替代视觉检查。可选通知在父进程和宿主 STA 两处有界队列满时都直接丢弃，不因提醒失败退出宿主。

## 规模与边界

提醒逻辑独立为小模块，不拆分已有大文件。沿用 chatPanel 装配闭包的内聚例外，详见[聊天桌面体验计划](../chat-desktop-experience/plan.md)。实现、工程验证、实体用户验收、本地提交及发布分别记录。
