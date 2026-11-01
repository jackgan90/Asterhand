# 运行期界面与提示国际化框架

## 基本信息

- 功能名称：`internationalization-framework`（运行期界面与提示国际化框架）
- 目标与用户价值：消除运行期界面和用户提示中的硬编码中文，建立统一可维护的多语言翻译表与构建/测试/提交前静态防呆机制；首版交付简体中文（`zh-CN`）与英文（`en-US`）无损实时切换，并在左到右（LTR）排版与命名参数插值能力范围内支持通过词表与注册表扩展新语言。
- 状态：已实现，通过工程自动化验证、独立复审及本机双语切换用户验收；双机跨节点及其余专项验证边界见[验证说明](validation.md)。
- 关联需求：R01、R03、C01、C02、C07、C08；WEB-I18N-01—05、DEV-12；涉及 WEB-01—13、WEB-CMD-01—08、WEB-CHAT-01—16 及 DEV-02/04/10。
- 相关领域依据：[全局需求](../../../overview/requirements.md)、[全局设计](../../../overview/design.md)、[Web 需求](../../../web/requirements.md)、[Web 设计](../../../web/design.md)、[任务设计](../../../tasks/design.md)、[持续会话设计](../../../assistant/conversations.md)、[节点设计](../../../node/design.md)、[安装与环境设计](../../../installation/design.md)、[开发治理需求](../../requirements.md)、[开发治理设计](../../design.md)、[开发流程](../../workflow.md)、[国际化开发指南](../../i18n-guide.md)、[测试设计](../../../testing/design.md)。
- ADR：[ADR 002：运行期国际化词表、延迟本地化与静态守卫](../../../web/adr/002-runtime-internationalization-framework.md)。

## 范围

### 本次实现（运行期面向用户的显示文本）

1. **首版语言集合与注册表驱动匹配规则**：
   - 首版内置 `zh-CN`（简体中文）与 `en-US`（English）；支持语言清单、配置合法值校验、界面选择器与环境匹配规则统一由单一语言注册表 `internal/i18n/locales/manifest.json` 派生，不在业务代码中写死固定语言分支。
   - 默认模式 `auto` 采用 **“本机系统语言优先、Web 端在系统未知时次选浏览器语言、最终回退注册表默认语言（`en-US`）”** 的分级策略。
2. **品牌与主题双语规范**：
   - `zh-CN`：主界面与桌面弹窗品牌显示 `Asterhand · 星伴`（快捷面板顶栏为 `Asterhand / 星伴`、窗口标题为 `星伴快捷输入`），内置主题为 `星核`（`深空霓光`）与 `霜蓝`（`明亮清透`）。
   - `en-US`：主界面与桌面弹窗品牌精简为纯英文 `Asterhand`（不展示中文副标题与分隔符；快捷面板窗口标题为 `Asterhand Quick Input`），内置主题译为 `Starcore`（`Deep space neon`）与 `Frost`（`Bright & crisp`）。
3. **运行期文本全量覆盖**：
   - **主 Web 管理控制台**：[index.html](../../../../web/index.html)、[app.js](../../../../web/app.js)、[desktop-settings.mjs](../../../../web/desktop-settings.mjs)、[node-controller.mjs](../../../../web/node-controller.mjs)、[node-settings.mjs](../../../../web/node-settings.mjs)、[local-commands.mjs](../../../../web/local-commands.mjs)、[task-view.mjs](../../../../web/task-view.mjs)、[view-model.mjs](../../../../web/view-model.mjs)、[ui.mjs](../../../../web/ui.mjs)、[api-client.mjs](../../../../web/api-client.mjs) 中的静态文案、表单属性、状态徽标、通知、确认框与日期时间本地化。
   - **快捷输入与持续聊天浮层**：[assets/index.html](../../../../internal/quickinput/assets/index.html)、[input.js](../../../../internal/quickinput/assets/input.js)、[chat-ui.mjs](../../../../internal/quickinput/assets/chat-ui.mjs)、[chat-renderer.mjs](../../../../internal/quickinput/assets/chat-renderer.mjs)、[chat-model.mjs](../../../../internal/quickinput/assets/chat-model.mjs)、[chat-outbox.mjs](../../../../internal/quickinput/assets/chat-outbox.mjs)、[chat-connection.mjs](../../../../internal/quickinput/assets/chat-connection.mjs)、[command-ui.mjs](../../../../internal/quickinput/assets/command-ui.mjs) 及原生窗口标题 [surface_window_windows.go](../../../../internal/quickinput/surface_window_windows.go)。
   - **Windows 原生桌面交互与启动器**：系统托盘菜单与悬停提示（[tray_menu_windows.go](../../../../internal/desktop/tray_menu_windows.go)、[desktop.go](../../../../internal/desktop/desktop.go)、[presentation.go](../../../../internal/quickinput/presentation.go)）、错误诊断与忙时退出确认弹窗（[open_windows.go](../../../../internal/desktop/open_windows.go)）、本地命令脚本选择窗口标题与过滤器（[script_picker_windows.go](../../../../internal/quickinput/script_picker_windows.go)）、启动器面向用户的失败诊断（[cmd/asterhand-launcher/main.go](../../../../cmd/asterhand-launcher/main.go)、[internal/launch/run.go](../../../../internal/launch/run.go)、[platform_windows.go](../../../../internal/launch/platform_windows.go)）。
   - **运行期后端与聊天桥接公开状态/错误**：[http.go](../../../../internal/app/http.go)、[api.go](../../../../internal/app/api.go)、[desktop_api.go](../../../../internal/app/desktop_api.go)、[outbox.go](../../../../internal/app/outbox.go)、[listener.go](../../../../internal/network/listener.go)、[tailscale.go](../../../../internal/network/tailscale.go)、[availability.go](../../../../internal/executor/availability.go)、[openclaw_availability.go](../../../../internal/executor/openclaw_availability.go)、[chatbridge/main.mjs](../../../../internal/executor/chatbridge/main.mjs)、[chatbridge/gateway.mjs](../../../../internal/executor/chatbridge/gateway.mjs)、[localcommands/service.go](../../../../internal/localcommands/service.go)、[conversation/service.go](../../../../internal/conversation/service.go)、[conversation/wire/protocol.go](../../../../internal/conversation/wire/protocol.go)、[tasks/engine.go](../../../../internal/tasks/engine.go)、[peer/peer.go](../../../../internal/peer/peer.go)、[config/config.go](../../../../internal/config/config.go) 等在展示边界呈现给用户的状态说明与业务错误。
