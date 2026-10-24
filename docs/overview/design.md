# 全局设计

状态：已确认架构方向；接口、依赖版本及 OS adapter 细节待 M0 验证。当前无功能实现。

## 系统边界

```text
浏览器 → Asterhand A Web/API → OpenClaw A（DeepSeek）
                               ↓ 提交工具
                         Asterhand A 发送队列
                               ↓ 版本化 HTTPS
                         Asterhand B 持久任务服务
                               ↓ 执行适配器
                         OpenClaw B（DeepSeek）
                               ↓ 受限工具
                         B 授权目录内的文件

A / B 页面 ← B 权威任务、事件、交互、结果
```

A 的助手理解意图并提交结构化目标；B 的助手在 B 上完成已接收任务。两者通过任务边界协作，不复制会话、不维持同步 RPC 执行链。工具注册与异步会话控制能力必须在 M0 真实验证。

每台完整 Windows 节点运行相同组件。低配 Linux 可只启用任务服务、FakeExecutor 或确定性工具；参与网络不强制安装完整助手或 coding harness。

## 组件职责

| 组件 | 职责 |
|---|---|
| Asterhand 核心服务（计划进程名 asterhandd） | 身份、配对、任务、持久事件、交互、取消、恢复、产物引用 |
| OpenClaw | LLM 工具循环、会话、自然语言、运行时 skills/memory |
| OpenClaw 执行适配器 | job/attempt 与会话映射、事件和交互转换、生命周期核对 |
| 受限本机工具 | 参数与路径校验、文件操作和真实结果，不接受任意 shell 字符串 |
| Web | 统一聊天、节点、任务页与配置向导，静态资源随服务分发 |
| SQLite | 各节点自己的任务、attempt、队列、事件、交互和去重记录 |
| 本地产物目录 | 文件或报告，通过授权 artifact ID 读取 |
| 环境准备 | 引导安装、能力选择、诊断、配置和启动 |

Go 是核心服务语言，运行节点使用预编译程序，无需安装 Go。SQLite 驱动优先避免 cgo；精确驱动、Go 版本、前端工具和最低 OS 版本 M0 固定。进程和路径行为使用窄 OS adapter。

## 状态权威

- B 对已接收 job 的归属、attempt、终态和交互拥有权威。
- A 的远端视图是带更新时间的缓存；失联不能改写 B 的任务状态。
- runtime 管内部工具循环，Asterhand 管 job 启动、取消、重试；两层不能独立重试同一工作。
- 文件变化由工具证据确认，不只依赖模型回复。
- 人工验收独立于执行结果和机器验证。

## 协议与部署

先实现版本化 HTTP + SSE，HTTPS 与独立 peer 凭据；不宣称 A2A 兼容。浏览器使用人类用户会话。OpenClaw 管理接口保留本机内部，具体认证 M0 验证。

使用 LAN / 已有 Tailnet，无固定业务中心。浏览器证书信任与网络部署在 [节点设计](../node/design.md) 限定并实测，不自建 NAT 穿透。

每节点初始只允许一个有副作用的执行任务，并独占对应授权工作区；查询和进度读取不占执行槽。自动继续委派深度为零，远端执行不自动派生跨节点任务。

## 安全与扩展

LLM 提出工具调用，目标节点校验权限；角色指令不构成权限控制。路径、工具和审批需在执行处校验。

未来 coding harness 通过执行契约接入，各自能力可不同。首版只定义服务于实际需求的契约，不提前实现任意插件框架。

## 相关领域

[节点](../node/design.md) · [任务](../tasks/design.md) · [助手](../assistant/design.md) · [执行器](../executors/design.md) · [Web](../web/design.md) · [安装](../installation/design.md) · [测试](../testing/design.md)
