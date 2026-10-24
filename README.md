# Asterhand · 星伴

A personal agent system across your devices.

Asterhand 是面向个人设备的分布式 agent 助手系统。用户通过自然语言调用本机或其他已配对节点的工具；每个执行节点独立持有任务，发起节点离线后仍可继续执行。

## 当前状态

**2026-10-24：项目骨架与设计文档已建立，尚未进入功能编码。**

当前没有可运行程序、安装脚本、已发布二进制或通过验收的功能。本文描述首版交付目标。Go module 路径和依赖精确版本在实施前确定；当前仅发布文档与工程骨架。

## 首版交付目标

- 两台 Windows 分别运行独立的 OpenClaw 助手，默认使用 DeepSeek。
- 每节点提供统一 Web：聊天、节点选择、任务进度、远程问答与结果。
- A 向 B 发起自然语言请求，B 在本地授权目录中创建、读取文本文件。
- B 持久接收后，A 下线不终止 B；A 重连可补读进度。
- 安装引导、DeepSeek key 配置、节点配对及环境诊断。
- Go 核心服务，SQLite 本地持久化，版本化 HTTP + SSE。
- 保留执行器接口；首版不安装、不认证、不集成任何 coding harness。

Linux 用于轻量节点和跨平台测试；Mac 暂未提供实际运行验证，手机使用响应式 Web。自动调度、微信、多仓库更新、跨节点记忆同步和自进化发布留待后续。

## 计划中的使用流程

正式发布后：下载指定 GitHub Release 的 `install.ps1` → 在 B 执行 → 本地 Web 向导配置 DeepSeek、目录和节点 → 启动 B → 在 A 配对 B → 发送请求。

示例：“让节点 B 在验收项目下新建 hello-world.txt，内容为 hello world。”

发布物必须包含安装入口、匹配平台的程序包和校验清单。当前尚不提供安装入口，流程见 [安装设计](docs/installation/design.md) 和 [人工验收](docs/testing/acceptance.md)。

## 阅读入口

| 内容 | 文档 |
|---|---|
| 开发 agent 必读约束 | [AGENTS.md](AGENTS.md) |
| 文档导航 | [docs/README.md](docs/README.md) |
| 首版范围与原始需求映射 | [全局需求](docs/overview/requirements.md) |
| 总体架构 | [全局设计](docs/overview/design.md) |
| 阶段与验证事项 | [路线图](docs/development/roadmap.md) |
| 测试与平台证据 | [测试设计](docs/testing/design.md)、[平台矩阵](docs/testing/platforms.md) |
| 贡献流程 | [CONTRIBUTING.md](CONTRIBUTING.md) |

## 工程与开源状态

`cmd/`、`internal/`、`web/`、`contracts/`、`tests/`、`scripts/`、`packaging/` 目前只有职责说明或占位，不包含功能实现。模块可随 M0 结果收敛。

公开仓库：[jackgan90/Asterhand](https://github.com/jackgan90/Asterhand)。本项目采用 [MIT License](LICENSE)，版权归属以 LICENSE 为准；当前没有二进制 Release。引入和再分发第三方组件前记录其版本、来源、许可证和分发方式。
