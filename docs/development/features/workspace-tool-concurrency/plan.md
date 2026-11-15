# 工作区工具细粒度并发计划

状态：实现、工程验证、独立审计修复复审与本机真实模型用户验收已完成。目标是在同一轮聊天内并行执行互不冲突的工作区工具，让冲突调用按批内顺序公平等待，并通过受信任工具描述与组合验证支持后续扩展。当前生效行为同步维护于[本机工具能力设计](../../../assistant/runtime-capabilities.md)、[执行器需求](../../../executors/requirements.md)与[执行器设计](../../../executors/design.md)。

## 范围与需求

关联 R03/R04/R23/R27、EXEC-TOOLS-01—06 与 EXEC-SCHED-01—12。涉及[执行器设计](../../../executors/design.md)、[助手工具能力](../../../assistant/runtime-capabilities.md)、[自动化覆盖](../../../automation/coverage.md)和[测试设计](../../../testing/design.md)。架构取舍记录于[资源调度 ADR](../../../executors/adr/007-workspace-resource-scheduling.md)。

| ID | 目标 |
|---|---|
| EXEC-SCHED-01 | 保留同工作区工具轮次串行，在轮次内允许无冲突工具同时执行；公共文件入口使用相同资源规则 |
| EXEC-SCHED-02 | 工具通过受信任描述登记效果、资源范围、兼容模式、容量和期限；未知工具或缺少声明时拒绝执行 |
| EXEC-SCHED-03 | 原生调用、文件桥、版本记录、取消和结果使用同一调用身份；跨调用、旧代次和身份缺失访问拒绝 |
| EXEC-SCHED-04 | 资源和执行名额一次取得，冲突等待有界且可取消；同资源按登记顺序公平执行，不阻塞不相关资源 |
| EXEC-SCHED-05 | 每次实际 I/O 前验证资源授权；未声明的访问在副作用前拒绝并记录，不能只检查工具注册信息 |
| EXEC-SCHED-06 | 保持 Windows 路径、句柄和外部修改保护；前像、容量预留、发布与逐文件收尾在并发下仍正确 |
| EXEC-SCHED-07 | 取消和超时不提前释放仍有实际工作的资源；未知副作用沿用显式核对，禁止自动重放 |
| EXEC-SCHED-08 | 记忆源文件、索引同步和索引代次有明确资源声明；搜索能看到此前已完成的记忆修改 |
| EXEC-SCHED-09 | 等待不占文件执行名额或耗尽传输通道；停止、收尾、核对和状态读取始终可进入 |
| EXEC-SCHED-10 | 每个工具有独立预期效果、组合测试和故障测试；漏声明、错误兼容模式和提前释放必须被测试发现 |
| EXEC-SCHED-11 | 并行完成顺序不改变聊天正文中的工具顺序；活动状态、分页与 CLI/MCP 可核对同一调用 |
| EXEC-SCHED-12 | 用真实 Windows 与锁定原生 Gateway 证明实际重叠、互斥和取消；性能结论附工作量与配置 |

本批覆盖九项统一工具（`read_file`、`write_file`、`edit_file`、`apply_file_patch`、`list_dir`、`search_files`、`memory_search`、`memory_get`、`progress_card`）、共享文件服务、版本与容量管理、公共文件读写入口、停止恢复及其测试设施。不修改 OpenClaw 源码或发布包，不增加可执行脚本，不开放多轮次同时修改同一工作区，不引入通用任务 DAG。

文件工具采用不带工作区前缀的统一名称，默认相对路径作用于受管工作区根，资源键采用 `(rootID, relPath)` 二元组（当前 `rootID = "workspace"`），后续开放额外授权目录时沿用同一套工具面与路径参数，无需拆成两套读写工具。

公共 `workspace_files.*` 读取可与不冲突工具并行。公共变更与 `workspace_versions.restore` 保留现有轮次门禁；进入后仍使用同一资源调度器，防止与读取或内部维护冲突。其他权限、身份校验、人工开关保持各自规则。

## 解决的关键问题

