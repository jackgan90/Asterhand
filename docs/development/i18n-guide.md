# 运行期国际化开发指南

状态：生效。本文规定运行期多语言文案的词条维护、代码写法、错误封装、动态刷新与静态守卫操作方法；架构决策与权威边界见 [Web 设计](../web/design.md) 与 [ADR 002](../web/adr/002-runtime-internationalization-framework.md)。

## 1. 新增词条与生成流程

1. **单一权威词表**：仅手工编辑 [zh-CN.json](../../internal/i18n/locales/zh-CN.json) 与 [en-US.json](../../internal/i18n/locales/en-US.json)；语种元数据与前端打包前缀在 [manifest.json](../../internal/i18n/locales/manifest.json) 维护。
2. **命名空间前缀**：
   - `common.*` / `brand.*`：跨端共用基础词条与品牌词条（如 `zh-CN` 为 `Asterhand · 星伴`，`en-US` 为纯 `Asterhand`）；
   - `web.*`：Web 管理控制台词条；
   - `quickinput.*`：快捷输入与持续聊天浮层词条；
   - `desktop.*`：Windows 托盘、启动器、原生弹窗与脚本选择器词条；
   - `status.*` / `err.*`：前后端共享的公开状态说明与错误文案。
3. **占位符规范**：统一采用 `{param}` 命名参数（如 `{count}`、`{name}`），所有语种同一键的占位符集合必须完全一致。
4. **重新生成与自检**：修改词表后执行以下命令同步只读前端模块并运行全栈检查：

```powershell
node ./scripts/sync-i18n.mjs
pwsh -NoProfile -File ./scripts/check-i18n.ps1
```

两份生成模块（`web/i18n-catalog.mjs` 与 `internal/quickinput/assets/i18n-catalog.mjs`）禁止手工编辑。

## 2. HTML、JavaScript 与 Go 参数化标准用法

### HTML 静态模板

在 `web/index.html` 与 `internal/quickinput/assets/index.html` 中使用 `data-i18n*` 声明绑定，由运行期通过 `textContent` / `setAttribute` 安全注入，禁止使用 `innerHTML` 拼接翻译文本：

```html
<h2 data-i18n="web.settings.title">设置</h2>
<input
  type="text"
  placeholder="描述你想完成的事…"
  data-i18n-placeholder="web.composer.placeholder"
  aria-label="任务内容"
  data-i18n-aria-label="web.composer.goal_aria"
/>
<button title="关闭" data-i18n-title="web.common.close">×</button>
```

### 前端与桥接 JavaScript

- 直接与带参翻译：`t("web.tasks.last_refresh", {time: clockText})`（快捷输入资源中使用对应的 `t(...)`）。
- 显式映射字典：对象值必须填写完整的词表键名，再将键传给 `t(...)`，不得在字典中写死中文或未登记假键：

```js
const stateKeys = {
  queued: "web.task.state.queued",
  running: "web.task.state.running",
};
badge.textContent = stateKeys[job.state] ? t(stateKeys[job.state]) : job.state;
```

- 展示后端结构化消息或异常：优先使用 `localizeMessage(item.error_key, item.error_args, item.error || "")` 与 `localizeError(error)`；桥接层构造协议错误时保留稳定机器码并附带真实 `message_key` 与英文兜底文本（如 `failure("unavailable", "err.chatbridge.binding_unconfirmed", "Conversation binding is not yet confirmed.")`）。

### Go 后端与桌面原生界面

按当前有效语言（HTTP 请求经 `requestLocale(r)` 解析，桌面宿主经控制器当前有效语言传入）调用 `i18n.T`：

```go
label := i18n.T(locale, "desktop.tray.title_with_name", map[string]string{
    "name": displayName,
})
```

## 3. 保留业务错误类型与持久化契约

1. **保留业务错误类型与链式判定**：
   - 纯展示错误可用 `i18n.Err("err.app.name_required")` 或 `i18n.ErrArgs("err.llm.model_unsupported", map[string]string{"model": model})`（见 [errors.go](../../internal/i18n/errors.go)）。
   - 当底层错误承担重试、排队、冲突或拒绝凭据等业务语义（如 `conversation.ErrBusy`、热键冲突错误、`TaskRejection`）时，**必须使用 `i18n.Wrap("err.conv.busy_draft_kept", nil, conversation.ErrBusy)` 包装底层错误**，或让自定义错误类型实现 `I18nKey() (string, map[string]string)`。`i18n.Wrap` 实现 `Unwrap() error`，保证 `errors.Is` / `errors.As` 行为不变。
2. **持久化与跨节点传输（延迟本地化）**：
   - `Job`、`Outbox`、`wire.Error` 及桌面状态快照不得只存写死某一种语言的字符串；应持久化可选 `error_key` + `error_args`（或传输 `message_key` + `message_args`），同时写入英文或兼容兜底 `error` / `message` 字段。
   - 展示边界（API 输出或前端渲染）使用 `i18n.LocalizeMessage(locale, key, args, fallback)` / `localizeMessage(...)` 按查看者当前语言实时解析；遇到历史旧记录（无 `error_key`）或未知新键时自动回退兜底文本，且不得把动态语言文本混入 `TaskRejection` 签名摘要。

