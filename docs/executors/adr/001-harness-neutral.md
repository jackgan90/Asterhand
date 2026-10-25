# ADR-001：执行器契约保持独立

- 状态：accepted（设计接受，尚未实现验证）
- 日期：2026-10-24
- 确认依据：产品需求与范围决策，详见所属领域 requirements/design。

## 决策

只定义实际需要的执行生命周期与能力契约。首版 OpenClaw 调用受限文件工具；不安装或登录具体 coding harness。

## 背景与后果

未来可接入 OpenCode、Claude Code、Codex、Antigravity；不假设能力等价，不提前建空 adapter。

## 关联

[领域需求](../requirements.md) · [当前设计](../design.md) · [全局路线图](../../development/roadmap.md)
