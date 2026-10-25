# 模型、运行时与依赖边界验证

运行 [Windows 助手基础检查](../windows-assistant/validation.md) 后，重点检查以下契约：

| 契约 | 源码入口 | 预期行为 |
|---|---|---|
| 模型与凭据选择 | [配置选择测试](../../../../internal/config/selection_test.go)、[provider 测试](../../../../internal/llm/provider_test.go) | 未知选择拒绝，旧配置迁移保持身份及凭据 |
| 运行时能力与结果 | [执行器契约](../../../../internal/executor/contract_test.go) | 成功、失败、取消有明确结果，未实现能力不公布 |
| 包描述与版本 | [包测试](../../../../internal/environment/bundle_test.go)、[清单测试](../../../../packaging/catalog_test.go) | 程序、描述与组件版本一致，缺失或漂移拒绝 |
| 插件回调 | [插件契约](../../../../tests/plugin-contract.test.mjs) | 工具调用受模式限制，秘密不进入公开结果 |

依赖变化必须使用 [依赖维护流程](../../../../packaging/upgrade.md) 重建并执行真实模型与安装验证。运行报告绑定源码和依赖摘要；旧程序的结果不能用作新构建的实测结果。
