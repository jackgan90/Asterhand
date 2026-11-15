# ADR-007：按资源声明与公开插件委托实现工作区工具细粒度并发

- 状态：accepted
- 日期：2026-11-15
- 所属领域与需求：执行器；EXEC-TOOLS-01—06、EXEC-SCHED-01—12
- 替代记录：无；本决策扩展持续会话工具轮内调度与插件工具归属，不改变本地脚本图调度或一次性任务 `Executor.Execute` 契约。

## 背景

首版本机工作区插件与 Go 文件服务均只保存单一活动调用，同一聊天轮次内的独立文件读取、修改与进度声明只能完全串行或直接冲突失败。此外，原先全局互斥还隐含保护了调用身份、句柄、前像复制、容量配额、记忆索引同步和取消收尾，简单移除单锁会导致跨调用串写或提前释放资源。

同时，锁定版本 OpenClaw `2026.10.7` 有两项关键约束：
1. 批处理先按模型调用顺序逐个运行 `before_tool_call` 准备钩子，随后才启动并行执行；若在 `before_tool_call` 内阻塞等待冲突资源，前序调用尚未启动，整批即死锁。
2. 内置核心工具（`read/write/edit/apply_patch/ls`）既不向 `SandboxFsBridge` 传递 `callId`，也不支持 `api.registerTool` 同名覆盖（同名插件工具会在解析期被静默跳过）。

## 决策

1. **零侵入公开 SDK 委托与统一工具命名**：不修改或修补 `node_modules/openclaw` 产物。由可信插件 `asterhand-workspace` 通过公开 `api.registerTool` 拥有全部九项工具（`read_file`、`write_file`、`edit_file`、`apply_file_patch`、`list_dir`、`search_files`、`memory_search`、`memory_get`、`progress_card`），内部委托 `openclaw/plugin-sdk/agent-harness`（`createOpenClawCodingTools` 配合按调用词法绑定的独立 `SandboxFsBridge`）与 `openclaw/plugin-sdk/memory-host-search`，通过 `tools.updatePlan: false` 与 `tools.deny` 屏蔽未受监督的原生同名工具注册，并在 `go test` 设置离线重名与归属预检门禁。
2. **预留多授权目录的单一文件工具面**：文件工具使用不带 `workspace_` 前缀的通用名称，默认相对路径解析到受管工作区；调度器资源键定义为 `(rootID, normalizedRelPath)`（当前 `rootID = "workspace"`），未来接入额外授权目录时复用同一套工具与冲突规则，不拆分为两套工具。
3. **两阶段批次封存与公平资源调度**：节点内建立共享资源调度器。`before_tool_call` 只做非阻塞顺序登记；各工具 `execute` 在同一微任务链收齐最终参数并一次原子封存整批声明（同批内若同一路径已有前序修改，后续无前文校验的全量覆写 `write_file` 在封存期以 `conflicting_batch_write` 拒绝，保留 `write_file → edit_file/read_file` 等有序管线与跨批次覆写），再申请全部资源（`S/X` 与 `exact/children/subtree`）和容量名额。冲突资源按批内顺序公平排队（禁止新读者饿死排队写者），不相交资源并发执行。
4. **逐次低层效果校验与排空式收尾**：每次低层文件/目录操作均校验精确调用身份、已授予范围与允许的效果集合；Windows 纯读采用独立句柄与 `FILE_SHARE_READ`（仍拒绝外部写/删），修改保持排他句柄与新文件发布；配额采用原子预留账本，版本清理固定活跃前像；取消与异常必须等待在途子 I/O 排空并持久化结果后才释放资源锁与轮次租约。

## 备选与后果

- **修补 OpenClaw 构建产物保留内置同名**：虽可沿用 `read/write` 旧名，但强耦合依赖内部散列 chunk，升级维护脆弱，故放弃。
- **按“工作区读写”与“外部目录读写”拆分两套工具**：未来扩展授权目录时造成工具膨胀与模型选择歧义；统一工具名配合服务端 `(rootID, relPath)` 路由与权限门禁更清晰。
- **仅靠工具级互斥组或乐观重试**：无法区分同工具不同文件，且多文件修改与未知副作用禁止自动重放；采用声明式资源调度加低层效果检查可兼顾细粒度并发、可验证性与未来工具扩展。

## 验证与关联

- [实施计划](../../development/features/workspace-tool-concurrency/plan.md)
- [公开 SDK 接入可行性与离线预检](../../development/features/workspace-tool-concurrency/preflight.md)
- [资源模型、工具映射与生命周期设计](../../development/features/workspace-tool-concurrency/design.md)
- [组合、变异、原生与 Windows 验证方案](../../development/features/workspace-tool-concurrency/validation.md)
- [本机工具能力与外部文件保护](../../assistant/runtime-capabilities.md)
