# 工作区工具资源调度设计

状态：已实现。范围与需求见[计划](plan.md)，公开 SDK 接入依据见[可行性验证](preflight.md)，验证矩阵见[验证方案](validation.md)。本设计约束通过受限工作区服务执行的助手工具与公共文件操作；新增能力的授权仍独立判断。

## 一次调用的生命周期

调度单位是一次助手工具调用或公共业务操作。每次调用先由受信任描述计算会读取或修改的资源集合，整批参数封存后由调度器原子授予全部资源与容量名额；文件服务在每次低层访问前校验精确调用身份、已授予范围与允许的效果模式。执行结束后等待在途子 I/O 排空、核对文件状态并持久化结果，最后归还资源占用。

```mermaid
flowchart LR
    A["原生调用或公共操作"] --> B["准备阶段按序登记身份与输入指纹"]
    B --> B2["执行入口同步收齐整批最终参数并原子封存"]
    B2 --> C["按批内顺序公平等待资源与名额"]
    C --> D["固定句柄、核验身份与预留容量前像"]
    D --> E["词法绑定单调用桥委托原生实现"]
    E --> F["每次低层 I/O 校验身份、范围与效果"]
    F --> G["排空在途 I/O、核对后像并持久化终态"]
    G --> H["释放资源与执行名额"]
```

同工作区仍一次处理一轮获工具授权的聊天；公共修改与版本恢复保留现有轮次门禁。节点内共享同一工作区调度器实例，使公共读取、轮内并行工具调用与受信任维护读取遵守统一资源规则。

## 零侵入公开 SDK 委托架构

`asterhand-workspace` 插件通过公开 `api.registerTool` 拥有全部九项工具（`read_file`、`write_file`、`edit_file`、`apply_file_patch`、`list_dir`、`search_files`、`memory_search`、`memory_get`、`progress_card`），不改动 OpenClaw 源码或构建产物：

- **基础文件与补丁委托**：在工具 `execute` 内部按需调用公开 `openclaw/plugin-sdk/agent-harness` 的 `createOpenClawCodingTools`，为当前 `callId` 注入词法绑定的专用 `SandboxFsBridge`（内部映射原生 `read`、`write`、`edit`、`apply_patch`、`ls`，并将目录项映射为含 `isDirectory` 元数据的 `DirectoryEntry` 以保留目录尾斜杠 `/` 标识），复用原生参数校验、行号读取、精确文本编辑与多文件补丁解析算法，同时杜绝跨调用全局状态与 `AsyncLocalStorage` 丢失问题。
- **进度与记忆接管**：运行时配置设 `tools.updatePlan: false` 以停用未受监督的内置 `progress_card`，并在 `tools.deny` 中加入 `"memory-core"` 以屏蔽其未包装工具注册，同时保留 `plugins.slots.memory = "memory-core"` 以维持原生记忆提示词段落与本地 FTS 索引器。插件自有的 `memory_search` 与 `memory_get` 委托 `getActiveMemorySearchManager`，禁用脱离调用上下文的后台监听唤醒，仅在受监督调用身份与在途计数保护下同步强制刷新本地索引（`manager.sync({reason: "memory_search", force: true})`）并执行检索。
- **离线防重名与别名守卫**：OpenClaw 对插件与内置核心工具（及 `TOOL_NAME_ALIASES` 别名表）重名采用静默跳过插件工具策略。工程测试通过直接调用公开 `createOpenClawCodingTools`、`toolPolicy.createToolPolicyMatcher` 与 `applyEmbeddedAttemptToolsAllow` 执行离线预检，断言九项工具无核心或别名碰撞，且经有效策略过滤后全部保留并归属 `asterhand-workspace`。

## 受信任工具描述与通用资源规则

`ToolDescriptor` 由受信任服务端代码维护，模型只提交普通工具参数：

| 字段 | 作用 |
|---|---|
| `name / semantic` | 工具或公共操作标识与语义分类（如 `file.read`、`file.write`、`file.patch`、`memory.search`） |
| `prepare(args)` | 规范化路径与参数别名，计算完整 `ResourceClaim` 集合、允许的低层 `Effect` 集合、保守字节增长与目标数上界 |
| `pools` | 占用的并发名额类型（活动调用槽、修改调用槽、记忆索引槽） |

