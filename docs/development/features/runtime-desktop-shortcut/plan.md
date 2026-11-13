# 运行期桌面图标开关

## 基本信息

- 功能名称（目录使用语义明确的英文名称）：`runtime-desktop-shortcut`
- 目标与用户价值：用户安装时可能先跳过桌面快捷方式，之后希望恢复显示；或安装时创建了桌面快捷方式，之后希望移除。本功能让 Windows 正式安装用户在应用安装后无需重装，直接在 Web「设置 → 通用」中即时切换桌面图标显示状态，并通过 CLI 与 MCP 暴露等价自动化能力。
- 状态：已完成源码实现、领域文档同步、工程验证及本机用户验收。
- 关联 Rxx / Cxx / 领域需求 ID：`C04`、`C05`、`INST-21`、`INST-32`、`WEB-14`、`WEB-UX-03`、`WEB-AUTO-01`、`AUTO-11`、`AUTO-24`
- 相关领域 requirements / design 链接：
  - [安装与环境需求](../../../installation/requirements.md) / [安装与环境设计](../../../installation/design.md)
  - [Web 交互需求](../../../web/requirements.md) / [Web 设计](../../../web/design.md)
  - [自动化需求](../../../automation/requirements.md) / [自动化设计](../../../automation/design.md) / [覆盖清单](../../../automation/coverage.md)
- ADR（仅有新决策时）：沿用[桌面入口 ADR](../../../installation/adr/004-desktop-launcher-and-tray.md) 的 `FOLDERID_Desktop\Asterhand.lnk` + `AsterhandLauncher.exe` 归属约定，无新增架构 ADR。

## 范围

- 本次实现：
  1. 在 `internal/launch` 新增平台抽象的桌面快捷方式管理器（`ShortcutManager`、`ShortcutStatus`、`ShortcutChange`、`ShortcutError`）：
     - 基于 `FindInstallation(executable, home)` 与同目录 `AsterhandLauncher.exe` 判定正式安装态；未安装/开发运行态返回 `supported: true, available: false, reason: "not_installed"`；非 Windows 返回 `supported: false`。
     - 以 `FOLDERID_Desktop\Asterhand.lnk` 实际快捷方式归属为唯一事实源（同时识别新版 `AsterhandLauncher.exe` 与历史 `start.ps1 -NoBrowser` 入口），不向 `desktop-settings.json` 写入缓存布尔值，也不在运行期修改 `installation.json`。
     - Windows 使用纯 Go `CGO_ENABLED=0` Win32 COM（`IShellLinkW`、`IPersistFile`）读写 `.lnk`，在按数据目录派生的启动互斥锁（`acquireLaunchLock`）保护下创建/更新（对齐 `install.ps1` `Set-InstallShortcut` 字段）或删除属于本安装的 `.lnk`，并调用 `SHChangeNotify` 通知 Explorer；保留 10 分钟/256 条内存操作幂等记录。
     - 遇到非本安装拥有的同名文件、目录、重解析点或指向其他程序的 `.lnk` 时，不覆盖且不删除：启用操作返回 `409`（`conflict`），状态标记 `conflict: true`。
  2. 在 `internal/app` 提供同源 HTTP 路由 `GET /api/desktop/shortcut` 与 `POST /api/desktop/shortcut`（纳入 `desktopRequests` 限流、Origin/CSRF、Stopping 检查与请求语言响应）。
  3. 在 `internal/app` 与 `internal/automation` 接入等价自动化能力：`settings.get` 返回 `desktop_shortcut` 字段；新增可批处理写操作 `settings.desktop_shortcut`，配合 `operations.State` 持久意图与崩溃恢复对账（`reconcileAutomationOperation`）共享 CLI 与 MCP stdio。
  4. 在 Web「设置 → 通用」新增独立于 `#settings-form` 的即时切换开关（`web/desktop-shortcut.mjs`、`#desktop-shortcut-field`、`#desktop-shortcut`），打开设置时读取、不随任务 SSE 轮询、支持响应丢失按 `last_operation` 对账与运行期中英双语切换，并在 `contracts/automation-coverage.json` 登记覆盖映射。
