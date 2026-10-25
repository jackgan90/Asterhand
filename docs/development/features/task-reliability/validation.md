# 任务可靠性与配置保留验证

| 行为 | 源码入口 | 预期结果 |
|---|---|---|
| 日志预算与拒绝新副作用 | [任务上限](../../../../internal/tasks/limits_test.go)、[存储操作](../../../../internal/store/operation_test.go) | 普通预算耗尽后拒绝新写入，已有证据及收尾仍有界可写 |
| 满容量恢复与失败停止 | [恢复测试](../../../../internal/app/recovery_budget_test.go) | 旧节点可进入待核对，收尾失败不启动后续任务 |
| 问答与取消竞争 | [任务可靠性](../../../../internal/tasks/reliability_test.go)、[交互测试](../../../../internal/app/interaction_test.go) | 取消持久化前后窗口均不会被问答状态覆盖 |
| 显式与自动运行时路径 | [发现测试](../../../../internal/executor/discovery_test.go) | 显式路径保留，自动路径按包迁移 |
| 就绪摘要 | [执行器契约](../../../../internal/executor/contract_test.go) | 旧或失效摘要要求重新验证 |

按 [基础检查](../windows-assistant/validation.md) 运行测试，并按 [安装升级流程](../../../../packaging/upgrade.md) 验证身份、凭据字节、配置和数据保留。故障测试使用隔离数据库/进程；不操作真实任务或复制真实凭据。
