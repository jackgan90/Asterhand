# ADR-AST-003 模型、agent 与安装 bundle 分层

日期：2026-10-24。状态：accepted。补充 [ADR-AST-002](002-agent-exec-runtime.md)，保留一次性 agent exec 决定。

## 决定

1. 模型 provider 描述可选模型、凭据引用和 endpoint 能力，不承担 LLM 工具循环。OpenClaw 仍执行自己的模型调用；其他 agent 有自己的 provider 转换。
2. agent runtime adapter 统一 Execute、能力描述、选择验证、安装路径发现和诊断。任务服务继续拥有持久状态、事件、交互、取消意图和重启对账。
3. 依赖 bundle 是不可变候选。清单统一驱动构建、诊断和安装；升级在隔离目录准备，结合契约、真实模型和安装回归后生成证据。

## 原因与代价

独立模型直调层会重复 agent 的工具循环，因此只抽象配置与转换。把运行时能力硬套为统一 resume/审批会制造虚假保证，因此显式声明能力，未实现为 false。首版采用静态 factory，新增 runtime 需要改源码和打包流程，避免未经审核的动态执行插件。

## 后续扩展入口

- 新模型 provider：实现 internal/llm.Provider 并登记；实现所选 runtime 的 provider bridge；补凭据/模型/endpoint 契约和真实工具验证；依赖清单加入对应插件。既有任务协议保持不变。
- 新 agent：实现 internal/executor.Runtime 并在工厂及描述列表登记；提供该 agent 的配置字段、工具桥、安装布局、诊断和打包适配；跑执行/取消/交互契约及真实模型验证。Hermes 暂无实现或兼容性结论。
- 新依赖版本：更新精确版本与来源，生成候选 lock；按 packaging/upgrade.md 验证，不把 npm 安装成功或交叉编译当作产品验收。

源码摘要与依赖摘要绑定就绪和验证证据，旧候选的 probe 结果不能自动继承到新候选。用户身份、配对、数据库与加密 key 独立保留。