4. **统一翻译表与分层静态国际化守卫**：集中维护词表与单一规则配置，接入构建脚本、单测与提交前检查。

### 明确非目标

- **安装与工程维护脚本不纳入多语言**：[install.ps1](../../../../install.ps1)、[install.sh](../../../../install.sh)、[install-local.cmd](../../../../install-local.cmd)、[packaging/start.ps1](../../../../packaging/start.ps1)、[packaging/configure-firewall.ps1](../../../../packaging/configure-firewall.ps1) 及 `scripts/*.ps1` 维持现有单语实现。
- **模型提示词与工具契约不纳入 UI 翻译表**：[default-chat.txt](../../../../internal/prompt/default-chat.txt)、[prompt/plan.go](../../../../internal/prompt/plan.go)、[tasks/engine.go](../../../../internal/tasks/engine.go) 中的路由/执行系统提示词及 [plugins/asterhand-tools/index.js](../../../../plugins/asterhand-tools/index.js) 面向 LLM，不混入 UI 翻译表。
- **用户自由数据与机器协议枚举不翻译**：用户自定义或已有历史来源不明的设备名、自定义工作区名、命令别名/描述、人工核对理由、任务目标、聊天消息正文、文件路径，以及 `/v1/*` 机器协议状态枚举保持原值。
- **能力边界限定**：不引入第三方前端打包器或重型 i18n 库；水平扩展免改代码仅针对 **LTR 书写方向与 `{param}` 命名插值语种**，不含复杂 CLDR 复数形态规则或 RTL 镜像布局。

### 依赖和约束

- 快捷输入浮层在 [surface_resources.go](../../../../internal/quickinput/surface_resources.go) 中执行 `connect-src 'none'` CSP，禁止运行时 `fetch` JSON 词表，仅允许加载精确白名单内的本地静态 ES module。
- 主 Web 与快捷输入浮层通过 [web/embed.go](../../../../web/embed.go) 和 [surface_resources.go](../../../../internal/quickinput/surface_resources.go) 独立 `//go:embed`，受 [modules.go](../../../../internal/testutil/webmodules/modules.go) 静态 import 闭包检查与 `node --test` 原生文件解析约束。
- 语言保存必须与热键注册、WebView2 宿主可用性、录制租约及任务执行锁完全解耦；语言切换不得覆盖表单/任务回答未提交编辑或清空聊天正文缓存。

## 架构与设计方案

### 1. 单一权威词表、语言注册表与零漂移分发

- **唯一人工维护源**：
  - `internal/i18n/locales/manifest.json`：声明 `default_fallback`（`"en-US"`）及有序 `locales` 注册表（每项含 `id`、`label`、`html_lang`、`prefixes` 及可选脚本校验规则 `disallow_cjk`，如 `zh-CN` 对应 `prefixes: ["zh"]`，`en-US` 对应 `prefixes: ["en"], disallow_cjk: true`）。配置合法性校验、UI 语言选择器、自动匹配逻辑与词表脚本检查均由该注册表驱动。
  - `internal/i18n/locales/<id>.json`（首版 `zh-CN.json`、`en-US.json`）：按语义点分键名组织（`common.*`、`brand.*`、`web.*`、`quickinput.*`、`desktop.*`、`status.*`、`err.*`），统一使用命名占位符 `{param}`；缺失参数时保留字面量 `{param}` 占位而不崩溃。
- **通用三级语言匹配规则**：
  - 对候选语言标签序列依次执行：① 大小写无关精确匹配注册表 `id`（连字符标准化）；② 提取语言族前缀匹配注册表 `prefixes`；③ 全部未命中或标签无效时回退至 `manifest.json` 的 `default_fallback`（`en-US`）。
- **编译期嵌入与前端只读模块生成**：
  - Go `internal/i18n` 通过 `//go:embed locales/*` 直接内嵌 `manifest.json` 与全部 `<id>.json`。
  - 零外部依赖脚本 `scripts/sync-i18n.mjs` 从 `internal/i18n/locales/` 确定性生成只读模块 `web/i18n-catalog.mjs` 与 `internal/quickinput/assets/i18n-catalog.mjs`（含注册表元数据与对应命名空间词条），满足 `connect-src 'none'`、`embed.FS` 与 `webmodules.Closure` 要求。

### 2. 结构化错误与状态国际化数据契约（保留既有错误类型与持久对账语义）