- 明确非目标：
  - 不在 Windows 系统托盘右键菜单重复放置快捷方式开关（避免误触且不符合各平台托盘差异）。
  - 不在运行期提供“登录后启动（Startup 文件夹）”开关，该项继续由安装器参数或系统启动目录控制。
  - 首期不启用 Linux（`.desktop`）与 macOS（`Applications` / 别名）桌面图标管理，非 Windows 节点返回 `supported: false` 并在 Web 隐藏该控件。
- 依赖和待验证假设：
  - 无新增第三方 Go 或 npm 依赖；Windows COM 与 Known Folder 仅依赖现有 `golang.org/x/sys/windows` 与系统 `ole32.dll` / `shell32.dll`。
  - Windows 桌面与 Explorer 通过 `SHChangeNotify` 刷新快捷方式图标，升级流程按 `Test-OwnedShortcut` 保留当前自有快捷方式的开关状态（本机用户验收已确认）。
- 验收用例及预期可见结果：
  1. 已安装节点开启“显示桌面图标”：打开 Web `设置 → 通用`，切换开启后立即保存，当前用户桌面出现可双击启动节点的 `Asterhand` 快捷方式（`settings-dirty` 不出现“有未保存的修改”）。
  2. 已显示时关闭：切换关闭后立即保存，桌面上的 Asterhand 快捷方式消失；随后重新运行本地安装升级后仍保持隐藏，不再自动复活。
  3. 冲突保护：当桌面已有指向其他程序的 `Asterhand.lnk` 时，开启开关提示冲突错误且开关回退为关闭，原有外部快捷方式字节完整保留；关闭动作安全无副作用。
  4. 非正式安装态：开发模式直接运行 `asterhand.exe` 时，通用设置中开关置灰并显示“仅正式安装版本可管理桌面图标。”；非 Windows 平台完全隐藏该项。
  5. CLI / MCP 自动化等价：`asterhand automation settings.get` 与 `settings.desktop_shortcut`（及 MCP 同名工具）产生与 Web 相同的磁盘快捷方式、冲突错误及持久操作收据。

## 实施与文档同步

- 实施步骤：
  1. 建立本计划并同步安装、Web、自动化三个领域的权威需求/设计/覆盖文档。
  2. 在双语词表增补 Web、状态与错误词条，运行 `scripts/sync-i18n.mjs` 生成前后端词表模块。
  3. 在 `internal/launch` 实现跨平台接口与 Windows COM `.lnk` 读写与互斥保护，并补齐隔离环境（Unicode/含空格目录）与 PowerShell `WScript.Shell` 互操作测试。
  4. 在 `internal/app`、`internal/automation` 与 `contracts/automation-coverage.json` 接入 HTTP API、CLI/MCP 公共操作、崩溃对账与 schema。
  5. 在 `web/` 增设通用页即时开关模块 `web/desktop-shortcut.mjs`，接入 `web/embed.go`、`web/app.js`、`web/node-settings.mjs` 与 `web/style.css`，补充前端单元测试与守卫回归。
- 必须更新的领域文档及实质内容：
  - [安装与环境需求](../../../installation/requirements.md)：新增 `INST-32` 运行期桌面快捷方式查询/切换与冲突/升级保留要求。
  - [安装与环境设计](../../../installation/design.md)：在桌面入口章节补充 `internal/launch` 运行期 `.lnk` 检查、Win32 COM 实现、启动锁协调及不改写 `installation.json` 的契约。
  - [Web 交互需求](../../../web/requirements.md)：新增 `WEB-14` 并更新 `WEB-UX-03` 通用设置项列表。
  - [Web 设计](../../../web/design.md)：更新 `设置结构` 与 `设置与登录`（移除早先“不在 Web 内修改系统快捷方式”的表述）。
  - [自动化需求](../../../automation/requirements.md)、[自动化设计](../../../automation/design.md)、[覆盖清单](../../../automation/coverage.md)：更新 `AUTO-11`，记录 `settings.get` 的 `desktop_shortcut` 字段与 `settings.desktop_shortcut` 公共操作及恢复语义。
  - [开发路线图](../../roadmap.md)、[文档导航](../../../README.md)：登记本功能入口与当前验证状态。
- 契约、配置、安装、测试和用户说明变化：
  - 新增 HTTP 端点 `GET/POST /api/desktop/shortcut`，持久化格式无 schema 破坏性变更（不改动 `config.json`、`desktop-settings.json` 与 `installation.json`）。
  - 新增自动化操作 `settings.desktop_shortcut` 与 `settings.get` 返回字段 `desktop_shortcut`，已在 `contracts/automation-coverage.json` 登记。
