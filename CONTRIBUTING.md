# 贡献指南

开始开发前阅读 [AGENTS.md](AGENTS.md)，确认产品范围和维护规则。

## Feature 交付

1. 按 [模板](docs/development/templates/feature-plan.md) 建立计划。
2. 补齐领域需求、设计和必要 ADR。
3. 在实施授权范围内实现，同步测试和文档。
4. 按 [验证规范](docs/testing/design.md) 记录结果和限制。
5. 按 [开发流程](docs/development/workflow.md) 检查完成条件。

单文件 1000 行、单函数 100 行为软阈值；文件超限后的结构化拆分遵循用户确认流程，见 [代码风格](docs/development/code-style.md)。

## 文档维护

文档面向不了解开发对话的维护者，按功能说明当前行为和操作方法。引用使用可理解的名称和受版本控制的目标；临时验证日志、本机路径和候选流水不进入长期文档。详细要求见 [文档长期可读性](AGENTS.md#文档长期可读性)。

## 本地工具与发布

开发使用 Go 1.27.3，执行 go test ./... 与 go vet ./...；完整包构建见 scripts/README.md，固定依赖见 packaging。运行节点与开发本项目的环境分开管理。

Feature 变更应包含对应代码、测试和文档；纯文档交付无需伪造代码或测试。仓库为 [jackgan90/Asterhand](https://github.com/jackgan90/Asterhand)，项目采用 [MIT License](LICENSE)。首次二进制发布前完成第三方依赖许可证、版本和安装包校验。发布或推送按用户授权执行。
