# Web 维护入口

统一中文页面由 [embed.go](embed.go) 内嵌，无 npm 构建步骤，服务仅监听 loopback。同源、认证、接口和状态契约见 [Web 设计](../docs/web/design.md)。

| 模块 | 唯一所有者与职责 |
|---|---|
| [app.js](app.js) | 装配依赖、页面事件与启动，dispose 解除挂载监听 |
| [api-client.mjs](api-client.mjs) | 实例 CSRF、登录/会话、JSON/HTTP 错误与事件流创建；写入不重试 |
| [app-lifecycle.mjs](app-lifecycle.mjs) | 连接会话、失效代次、EventSource 与 20 秒节点刷新；pagehide 停止，缓存恢复重建一次 |
| [task-controller.mjs](task-controller.mjs) | 快照读取、提交互斥、pending 请求、答案 ID、任务动作与详情读取 |
| [task-view.mjs](task-view.mjs) | 任务 DOM、回答焦点和编辑保护、按文本渲染；沿用全量列表 |
| [view-model.mjs](view-model.mjs) | 纯排序、合并去重和发送状态计算 |
| [node-controller.mjs](node-controller.mjs) | 节点快照、显式目标、项目选择和配对事务 |
| [node-settings.mjs](node-settings.mjs) | 节点表单读取/保存与模型检查轮询；关闭/离开使迟到读取失效 |
| [desktop-settings.mjs](desktop-settings.mjs) | 独立桌面设置操作 ID、修订号、录制租约和轮询 |
| [ui.mjs](ui.mjs)、[browser-actions.mjs](browser-actions.mjs) | DOM 文本与通知计时器、现有 confirm/prompt/clipboard 窄依赖 |

视图通过回调发出业务操作，不创建 fetch/EventSource。节点选择通过只读快照供任务使用；没有全局 store 或事件总线。快捷输入的[独立桥](../internal/quickinput/assets/webview2-bridge.mjs)只依赖 WebView2，不导入主页面客户端。

新增模块须显式加入 embed；浮层模块另需更新原生资源白名单。Node 操作测试、Go 静态 import 闭包检查、独立构建及真实浏览器/WebView2 步骤见[维护性验证](../docs/development/features/web-ui-maintainability/validation.md)。
