# 安装与环境需求

范围：Windows 完整安装与升级，验证方法见 [数据与配置保留](../development/features/task-reliability/validation.md) 和 [安装路径解析](../development/features/installer-path-resolution/plan.md)。关联 C04、C05、C09。

| ID | 要求 |
|---|---|
| INST-01 | Windows 新节点通过版本化 install.ps1 引导，无需手动逐个下载依赖或编译 |
| INST-02 | 检测 OS、架构、环境和已有组件；复用兼容依赖，补齐缺失项 |
| INST-03 | 完整节点默认含 OpenClaw + DeepSeek 配置，不含 coding harness |
| INST-04 | 首次 Web 向导完成节点、凭据、授权目录、网络与配对准备 |
| INST-05 | 重复执行保留节点身份、配置与数据；中断可重试 |
| INST-06 | 发布锁定组件版本、来源和校验信息，安装不追随不受控 latest |
| INST-07 | 诊断区分已安装、待配置、检查失败和真正就绪 |
| INST-08 | 提供明确启动入口和可选登录后启动；系统修改说明范围 |
| INST-09 | Linux 提供基础节点安装路径，不强制安装模型或开发工具 |
| INST-10 | 节点运行环境与业务项目构建环境分开，不演变成通用包管理器 |
| INST-11 | 依赖版本由单一清单驱动构建、诊断和安装；更新先准备隔离候选，不原地修改运行中的 bundle |
| INST-12 | 升级验证包含锁文件、契约、真实模型与安装/数据保留回归；缺少任一项不得把升级标为通过 |
| INST-13 | 包描述明确组件、默认选择和全部运行时验证目标；公共准备/安装检查不写死模型或 agent 提供者，未知目标和未声明直接依赖拒绝 |
| INST-14 | Windows 安装参数中的相对路径以 PowerShell 当前 FileSystem 位置为基准；不受 .NET 进程工作目录影响，自动清单位于指定本地包旁 |

必须通过 [V01 干净环境安装](../testing/acceptance.md)；在开发机直接运行不替代安装验收。

提供 Windows x64 自带运行时的完整包、本地包/固定 Release 安装、摘要校验、独立数据目录、可选登录后启动、doctor/repair 和 Tailscale 引导。Linux 基础入口仍为实验交叉编译产物；干净设备安装按人工验收规程独立验证。
