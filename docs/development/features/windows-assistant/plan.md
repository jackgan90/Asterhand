# Windows 跨节点助手

## 目标与范围

关联 R01—R09、R17—R18、C02—C05，涉及节点、任务、助手、执行器、Web 和安装领域。用户在 A 发出自然语言请求，B 使用自己的运行时、凭据和授权目录完成任务，返回可检查的文件结果。

实现为 Go + SQLite 持久服务、OpenClaw + DeepSeek、内嵌 Web 和 Windows 安装包。Linux 基础包只提供有限服务能力，具体支持见 [平台矩阵](../../../testing/platforms.md)。

## 设计与边界

- 独立节点通过 TLS、一次性邀请和固定证书配对；模型凭据留在目标节点。
- 每个输入先路由，再由目标持久接收 execute attempt；固定 owner、单执行槽、单次尝试。
- 文件创建与回读经过授权目录、幂等和证据校验；普通问答、取消和恢复进入持久状态。
- 浏览器仅连接本机；后台任务独立于页面生命周期。
- 具体 coding harness、任意 shell、自动接管、全网会话同步和原生手机应用不在首版范围。

权威设计：[总览](../../../overview/design.md)、[节点](../../../node/design.md)、[任务](../../../tasks/design.md)、[助手](../../../assistant/design.md)、[执行器](../../../executors/design.md)、[Web](../../../web/design.md)、[安装](../../../installation/design.md)。

## 验证与规模

自动化和人工入口见 [验证方法](validation.md)。产品代码按配置、存储、任务、执行器、节点传输、Web 与安装分离；函数规模例外在 [任务可靠性](../task-reliability/plan.md) 和 [双节点交互](../two-node-usability/plan.md) 中说明。生成依赖锁与第三方代码单独分类。
