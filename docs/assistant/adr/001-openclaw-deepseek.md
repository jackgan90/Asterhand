# ADR-001：OpenClaw 与默认 DeepSeek

- 状态：accepted（设计接受，尚未实现验证）
- 日期：2026-10-24
- 确认依据：本轮用户需求及后续明确收敛。

## 决策

首版完整节点接入 OpenClaw，默认 DeepSeek，在本地 Web 配置 key 并验证工具调用。A、B 独立保存凭据。

## 背景与后果

已收敛运行时选择；精确版本和接口仍需 M0 真实验证。统一 UI 不重写 LLM 工具循环。

## 关联

[领域需求](../requirements.md) · [当前设计](../design.md) · [全局路线图](../../development/roadmap.md)
