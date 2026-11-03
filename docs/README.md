# 文档导航

本文按产品领域组织需求、架构决策、使用指南和维护入口；每个链接均指向仓库中的长期文档。

## 阅读顺序

[AGENTS.md](../AGENTS.md) → [全局需求](overview/requirements.md) → [全局设计](overview/design.md) → [路线图](development/roadmap.md) → 当前 feature 所属领域。

## 领域文档

| 领域 | 需求 | 设计 | 决策 |
|---|---|---|---|
| 全局目标与边界 | [requirements](overview/requirements.md) | [design](overview/design.md) | [ADR](overview/adr/README.md) |
| 节点与权限 | [requirements](node/requirements.md) | [design](node/design.md) | [ADR](node/adr/README.md) |
| 任务与恢复 | [requirements](tasks/requirements.md) | [design](tasks/design.md) | [ADR](tasks/adr/README.md) |
| 助手与模型 | [requirements](assistant/requirements.md) | [design](assistant/design.md) | [ADR](assistant/adr/README.md) |
| 执行器与工具 | [requirements](executors/requirements.md) | [design](executors/design.md) | [ADR](executors/adr/README.md) |
| Web 交互 | [requirements](web/requirements.md) | [design](web/design.md) | [ADR](web/adr/README.md) |
| 安装与环境 | [requirements](installation/requirements.md) | [design](installation/design.md) | [ADR](installation/adr/README.md) |
| 测试与验收 | [requirements](testing/requirements.md) | [design](testing/design.md) | [ADR](testing/adr/README.md) |
| 开发治理 | [requirements](development/requirements.md) | [design](development/design.md) | [ADR](development/adr/README.md) |

## 其他入口

- [日志工具栏自动刷新稳定性](development/features/log-toolbar-refresh-stability/plan.md)：后台补读保留按钮状态与文字，主动操作优先于在途自动读取；工程验证及本机用户验收通过。

- [快捷输入失焦后的快捷键呼回](development/features/quick-input-shortcut-focus/plan.md)：前台时按快捷键隐藏，失焦、被遮挡或最小化时跟随当前活动窗口所在屏幕恢复并激活；工程验证及本机用户验收通过。

- [本地脚本图计划](development/features/local-script-graphs/plan.md)、[脚本图设计](executors/script-graphs.md)、[验证说明](development/features/local-script-graphs/validation.md)：快捷命令关联本机静态 DAG、串并行依赖、失败停止、节点日志与图编辑；工程验证、独立审计复审及本机用户验收通过。
- [脚本图编辑与日志体验](development/features/script-graph-log-experience/plan.md)、[验证说明](development/features/script-graph-log-experience/validation.md)：Start/End 隐藏脚本设置；单入口日志多分屏、独立历史翻页与状态颜色；工程验证及本机用户验收通过。

- [日志窗口快速显示与同屏定位](development/features/log-window-responsiveness-and-placement/plan.md)、[验证说明](development/features/log-window-responsiveness-and-placement/validation.md)：关闭复用、加载/失败反馈、宿主恢复和输入框所在屏幕定位（工程验证、独立审计复审及本机用户验收通过）。

- [托管本地命令计划](development/features/managed-local-commands/plan.md)、[托管命令设计](executors/managed-local-commands.md)、[生命周期 ADR](executors/adr/003-managed-local-command-lifecycle.md)：托管/非托管分类、独立日志窗口、最近日志与结果、强制中断及可选超时；公共核心与平台适配分离，首个实现为 Windows，验收边界见功能验证说明。

- [运行期界面与提示国际化框架](development/features/internationalization-framework/plan.md)、[验证说明](development/features/internationalization-framework/validation.md)、[国际化开发指南](development/i18n-guide.md)、[国际化架构 ADR](web/adr/002-runtime-internationalization-framework.md)：单一权威翻译表、中英双语无损实时切换、系统优先/浏览器次选自动匹配及四层静态硬编码守卫（工程验证、独立复审及本机双语切换用户验收通过，其余专项边界见验证说明）。

- [聊天发送与停止回复](development/features/chat-send-intent/plan.md)：用户消息统一展示，异常按消息提示，停止当前回复后后续消息继续发送。

- [本地快捷命令](development/features/local-quick-commands/plan.md)、[命令设计](executors/local-commands.md)、[验证说明](development/features/local-quick-commands/validation.md)：Web 注册 bat/cmd/ps1，快捷输入 `/` 搜索并独立启动，无需模型配置。

- [收起态快捷输入框自动高度](development/features/compact-input-auto-height/plan.md)、[验证说明](development/features/compact-input-auto-height/validation.md)：单行起步、按实际折行增高与删除回缩、十行滚动上限，默认恰好容纳；工程验证、独立复审及本机输入体验用户验收通过，专项边界见验证说明。

