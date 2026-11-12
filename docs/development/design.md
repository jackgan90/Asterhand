# 开发治理设计

状态：规范生效，已有本地 Go 测试和构建脚本；CI 尚未建立。

## 文档权威

AGENTS.md 提供开发约束和入口；overview 描述产品目标与跨领域边界；各领域 requirements/design 是相应需求与当前设计的权威。ADR 保存已接受决定及替代关系。

feature 计划连接相关领域和验收用例，不复制完整需求。大型子功能可建立自己的子目录与 requirements/design/adr，需在领域导航明确权威范围。

## 状态与追溯

计划区分 proposed、ready、in_progress、implemented、verified、accepted、released 等概念；不要求所有任务机械使用全部状态，但必须分别说明实现、验证和用户验收。

原始 Rxx / Uxx 保留，当前新增 Cxx 及各领域 ID 用于定位。迁移和范围调整记录 ADR；不会为“让旧草稿看起来一致”删除长期需求。

## 检查机制

日常操作使用[覆盖清单](../automation/coverage.md)连接人类入口、公共操作、CLI、MCP 与测试；长期规则见 DEV-13—15。注册定义是 schema、帮助和工具描述的单一来源；[机器覆盖登记](../../contracts/automation-coverage.json)维护 UI/操作/测试对应关系。`build-application.ps1` 强制运行 Go 注册覆盖和 JS UI 入口守卫，常规全量测试也包含两项检查。

新增 UI 业务必须注册操作或声明经审查的敏感/展示例外；检查 CLI/MCP 是否指向同一操作定义、是否缺输出/错误 schema、是否缺行为测试关联。静态注册无法发现所有动态 UI 行为，审查仍对照需求和真实入口。业务等价测试比较持久结果、副作用和错误，不比较本地化文字或鼠标步骤。迁移前登记待办，已迁移领域与最终全覆盖的完成门槛由[自动化计划](features/agent-automation/plan.md)定义。

后续 CI 检查 Markdown 链接、feature 文档关联、契约和生成文件一致性；每个 feature 必须有实质领域文档更新，代码审查确认内容。

代码规模检查报告手写文件物理行数与函数范围，超限输出提醒。函数检测使用对应语言的语法信息，避免以正则错误识别；生成/第三方排除规则必须可见。

超限例外、用户拆分许可及验证记录保存 feature 计划。CI 不因软阈值自动拒绝，也不能替代用户批准。

运行期国际化守卫共用 `internal/i18n/guard-rules.json` 单一规则源：`scripts/sync-i18n.mjs` 负责从 `internal/i18n/locales/` 确定性生成前端只读词表模块并按配置中的 `js_ui_sinks` 校验词表对称性、占位符一致性、HTML `data-i18n*` 绑定、显式映射字典与前端/桥接 JS 硬编码字面量；`internal/i18n/guard_test.go` 负责按配置中的 `go_desktop_ui_sinks` 与 `go_symbol_exemptions` 校验全栈键引用对称性、死键并集与受检 Go/桌面源码字面量。应用与输入宿主构建脚本、`go test`、`node --test` 及提交前快捷入口 `scripts/check-i18n.ps1` 共同阻断违规；日常开发写法与守卫盲区见 [国际化开发指南](i18n-guide.md)。

## 文档与证据的生命周期

长期文档说明当前契约、设计原因、支持边界和可复现验证方法。链接文本使用功能名称，链接目标必须受版本控制；其他维护者只克隆仓库即可理解引用。

临时执行日志、截图、机器路径、进程状态、候选摘要和聊天授权属于执行记录，不写入长期文档。原始结果保存在忽略目录；需要公开的发布证据随版本发布物归档。Git 历史保存变更过程，ADR 保存设计决策。

功能说明只引用仓库测试源码、参数定义和复现入口。目录使用功能语义名称，不用临时编号代替功能描述；代码、测试、模板和文档同步维护。完整规则见 [AGENTS.md](../../AGENTS.md#文档长期可读性)。

## 源码仓库与许可证

源码仓库为 [jackgan90/Asterhand](https://github.com/jackgan90/Asterhand)，采用 [MIT License](../../LICENSE)。保留许可证原文、版权声明及第三方许可。

本地提交前检查暂存范围、内容、许可证、凭据误提交和差异格式。提交与推送分别授权，结果以 Git refs 为准；二进制发布通过对应 Release 记录。
