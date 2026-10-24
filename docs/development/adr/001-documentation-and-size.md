# ADR-001：文档强制同步与规模软约束

- 状态：accepted（设计接受，尚未实现验证）
- 日期：2026-10-24
- 确认依据：本轮用户需求及后续明确收敛。

## 决策

每领域 requirements/design/adr，每个 feature 必须同步文档。文件 1000 行、函数 100 行软阈值，例外需理由；超限文件可安全拆分时先征得用户同意。

## 背景与后果

防止 AI 开发偏移目标或形成 master class；自动化检查辅助，不能代替内容审查和用户确认。

## 关联

[领域需求](../requirements.md) · [当前设计](../design.md) · [全局路线图](../../development/roadmap.md)