不在业务产生点把错误替换成纯翻译字符串或单一通用错误类型，严格分离以下四项职责，并在展示边界解析语言：

1. **业务控制类型与机器错误码（保持不变）**：
   - [admission.go](../../../../internal/model/admission.go) 的 `*model.TaskRejection`（含 `Code`、`RequestID`、`RequestDigest` 及非空 `Message` 证明要求）、[conversation/wire/protocol.go](../../../../internal/conversation/wire/protocol.go) 的 `*wire.Error`（`busy`、`rejected`、`not_found`、`unavailable` 等）、[localcommands/wire/protocol.go](../../../../internal/localcommands/wire/protocol.go) 的 `*wire.Error`、[change.go](../../../../internal/quickinput/change.go) 的 `*quickinput.Error` / `*quickinput.RegistrationError` 及 `*executor.ConfigurationError` **全部保留原有 Go 类型与 `Code` 判定逻辑**。任务准入去重、队列调度、草稿回填与 HTTP 状态码仅依据原类型和机器码，绝不解析翻译后文本，也不破坏独立输入宿主仅依赖 `conversation/wire` 的包隔离。
2. **展示消息标识与公开参数（`message_key` + `message_args`）**：
   - 在上述公开错误结构、HTTP 错误响应体 `{"error": "...", "message_key": "...", "message_args": {...}}`、以及能力/网络诊断结构（`Availability`、`network.Status`、`localcommands/wire.Entry`）中附加可选字段 `message_key`（`string, omitempty`）与 `message_args`（`map[string]string, omitempty`，仅承载经校验的非敏感展示参数，禁含凭据或内部栈）。
   - [chatbridge](../../../../internal/executor/chatbridge/main.mjs) 向 Go 父进程返回的公开错误附带稳定 `message_key`，由 Go 适配层装入 `*conversation/wire.Error` 的可选标识字段并生成兜底 `Message`。
3. **持久化记录延迟本地化（`model.Job` 与 `model.Outbox`）**：
   - 在 [model.go](../../../../internal/model/model.go) 的 `Job` 与 `Outbox` 中新增可选持久化字段 `ErrorKey string`（`json:"error_key,omitempty"`）与 `ErrorArgs map[string]string`（`json:"error_args,omitempty"`），**不改动参与 `RequestDigest` 哈希计算的 `model.Request` 结构**。
   - 系统产生的固定语义状态（如重启中断待核对 `Recover()`、远端撤销/连接失败等）同时写入 `ErrorKey`/`ErrorArgs` 与按节点当前有效语言生成的兜底 `Error` 字符串；人工核对填写的 `Resolve(id, reason)` 及底层运行时自由文本仅写入 `Error`（`ErrorKey` 为空），原样保留。
4. **安全兜底与双向兼容规则**：
   - 任何时候 HTTP / `/v1/*` / IPC 的原有 `error`、`message`、`detail` 字符串字段均保证**非空可读兜底文本**（尤其是 `TaskRejection.Message != ""` 兼容 [peer.go](../../../../internal/peer/peer.go) 校验；`/v1/*` 客户端采用标准 `json.Unmarshal`，天然忽略未知扩展字段）。
   - 前端展示优先级：若携带已知 `message_key` / `error_key`，使用当前界面语言 + `args` 动态渲染（切换语言时历史待核对说明与远端拒绝原因同步切换）；若遇旧数据库记录（无 `error_key`）、旧版远端节点响应（无 `message_key`）、未知新 `message_key` 或自由文本错误，则直接展示 `error` / `message` / `detail` 兜底原文。

### 3. 语言状态权威、独立更新事务与跨进程传递

#### 3.1 四层语言权威与回退边界

| 状态层 | 权威归属与解析规则 |
|---|---|
| **持久偏好（`desired.locale`）** | 存于本机 `desktop-settings.json`（取值 `"auto"` 或注册表中的具体语言 `id`；旧文件缺省归一化为 `"auto"`），是跨重启唯一配置权威。 |
| **节点有效语言（`effective_locale`）** | `desired.locale` 为具体语言时取该值；为 `"auto"` 时按**本机操作系统用户 UI 语言**匹配注册表，系统语言不可识别时回退 `default_fallback`（`en-US`）。托盘、快捷输入浮层与独立启动器无浏览器上下文，统一以节点有效语言为准（当系统语言未知且未显式保存语言时，桌面原生组件回退 `en-US`，不猜测浏览器语言）。 |
| **Web 页面生效语言** | 已获取节点状态时：若 `desired.locale != "auto"`，采用 `desired.locale`；若 `desired.locale == "auto"` 且节点 `system_locale_detected == true`，采用节点系统语言结果（系统优先）；仅当 `desired.locale == "auto"` 且 `system_locale_detected == false` 时，次选浏览器 `navigator.languages`，并通过请求头 `X-Asterhand-Locale` 使当次 API 错误响应保持同语种。 |
| **未登录临时覆盖与首帧缓存** | `localStorage`（`asterhand-locale`）仅作首帧乐观渲染提示，非权威状态，`/health`（返回 `{node_id, pid, locale, locale_mode, system_locale_detected}`）与 `/api/desktop/settings` 响应到达后立即校准。用户在未登录页手动切换语言存入当前标签页 `sessionStorage`，仅作用于本次登录前视图与 `/api/login` 请求；登录成功后以服务端持久配置为准，不静默覆写服务端设置。 |

#### 3.2 与快捷输入生效事务隔离的专用保存路径

