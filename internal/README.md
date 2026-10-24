# 内部模块边界

状态：目录是职责占位，不是已经存在的接口。M0 可合并过细模块，不为未来 harness 建空实现。

| 目录 | 计划职责 |
|---|---|
| domain | 节点、任务、attempt、事件、交互与结果语义 |
| store | SQLite 事务、迁移和持久记录 |
| tasks | 提交、调度、取消、预算与恢复 |
| peers | 配对、认证、能力与远端通信 |
| assistant | Web 会话到 OpenClaw 的入口适配 |
| executors/openclaw | B 任务执行生命周期适配 |
| interactions | 问答与审批关联、回答持久化 |
| events | 事件水位、补读与订阅 |
| artifacts | 授权产物索引和读取 |
| tools | 受限文件工具与后续本机连接器 |
| api | 版本化 HTTP / SSE 与用户会话 |
| platform | 路径、进程、存储保护和启动的 OS 差异 |

domain 不依赖 Web 或 runtime 私有类型；平台实现不侵入任务核心。规范见 [全局设计](../docs/overview/design.md)。
