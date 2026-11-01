# 运行期界面与提示国际化框架验证说明

## 目标与契约

验证 `internationalization-framework` 在 Web 管理控制台、快捷输入与持续聊天浮层、Windows 托盘/原生弹窗/脚本选择器/启动器及公开 API 错误与状态边界的中英双语（`zh-CN` / `en-US`）一致性、无损实时语言切换、并发与故障隔离，以及四层静态国际化守卫。

关联规范与实现入口：

- 需求与架构：[功能计划](plan.md)、[Web 需求](../../../web/requirements.md)、[Web 设计](../../../web/design.md)、[ADR 002](../../../web/adr/002-runtime-internationalization-framework.md)、[开发治理需求](../../requirements.md)、[开发治理设计](../../design.md)、[开发流程](../../workflow.md)、[国际化开发指南](../../i18n-guide.md)、[测试设计](../../../testing/design.md)。
- 词表与守卫：[manifest.json](../../../../internal/i18n/locales/manifest.json)、[zh-CN.json](../../../../internal/i18n/locales/zh-CN.json)、[en-US.json](../../../../internal/i18n/locales/en-US.json)、[guard-rules.json](../../../../internal/i18n/guard-rules.json)、[sync-i18n.mjs](../../../../scripts/sync-i18n.mjs)、[check-i18n.ps1](../../../../scripts/check-i18n.ps1)。
- 自动化回归：[catalog_test.go](../../../../internal/i18n/catalog_test.go)、[guard_test.go](../../../../internal/i18n/guard_test.go)、[locale_test.go](../../../../internal/quickinput/locale_test.go)、[locale_api_test.go](../../../../internal/app/locale_api_test.go)、[locale_test.go](../../../../internal/launch/locale_test.go)、[i18n-guard.test.mjs](../../../../tests/i18n-guard.test.mjs)、[i18n-runtime.test.mjs](../../../../tests/i18n-runtime.test.mjs)。

## 前置条件

- Go 1.27.3（与 [dependencies.json](../../../../packaging/dependencies.json) 一致；若未加入系统 `PATH`，在脚本中通过 `-Go <go-executable>` 指定）。
- Node.js 24.x 与 PowerShell 7（`pwsh`）。
- 常规单元、契约与构建守卫测试无需在线模型凭据、无需联网或修改本机活跃安装。
- 真实交互桌面 WebView2 / 托盘原生窗口检查需在当前登录的 Windows 交互桌面显式开启 `ASTERHAND_DESKTOP_TEST=1` 或 `-Desktop` 参数。

## 验证方法