为避免 [change.go](../../../../internal/quickinput/change.go) 中检查交互桌面、初始化 WebView2 宿主、注册全局热键及清除录制租约的副作用阻塞语言保存，建立独立更新路径：

- **专用接口 `POST /api/desktop/locale` 与 `Controller.SetLocale`**：
  - 请求体为 `{operation_id, expected_revision, locale}`（若前端尚未持有当前 `desired.revision`，先读取 `GET /api/desktop/settings` 获取基线修订号再发起）；与 `Controller.Apply` 共用同一互斥锁 `c.op`、5 分钟操作 ID 幂等表 `c.operations` 与 `PreferencesStore` 原子写盘。`Status` 独立维护 `LastLocaleOperation`（`json:"last_locale_operation,omitempty"`），不覆盖快捷键/主题保存使用的 `LastOperation`。
  - **严格零热键/宿主前置条件与写盘前修订号校验**：`SetLocale` 在 `c.op` 内先检查幂等表 `c.operations[operation_id]`（同一操作重放直接返回原结果），再校验节点未退出（`!c.isStopping()`）、语言值合法、原配置未损坏（`!c.invalid`）以及 **`expected_revision == before.Desired.Revision`**——修订号不符时立即返回 `409` 明确拒绝（附带当前 `desktop` 状态），绝不写盘覆盖较新提交；全程**不要求** `before.Supported` 或 `c.registrar != nil`，**不调用** `ReadySurface()`，**不重新注册或释放热键**，**不清除**活跃的快捷键录制租约 `c.record`。
  - 写盘提交成功后更新内存 `c.status.Desired.Locale`、`c.status.Desired.Revision` 与 `c.status.LastLocaleOperation = operation_id`，并通过 `/api/stream` 广播状态刷新；若此时快捷输入宿主 `c.surface != nil` 已处于运行态，仅向其推送 `{kind: "locale", locale: effectiveLocale}`（若宿主未创建则不拉起，待下次创建时由 `init` 携带；推送异常仅按宿主降级处理，绝不回滚已提交的语言设置）。
- **并发修改保护与未提交表单隔离**：
  - 原 `POST /api/desktop/settings` 的 `Change` 将 `Locale *string` 设为可选指针：省略或为 `nil` 时**保留已持久化的 `before.Desired.Locale`**，兼容旧请求与仅提交快捷键/主题的场景。
  - [desktop-settings.mjs](../../../../web/desktop-settings.mjs) 独立维护表单上次填充时的非语言基线快照 `formBaseline`（`{revision, enabled, binding, theme_id}`）：
    1. 语言切换返回新 `Status` 时，更新状态文案与 `current.desired.locale`，**不执行 `fill=true` 重置用户在表单中尚未保存的开关、按键或主题草稿**；
    2. **仅当 `pending == null` 且返回的 `desired` 非语言字段（`enabled`、`binding`、`theme_id`）与页面 `formBaseline` 完全一致时**，才将 `formBaseline.revision` 推进至最新 `desired.revision`；若其他页面已并发修改过快捷键或主题，保持原 `formBaseline.revision` 不变，确保后续提交旧表单时由后端正常触发 409 冲突保护并保留用户草稿；
    3. 已发出的未确认快捷键保存请求 `pending` 及其 `operation_id`、`expected_revision` 与请求体内容始终保持原样不变。
- **明确拒绝、结果未知对账与防迟到覆盖规则**：
  - **明确拒绝**（服务端返回结构化 4xx/5xx HTTP 错误，含 `expected_revision` 不符的 `409`）：清除本次挂起操作，重新读取或采用响应附带的最新服务端权威状态刷新页面，**不自动重放过时的语言选择**，并展示提示。
  - **结果未知**（网络超时、连接中断或写盘成功后响应丢失）：**不盲目假定旧请求已终止**，保留 `{operation_id, expected_revision, locale}` 并读取 `GET /api/desktop/settings` 对账：
    1. 若 `last_locale_operation == operation_id`，证明该操作已提交生效，清除挂起态并更新基线 `revision`；
    2. 若 `desired.revision != expected_revision`，证明服务端修订号已前进（即便旧请求稍后迟到抵达，也会在 `c.op` 内因 `expected_revision` 过期被 `409` 拦截），清除旧挂起态并对齐当前权威状态；
    3. 若 `last_locale_operation != operation_id` 且 `desired.revision == expected_revision`，**不同的 `last_locale_operation` 仅代表当前时间点快照，不代表在途旧请求已经结束**：页面先对齐当前快照，仅在用户显式重试同一操作时复用原 `{operation_id, expected_revision}`；若用户改选新语言并发起请求 `B`，`B` 提交推进 `revision` 后，迟到抵达的旧请求 `A` 必被服务端 `expected_revision` 校验以 `409` 拒绝（反之若 `A` 在快照后抢先落盘，`B` 收到 `409` 后客户端重新读取权威状态且不自动重放，若无后续 `B` 则 `A` 落盘后由 `/api/stream` 通知页面收敛）；
    4. 若对账读取仍离线失败，保留同一 `{operation_id, expected_revision, locale}` 待连接恢复时先读后核对。
  - **单调代次与多标签页同步**：语言读取、切换与对账回调均绑定单调代次，屏蔽过期的本地异步回调；同源多标签页通过 `storage` 事件及 `/api/stream` 同步最新持久语言。

#### 3.3 原生子进程窄参数传递

