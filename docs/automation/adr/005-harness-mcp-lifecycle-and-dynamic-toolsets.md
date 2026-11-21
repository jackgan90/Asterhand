# ADR-005：Coding Harness 统一 MCP 托管与三开关动态工具集

- 状态：accepted（Milestone 1 核心机制已实现并完成本机真机用户验收；Milestone 2 稳定转发入口待实施）
- 日期：2026-11-21
- 所属领域与需求：自动化、CLI 与 MCP（[自动化需求](../requirements.md) `AUTO-HARNESS-01`—`05`、[安装需求](../../installation/requirements.md) `INST-33`）
- 替代 / 被替代记录：扩展 [自动化 ADR-001](001-shared-operations.md) 与 [自动化 ADR-004](004-external-clients-without-ui-workflow.md)，不改变既有 Named Pipe + TLS 1.3 传输与公共业务 API 版本 1

## 背景

外部 Coding Harness 手工接入 Asterhand 存在三类痛点：版本化路径随升级失效、全量暴露二十余个自动化工具污染编码上下文、以及缺乏统一的启停与协作权限控制。同时，经复核现有 `internal/automation` 实现发现：
1. `mcpResult` 当前仅在 `content[].text` 输出 `"Completed"` 或收据状态，若客户端仅向模型注入文本将丢失 `turn_id` 与审查结果；
2. 现有 `Access.Authenticate` 将全部本机连接映射为同一全权 `localPrincipal()`，且 `Client.Call` 在本机直接处理 `node.start` 与启动收据，仅靠未校验的 `--caller` 字符串无法构成一致权限闸门；
3. `Installation` 同时记录 `Executable` 与 `Data`，转发入口若不透传 `--home <Data>` 会连错自定义数据目录节点；
4. 外部 MCP 配置文件可能包含用户其他 server 的敏感认证头，整文件备份存在凭据复制风险。

## 决策

1. **三开关正交模型与 8 种组合确定性行为**：
   - 每个 Harness 独立维护 `general_tools_enabled`、`collab_caller_enabled`、`collab_worker_enabled`（Antigravity 静态锁定为 `false`）；
   - 外部 MCP 期望注册状态仅由 `general_tools_enabled || collab_caller_enabled` 派生，工具集切换无需反复改写外部配置文件。
2. **诚实信任边界与全路径受限 `Principal`**：
   - 明确声明在同一 Windows 用户账户下，托管入口策略面向合作型外部 Harness（防模型误调用、工具污染与意外递归），不宣称对抗同账户恶意本地进程；
   - 为每个受管 Harness 建立可撤销的受限调用身份与独立 `Principal`，统一作用于 **`tools/list`、`Dispatch`、`batches.submit` 逐项子调用、`operations.get` 与 `batches.get` 逐项收据查询（含返回前按原操作所属工具集与最新权限复核）及 `Client.Call` 中的本地 `node.start`**；吊销整份凭据使旧连接全部失效，仅关闭 `collab_caller_enabled` 时按剩余开关实时重算权限（如保留 `core + general`）；无 `--caller` 的既有兼容入口明确定位为本机显式全量入口；
   - 启动受管 Worker B 时优先通过原生参数禁用其加载 `asterhand` MCP，并以服务端 `max_delegation_depth = 1` 兜底。
3. **面向模型的 MCP 双通道响应契约（含 `core` 底座全覆盖）**：
   - `mcpResult` 对 `operations.new_id`、`operations.get`、`capabilities` 及全部 `harnesses.*`、`workers.*` 在保留 `structuredContent` 的同时，向 `content[].text` 输出有界、包含关键标识（`operation_id`、`worker_id`、`turn_id`、`status`）、审查结果正文或续读游标指引的可读摘要，确保仅消费文本通道的客户端从“生成首个 ID”到“断线找回与读取结果”全链路不断链。
4. **可重试的期望/观察状态对账与无凭据恢复元数据**：
   - 分别记录 `desired_state` 与 `observed_state`，关闭权限先在服务端闸门生效并发送 `notifications/tools/list_changed`，外部写入失败可重试对账；
   - Codex/Claude 优先使用官方 `mcp add/remove` CLI，Antigravity/DeepSeek 仅原子操作自身单键；**严禁整文件备份外部配置**，仅记录受管条目的恢复元数据，发现非托管同名条目时标为 `conflict` 拒绝静默覆盖；
   - 外部配置改写、三开关、冲突接管与授权工作区配置按 `AUTO-19` 保留为人类专用入口。
5. **受管 MCP 工具名仅使用 `[a-zA-Z0-9_-]`（禁止 `.`）**：
   - 经本机 Antigravity 实测确认，工具名包含 `.` 会导致客户端懒加载 Schema 生成 `"parameters": null`，且 OpenAI / Anthropic 工具名同样限定 `[a-zA-Z0-9_-]`。受管 Harness 调用方（`--caller` 非空）在 MCP 边界统一将内部操作名中的 `.` 映射为 `_`（并自动反解 `operation` 参数），由单元测试守卫零冲突与合法字符集；内部、CLI、HTTP 及无 `--caller` 的旧直连 MCP 保留原点号规范名。
6. **稳定无版本号转发入口透传 `--home <Data>`**：
   - 安装根目录 `asterhand-mcp.exe`（Milestone 2）复用 `launch.ReadInstallation` 校验并自动向活动版本 `asterhand.exe mcp` 透传 `--home <Installation.Data>`，保证自定义数据目录与覆盖升级均连接正确实例；Milestone 1 先通过节点启动自对账刷新已托管条目路径。

## 备选与后果

- **备选 A：在 `mcpResult` 继续只输出 `"Completed"`，依赖所有客户端完整喂入 `structuredContent`**：不同 MCP 客户端对 `structuredContent` 的模型注入行为差异大，风险过高，予以否决。
- **备选 B：整文件备份外部 `mcp_config.json` 以支持回滚**：会意外复制其他第三方 MCP server 的敏感凭据，违反零凭据原则，予以否决。
- **后果与限制**：客户端不支持 `notifications/tools/list_changed` 热刷新时，仍由服务端闸门即时拦截，重开会话自动同步工具清单。

## 验证与关联

- 关联计划：[多 Coding Harness 中介计划](../../development/features/multi-harness-broker/plan.md)
- 关联文档：[自动化需求](../requirements.md) · [自动化设计](../design.md) · [覆盖清单](../coverage.md) · [安装设计](../../installation/design.md)
- 待验证项：Milestone 1 需验证锁定版 `go-sdk v1.7.0` 动态工具集、`mcpResult` 文本在真实 Antigravity 对话中的引用效果，以及受限 `Principal` 对旧连接、批处理、收据和 `node.start` 的拦截覆盖。