资源声明结构为 `ResourceClaim(domain, root, key, coverage, access)`：

- `domain`：`fs`（文件系统对象）、`memory_sources`（记忆源集合）、`memory_index`（本地记忆 FTS 索引）、`progress`（当前轮次进度卡片）。
- `root`：逻辑受管根目录标识。当前默认工作区固定为 `"workspace"`；未来开放额外授权目录时，解析入口将路径映射为对应 `(rootID, normalizedRelPath)`，同一套工具面与相交算法直接生效，不同 `root` 互不冲突。
- `key`：大小写无关规范化相对路径（以 `/` 分隔组件，根为 `""`）或域内固定键。路径按组件边界比较，`a` 与 `ab` 不相交。
- `coverage`：`exact`（仅目标项）、`children`（目录自身与直接子项）、`subtree`（目录自身及全部后代）。
- `access`：共享 `S` 与独占 `X`。仅当同 `domain`、同 `root`、范围相交且至少一方为 `X` 时冲突；`S/S` 可并发共享，`S/X` 与 `X/X` 排队等待。

低层 `Effect` 集合（`read`、`stat`、`list`、`search`、`write`、`create`、`remove`、`mkdir`）独立约束已持锁调用的具体动作：即使 `edit_file` 持有目标文件 `X`，也无权执行 `remove` 或访问未声明的兄弟路径。

## 九项工具与公共入口映射

| 工具或公共入口 | 资源声明（`root = "workspace"`） | 允许效果与并行特征 |
|---|---|---|
| `read_file` | 目标 `fs exact S` | `read/stat`；不同文件与同文件纯读均可并行 |
| `write_file` | 目标 `fs exact X` + 缺失父目录 `fs exact X`；命中记忆源时加 `memory_sources exact X` | `read/stat/write/create/mkdir`；不同文件并行 |
| `edit_file` | 目标 `fs exact X`；命中记忆源时加 `memory_sources exact X` | `read/stat/write`；同调用内先读后写共用同一 `X` 租约 |
| `apply_file_patch` | 全部补丁源/目标 `fs exact X` + 缺失父目录 `fs exact X`；命中记忆源时加 `memory_sources exact X` | `read/stat/write/create/remove/mkdir`；不相交补丁并行，任意路径重叠则整批等待 |
| `list_dir` | 目标目录 `fs children S` | `stat/list`；深层后代内容修改不阻塞上层非递归列目录 |
| `search_files` | 查询根 `fs subtree S` | `stat/search`；查询子树外的文件修改可并行 |
| `memory_get` | 允许的记忆文件 `fs exact S` | `read/stat`；仅限 `MEMORY.md`、`USER.md` 与 `memory/**`，越界直接拒绝 |
| `memory_search` | `memory_sources exact S` + `memory_index exact X` + 记忆根与文件只读范围 `S` | 记忆同步与检索串行；普通非记忆工作区文件读写可与其完全并行 |
| `progress_card` | `progress exact X` | 不触碰文件系统，与任意文件或记忆工具并行；同轮多次进度更新保序 |
| `workspace_files.read` / `artifacts.read` | 目标 `fs exact S`（产物另核对创建时修订） | 独立纯读句柄，与不冲突工具共享运行 |
| `workspace_files.list` / `search` | 查询目录 `fs subtree S` | 与助手工具共用相交规则与扫描预算 |
| `workspace_files.mutate` / `workspace_versions.restore` | 保留轮次门禁，进入后申请全部目标 `fs exact X`（涉及记忆源加 `memory_sources X`） | 与并发公共读取严格互斥，保留幂等操作收据与前像保护 |
| 系统启动引导读取 | `workspace.bootstrap` 只读范围 `S` | 仅限显式携带 `bootstrap: true` 的启动只读桥接请求；禁止脱离工具调用上下文的无身份记忆或检索请求 |

## 批次封存、公平调度与容量池