- **本地命令脚本选择器（`pick-local-script`）**：由父进程在 [script_picker_windows.go](../../../../internal/quickinput/script_picker_windows.go) 拉起 helper 时直接传入经注册表白名单校验的 `--locale <effective-locale>` 参数；受限令牌子进程无需定位或读取节点私有数据目录。
- **桌面启动器（`AsterhandLauncher.exe`）**：基于已知 `installation.json` 数据目录只读尝试解析 `desktop-settings.json` 的 `locale` 字段；文件不存在、读取受限或值为 `"auto"` 时，直接调用本机系统 UI 语言探测并回退 `en-US`。

### 4. 无损局部展示刷新契约（保留编辑草稿、DOM 选区与公式缓存）

切换语言时禁止粗暴整树清空重建活跃编辑区或重跑重型排版，各视图模块实现专用 `relocalize()` 接口：

- **静态声明节点**：所有静态文案与属性使用 `data-i18n`（`textContent`）、`data-i18n-placeholder`、`data-i18n-aria-label`、`data-i18n-title` 声明，严禁 `innerHTML`；含 `<svg>` 图标的按钮将文字包裹于独立 `<span data-i18n="...">`。
- **任务列表与详情视图（[task-view.mjs](../../../../web/task-view.mjs)）**：
  - 维护按 `(job.id, interaction.id)` 索引的未提交回答草稿缓存（输入时实时同步，提交成功后清除），并在语言切换刷新前保存当前聚焦输入框 ID 与光标选区；更新状态徽标、相对/绝对时间、产物标题、操作按钮后原位恢复未提交回答文本与焦点。若 `#detail` 弹窗打开中，同步刷新摘要项与元数据说明，保留已加载的事件列表与滚动偏移。
- **表单与命令管理控制器（[node-settings.mjs](../../../../web/node-settings.mjs)、[desktop-settings.mjs](../../../../web/desktop-settings.mjs)、[local-commands.mjs](../../../../web/local-commands.mjs)）**：
  - `relocalize()` 仅刷新状态行、探测状态、计数文案、空态提示、表单标题（新建/编辑）及动态参数行的 `aria-label`/移除按钮文字，**不调用 `populate()`，完整保留未保存输入值与 `dirty` 标记**。
- **快捷输入与持续聊天渲染器（[chat-renderer.mjs](../../../../internal/quickinput/assets/chat-renderer.mjs)）**：
  - 将单条消息节点拆分为**语言无关的正文容器 `body`（Markdown 段落与 KaTeX 公式节点）**与**可原位更新的外壳/辅助控件**（作者角色标签、`<time>` 格式化文本、送达状态提示、分片“从头读取/读取完整消息”按钮，以及代码块工具栏带 `data-i18n-role` 标记的默认标签与复制按钮）。
  - 收到 IPC `locale` 消息时调用 `render.relocalize()` 原地修改上述标签与按钮文本，并触发 [composer-layout.mjs](../../../../internal/quickinput/assets/composer-layout.mjs) 重算收起态高度：**不改变消息缓存签名、不重跑 `markdown()` 与 KaTeX 渲染、不破坏消息选区、历史分页锚点、IME 组合状态或流式增量回复**。

### 5. 默认名称与用户自定义名称来源区分

- **节点设备名称（`Config.Name`）**：
  - 设备名称参与跨节点同步（`/v1/metadata`）与自然语言路由提示词，持久化的 `Name` 始终保持有效字符串。
  - 在 [config.go](../../../../internal/config/config.go) 的 `Config` 中新增可选字段 `NameDefault bool`（`json:"name_default,omitempty"`）：仅**全新初始化**节点时设为 `true`，并按当时节点系统有效语言写入初始 `Name`（中文环境 `"我的节点"`，英文环境 `"My Node"`）。
  - **展示与整表单保存不产生误改名意图**：
    - [node-settings.mjs](../../../../web/node-settings.mjs) 打开设置时记录 `nameDirty = false`（仅当用户编辑 `name` 输入框触发 `input` 事件时置 `true`）。当 `c.name_default && !nameDirty` 时，输入框与顶栏均按当前语言展示默认设备名译文（切换语言执行 `relocalize()` 时同步更新输入框显示），但提交 `POST /api/settings` 时**回传原持久值 `c.name` 及 `name_edited: false`**，后端保持 `c.Name = before.Name` 与 `c.NameDefault = true` 不变；
    - 一旦用户主动编辑过名称输入框（`nameDirty == true`，即便手动输入 `"我的节点"` 或 `"My Node"`），提交编辑后字符串与 `name_edited: true`，后端立即置 `c.NameDefault = false` 并固化为用户自定义名称（对未传 `name_edited` 的直接 API 调用，仅当 `input.Name != before.Name` 时清除 `NameDefault`）。
  - **旧数据兼容规则**：升级前已存在的 `config.json` 无 `NameDefault` 字段（反序列化为 `false`），一律视为已有确定名称**原样保留，绝不根据字符串内容猜测或自动改写**。跨节点同步与路由始终使用 `c.Name` 稳定字符串。
