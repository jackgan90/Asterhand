# 原生工具并发接入可行性

S0 结论：已通过公开插件接口验证（passed），无需修改 OpenClaw 源码或发布包。锁定 OpenClaw `2026.10.7` 的内置核心工具本身不对外暴露 around-execute 包装或文件桥调用身份，但公开 `OpenClawPluginApi.registerTool` 配合 `openclaw/plugin-sdk/agent-harness`（`createOpenClawCodingTools`）、`openclaw/plugin-sdk/sandbox`、`openclaw/plugin-sdk/agent-workspace-runtime` 与 `openclaw/plugin-sdk/memory-host-search`（`getActiveMemorySearchManager`）可由可信插件完整拥有九项统一工具的执行包装、批次封存、每调用独立文件桥和在途 I/O 收尾。

## 可复现入口

使用依赖清单准备的 Node `24.22.1` 与 OpenClaw `2026.10.7`，在真实 Windows 上可执行基线探针与正式预检测试：

~~~powershell
node scripts/probe-workspace-concurrency.mjs --runtime <prepared-runtime>/node_modules/openclaw
node scripts/fixtures/workspace-concurrency-probe/sdk-inventory.mjs <prepared-runtime>/node_modules/openclaw <evidence-directory>
go test ./internal/executor -run TestWorkspacePluginSDKPreflight -count=1
~~~

`<prepared-runtime>` 是[依赖准备流程](../../../../scripts/README.md)生成的 `runtime_root`；`<evidence-directory>` 为忽略目录下的证据输出路径。`TestWorkspacePluginSDKPreflight` 直接通过锁定 Node 加载正式 `workspaceplugin`、`openclaw/plugin-sdk/agent-harness`（`createOpenClawCodingTools`）与 `openclaw/plugin-sdk/agent-harness-runtime`（`toolPolicy.createToolPolicyMatcher`、`applyEmbeddedAttemptToolsAllow`），在不启动 Gateway、不联网、不消耗模型额度的前提下，校验九项工具无核心重名或 `TOOL_NAME_ALIASES` 别名冲突、插件清单与实际注册一致、经 OpenClaw 策略与尝试白名单过滤后的有效工具面严格等于 `nativeWorkspaceTools()` 且 100% 归属可信插件实例。

## 原生行为核对与设计对应

| 检查项 | 固定版本实测事实 | 正式采用方案 |
|---|---|---|
| 批次准备与启动顺序 | `executeToolCallGroups` 先按模型调用顺序逐个完成 `validateToolCallForBatchAdmission` 与 `before_tool_call`，再由 `launchParallelToolCalls` 在同一微任务链内按序同步进入各工具 `execute` | `before_tool_call` 只做非阻塞顺序登记；各工具 `execute` 同步汇集最终参数并一次原子封存整批，随后再进入 `acquire` 等待资源 |
| 内置同名冲突检查 | `registerTool` 遇到核心工具名或 `TOOL_NAME_ALIASES` 重名时，在插件解析阶段记录冲突诊断并跳过插件工具，保留未受监督的核心工具 | 采用无冲突统一工具名（`read_file`、`write_file`、`edit_file`、`apply_file_patch`、`list_dir`、`search_files`），设置 `tools.updatePlan: false` 并在 `tools.deny` 加入 `"memory-core"` 抑制原生同名工具注册，离线测试强制核验插件归属 |
| 文件桥上下文传递 | `SandboxFsBridge` 方法参数不含 `callId`，且在 `before_tool_call` 中设置的 `AsyncLocalStorage` 不会传播到内置工具执行 | 插件自有 `execute` 为每次调用构造词法绑定 `(callId, tracker)` 的专用 `SandboxFsBridge` 实例，直接传入 `createOpenClawCodingTools` 生成的原生读写/编辑/补丁/列目录实现，跨内部队列与多次 `await` 恒定保持精确身份 |
| `after_tool_call` 与取消终态 | `after_tool_call` 不阻塞下一轮模型输入，原生 abort 可能在底层文件操作退出前先返回 | 资源持有与释放完全由插件 `execute` 的 `try / finally` 配合在途 I/O 排空与 Go 服务 `end` 控制；不依赖 `after_tool_call` 释放锁 |
| 记忆能力与系统提示词 | `plugins.slots.memory = "memory-core"` 注册记忆运行时与 `## Memory Recall` 提示词构建器（检查可用工具含 `memory_search` / `memory_get`）；`tools.deny` 含 `"memory-core"` 仅屏蔽其原始工具导出 | 插件接管 `memory_search` 与 `memory_get`，禁用后台监听唤醒并在受监督调用身份与 `tracker` 排空保护下同步执行 `manager.sync({force: true}) -> search/readFile`，拒绝无调用上下文的记忆桥接请求 |

## 放弃的备选路径

1. **直接复用核心同名并修补 `node_modules/openclaw` 产物**：虽然可在构建块内注入自定义包装，但侵入锁定依赖内部散列模块，升级 OpenClaw 时存在高维护风险，不符合外部可扩展边界要求，已放弃并移除候选脚本。
2. **拆分为“工作区工具”与“外部目录工具”两套名称**：未来扩展额外授权目录时会导致工具面膨胀与模型选择歧义。现采用不带 `workspace_` 前缀的统一文件工具名，并在调度器使用 `(rootID, relPath)` 键预留多授权根演进空间。

## 预检与探针回归

- 探针正向对照的独立负例检查：`node --test scripts/fixtures/workspace-concurrency-probe/assertions.test.mjs`。
- 正式插件 SDK 预检与原生并发批次验证：`go test ./internal/executor -run 'TestWorkspacePluginSDKPreflight|TestNativeWorkspace' -count=1`（设置 `ASTERHAND_CHAT_RUNTIME=1` 时一并执行真实 Gateway 多批次集成验证）。