1. **顺序准备登记**：`before_tool_call` 按模型工具批次原顺序向 Go 服务发送非阻塞 `register`，分配单调递增序号（ticket）并记录 `input_fingerprint`；此阶段不获取文件句柄或容量名额。若被后续钩子拦截或在进入 `execute` 前中止，立即标记取消登记。
2. **微任务整批原子封存与同批盲写冲突拦截**：OpenClaw `launchParallelToolCalls` 在同一微任务链内按批内顺序同步进入全部就绪工具的 `execute`。插件批次协调器收齐当批所有就绪项的最终 `params` 与 `execution_fingerprint`（绑定工具名、参数与规范化范围）后，一次调用 `seal` 原子更新并封存整批最终资源声明（即使后置钩子将前序 `write_file(a)` 改写为 `write_file(b)`，封存后 `read_file(b)` 也会看见前序 `write_file(b)` 的 `X` 声明并等待）。若同一工具批次中已有前序有效修改项（`write_file`、`edit_file`、`apply_file_patch`）作用于规范化路径 `P`，后续再次对同一路径 `P` 发起无前文校验的全量覆写 `write_file`，封存阶段直接以 `conflicting_batch_write` 拒绝该后续覆写并保留前序修改，避免同批盲写静默吞掉前序结果；而 `write_file → read_file`、`write_file → edit_file`、`edit_file → edit_file`（由上下文精确匹配把关）及跨批次/下一轮 `write_file` 保持正常有序执行。封存完成前当批零调用准入；封存后若参数指纹漂移，直接拒绝执行。
3. **全量原子授予与写者公平**：`acquire` 一次检查全部资源声明与计数池名额，全取或全等。任何与前序已封存（含已获锁或排队等待中）调用冲突的请求都必须排在其后，防止源源不断的新读者越过排队写者造成饥饿；互不相交的后续调用可直接越过等待队列并发执行。
4. **容量上限与保留控制通道**：单工作区及单节点上限为最多 `8` 个活动调用、其中最多 `4` 个修改调用、`1` 个记忆索引同步、最多 `64` 项等待队列，轮次总上限保持 `500` 次工具调用与 `10` 分钟预算。本地 HTTP 桥将数据/等待槽位与控制槽位（`register`、`seal`、`cancel`、`end`）分离，确保满载排队或慢读取下取消与收尾永不被 `429 busy` 阻挡。

## Windows 句柄所有权、原子预算与收尾

- **独立句柄与共享纯读**：修改操作沿用相对父句柄逐级 `NtCreateFile` 打开、排他占用（`share = 0`）、私有暂存写入与 `NtSetInformationFile` 条件重命名发布；纯读操作使用 `FILE_SHARE_READ`（仍拒绝外部 `FILE_SHARE_WRITE | FILE_SHARE_DELETE`）及独立句柄 `ReadAt` 定位读取，禁止跨调用复用 `heldFile`。每次请求必须携带匹配的 `(callId, executionFingerprint)`，违者或越界动作在副作用前返回 `undeclared_effect` 或 `invalid_call` 并关闭该调用后续 I/O 准入。
- **共同缺失父目录创建**：多个修改调用涉及缺失父目录时，对各缺失祖先目录项声明 `fs exact X`，由先获锁调用创建并校验目录身份，后续调用获锁后复核现存目录，容量与 `new_parent_directories` 不重复虚增。
- **原子预算账本与前像固定**：服务锁仅保护内存调度表、原子预留累加器（`reservedBytes`、`reservedFiles`、`reservedVersionBytes`、`reservedActivityBytes`）和短提交元数据；目录扫描、前像复制与磁盘写入均在锁外执行。版本 GC 固定所有活动、`unknown` 及恢复读取中的记录 ID，防止并发清理删掉在途前像。
- **排空式取消与崩溃恢复**：取消或超时先关闭该调用的新 I/O 准入并进入 `draining`，待插件包装与服务端在途 I/O 计数均归零、已触碰文件完成核对且日志落盘后才释放对应资源锁与轮次租约。出现 `unknown` 或终态持久化失败时立即封锁工作区新增修改准入，等待在途调用排空后释放运行态 `busy`，保留显式人工核对恢复语义。
