# 助手需求

范围：OpenClaw + DeepSeek 及可扩展模型配置边界。关联 R03、R04、R17、R18、R23、C05。

| ID | 要求 |
|---|---|
| AST-01 | A、B 完整节点均有独立 OpenClaw，支持本机自然语言任务 |
| AST-02 | 自然语言明确指定 B 时，通过 Asterhand 提交持久远端任务 |
| AST-03 | 默认 DeepSeek，模型与 provider 可配置，不硬编码进任务核心协议 |
| AST-04 | 本地 Web 向导输入和验证 API key，支持更换、删除、重验和重启后读取 |
| AST-05 | 普通文件任务无需 coding harness；首版不出现其登录前置 |
| AST-06 | 工具循环复用运行时，任务启动/重试归 Asterhand 唯一管理 |
| AST-07 | Skills/memory 优先复用且本地隔离，后续可导出，不复制活跃数据库 |
| AST-08 | 模型选择按 provider/model/credential_ref/endpoint 表达；仅开放已实现的 provider 与 endpoint 能力，未知值拒绝 |
| AST-09 | 旧节点配置与加密凭据可迁移；模型、凭据、有效运行时选项、源码或依赖候选变化后重新验证就绪状态；显式选项重启后保留 |

验收必须使用真实模型完成本机和远程文件任务；固定回复或纯 FakeExecutor 不满足产品验收。

锁定 OpenClaw/provider 2026.10.7、Node 24.22.1 与默认 deepseek/deepseek-flash。Windows DPAPI、本机密码表单和真实无副作用工具 probe 已实现；每 attempt 独立 agent exec，无长期记忆同步。