- **工作区项目名称（`Project.Name`）**：
  - 在 `config.Project` 中新增可选字段 `NameCustom bool`（`json:"name_custom,omitempty"`）。
  - **升级归一化与按 ID 保存保留规则**：
    - 配置加载归一化时：当 `Project.ID == "workspace"` 且 `!NameCustom` 时，若旧配置 `Name` 为空或等于历史内置常量 `"授权目录"`，保持 `NameCustom = false`，UI 按当前语言展示 `"工作目录"` / `"Workspace"`；若用户曾在配置文件或夹具中指定了其他非空 `Name`，归一化时置 `NameCustom = true` 并原样保留。
    - [api.go](../../../../internal/app/api.go) 处理 `POST /api/settings` 重建 `Projects` 时：**按项目 `ID`（`"workspace"`）查找 `before.Projects`，若已存在则原样继承其 `Name` 与 `NameCustom`**（仅更新 `Root`），仅在首次新建内置 `"workspace"` 项目时写入默认 `Name: "授权目录"` 与 `NameCustom: false`，绝不覆盖迁移或配置中已有的自定义项目名。任务路由提示词继续使用 `Project.ID` 与实际路径。

### 6. 分层静态国际化守卫与精确接线

共用单一机器可读规则清单 `internal/i18n/guard-rules.json`（统一维护受检包/目录范围、排除分类与带原因的符号级窄豁免），避免 Go 与 Node 两套检查规则各自漂移：

| 守卫层级 | 范围与可检查语法定义 |
|---|---|
| **G1 词表与注册表强校验** | 100% 强制：`manifest.json` 合法；所有已注册语言 `<id>.json` 键集合完全对称、无空值；对 `manifest.json` 中标记 `disallow_cjk: true` 的语言（首版为 `en-US`）检查不得残留 CJK 字符（不误伤日语等合法使用汉字的扩展语种）；同键 `{param}` 占位符集合完全一致；生成的两份 `i18n-catalog.mjs` 与 JSON 源字节完全一致。 |
| **G2 双向键引用强校验** | 100% 强制：提取 HTML `data-i18n*`、JS `t("...")` 与 Go `i18n` 调用/错误键常量（枚举状态映射使用显式字面量字典），禁止出现未定义缺键或全栈并集均未引用的死键。 |
| **G3 HTML 模板属性与文本检查** | 扫描 [web/index.html](../../../../web/index.html) 与 [quickinput/assets/index.html](../../../../internal/quickinput/assets/index.html)：除 `guard-rules.json` 登记的技术符号白名单（如 `×`、`◈`、`Ctrl`、`Alt`、`Shift`）外，可见文本节点与 `placeholder` / `aria-label` / `title` / `<title>` 必须具备对应 `data-i18n*` 属性。 |
| **G4 界面与公开边界字面量检查** | **明确边界，不做跨函数数据流分析，不拦截内部协议/诊断英文错误**：<br>1. **排除分类**：模型提示词（`internal/prompt/`、`tasks/engine.go` 的 `prompt`/`routePrompt`、`plugins/`）、自动生成词表模块与第三方 `vendor/` 明确排除；<br>2. **CJK 字符串检查**：使用 `go/scanner` / JS 词法扫描受检运行期包（`web/`、`internal/quickinput/`、`internal/desktop/`、`internal/launch/`、`internal/app/`、`internal/network/`、`internal/localcommands/`、`internal/conversation/`、`internal/executor/`、`internal/tasks/`、`internal/peer/`、`internal/config/`、`internal/llm/`）的字符串字面量，自动忽略注释；必要兼容常量（如旧配置迁移中的历史常量 `"授权目录"`）在 `guard-rules.json` 按 `文件 + 声明符号 + 原因` 精确登记；<br>3. **英文直接字面量检查**：仅限前端 UI 注入点（`.textContent = "..."`、`node(tag, "...")`、`element(tag, "...")`、`confirm("...")`、`notice("...")`、`say("...")`、`setAttribute("aria-label"|"title"|"placeholder", "...")`）与桌面原生菜单/弹窗调用中的**直接自然语言字面量**，作为防手误护栏，不在后端领域包拦截内部 `errors.New` / `fmt.Errorf`。 |

#### 单一检查实现复用与实际接线位置

不重复编写多套扫描器：前端/词表检查唯一实现在 `scripts/sync-i18n.mjs`（导出检查函数供 CLI `--check` 与 `tests/i18n-guard.test.mjs` 直接调用），Go 源码与全栈死键并集检查唯一实现在 `internal/i18n/guard_test.go`（使用标准库 `go/scanner` + `go/ast`），两者共读同一份 `internal/i18n/guard-rules.json`：

1. **应用与快速本地安装构建**：在 [scripts/build-application.ps1](../../../../scripts/build-application.ps1) 切换交叉编译环境变量前运行 `& $Go -C $repo test -count=1 ./internal/i18n`，并在 `verify-frontend-assets.mjs` 后运行 `node scripts/sync-i18n.mjs --check`，使 `build.ps1`、`release.ps1` 与 `install-local.cmd`（Fast/Full）同时阻断词表漂移、HTML/JS 违规与 Go/桌面源码违规。
2. **独立输入宿主构建**：在 [scripts/build-input-host.ps1](../../../../scripts/build-input-host.ps1) 同样串联 `node scripts/sync-i18n.mjs --check` 与宿主机环境下的 `& $Go -C $repo test -count=1 ./internal/i18n`，覆盖开发宿主构建与 `test-quick-input.ps1 -Desktop`。
3. **测试套件与候选验证分界**：
   - `go test ./...`（及 [verify-dependencies.ps1](../../../../scripts/verify-dependencies.ps1) 的 `go_tests`）运行 `internal/i18n/guard_test.go`，覆盖 G1、全栈 G2 缺键/死键并集与 Go/桌面 G4 检查；
   - `node --test`（及 `verify-dependencies.ps1` 的 `javascript_contracts`）运行 `tests/i18n-guard.test.mjs`，直接调用 `sync-i18n.mjs` 检查函数（**不拉起 `go` 子进程**），专注覆盖 G1、HTML/JS 缺键、HTML G3 与前端/桥接 JS G4 检查。