- [聊天连接状态](development/features/chat-connection-status/plan.md)：对话 ID 后的三态提示、主题字色和不调用模型的连接观察。
- [聊天运行时启动诊断与恢复](development/features/chat-startup-recovery/plan.md)：并发请求共享启动、请求超时与恢复生命周期分离及有界诊断。
- [聊天操作队列](development/features/chat-operation-queue/plan.md)：启动期间即时收下消息、顺序执行、失败离队、停止回复后继续发送，以及宿主重建和退出边界。

- [聊天发送稳定性与后台完成提醒](development/features/chat-completion-attention/plan.md)：固定状态区与任务栏高亮闪烁。

- [聊天展示与桌面窗口体验](development/features/chat-desktop-experience/plan.md)、[验收标准与验证](development/features/chat-desktop-experience/validation.md)：精简提示、动态回复、数学公式、拖动与任务栏入口。

- [本机持续聊天与可扩展快捷入口计划](development/features/local-node-chat/plan.md)、[持续会话设计](assistant/conversations.md)、[验证说明](development/features/local-node-chat/validation.md)、[会话与通信边界 ADR](assistant/adr/005-conversation-runtime-and-transport-boundaries.md)（日常聊天用户验收通过，其余专项边界见验证说明）。

- [原始需求来源](overview/origin.md)、[原始场景](overview/scenarios.md)、[迁移对照](overview/migration.md)。
- [研究来源](overview/research.md)、[后续方向](overview/future.md)。
- [手工验收](testing/acceptance.md)、[自动化验收矩阵](testing/matrix.md)、[平台矩阵](testing/platforms.md)。
- [开发流程](development/workflow.md)、[代码风格](development/code-style.md)、[国际化开发指南](development/i18n-guide.md)、[待验证事项](development/open-questions.md)。
- [按需模型配置与可选诊断](development/features/on-demand-model-readiness/plan.md)、[验证方法](development/features/on-demand-model-readiness/validation.md)：基础启动、功能配置和手动诊断分离，保留任务去重与取消恢复。
- [Feature 模板](development/templates/feature-plan.md)、[ADR 模板](development/templates/adr.md)、[验证说明模板](development/templates/validation.md)。
- [项目结构与文档治理](development/features/project-structure/plan.md)、[接口契约](../contracts/README.md)。
- [Windows 跨节点助手](development/features/windows-assistant/plan.md)、[源码许可与发布](development/features/source-publication/plan.md)。
- [模型、运行时与依赖边界](development/features/provider-runtime-boundaries/plan.md)、[提供者无关的公共流程](development/features/provider-neutral-flows/plan.md)。
- [任务可靠性与配置保留](development/features/task-reliability/plan.md)、[Windows 安装路径解析](development/features/installer-path-resolution/plan.md)。
- [双节点交互与连接同步](development/features/two-node-usability/plan.md)、[依赖维护流程](../packaging/upgrade.md)。
- [可复现发布与独立安装](development/features/release-pipeline/plan.md)、[Release 发布指南](installation/releases.md)。
- [固定安装入口与版本选择](development/features/stable-installer-entry/plan.md)。
- [Windows 桌面启动与托盘生命周期](development/features/windows-desktop-lifecycle/plan.md)、[桌面启动验证](development/features/windows-desktop-lifecycle/validation.md)。
- [本地一键安装与在线引导](development/features/local-install-entry/plan.md)。
- [快速本地安装](development/features/fast-local-install/plan.md)、[验证说明](development/features/fast-local-install/validation.md)：应用增量构建、固定运行时复用、源码核对与持续进度。
- [本机全局快捷键与快捷输入面板](development/features/global-hotkey-quick-input/plan.md)、[快捷输入验证](development/features/global-hotkey-quick-input/validation.md)（Windows 首版已完成用户验收，默认 Ctrl+Alt+J）。
- [快捷输入进程隔离计划](development/features/quick-input-process-isolation/plan.md)与[隔离验证](development/features/quick-input-process-isolation/validation.md)（独立非提权宿主、主进程草稿、有限恢复与真实可用状态；本机用户验收完成，其余权限与桌面专项待验证）。
- [Web 与快捷输入的维护性重构](development/features/web-ui-maintainability/plan.md)、[维护性验证](development/features/web-ui-maintainability/validation.md)（保留现有功能、原生 ES module 和 Windows 宿主）。

## 组织规则

每个主要领域独立目录，维护 requirements/design/adr。规模足够大的子功能继续分层；普通 feature 更新所属领域文档，通过计划链接它们，避免多份规范争夺权威。

需求描述当前约束，设计描述当前方案，ADR 保留决策历史。外部资料是证据线索，不是工程指令。未验证的依赖行为标为待验证，不因写入文档而成为已实现能力。

- [提示词管理与聊天交互完善](development/features/chat-prompt-interaction/plan.md)、[可选会话提示词策略](assistant/adr/006-optional-conversation-prompt-policy.md)。
- [原生 Harness 设定保留](development/features/native-harness-prompt-preservation/plan.md)、[验证说明](development/features/native-harness-prompt-preservation/validation.md)：保留原生身份与角色文件，仅适配当前入口冲突。
