# 全局需求

状态：已确认首版范围；未实现。来源为 [原始需求](origin.md) 及 2026-10-24 用户决策；冲突以后续明确决定为准。

## 产品目标

个人跨设备自然语言助手。完整节点独立运行，在线节点直接协作；用户在任意可用入口调用授权工具，观察远端任务并回答问题。

首版交付两台 Windows 的可见闭环：安装、模型配置、配对、本机和远端文件创建、断线继续、事件补读、远程问答、取消与重启对账。测试通过是工程证据，真实用户操作和文件结果是产品验收。

## 原始需求追溯

| ID | 原始需求 | 当前处理 |
|---|---|---|
| R01 | 个人 LLM 驱动的多终端助手 | 总目标，首版两机闭环 |
| R02 | Windows、MacBook、Android、iOS、Linux | Windows 完整节点；Linux 轻量验证；Mac 待实测；手机 Web |
| R03 | 单终端自然语言与本机任务 | 首版 OpenClaw + DeepSeek |
| R04 | 工具统一入口 | 首版文件工具，后续增量接入 |
| R05 | 离线、上线为正常状态 | 首版必须覆盖 |
| R06 | 在线可达节点通信 | 静态配对、直接通信 |
| R07 | 无固定中心，Linux 不必经 | 首版架构约束 |
| R08 | A 指定 B 执行 | 首版核心能力 |
| R09 | 远端执行进度监控 | 首版结构化事件与 Web 任务卡片 |
| R10 | 委托 coding harness | 首版只保留接口；具体集成后置 |
| R11 | 编写与执行脚本 | 长期保留；首版授权目录内受限工具，不开放任意远程 shell |
| R12 | 多仓库更新、冲突和回滚 | 后置，VCS 不预设全部为 Git |
| R13 | 自然语言精确文件搜索 | 后续增量；首版必验收为文件创建和读取 |
| R14 | Windows Everything | 可选连接器，非首版发布前置 |
| R15 | Coding harness memory manager | 后置 |
| R16 | Repository update scheduler | 后置 |
| R17 | 可配置 LLM provider | 配置边界清晰，仅 DeepSeek 为首版必测 provider |
| R18 | 默认 DeepSeek，重视速度成本 | 已确认；具体模型和版本 M0 验证 |
| R19 | Coding subscription 备选 | 具体 harness 接入后处理，首版无登录要求 |
| R20 | 多角色，同机或跨机 | 首版通用助手；角色扩展保留边界 |
| R21 | coder、teacher、advisor、news 等 | 后续按场景实现 |
| R22 | 按在线资源动态分配 | 首版明确指定节点，自动 placement 后置 |
| R23 | Skills 与 memory | 优先复用运行时，不自建同步系统 |
| R24 | 自进化、理解用户 | 长期候选经验、验证、版本发布、回滚；首版不自动修改核心 |
| R25 | 微信等通信渠道 | 后置 |
| R26 | 优先开源，减少生态绑定 | 全阶段；项目采用 [MIT License](../../LICENSE)，源码公开到 GitHub |
| R27 | 直接用运行时或薄编排层 | 已选 Asterhand 薄层 + OpenClaw |
| R28 | 先做原型，不追求大而全 | 首版范围最高优先原则 |
| R29 | 汇总 Markdown 交接材料 | 分层项目文档，持续维护 |

## 本轮新增与收敛需求

| ID | 已确认要求 | 权威文档 |
|---|---|---|
| C01 | 项目名称 Asterhand，中文星伴 | 本文 |
| C02 | 每节点统一 Web，自然语言选择执行节点 | [Web](../web/requirements.md) |
| C03 | 真机 hello-world、断线继续和远程问答 | [验收](../testing/acceptance.md) |
| C04 | 全新节点安装、诊断、初始化和配对 | [安装](../installation/requirements.md) |
| C05 | OpenClaw + DeepSeek 及本地 key 配置 | [助手](../assistant/requirements.md) |
| C06 | 具体 coding harness 全后置，仅留接口 | [执行器](../executors/requirements.md) |
| C07 | 按主要领域分层文档，feature 强制同步 | [开发治理](../development/requirements.md) |
| C08 | 文件 1000 行、函数 100 行软限制；安全拆分先确认 | [代码风格](../development/code-style.md) |
| C09 | 单 Windows 快测、两 Windows 验收、低配 Linux、无 Mac | [测试平台](../testing/platforms.md) |
| C10 | 当前只建骨架与文档，抽取并删除旧 draft | [F000](../development/features/F000-project-bootstrap/plan.md) |

## 明确排除

首版不交付具体 coding harness、真实编码演示、自动节点选择、跨节点任务接管、全网强一致数据库、通用 DAG、A2A 兼容层、原生手机 App、微信、主动推送、全网会话同步、完整多仓库更新、自动知识发布。

不承诺外部副作用 exactly-once，不将执行节点重启解释为原进程必然续跑，不要求每个参与节点具备完整助手能力。

## 支持与完成标准

- Go 服务 + 本地 SQLite + OpenClaw + DeepSeek + 统一 Web；网络使用已有 LAN / Tailnet。
- 支持矩阵细到 OS、架构、依赖版本与能力，语言支持平台不能替代产品验证。
- 实现、工程测试、真实设备验收、用户接受、发布分别记录。
- 密钥、对话和活跃数据库保留本地，默认不跨节点复制。
