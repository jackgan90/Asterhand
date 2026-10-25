# ADR-001：单机快测与真实设备验收

- 状态：accepted（设计接受，尚未实现验证）
- 日期：2026-10-24
- 确认依据：产品需求与范围决策，详见所属领域 requirements/design。

## 决策

无 key 单机多进程、FakeExecutor、故障代理与可控时钟负责快速回归；真实 OpenClaw + DeepSeek 和两 Windows 负责可见验收。

## 背景与后果

平台不可用记录 skipped/blocked，发布必要条件缺失阻止发布；WSL 和交叉编译不替代 Mac 实测。

## 关联

[领域需求](../requirements.md) · [当前设计](../design.md) · [全局路线图](../../development/roadmap.md)