## 4. 动态视图无损刷新（`relocalize()`）要求

每个动态组件必须提供 `relocalize()` 钩子并在语言变化时**原位更新文案与属性**，遵守以下红线：

1. **禁止整棵销毁重建活跃交互控件**：任务问答表单、设置表单、本地命令编辑区在切换语言时必须保留原有 `<form>`、`<input>` / `<textarea>` 与 `<button>` DOM 节点，不得丢失未提交草稿、焦点与光标选区、IME 组合输入态，或正在提交期间的 `button.disabled` 防重入状态。
2. **禁止重跑高开销正文排版**：快捷输入聊天记录切换语言时仅原位更新角色标签、时间、送达提示与复制/展开按钮文本，保留已渲染的 `.chat-message-body` Markdown DOM、KaTeX `<math>` 节点及列表滚动位置。
3. **默认名称标志保护**：内置默认节点名（`name_default: true`）与默认工作区名（`name_custom: false`）在界面按当前语言展示译名，但用户在设置表单中未手动编辑名称字段时，提交保存必须保留原持久化值与标志位，防止保存模型或目录配置时误把译名固化为用户自定义名称。
4. **并发与跨标签页权威**：桌面状态读取必须按单调请求序号丢弃迟到旧快照；登录前临时语言仅保存在当前标签页 `sessionStorage`，不受其他标签页 `localStorage` 事件覆盖；已登录标签页收到缓存变更时向服务端对账权威状态。

## 5. 守卫配置与豁免登记方法

静态守卫规则集中定义于 [guard-rules.json](../../internal/i18n/guard-rules.json)，同时驱动 [sync-i18n.mjs](../../scripts/sync-i18n.mjs) 与 [guard_test.go](../../internal/i18n/guard_test.go)：

- **登记新 UI 输出出口**：
  - 前端新增提示函数、DOM 构造器或属性赋值点时，将其加入 `js_ui_sinks`（支持 `"toastFn"` 简写，或 `{"kind": "call" | "property" | "attribute_call", "name": "...", ...}` 结构化配置）；
  - Go 桌面层新增菜单、原生弹窗或 Win32 文本 API 封装时，将其加入 `go_desktop_ui_sinks`（支持 `"funcName"`、`"receiver.Method"` 简写，或 `{"kind": "func" | "selector" | "win32_proc", ...}` 结构化配置）。配置更新后扫描器自动拦截向新出口直接传入的自然语言字面量。
- **登记必要豁免**：
  - HTML 模板中的纯符号、按键名或格式示例（如 `×`、`Ctrl`、`100.x.x.x:17433`）加入 `html_allowed_literals`；
  - Go 源码中确需保留中文的历史兼容常量或内联模型提示词，必须在 `go_symbol_exemptions` 按精确 `file` + `symbol` 登记并填写具体 `reason`，禁止将整个业务包移出 `go_checked_dirs`。

## 6. 不翻译范围与静态守卫盲区

### 明确不翻译的文本

1. **安装、打包与工程维护脚本**：`install.ps1`、`install.sh`、`install-local.cmd`、`packaging/*.ps1`、`scripts/*.ps1`。
2. **模型系统提示词与插件契约**：`internal/prompt/`、`internal/tasks/engine.go` 内置执行/路由提示词、`plugins/` 工具描述与参数 schema。
3. **协议机器码与内部诊断键**：`/v1/*` 协议枚举（如 `busy`、`rejected`、`waiting_input`）、签名输入、内部结构化日志字段名。
4. **用户与模型自由文本**：用户自定义节点名/目录名/命令名、任务目标与回答、聊天消息与模型生成正文、外部进程原始 `stderr`。

### 静态守卫覆盖不到的情况（代码审查与验收必查）

1. **跨函数或中间变量间接拼接的纯英文文案**：Go 非桌面包先用局部变量构造纯英文句子再返回给上层，或前端 JS 先把英文提示存入普通变量再赋给 `.textContent` / 提示函数时，单文件 AST 与词法规则不做全程序数据流追踪，需在代码审查中核对是否走了 `i18n` 词表。
2. **动态运行时拼接与条件遗漏**：新加的动态 DOM 节点若未挂入组件的 `relocalize()` 更新闭包，初次渲染虽使用了 `t(...)`，切换语言时仍会停留在旧语言；新增动态状态时必须配套补充 `relocalize()` 测试。
3. **语义准确度与双语排版溢出**：静态工具只校验词条存在性、占位符集合与字符集约束，无法判断译文语气是否自然或英文长文案在窄面板中是否折行、截断；涉及界面布局改动时须按 [开发流程](workflow.md) 完成双语 × 双主题（2 × 2）真实界面核对。