4. **提交前快捷检查**：提供 `scripts/check-i18n.ps1` 一键串联 `node scripts/sync-i18n.mjs --check`、`go test -count=1 ./internal/i18n` 与 `node --test tests/i18n-guard.test.mjs`，并在 [开发流程](../../workflow.md) 中列为本地提交前检查项。

## 实施与文档同步

### 实施步骤

1. **词表、注册表与生成守卫基础设施**：建立 `internal/i18n`（`manifest.json`、中英词表、三级匹配、系统语言窄平台适配、结构化消息辅助）、`scripts/sync-i18n.mjs`、`internal/i18n/guard-rules.json` 及前端 `i18n.mjs`，接入 [web/embed.go](../../../../web/embed.go)、[surface_resources.go](../../../../internal/quickinput/surface_resources.go) 与两处构建脚本。
2. **独立语言更新路径与跨进程接线**：扩展 [preferences.go](../../../../internal/quickinput/preferences.go)、新增 `Controller.SetLocale` 与 `POST /api/desktop/locale`（隔离热键/宿主/录制副作用）、扩展 `/health`、快捷输入 IPC `init`/`locale` 及 `pick-local-script --locale`。
3. **结构化错误/状态契约与后端/桌面迁移**：为公开错误与 `Job`/`Outbox` 补充可选 `message_key`/`error_key` 与兜底文本，完成托盘、启动器、脚本选择器、后端公开状态及 `chatbridge` 迁移，实现 `NameDefault` / `NameCustom` 兼容逻辑。
4. **前端双界面与无损 `relocalize()` 改造**：迁移 [web/](../../../../web/index.html) 与 [quickinput/assets/](../../../../internal/quickinput/assets/index.html) 全部文案，实现任务回答草稿保留、未保存设置保留与聊天消息外壳原位刷新。
5. **守卫全量启用、故障/兼容回归与权威文档同步**：清零存量违规，补齐正常、故障与升级兼容测试，同步更新所有涉及领域的权威文档。

### 实施阶段必须更新的领域文档

- [Web 需求](../../../web/requirements.md) 与 [Web 设计](../../../web/design.md)：运行期双语、注册表驱动匹配、四层语言状态权威、`POST /api/desktop/locale` 独立事务、安全 DOM 绑定与 `relocalize()` 无损刷新契约。
- [任务设计](../../../tasks/design.md)、[持续会话设计](../../../assistant/conversations.md) 与 [节点设计](../../../node/design.md)：`Job`/`Outbox` 可选 `error_key`/`error_args` 持久化与兜底 `error` 规则、`conversation/wire.Error` 与 `TaskRejection` 保持机器码及拒绝证明不变、跨节点新旧版本错误/状态兼容与设备名边界。
- [安装与环境设计](../../../installation/design.md)：`desktop-settings.json` 可选 `locale` 兼容规则、启动器只读回退、`pick-local-script --locale` 窄参数传递、隐藏宿主恢复时的语言同步，以及 `install.ps1` 单语边界。
- [开发治理需求](../../requirements.md)、[开发治理设计](../../design.md)、[开发流程](../../workflow.md) 与 [测试设计](../../../testing/design.md)：单一词表规范、`guard-rules.json` 分层守卫边界与构建/测试/提交前接线。
- [脚本指南](../../../../scripts/README.md)、[测试入口](../../../../tests/README.md)、[文档导航](../../../README.md) 与 [路线图](../../roadmap.md)：更新脚本说明、回归入口与实现/验收状态。

## 规模与拆分评估

- **现有相关文件规模**：涉及的运行期源码文件均低于 500 行（[controller.go](../../../../internal/quickinput/controller.go) 478 行、[api.go](../../../../internal/app/api.go) 451 行、[chat-ui.mjs](../../../../internal/quickinput/assets/chat-ui.mjs) 429 行、[style.css](../../../../web/style.css) 742 行、[web/index.html](../../../../web/index.html) 275 行），远低于 1000 行软阈值，无需拆分现有业务文件。
- **新增模块规模**：`internal/i18n/` 按词表与匹配、系统语言探测、错误辅助、静态守卫拆分为职责单一的窄文件（单文件预计 80—220 行，单函数 < 100 行）；`SetLocale` 独立于 `Apply` 实现以保持各自低于 100 行；前端 `i18n.mjs` 独立封装（预计 < 150 行）。
- **分类说明**：`internal/i18n/locales/*.json` 为词表数据文件，两份 `i18n-catalog.mjs` 为带头声明的自动生成只读模块，按 [代码风格规范](../../code-style.md) 明确分类。若实施中任何手写文件超过 1000 行，严格先经用户确认后再拆分。

## 验收与验证计划

### 功能与无损交互验收

