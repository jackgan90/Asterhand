# 原草稿迁移记录

本文保留原始产品草案的章节与设计决定映射，帮助维护者追溯需求来源。当前行为以各领域 requirements/design 和有效 ADR 为准；原草案的交接提示词不作为开发指令。

## 内容去向

| 原节 | 迁移位置与处理 |
|---|---|
| 1 目的、证据与状态 | [文档导航](../README.md)、本文、[研究来源](research.md) |
| 2 原始需求 | [全局需求](requirements.md)，保留 R01—R29 并更新首版状态 |
| 3 场景 | [场景](scenarios.md)，保留 U01—U07，当前验收独立维护 |
| 4 调研与复用 | [研究来源](research.md)，保留来源与线索，去除过时选择的当前效力 |
| 5 架构 | [全局设计](design.md) |
| 6 MVP | [全局需求](requirements.md)、[范围 ADR](adr/002-mvp-scope.md) |
| 7 对象与 ID | [任务设计](../tasks/design.md)、[节点设计](../node/design.md) |
| 8 可靠性 | [任务设计](../tasks/design.md) |
| 9 协议与适配器 | [执行器](../executors/design.md)、[契约目录](../../contracts/README.md) |
| 10 网络与权限 | [节点](../node/design.md)、[任务](../tasks/design.md) |
| 11 交互 | [Web](../web/design.md)、[任务](../tasks/design.md) |
| 12 角色、模型与调度 | [助手](../assistant/design.md)、[后续方向](future.md) |
| 13 Memory / 自进化 | [助手](../assistant/design.md)、[后续方向](future.md) |
| 14 本机工具 / 仓库 | [执行器](../executors/design.md)、[后续方向](future.md) |
| 15 Scheduler / 渠道 | [后续方向](future.md) |
| 16 D01—D15 | 下表映射到当前 ADR / 设计，不沿用过期选型 |
| 17 研发阶段（接口、单机、多机、交付） | [路线图](../development/roadmap.md)，移除 harness 前置并提前恢复验证 |
| 18 原开发工作包 | [按能力组织的路线图](../development/roadmap.md)，替换旧工作包；[工程职责](../../internal/README.md) |
| 19 A01—A17 | [矩阵](../testing/matrix.md)，保留编号，编码改文件任务；新增 A18—A30 |
| 20 风险与问题 | 各领域设计和 [待验证事项](../development/open-questions.md) |
| 21 交接提示词 | 由 [AGENTS.md](../../AGENTS.md) 与 [开发流程](../development/workflow.md) 取代，不作为运行指令保留 |
| 附录 A | [原始描述](origin.md)，保留用户原文 |
| 附录 B / S01—S30 | [研究来源](research.md)，保留全部 30 项来源 |

## 原技术决定的当前状态

| 原 ID | 当前状态 |
|---|---|
| D01 节点自治 | 保留；区分完整节点和能力精简节点 |
| D02 OpenClaw | 从优先候选收敛为首版已选运行时 |
| D03 Go | 保留为核心语言，版本由依赖清单固定 |
| D04 SQLite | 保留，驱动与持久化策略见任务和存储设计 |
| D05 首个 Codex executor | 已被后续用户决定替代：具体 harness 全部后置 |
| D06 DeepSeek | 已确认默认 provider，本地配置 key |
| D07 静态 peer / 指定目标 | 保留，并补充配对向导 |
| D08 一个编码执行槽 | 调整为首版每节点一个有副作用任务槽 |
| D09 固定 owner | 保留 |
| D10 HTTP + SSE 任务页 | 收敛为统一 Web 聊天与任务入口 |
| D11 最小 A2A 或 adapter | 收敛为内部版本化协议，A2A 后置 |
| D12 LAN / Tailnet | 保留 |
| D13 手机浏览器 | 保留 |
| D14 推送 / 微信后置 | 保留 |
| D15 可迁移知识 | 保留长期方向，首版不自建同步 |

## 维护入口

需求原文见 [原始描述](origin.md)，目录与追溯规则见 [项目结构与文档治理](../development/features/project-structure/plan.md)。
