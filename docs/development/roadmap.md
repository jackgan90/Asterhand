# 开发路线图

路线图区分现有能力和后续工作。具体行为以各领域 requirements/design 为准，验证方式见 [测试指南](../../tests/README.md)，平台边界见 [支持矩阵](../testing/platforms.md)。

## 已实现的 Windows 助手基础

| 能力 | 设计与维护入口 |
|---|---|
| 本地与远端自然语言文件任务、问答、取消 | [Windows 跨节点助手](features/windows-assistant/plan.md) |
| 模型配置、执行器契约、依赖锁定 | [模型、运行时与依赖边界](features/provider-runtime-boundaries/plan.md) |
| 运行时专用选项、受保护目录、通用包描述 | [提供者无关的公共流程](features/provider-neutral-flows/plan.md) |
| 日志预算、取消竞争、文件证据与配置迁移 | [任务可靠性与配置保留](features/task-reliability/plan.md) |
| Windows 安装、升级、相对路径与数据保留 | [安装路径解析](features/installer-path-resolution/plan.md) |
| 任务排序、节点名称/地址同步、就绪保持 | [双节点交互与连接同步](features/two-node-usability/plan.md) |
| 统一发布、单脚本下载安装与平台扩展接口 | [可复现发布与独立安装](features/release-pipeline/plan.md) |

## 后续验证与功能

- Linux/macOS 平台准备器、构建器、安装入口与原生系统验证；平台注册表在验证前保持禁用。
- 手机远程访问的认证、部署方式和物理设备测试。
- 物理断线与重启、复杂 NAT 环境、长历史规模的系统化可复现测试。
- 协议故障代理、可控时钟和持续集成；现有覆盖见 [测试设计](../testing/design.md)。
- 根据实际需求扩展工具和模型/运行时适配器。具体 coding harness、自动调度、全网会话同步等见 [后续方向](../overview/future.md)。

这些条目需要分别确定范围与授权。新增功能先建立语义明确的功能计划，再同步领域文档、实现和验证方法。用户验收、本地提交、推送与发布各自确认，不以路线图状态替代授权。