1. **环境匹配与权威边界**：`auto` 模式下，系统语言可识别时 Web、托盘、启动器与浮层统一跟随系统语言；模拟系统语言未知时，Web 次选浏览器语言，无浏览器上下文的启动器/托盘回退 `en-US`。未登录页临时切换不影响已登录服务端持久设置。
2. **双语完整度、品牌规范与汉字语种扩展**：`en-US` 下全部运行期界面、对话框、托盘菜单与公开错误均显示准确英文，品牌为纯 `Asterhand`，主题为 `Starcore` / `Frost`；`zh-CN` 恢复 `Asterhand · 星伴` 与 `星核` / `霜蓝`。添加包含汉字词条的模拟第三语言词表（如 `ja-JP`，`disallow_cjk` 未启用）与 `manifest.json` 条目时，无需修改匹配或选择器代码即可通过 G1 校验并正确识别展示，而 `en-US`（`disallow_cjk: true`）仍拦截残留汉字。
3. **编辑与生成期间无损切换**：在以下场景中途切换语言均不丢失输入、不重置状态、不重跑数学排版：① 任务卡片问答输入框含未发送答案；② 本地命令表单处于编辑 `dirty` 状态；③「设置 → 快捷输入」含未保存按键或正在录制热键；④ 快捷输入处于 IME 组合、收起态多行草稿、展开态历史分页浏览或流式回复生成期间；⑤ 新节点在英文界面打开设置仅修改模型配置并保存，不将默认节点名误判为用户改名。

### 故障、并发与兼容回归矩阵

1. **与快捷输入故障隔离**：模拟无交互桌面、WebView2 初始化失败、热键被占用冲突或正在录制快捷键时，调用 `POST /api/desktop/locale` 仍成功保存并切换 Web/后端语言，且不取消当前录制租约；旧格式 `POST /api/desktop/settings`（不含 `locale`）保存主题或热键后不丢失已设语言。
2. **并发修改保护、响应丢失对账与迟到响应**：
   - **并发修订号保护**：页面 A 打开旧快捷输入设置 → 页面 B 修改快捷键或主题 → 页面 A 切换语言成功后尝试保存旧表单，验证 `formBaseline.revision` 未被盲目推进，后端正常返回 409 冲突并保留页面 A 草稿；若无外部并发修改，页面 A 切换语言后保存表单一次成功；已存在的 `pending` 请求及其 `operation_id` 不受语言切换影响。
   - **明确拒绝、写盘后响应丢失与迟到写入防护**：磁盘写入失败、`desktop-settings.json` 处于损坏保护态或 `expected_revision` 不符等明确拒绝时，重新对齐服务端权威语言且不自动重放过时选择；模拟服务端已写盘更新后丢弃 HTTP 响应（以及对账期间发生第二次语言修改），客户端通过 `operation_id` 与服务端状态核对收敛；确定性覆盖**“拦截延迟请求 A → 客户端超时并完成对账 GET → 请求 B 提交成功 → 放行迟到请求 A”**时序，确认 `A` 在 `c.op` 内因 `expected_revision` 过期被 `409` 拒绝且 `B` 保持生效（同时覆盖 `A` 在快照后抢先落盘、`B` 被 `409` 拒绝后重新读取权威状态的对称时序）。
   - **迟到读取隔离**：并发多标签页切换与人为延迟的旧读取响应不覆盖最新已确认语言。
3. **隐藏宿主与重建恢复**：快捷输入浮层处于隐藏挂起态时在 Web 切换语言，再次呼出或宿主崩溃重建后立即呈现新语言且保留主进程内存草稿。
4. **升级与跨版本兼容**：
   - 旧版 `desktop-settings.json`（无 `locale`）平滑按 `"auto"` 加载；
   - 旧版 `config.json`（无 `name_default` / `name_custom`）升级后原节点名（含 `"我的节点"`）100% 保留不误改；内置 `"授权目录"` 按当前语言显示，而迁移后的自定义项目名在经 Web 保存节点设置后依然按项目 `ID` 完整保留；
   - 旧 SQLite `Job`/`Outbox` 记录（无 `error_key`）、旧版本远端节点 `/v1/*` 响应（无 `message_key`）、未知语言码、未知 `message_key` 或缺失 `{param}` 参数时，均安全回退而不报错崩溃，`TaskRejection` 持久拒绝证明继续通过跨语言 fixture 校验。
5. **静态守卫与构建阻断（按入口实际职责验证）**：
   - 词表缺键、占位符不一致、`disallow_cjk: true` 语言残留汉字、未重新生成 `i18n-catalog.mjs`：`build-application.ps1`、`build-input-host.ps1`、`go test ./...`、`node --test` 与 `scripts/check-i18n.ps1` 全部阻断；
   - HTML 未绑定文案或前端/桥接 JS 直接字面量：`build-application.ps1`、`build-input-host.ps1`、`node --test` 与 `scripts/check-i18n.ps1` 阻断；
   - Go/桌面原生代码未登记 CJK 或直接英文菜单/弹窗字面量，以及全栈死键并集：`build-application.ps1`、`build-input-host.ps1`、`go test ./...` 与 `scripts/check-i18n.ps1` 阻断。

### 验证命令与支持边界

```powershell
node ./scripts/sync-i18n.mjs --check
pwsh -NoProfile -File ./scripts/check-i18n.ps1
$uiTests = Get-ChildItem -LiteralPath tests -Filter '*.test.mjs' -File
node --test $uiTests.FullName
go test ./...
go vet ./...
```

- 真实浏览器与原生宿主检查：使用 [web_browser_fixture_test.go](../../../../internal/app/web_browser_fixture_test.go) 与 `scripts/test-quick-input.ps1 -Desktop` 执行双语 × 双主题（2 × 2）及上述无损切换检查。
- 支持边界：自动化测试证明契约、故障隔离、兼容回退与隔离桌面渲染；真实中英文 Windows 会话与双机跨语言体验由用户验收独立确认。
