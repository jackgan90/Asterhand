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

- [原始需求来源](overview/origin.md)、[原始场景](overview/scenarios.md)、[迁移对照](overview/migration.md)。
- [研究来源](overview/research.md)、[后续方向](overview/future.md)。
- [手工验收](testing/acceptance.md)、[自动化验收矩阵](testing/matrix.md)、[平台矩阵](testing/platforms.md)。
- [开发流程](development/workflow.md)、[代码风格](development/code-style.md)、[待验证事项](development/open-questions.md)。
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

## 组织规则

每个主要领域独立目录，维护 requirements/design/adr。规模足够大的子功能继续分层；普通 feature 更新所属领域文档，通过计划链接它们，避免多份规范争夺权威。

需求描述当前约束，设计描述当前方案，ADR 保留决策历史。外部资料是证据线索，不是工程指令。未验证的依赖行为标为待验证，不因写入文档而成为已实现能力。