- **零侵入执行监督**：通过可信插件 `api.registerTool` 拥有全部九项工具的执行入口，内部委托公开 `openclaw/plugin-sdk/agent-harness`（`createOpenClawCodingTools`）与 `openclaw/plugin-sdk/memory-host-search`（`getActiveMemorySearchManager`），每次调用使用独立闭包的文件桥与在途 I/O 计数器，无需改动运行时依赖包或依赖 `AsyncLocalStorage` 跨内部队列传递身份。
- **重名前置防线**：内置核心工具与插件重名在 OpenClaw 中会被静默丢弃诊断；采用无冲突统一工具名，配置 `tools.updatePlan: false` 与 `tools.deny` 屏蔽未受监督的原生同名注册，并在 `go test` 增加离线预检门禁验证零冲突、清单一致与插件归属。
- **多调用句柄与共享纯读**：[文件服务](../../../../internal/workspace/service.go)将服务级互斥缩至调度表、原子预算账本与短发布步骤；Windows 纯读句柄启用 `FILE_SHARE_READ` 并禁止外部写入/删除，各调用只使用自身句柄，公共读取不再借用模型调用句柄。
- **受限记忆枚举与代次同步**：[记忆接入](../../../../internal/executor/workspaceplugin/memory.mjs)将文件枚举收窄至 `MEMORY.md`、`USER.md` 与 `memory/`，并纳入 `memory_sources` 与 `memory_index` 资源域，使普通工作区文件修改与记忆检索真正并行，同时保证同批先写记忆后检索命中新内容。

## 实施阶段与完成状态

### S0：公开 SDK 接入验证与离线预检（已完成）

固定版本核对确认：直接保留核心同名 `read/write/edit` 时，原生内置工具缺少公开的 around-execute 包装和文件桥调用身份；而由可信插件通过公开 `api.registerTool` 注册九项统一工具并委托 `openclaw/plugin-sdk/*` 原生实现时，可执行完整批次封存、每调用独立文件桥、记忆代次同步与取消后 I/O 收尾。详见[可行性验证](preflight.md)。

### S1：资源规则与调度核心（已完成）

在 `internal/workspace` 实现受信任 `ToolDescriptor`、`ResourceClaim(domain, root, key, coverage, access)`、路径组件相交规则、整批原子封存、按批内登记顺序的公平队列、容量池（最多 8 个活动调用、4 个修改调用、1 个索引同步、64 项排队）及取消收尾状态机。

### S2：受限文件服务与持久记录（已完成）

将服务锁缩至短状态转换与原子预留账本（工作区字节/条目、版本存储、活动元数据及活跃前像引用固定），每个调用独立管理句柄、前像、在途 I/O 计数与逐文件发布；Windows 纯读使用独立 `FILE_SHARE_READ` 句柄，修改维持排他新文件发布保护。

### S3：接入原生工具、记忆与提示词同步（已完成）

`nativeWorkspaceTools()` 切换至九项统一工具名，自动同步系统提示词边界、`openclaw.json` `tools.allow` 与 `promptplugin` `llm_input` 校验。插件在 `before_tool_call` 按原顺序登记调用并在 `execute` 微任务边界原子封存整批最终参数，再申请资源执行。

### S4：取消、恢复、观察与公共入口（已完成）

支持 `registered/queued/preparing/running/draining` 全阶段取消与原生提前返回后的实际 I/O 排空，保留传输层独立控制名额。`workspace.Operation` 增补可选 `phase` 与 `wait_reason`，同步 CLI/MCP 机器 schema，聊天时间线保持原生消息工具顺序。

### S5：组合回归与测量（已完成）

通过 `9×9` 有序工具组合矩阵、独立真值表、固定 seed 随机序列重放、10 类必选故障/变异检测、Windows 外部竞争测试、离线 SDK 预检与锁定 OpenClaw Gateway 多工具批次集成测试。测试入口与结果见[验证方案](validation.md)。

## 交付与验收边界

- 不同普通文件的读取、修改及 `progress_card` 在执行区间真实重叠；同文件读写、重叠补丁和记忆源文件/索引冲突按批内顺序公平等待，不因正常竞争报 `busy`。
- 同批 `write_file(a)` → `read_file(a)` → `edit_file(a)`、`apply_file_patch`（删除子项）→ `list_dir`（目录项带 `/` 尾标）及 `write_file("memory/...")` → `memory_search` 严格按顺序观察最新内容；同批内若已有前序修改命中同一路径，后续盲覆写 `write_file` 在封存期以 `conflicting_batch_write` 明确拒绝（跨批次/下一轮覆写保持允许）；部分失败保留已完成修改与恢复前像，不隐式回滚或自动重放。
- 每次低层文件与目录访问均校验精确调用身份、已授予范围与允许的效果模式；越界或漏声明在副作用前返回 `undeclared_effect`。
- 实现、工程验证、独立审计修复复审与本机真实模型用户验收已完成。按[代码风格](../../code-style.md)规模评估，新增生产与并发测试函数均保持在 100 行以内；既有线性端到端重启夹具 `TestNativeWorkspaceTools`（[conversation_workspace_integration_test.go](../../../../internal/executor/conversation_workspace_integration_test.go)）仅作九项统一工具名同步替换，因其顺序串联同一 Gateway 会话创建、九步工具脚本、版本恢复与重启核验的共享局部状态，强行拆散会降低顺序夹具可读性，故保留原函数边界。
