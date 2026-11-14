# 原生文件修改的实施门槛

本文件说明[计划](plan.md) G3 的适配限制、可重复验证方法和文件 I/O 桥接方向。宿主工具探针直接构造固定 OpenClaw 包中的原生文件工具，不启动 Gateway、不调用在线模型、不更改用户节点。它能验证这些文件实现的竞争行为，不能代替 G2 的有效工具权限或 G4 的会话事件验证。

## 已确认的限制

OpenClaw 2026.10.7 的宿主 write/edit/apply_patch 在 `workspaceOnly` 下仍自行打开文件。调用前保存旧内容，再放行原生工具，无法保证被覆盖或删除的版本就是保存的版本：外部进程可以在两步之间原地写入、替换文件，或者抢先创建原本不存在的目标。

原生 write 会覆盖抢先创建的文件。edit 和补丁更新可能保留外部新加的部分，但节点记录的旧版本仍缺少这部分；随后恢复该旧版本会丢失外部修改。删除和移动也会对快照之后的版本执行，不能只靠工具内部的修改队列协调外部进程。

Windows `FileShare.Read` 保护能阻止外部写入/替换，但原生工具自行打开文件也受同一保护限制。持锁期间原生写入、编辑、补丁更新和删除失败；释放后正常成功。原生移动先创建目标、再删除源，源被锁定时可留下两份文件；这是必须记录的部分完成，不能自动重放补丁。

原生补丁新增使用独占创建，拒绝抢先创建的同名目标。这项有效保护没有解决已有文件修改和普通 write 的问题。测试因此把正常对照、独占创建与失败保护分开记录，不将“所有写入被拒绝”记为 G3 通过。

固定包中的核查入口：

- `dist/core-coding-tools-*.mjs`：`createCoreCodingTools`、`resolvePatchFileOps`、`applyPatch`。
- `dist/agent-tools.read-*.mjs`：`createHostMutationOperations`、`writeWorkspaceFile`。
- `dist/tools-*.mjs`：`createWriteTool`、`createEditTool`。
- `dist/config-contracts-*.d.ts`：`PluginHookBeforeToolCallResult` 允许修改参数、阻止或申请审批；该结果没有传入已锁定文件句柄或预期文件修订的字段。

这些是固定包的检查入口，不是产品可以依赖的公开 SDK。探针记录实际模块名和摘要，包结构变化须重新评估。仅凭本次结果不能断言所有未来版本或所有插件实现均不可行；当前“原生宿主工具 + 调用前快照/共享锁”方案没有通过门槛。

[2026.10.9 官方变更](https://docs.openclaw.ai/releases/2026.10.9)中与 workspace 相关的修复涉及 Gateway 构造的委派和媒体工具。该说明不是文件条件写入的证明，也不替代任何新版实测。本轮保留锁定依赖，不将候选升级记为通过。

## 复现

依照[脚本指南](../../../../scripts/README.md)准备依赖，使用清单锁定的 Node、OpenClaw 及全部传递依赖。在正常 Windows 用户进程下运行：

~~~powershell
& '<prepared-node-directory>/node.exe' tests/runtime-file-preflight/run.mjs `
    '<prepared-runtime-root>' '<build-output-directory>/audit-local-runtime-tools'
~~~

`prepared-runtime-root` 包含 `package-lock.json` 和 `node_modules`；输出目录取当前构建配置的 `OutputDir`（`scripts/build.ps1` 默认 `dist`）。探针只在输出目录下创建新的独立测试目录。入口为[探针](../../../../tests/runtime-file-preflight/run.mjs)、[外部修改进程](../../../../tests/runtime-file-preflight/external-write.mjs)和[Windows 共享锁进程](../../../../tests/runtime-file-preflight/hold-file.ps1)。

探针先校验 Node/OpenClaw 版本、许可证标识及仓库锁文件一致性；这些检查不重新验证 npm 下载归档的内容摘要，下载来源和完整性仍由依赖准备流程负责。随后运行 23 个场景：6 个正常操作，5 种已有文件操作各自的外部原地写入/替换/共享锁，以及普通 write 与补丁新增的抢先创建竞争。测试在单独进程中制造竞争，前像落盘并同步后再放行原生操作。

退出码 0、`probe: passed` 表示探针成功复现预期行为，**不表示 G3 通过**；`hostStrategy: blocked` 只针对上述宿主直写组合。现用正式桥接另由下面的测试验证。报告保留每例前像、执行前实际内容、工具结果及实际后像；共享锁例另外记录释放后成功或移动的部分完成。报告和测试文件留在生成目录，不作为产品数据。

若正常操作对照失败，整个探针为 failed，不能用后续拒绝解释安全性。限制子进程句柄操作的执行环境可能导致原生替换失败，应先在受控的普通 Windows 用户进程复现；不要据此修改产品权限或把环境失败当作 G3 的功能结论。

## 正式文件 I/O 桥接

固定版本正式 SDK `openclaw/plugin-sdk/sandbox` 提供 `registerSandboxBackend`，backend 可通过 `createFsBridge` 接管文件 I/O。原生 read/write/edit/apply_patch 保留工具 schema、文本变换和工具循环，由窄桥把实际读取、条件创建、写入和删除交给 Asterhand 服务；UI/CLI/MCP 复用同一服务。

产品使用上述正式扩展，不导入探针中的私有模块。接口名称含 sandbox 不代表安装了系统隔离环境；该后端拒绝脚本执行接口。文件服务与真实 Gateway 的验证分层进行：

- Windows 通过排他句柄读取前像、校验修订并保持旧文件和父目录身份；替换使用私有新文件、句柄重命名及不可覆盖发布，使竞争期间新建的硬链接仍保留旧字节。不能退回先检查后普通打开。
- 新文件使用不可覆盖的条件创建；移动保护源文件和目标目录，逐项记录结果；恢复使用同一修改实现。
- 保留现有预算、前像日志、工作区租约、未知结果核对和明确取消规则；普通设置不扩大模型权限。
- 只复用有适用许可和稳定入口的原生文本变换实现；没有正式接口时明确评估实现量，不将探针中的私有模块导入带入产品。
- 仍需通过 G1—G5 和 LT01—LT18，不能用此调整省略目录保护、版本恢复或用户验收。

服务验证入口是 [Windows 文件测试](../../../../internal/workspace/service_windows_test.go)和[分页/保留测试](../../../../internal/workspace/paging_windows_test.go)，覆盖外部进程原地写入、替换、父目录改名、抢先创建、已有/竞争硬链接、真实 junction、长路径、修订冲突、重启核对、版本保留和分页。原生适配入口是[原生工具集成测试](../../../../internal/executor/conversation_workspace_integration_test.go)及[取消测试](../../../../internal/executor/conversation_workspace_cancel_test.go)，通过真正 Gateway 和本地 SSE 模型验证工具 schema、读写编辑/补丁、关键词记忆、进度、重启、越界拒绝、禁止执行和取消后继续聊天。

两层共同验证正式桥，真实 DeepSeek、故障容量及用户工作流的剩余验收按[验证矩阵](validation.md)执行。依赖升级时重新运行这两层，不能只重跑宿主限制探针。
