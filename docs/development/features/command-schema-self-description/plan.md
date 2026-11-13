# 命令与脚本图 Schema 自描述增强计划

## 基本信息

- 功能名称：命令与脚本图 Schema 自描述增强（`command-schema-self-description`）
- 目标与用户价值：让外部 agent 仅通过 `automation schema` / MCP 工具定义及错误信息，即可获取本地命令与脚本图编辑所需的全部格式正则、数值边界、节点互斥规则、脚本参数限制与 Windows Job 执行语义，同时通过 `installation.json` 稳定定位 CLI/MCP 可执行文件，无需翻阅项目源码。
- 状态：已完成源码实现、领域文档同步、工程验证及本机用户验收。
- 关联需求 ID：`AUTO-02`、`AUTO-03`、`AUTO-05`、`AUTO-06`、`AUTO-07`、`AUTO-08`
- 相关领域文档：[自动化需求](../../../automation/requirements.md)、[自动化设计](../../../automation/design.md)、[CLI 与 MCP 使用](../../../automation/usage.md)、[覆盖清单](../../../automation/coverage.md)

## 范围

- 本次实现：
  1. 在 `internal/localcommands/graph.go`、`internal/localcommands/command.go`、`internal/automation/command_types.go`、`internal/automation/log_types.go`、`internal/app/automation_commands.go` 补充结构体字段 `description` tag 与操作 `Description`，覆盖命令名/节点 ID 正则、别名上限、坐标与并行度区间、`start`/`end` 互斥规则、`.bat`/`.cmd`/`.ps1` 路径及双引号规则、`failure_policy`、运行/日志参数与 Windows 非脱离 Job 执行语义。
  2. 细化 `internal/i18n/locales/zh-CN.json` 与 `internal/i18n/locales/en-US.json` 中 `err.graph.identity` 和 `err.graph.node` 文案，明确写出 16–64 位字符要求与节点属性规则，并运行 `sync-i18n.mjs` 同步前端只读词表。
  3. 同步更新 [自动化设计](../../../automation/design.md)、[CLI 与 MCP 使用](../../../automation/usage.md)（补充 `installation.json` 发现 `executable` / `data` 路径、UTF-8 无 BOM `--input` 与 `stderr` 输出说明）、[路线图](../../roadmap.md) 及 [文档导航](../../../README.md)。
  4. 在 `internal/app/automation_coverage_test.go` 新增 `TestCommandAndGraphSchemasAreSelfDescribing`，验证 MCP Server 构造、`commands.create` Schema 关键约束描述、`commands.validate` 空 `id`/`revision` 输出兼容以及 `err.graph.identity` / `err.graph.node` 错误文案。
- 明确非目标：
  - 不更改命令注册 schema 版本（保持 4）或脚本图/命令核心校验逻辑；
  - 不在共享结构体上新增硬性 JSON Schema 校验关键字（`pattern`/`minimum`/`maximum`/`enum`），避免误拦 `commands.validate` 的空 `id`/`revision` 返回或使 MCP 协议拦截与 CLI 领域校验/收据分叉；
  - 不修改现有命令或脚本执行生命周期行为。
- 验收用例及预期可见结果：
  - `asterhand automation schema commands.create`（及 `commands.patch`、`commands.validate`、`commands.run`、`logs.read`）与 MCP 工具列表直接包含完整字段规则说明；
  - 短节点 ID 或 `start`/`end` 误带脚本属性时，`commands.validate` 返回带明确格式要求的错误文案；
  - `commands.validate` 对未保存合法候选仍正常返回空 `id` / `revision`。

## 规模与拆分评估

- 所有修改的 Go 文件均远低于 1000 行（`schema.go` 257 行、`graph.go` 225 行、`command.go` 118 行、`command_types.go` 85 行、`log_types.go` 18 行、`automation_commands.go` 179 行），新增测试函数约 55 行，未触发超限拆分。
- `internal/i18n/locales/zh-CN.json` 与 `en-US.json` 保持 941 行，未超出 1000 行阈值。

## 验证与维护

- 仓库检查与测试入口：
  - `node ./scripts/sync-i18n.mjs`
  - `pwsh -NoProfile -File ./scripts/check-i18n.ps1`
  - `go vet ./...`
  - `go test ./internal/automation/... ./internal/localcommands/... ./internal/app/... ./internal/i18n/...`
  - `go run ./cmd/asterhand automation schema commands.create`
- 支持边界：Windows x64 本机 CLI/MCP；未改变跨节点与远程能力边界。
