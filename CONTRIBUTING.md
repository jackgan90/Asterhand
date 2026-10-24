# 贡献指南

当前仅含文档和骨架。开始功能实现前阅读 [AGENTS.md](AGENTS.md)。

## Feature 交付

1. 按 [模板](docs/development/templates/feature-plan.md) 建立计划。
2. 补齐领域需求、设计和必要 ADR。
3. 在实施授权范围内实现，同步测试和文档。
4. 按 [验证规范](docs/testing/design.md) 记录结果和限制。
5. 按 [开发流程](docs/development/workflow.md) 检查完成条件。

单文件 1000 行、单函数 100 行为软阈值；文件超限后的结构化拆分遵循用户确认流程，见 [代码风格](docs/development/code-style.md)。

## 本地工具与发布

尚未选定 Go 版本、SQLite 驱动、前端工具链和 module 地址，没有可执行测试命令；这些是 M0 交付项。运行节点与开发本项目的环境分开管理。

Feature 变更应包含对应代码、测试和文档；纯文档交付无需伪造代码或测试。仓库为 [jackgan90/Asterhand](https://github.com/jackgan90/Asterhand)，项目采用 [MIT License](LICENSE)。首次二进制发布前完成第三方依赖许可证、版本和安装包校验。发布或推送按用户授权执行。
