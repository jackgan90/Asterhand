# ADR-002：运行期界面与提示国际化框架

- 状态：accepted（已进入工程实现）
- 日期：2026-11-01
- 确认依据：运行期多语言切换、统一翻译表维护与提交前静态防呆决策，详见 [Web 需求](../requirements.md)、[Web 设计](../design.md) 与 [功能计划](../../development/features/internationalization-framework/plan.md)。

## 决策

1. **单一权威词表与注册表驱动**：以 `internal/i18n/locales/manifest.json` 维护支持的语言清单、默认回退语言（`en-US`）、三级语言标签匹配前缀与词表脚本检查规则；以 `internal/i18n/locales/<id>.json`（首版 `zh-CN`、`en-US`）集中维护运行期所有用户可见文案与 `{param}` 命名插值模板。
2. **编译期嵌入与前端只读模块确定性生成**：Go 后端与原生桌面组件通过 `//go:embed locales/*` 直接内嵌 JSON 词表；零外部依赖脚本 `scripts/sync-i18n.mjs` 从同一源确定性生成只读 ES module `web/i18n-catalog.mjs` 与 `internal/quickinput/assets/i18n-catalog.mjs`，不引入前端打包器或运行时网络拉取，满足快捷输入宿主 `connect-src 'none'` 与静态模块闭包要求。
3. **结构化错误延迟本地化与独立语言保存事务**：保留既有业务错误类型、机器码与 `TaskRejection` 摘要证明，在公开响应与 `Job`/`Outbox` 持久化记录中附加可选 `message_key`/`error_key` 与非敏感参数，同时保留非空可读兜底字符串，在展示边界按当前语言解析；语言偏好复用 `desktop-settings.json` 持久化，但通过专用 `POST /api/desktop/locale`（附 `operation_id` 与 `expected_revision`）更新，与热键注册、WebView2 宿主初始化及录制租约完全隔离。
4. **安全局部刷新与分层静态守卫**：前端静态节点统一采用 `data-i18n*` 属性与 `textContent` 安全绑定，动态视图实现 `relocalize()` 原位刷新标签与提示，保留未提交草稿、焦点选区与数学公式 DOM 缓存；共用 `internal/i18n/guard-rules.json`，在构建脚本、`go test`、`node --test` 与 `scripts/check-i18n.ps1` 中强制执行词表对称、双向键引用、HTML 绑定与受检源码字面量检查。

## 背景与后果

早期实现将中文文案分散硬编码在 HTML、前端模块、Windows 托盘/对话框与后端公开响应中，无法按用户环境切换语言，且易在后续改动中遗漏翻译或引入语言漂移。

采用注册表与单一词表生成静态模块，在保持零打包器、零运行时外部请求的前提下消除前后端词条重复维护；延迟本地化避免将固定语言固化到持久任务记录或破坏机器状态分流；专用语言保存接口避免无交互桌面、热键冲突或宿主异常阻断界面语言切换；静态守卫仅约束受检运行期 UI 与公开边界字面量，不误伤模型提示词、安装脚本或内部诊断错误。

## 关联

[领域需求](../requirements.md) · [当前设计](../design.md) · [开发治理设计](../../development/design.md) · [功能计划](../../development/features/internationalization-framework/plan.md)