| 行为与契约 | 仓库测试入口或复现命令 | 预期可观察结果 |
|---|---|---|
| **G1—G4 全栈静态国际化守卫**：词表对称性、占位符一致性、`disallow_cjk` 拦截与汉字语种（如 `ja-JP`）扩展兼容、生成模块字节同步、全栈缺键/死键清零、空白/注释间隔调用与显式映射字典假键阻断、`guard-rules.json` 配置驱动的 JS/Go UI 注入点阻断 | `pwsh -NoProfile -File ./scripts/check-i18n.ps1 -Go <go-executable>` | 串联 `sync-i18n.mjs --check`、`go test -count=1 ./internal/i18n` 与 `node --test tests/i18n-guard.test.mjs`，零违规通过；新增自定义出口与合成违规用例均被准确捕获 |
| **四层语言权威、跨标签页隔离与品牌规范**：`auto` 系统语言优先、系统未知时浏览器语言按优先级次选、最终回退 `en-US`；未登录页临时切换仅写 `sessionStorage` 且不受跨标签页 `storage` 覆盖，已登录页收到缓存变更时向服务端对账权威状态；`en-US` 纯 `Asterhand` 与 `zh-CN` `Asterhand · 星伴` 切换 | `node --test tests/i18n-runtime.test.mjs`、`go test -count=1 ./internal/app -run TestHealthLocaleDetectionAndDesktopLocaleAPI` | 四级解析符合优先级；未登录临时覆盖不被其他标签页 `localStorage` 冲掉，登录后清除临时覆盖并对齐服务端权威状态 |
| **独立语言更新事务、迟到读写防护与并发修订号保护**：无交互桌面/宿主故障/热键冲突/录制租约期间 `SetLocale` 独立成功；确定性拦截延迟请求 A → 对账 GET → 请求 B 提交 → 放行迟到请求 A；并发桌面状态 GET 乱序返回或跨过写入返回时按单调序号丢弃旧快照；跨标签页并发修改时 `formBaseline.revision` 不被盲目推进 | `go test -count=1 ./internal/quickinput -run "TestSetLocale|TestLegacyAndCorrupt"`、`node --test tests/i18n-runtime.test.mjs` | 录制租约保持有效；迟到写请求 A 被 `409` 拒绝且迟到读请求返回 `stale` 不回滚新语言；写盘后丢弃响应通过 `last_locale_operation` 对账收敛；损坏 `desktop-settings.json` 不被覆盖 |
| **默认名称与历史配置升级兼容**：新节点 `NameDefault: true` 在英文 UI 未改名保存模型/目录时不清除 `NameDefault`；旧 `config.json` 保留原节点名与自定义工作目录名 | `go test -count=1 ./internal/app -run TestDefaultAndLegacyNodeAndWorkspaceNamesOnSave`、`node --test tests/i18n-runtime.test.mjs` | 未编辑节点名时提交 `name_edited: false` 并保留原持久值；`POST /api/settings` 重建 `Projects` 时按 ID `"workspace"` 保留迁移的自定义项目名与 `NameCustom` |
| **结构化错误延迟本地化与拒绝证明不变性**：`X-Asterhand-Locale` 动态本地化公开 API 错误及持久化 `Job`/`Outbox` 的 `error_key`，无键历史记录原样回退，`TaskRejection` 摘要跨语言不变 | `go test -count=1 ./internal/app -run TestJobAndOutboxDynamicErrorLocalizationAndRejectionDigestInvariance`、`go test -count=1 ./internal/store` | 同一持久化记录随请求语言返回中/英文错误说明，历史无键错误原样保留，`RequestDigest` 在不同语言下完全一致 |
| **无损 `relocalize()` 原位局部刷新**：任务卡片标签/状态/时间原位更新，回答表单 DOM 节点、输入草稿、光标选区及提交进行中的按钮禁用防重入态（`button.disabled`）完整保留；快捷输入聊天外壳与按钮原位更新且不重建正文或重跑 KaTeX | `node --test tests/i18n-runtime.test.mjs` | 切换语言不重建 `<form>`/`<input>`/`<button>`，回答请求未完成时禁用态持续有效且阻止重复提交；聊天消息 `chat-message-body` 与 `<math>` 节点保持同一对象引用 |
| **启动器与脚本选择器窄语言同步，以及构建入口守卫接线** | `go test -count=1 ./internal/launch -run TestLauncherReadsDataLocaleAndFallsBackSafely`、`pwsh -NoProfile -File ./scripts/build-input-host.ps1 -Go <go-executable> -OutputDirectory ./out/i18n-input-host-check` | 启动器只读解析 `desktop-settings.json` 并在缺失/损坏时安全回退；`build-input-host.ps1` 与 `build-application.ps1` 在编译前自动执行前端与 Go 国际化守卫 |
| **全量工程单元与静态检查** | `$uiTests = Get-ChildItem -LiteralPath tests -Filter '*.test.mjs' -File; node --test $uiTests.FullName`、`go test -count=1 ./...`、`go vet ./...` | 全部前端契约测试、Go 包测试与 `go vet` 通过 |

## 支持边界

- **已覆盖**：仓库自动化测试与构建脚本已验证词表对称性、生成模块一致性、全栈静态守卫、API 与持久化延迟本地化、并发与迟到读写隔离、历史配置兼容、Web 与快捷输入 DOM 级无损切换及独立输入宿主构建；本机中英双语切换体验已通过用户验收。
- **需交互桌面专项或双机验证**：
  - 实体输入法极端边界、多屏/DPI、脚本选择原生对话框（`ASTERHAND_SCRIPT_PICKER_TEST=1`）及自动化桌面模式（`pwsh -NoProfile -File ./scripts/test-quick-input.ps1 -Go <go-executable> -OutputDirectory ./out/quick-input-validation -Desktop`）专项验证单独记录。
  - 两台 Windows 节点配对与跨节点真实任务执行（OpenClaw + DeepSeek）下的双语人工验收独立于单机验收。
- **明确排除范围**：安装与工程维护脚本（`install.ps1`、`install.sh`、`install-local.cmd`、`packaging/*.ps1`、`scripts/*.ps1`）及面向模型的系统提示词与插件工具契约不在运行期 UI 多语言范围内。
