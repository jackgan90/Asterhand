# 执行器与工具需求

状态：已确认边界；未实现。关联 R10、R11、R14、C03、C06。

| ID | 要求 |
|---|---|
| EXEC-01 | 任务服务依赖窄执行器契约，不绑定某个 coding harness 的会话类型 |
| EXEC-02 | 首版真实执行由 OpenClaw 调用受限本机文件工具完成 |
| EXEC-03 | 能力显式声明：事件、问答、审批、取消、查询、恢复及断连行为 |
| EXEC-04 | 任何执行必须关联 job/attempt，返回真实工具结果与产物 |
| EXEC-05 | 首版不安装、不认证、不实现具体 coding harness adapter |
| EXEC-06 | FakeExecutor 支持确定性故障脚本，仅供自动化验证 |

后续候选包括 OpenCode、Claude Code、Codex、Antigravity；它们不是首版依赖，也不假定存在等价 API。首个具体适配器由后续场景选择。
