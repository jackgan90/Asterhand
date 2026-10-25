# ADR-AST-002：首版采用 agent exec 与受限回调插件

状态：接受。日期：2026-10-24。细化 ADR-AST-001 的正式接口选择，不替换 OpenClaw + DeepSeek。

## 验证结果与决定

固定 OpenClaw/provider 2026.10.7 和 Node 24.22.1。真实 `agent exec --json --message-file - --code-mode direct` 能加载 DeepSeek provider 和自有插件，完成 probe、创建文件、普通问答。首个 probe 因隐含 coding profile 过滤自有工具而失败；改为 full profile 加精确 allow 和内置工具组 deny 后通过。

每个 attempt 使用独立 state/config/workspace、一次性 callback token。Go 服务持有持久任务和工具操作，OpenClaw 完成模型循环；不依赖 Gateway 常驻会话或外部 coding harness。路由 attempt 保存 outbox，执行 attempt 只能操作本机已授权项目。

## 生命周期与限制

Windows Job Object 监督进程树，目标在成功分配 Job 后才写入 stdin。取消关闭执行树，待 Go 工具回调完成才释放执行槽。runtime 状态留在本地用于核对，服务重启不启动旧 attempt。

当前不公布原生 resume、远端审批、长期会话或共享 memory 能力。普通问答通过持久 interaction 和阻塞工具回调实现。所有外部文件工具均由 Go 的 os.OpenRoot 和 create-only 策略限制。

版本来源见 [依赖清单](../../../packaging/dependencies.json)，验证方法见 [助手契约](../../development/features/windows-assistant/validation.md)。
