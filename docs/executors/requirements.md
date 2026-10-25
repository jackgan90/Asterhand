# 执行器与工具需求

状态：已确认边界；实现契约见本领域 design.md，平台边界见测试文档。关联 R10、R11、R14、C03、C06。

| ID | 要求 |
|---|---|
| EXEC-01 | 任务服务依赖窄执行器契约，不绑定某个 coding harness 的会话类型 |
| EXEC-02 | 首版真实执行由 OpenClaw 调用受限本机文件工具完成 |
| EXEC-03 | 能力显式声明：事件、问答、审批、取消、查询、恢复及断连行为 |
| EXEC-04 | 任何执行必须关联 job/attempt，返回真实工具结果与产物 |
| EXEC-05 | 首版不安装、不认证、不实现具体 coding harness adapter |
| EXEC-06 | FakeExecutor 支持确定性故障脚本，仅供自动化验证 |
| EXEC-07 | Agent runtime 由工厂选择，adapter 自己管理执行、配置校验、安装布局发现和诊断；未知 runtime 不回退 |
| EXEC-08 | 适配器必须通过结果、失败、取消、工具模式和凭据边界契约；新增运行时不修改任务和节点协议 |
| EXEC-09 | 公共配置/CLI 不声明提供者专用字段；运行时选项由适配器校验并声明全部已知受保护目录，公共授权检查拒绝重叠和目录别名绕过 |

后续候选包括 OpenCode、Claude Code、Codex、Antigravity；它们不是首版依赖，也不假定存在等价 API。首个具体适配器由后续场景选择。

实际接口为 Execute(ctx, Run)，查询/问答/取消/恢复由任务服务和工具回调提供。文本 create-only、64 KiB、os.OpenRoot、操作日志与回读摘要已实现；未支持的 native resume/审批不声明为可用。
