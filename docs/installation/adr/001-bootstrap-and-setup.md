# ADR-001：薄引导与能力化环境准备

- 状态：accepted（设计接受，尚未实现验证）
- 日期：2026-10-24
- 确认依据：产品需求与范围决策，详见所属领域 requirements/design。

## 决策

引导脚本取得匹配程序，再由 setup/doctor/repair 准备环境。版本清单固定依赖，重复安装保留身份和数据。

## 背景与后果

新设备无需手工逐个下载依赖；节点环境与项目构建环境分离，首版不包含 coding harness。

## 关联

[领域需求](../requirements.md) · [当前设计](../design.md) · [全局路线图](../../development/roadmap.md)
