# 模型、运行时与依赖边界

## 目标与范围

关联 R17、C12、AST-03/04/08/09、EXEC-01/03/07/08、INST-06/11/12。将模型选择、执行运行时和安装依赖分为独立职责，使扩展实现不侵入任务与节点协议。

首版只注册 DeepSeek + OpenClaw；新增提供者需要自己的适配器与验证，不登记占位能力。

## 设计

- 模型选择包含 provider、model、credential_ref 和可选 endpoint，由 provider 校验模型及 endpoint 能力。
- runtime adapter 提供描述、能力、发现、诊断和 Execute；任务层只依赖执行契约，不推断原生 resume 或审批能力。
- 旧配置迁移保留身份和密钥文件；未知 provider/runtime 显式拒绝。
- 依赖清单统一驱动内嵌诊断、构建和安装；升级使用隔离目录与锁文件，缺少必要验证时拒绝宣布升级就绪。

权威文档：[助手](../../../assistant/design.md)、[执行器](../../../executors/design.md)、[安装](../../../installation/design.md)、[边界决策](../../../assistant/adr/003-provider-runtime-bundle-boundaries.md)。

## 验证与规模

见 [契约验证方法](validation.md)。实现分为 provider、运行时工厂、适配器和包清单，不建立通用动态插件系统。CLI 启动函数保持锁、初始化和退出清理的顺序，规模例外见 [任务可靠性说明](../task-reliability/plan.md)。
