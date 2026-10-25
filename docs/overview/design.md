# 全局设计

本文描述 Windows 跨节点助手的当前架构。接口与数值由各领域契约定义，验证能力与平台限制见 [支持矩阵](../testing/platforms.md)。

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

A 的助手理解意图并提交结构化目标；B 的助手在 B 上完成已接收任务。两者通过任务边界协作，不复制会话、不维持同步 RPC 执行链。运行时工具与生命周期能力必须通过真实调用验证。

每台完整 Windows 节点运行相同组件。当前发布只启用 Windows x64，Linux/macOS 通过平台注册表预留，尚未接入正式安装。FakeExecutor 只用于测试，不暴露为产品能力。

## 组件职责

| 组件 | 职责 |
|---|---|
| Asterhand 核心服务（asterhand.exe / asterhand） | 身份、配对、任务、持久事件、交互、取消、恢复、产物引用 |
| OpenClaw | LLM 工具循环、会话、自然语言、运行时 skills/memory |
| OpenClaw 执行适配器 | job/attempt 与会话映射、事件和交互转换、生命周期核对 |
| 受限本机工具 | 参数与路径校验、文件操作和真实结果，不接受任意 shell 字符串 |
| Web | 统一聊天、节点、任务页与配置向导，静态资源随服务分发 |
| SQLite | 各节点自己的任务、attempt、队列、事件、交互和去重记录 |
| 本地产物目录 | 文件或报告，通过授权 artifact ID 读取 |
| 环境准备 | 安装器下载校验组件后退出；用户随后启动、配置与诊断 |

Go 是核心服务语言，运行节点使用预编译程序，无需安装 Go。SQLite 使用纯 Go 驱动 modernc.org/sqlite 1.61.0，Go 1.27.3，Web 为原生 ES module；Windows 10+ x64 为安装器门槛，实际测试 OS 见平台矩阵。进程和路径行为使用窄 OS adapter。

## 状态权威

- B 对已接收 job 的归属、attempt、终态和交互拥有权威。
- A 的远端视图是带更新时间的缓存；失联不能改写 B 的任务状态。
- runtime 管内部工具循环，Asterhand 管 job 启动、取消、重试；两层不能独立重试同一工作。
- 文件变化由工具证据确认，不只依赖模型回复。
- 人工验收独立于执行结果和机器验证。

## 协议与部署

先实现版本化 HTTP + SSE，HTTPS 与独立 peer 凭据；不宣称 A2A 兼容。浏览器使用人类用户会话。OpenClaw 管理接口保留本机内部，认证由执行适配器管理。

使用 LAN / 已有 Tailnet，无固定业务中心。浏览器证书信任与网络部署在 [节点设计](../node/design.md) 限定并实测，不自建 NAT 穿透。

每节点初始只允许一个有副作用的执行任务，并独占对应授权工作区；查询和进度读取不占执行槽。自动继续委派深度为零，远端执行不自动派生跨节点任务。

## 安全与扩展

LLM 提出工具调用，目标节点校验权限；角色指令不构成权限控制。路径、工具和审批需在执行处校验。

未来 coding harness 通过执行契约接入，各自能力可不同。首版只定义服务于实际需求的契约，不提前实现任意插件框架。

将模型选择（provider/model/credential_ref/endpoint）、agent Runtime adapter（执行/能力/发现/诊断）和安装 bundle（单一清单/锁/升级验证）分开。首版仅实现 DeepSeek/OpenClaw；新增 provider 或 Hermes 通过各自 adapter 接入，任务与节点协议保留。见 [ADR-AST-003](../assistant/adr/003-provider-runtime-bundle-boundaries.md)。

将公共配置改为按 runtime ID 隔离的选项 JSON，并由适配器声明受保护目录。安装包采用通用组件与目标描述，公共流程只分派描述，不按具体模型或 agent 名称判断；旧配置的厂商名称只留在兼容迁移器。见 [ADR-AST-004](../assistant/adr/004-provider-neutral-public-flows.md)。

## 相关领域

[节点](../node/design.md) · [任务](../tasks/design.md) · [助手](../assistant/design.md) · [执行器](../executors/design.md) · [Web](../web/design.md) · [安装](../installation/design.md) · [测试](../testing/design.md)

## 首版部署与实测

Windows 包含 Go 1.27.3 编译产物、Node 24.22.1、OpenClaw/provider 2026.10.7；纯 Go SQLite 驱动 modernc.org/sqlite 1.61.0。浏览器只连接本机，跨设备通信通过 Tailscale 上的独立 TLS API。用户批准的 Tailscale 引导见 [ADR](../node/adr/002-tailscale-connectivity.md)。

每次输入经 A 的 route attempt，再由选定节点持久接收 execute attempt；OpenClaw 使用一次性 agent exec 运行。状态查询来自 SQLite，取消使用 OS 进程树控制，重启进入人工核对。尚未提供 runtime 原生 resume、远端审批与手机远程登录，具体限制见 [平台矩阵](../testing/platforms.md)。
