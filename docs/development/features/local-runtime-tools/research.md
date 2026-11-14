# 本机工具能力调研依据

本记录区分仓库现状、上游说明和拟议设计；实现能力须按[验证方案](validation.md)在选定固定版本确认。项目产品方向和首批范围以[计划](plan.md)为准。

## 仓库基线

调研时[依赖清单](../../../../packaging/dependencies.json)固定 OpenClaw/provider 2026.10.7、Node 24.22.1。可按[准备依赖说明](../../../../scripts/README.md)取得同版本包，读取包内 docs 和 dist；本记录不依赖某台机器的缓存目录。

| 发现 | 仓库证据 | 对计划的影响 |
|---|---|---|
| 持续聊天使用独立持久 workspace/state | [启动](../../../../internal/executor/conversation_startup.go)、[配置](../../../../internal/executor/conversation_config.go) | 复用原工作区，不使用个人 OpenClaw home |
| 配置主动 deny 全部工具，关闭 memory、Code Mode、cron、browser | [配置](../../../../internal/executor/conversation_config.go) | 开放要按最终有效集合验证，不是补一个工具声明 |
| prompt hook 返回空 toolsAllow，观察端要求零工具 | [插件](../../../../internal/executor/promptplugin/index.mjs) | 配置、提示词能力事实与安全检查一起调整 |
| bridge 只投影文本，按 assistant 消息参与判断完成 | [投影](../../../../internal/executor/chatbridge/projection.mjs) | 工具轮次需新增调用事件与明确终态 |
| 聊天通过 Gateway，旧任务使用 agent exec | [聊天桥](../../../../internal/executor/chatbridge/main.mjs)、[执行器](../../../../internal/executor/executor.go) | 扩展持续会话，不扩展旧 route/execute |
| 公共自动化已有注册命令执行 | [自动化覆盖](../../../automation/coverage.md) | 内置助手不得取得完整 MCP 或同用户全量凭据 |

## OpenClaw 原生能力

固定包的 docs/tools/index.md、docs/gateway/config-tools/tool-policy.md、docs/tools/apply-patch.md、docs/concepts/agent-workspace.md 与 docs/concepts/memory-search.md 是首轮对照入口。

- 原生文件组为 read/write/edit/apply_patch。apply_patch 支持多文件、移动、删除，固定包说明默认面向全部模型启用；实际 DeepSeek 工具兼容性仍需验证。
- workspace 是默认工作路径；tools.fs.workspaceOnly 与补丁独立限制才负责核心文件路径约束。宿主 shell 不受 cwd 构成的目录边界限制。
- 记忆插件提供 memory_search/memory_get，可配置关键词检索；启用时需检查 dreaming、compaction flush、cron 等默认后台行为。
- 核心文件组没有独立的目录/内容搜索工具；禁 shell 后需要补上可控的文件发现入口。
- session_status 含模型覆盖参数；profile/group 可能包括管理或委派能力，不按显示名称推断纯读取。
- 官方工具还覆盖网络、进程、浏览器/桌面、消息、委派、计划/问答、媒体、自动化和运行时管理；采用具体能力逐批纳入，不直接打开整个 profile。
- OpenClaw Code Mode 的编排执行器和 shell 执行隔离是两个问题；第一批关闭，后续单独评估。

参考：[workspace](https://docs.openclaw.ai/concepts/agent-workspace)、[工具概览](https://docs.openclaw.ai/tools)、[工具策略](https://docs.openclaw.ai/gateway/config-tools/tool-policy)、[工具权限](https://docs.openclaw.ai/gateway/security/tool-permissions)、[Sandbox](https://docs.openclaw.ai/gateway/sandboxing)、[Memory](https://docs.openclaw.ai/concepts/memory)。

在线文档随版本变化，不能用最新页面替代固定包行为。官方 [2026.10.9 发布记录](https://docs.openclaw.ai/releases/2026.10.9)列出 Gateway 构造工具的 workspace 约束扩展；第一批选择版本时评估与所选工具的关系，不据此直接宣称 2026.10.7 全面失效或已验证安全。

## 跨 harness 对照

| 维度 | OpenClaw | Hermes | DeepSeek Harness |
|---|---|---|---|
| 文件接口 | read/write/edit/apply_patch | read_file/write_file/patch/search_files | fs 能力族分离接口、实现与模型工具 |
| 执行环境 | 原生工具策略与 sandbox | terminal backend 及文件工具安全检查 | subprocess/shell/sandbox 能力族 |
| 会话与交互 | Gateway 会话、事件及工具循环 | 原生会话与工具循环、交互能力 | session/interaction/sdk 等能力族 |
| 可共用抽象 | 语义能力、执行约束、事件/结果/恢复 | 同左，工具名称和具体保证需独立映射 | 同左，不把其插件架构当公共强制结构 |

Hermes 的写入安全根主要保护 write_file/patch，终端仍可访问当前系统用户的文件；单一“支持工作区”标志无法表达读、写、执行的区别。参考：[工具集合](https://hermes-agent.nousresearch.com/docs/reference/toolsets-reference)、[工具参考](https://hermes-agent.nousresearch.com/docs/reference/tools-reference)、[安全](https://hermes-agent.nousresearch.com/docs/user-guide/security)。

DeepSeek Harness 已将 fs、sandbox、session、interaction 等拆成独立能力族；这里只用其架构交叉验证公共边界，不冻结其将来的 API。参考：[包职责](https://github.com/deepseek-ai/deepseek-harness/blob/master/packages/README.md)、[文件能力](https://github.com/deepseek-ai/deepseek-harness/blob/master/packages/fs/README.md)、[隔离能力](https://github.com/deepseek-ai/deepseek-harness/blob/master/packages/sandbox/README.md)。

## 待验证项

同步阻止原生写入、可靠前像、原生调用身份/终态、进度卡持久读回、记忆索引/后台任务及权限覆盖，是实现前 G1—G5；调研结论不将这些接口行为记为 passed。其他 harness 只有架构对照，没有产品接入或 Windows 实测。