- 日常操作的人类入口、公共操作、CLI/MCP 与等价测试；敏感人工例外、展示等价或迁移待办（见[覆盖清单](../../../automation/coverage.md)）：
  - 人类入口：Web `设置 → 通用 → 显示桌面图标`（`GET/POST /api/desktop/shortcut`）。
  - 公共操作与 CLI/MCP：`settings.get`（包含 `desktop_shortcut` 状态）及 `settings.desktop_shortcut`（写入 `enabled` 布尔值），共用 `internal/launch.ShortcutManager`。
  - 等价测试：`internal/app/desktop_shortcut_test.go` 验证 HTTP 与 `automation.Registry` 对相同安装/桌面目录的一致行为、持久收据、幂等重放、冲突拒绝及中断恢复；`tests/desktop-shortcut.test.mjs` 与 `tests/web-interactions.test.mjs` 验证前端交互、响应式错误回滚、响应丢失对账与不污染表单脏状态。

## 规模与拆分评估

- 超限文件 / 函数及物理行数：本次新增与修改的所有代码及词表文件均保持在 `<= 1000` 物理行以内，新增与修改函数均保持在 `<= 100` 物理行以内（`zh-CN.json` / `en-US.json` 约为 941 行，未超 1000 行）。
- 内聚性、拆分理由和安全性：桌面快捷方式控制器独立拆分为平台无关管理与幂等层、Windows 快捷方式管理层、Windows ShellLink COM 读写层及非 Windows 占位层；前端将开关状态机与对账逻辑放入独立的 `web/desktop-shortcut.mjs`，避免继续膨胀已约 500 行的 `web/desktop-settings.mjs`，也不在 `nodeSettings` 或 `mountApp` 内联业务逻辑。
- 用户确认记录与获准范围（触发文件拆分时）：未触发大于 1000 行文件的拆分阈值，无需额外拆分审批。
- 例外理由或后续处理：若后续支持 Linux `.desktop` 或 macOS 桌面入口，只需在 `internal/launch` 新增对应平台文件，保持公共 `ShortcutStatus` / `ShortcutManager` 与 Web/自动化契约不变。

## 验证与维护

- 仓库测试入口、复现方法和预期结果：
  - 词表生成与静态多语言守卫：`node ./scripts/sync-i18n.mjs`、`pwsh -NoProfile -File ./scripts/check-i18n.ps1`（期望全部通过，无死键、无未翻译或硬编码文本）。
  - 静态资源与覆盖守卫：`node ./scripts/verify-frontend-assets.mjs`、Node 全量前端与覆盖测试 `node --test tests/*.test.mjs`（含 `tests/desktop-shortcut.test.mjs`、`tests/web-interactions.test.mjs`、`tests/automation-coverage.test.mjs`）。
  - Go 单元、集成、静态检查及真实可执行文件 CLI/MCP 管道测试：`go test ./...`、`go vet ./...`（覆盖 `internal/launch`、PowerShell `WScript.Shell` 双向 `.lnk` 校验与 `internal/app` HTTP/CLI/MCP 等价测试）以及 `ASTERHAND_AUTOMATION_BINARY` 指定编译产物的 `TestCLIAndRealMCPStdioSharePipeBusinessOperation`。
- 前置环境和未覆盖平台：
  - 本仓工程验证在 Windows x64 环境运行；Linux / macOS 的交互桌面与实际非 Windows 构建运行在本机未实测，其能力边界以编译期 `!windows` 返回 `supported: false` 为准，需在目标平台实测后再开放对应支持。
  - 安装包打包与真实系统桌面手动视觉刷新由开发者或用户通过 `install-local.cmd` 完成本机验收。
- 相关功能与领域文档链接（明确名称，目标受版本控制）：
  - [Windows 桌面启动与托盘生命周期](../windows-desktop-lifecycle/plan.md)
  - [运行期国际化框架](../internationalization-framework/plan.md)
  - [日常操作覆盖清单](../../../automation/coverage.md)
- 当前能力边界及后续工作：
  - 当前仅支持 Windows 当前用户桌面的单个本机安装包快捷方式；登录自启开关、多节点同名安装选择与 Linux/macOS 原生桌面快捷方式留待后续按平台需求单独评估。
