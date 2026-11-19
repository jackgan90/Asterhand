# 本机工作区脚本与命令执行计划（第一阶段）

## 基本信息

- **功能名称**：`workspace-script-execution`（本机工作区受限脚本与命令执行）
- **目标与用户价值**：在不大幅削弱脚本能力（支持 Python、Node.js、PowerShell、pwsh、CMD、Git、编译工具链及出站网络）且不打断日常工作流（零频繁审批）的前提下，为本机持续聊天助手开放受限工作区内的脚本与命令执行能力；同时按平台诚实边界保护 Asterhand 凭据与用户常见敏感凭据目录，统一聊天“停止”按钮对所有在途工具与脚本子进程的中断语义，并在架构层面保持 Windows / macOS / Linux 一致的上层工具、调度与停止契约。
- **状态与阶段范围**：
  - **第一阶段**（Stage 1，当前交付）：实现、工程验证、独立审计修复复审与本机真实模型用户验收已完成；双机物理节点专项与跨平台实机验证按[验证方案](validation.md)独立推进。
  - **第二阶段**（Stage 2，未来独立提交）：聊天内嵌审批卡与 `UnrestrictedExceptProtected` 跨区提权、会话内目录/命令前缀授权、持久化信任规则、审批后脚本 SHA-256 TOCTOU 绑定。第二阶段不在第一阶段代码中预埋空开关或死分支。
- **关联 Rxx / Cxx / 领域需求 ID**：R03、R04、R23、R27；`AST-CHAT-04`、`AST-TOOLS-01`—`AST-TOOLS-08`；`EXEC-TOOLS-01`—`06`、`EXEC-SCHED-01`—`12`；[自动化需求](../../../automation/requirements.md)。
- **相关领域文档**：
  - [助手需求](../../../assistant/requirements.md)、[助手设计](../../../assistant/design.md)、[本机工具能力与运行时适配](../../../assistant/runtime-capabilities.md)、[持续会话设计](../../../assistant/conversations.md)
  - [执行器需求](../../../executors/requirements.md)、[执行器设计](../../../executors/design.md)、[细粒度并发设计](../workspace-tool-concurrency/design.md)
  - [日常操作覆盖清单](../../../automation/coverage.md)
- **ADR**：[ADR-AST-012：本机工作区受限脚本执行、敏感目录保护与统一中断边界](../../../assistant/adr/012-workspace-script-execution-boundary.md)。

## 范围

### 本次实现（第一阶段）

1. **受信任代理执行入口（Go 服务经纪）与 OpenClaw 插件适配**：
   - OpenClaw 运行时继续通过 `tools.deny` 彻底禁用内置 `"exec"`、`"process"`、`"code_execution"`。
   - 由可信插件 `asterhand-workspace` 注册统一工具 `exec_command` 与 `process_control`，通过经认证的本地工作区控制桥（`"exec/start"`、`"exec/poll"` 走 `waitSlots`，`"exec/kill"` 走 `controlSlots`，`"exec/control"` 走 `ioSlots`）委托 Go `workspace.Service` 执行；OpenClaw Gateway Node 进程不直接 `spawn` 脚本子进程，避免暴露注入其中的 `DEEPSEEK_API_KEY` 与 `OPENCLAW_GATEWAY_TOKEN`。
   - 将工作区本地桥认证令牌从磁盘 `openclaw.json` 明文配置迁移至环境变量 `${ASTERHAND_WORKSPACE_TOKEN}` 展开，不再落盘（`files.mjs` 同步保留 `process.env.ASTERHAND_WORKSPACE_TOKEN` 回退）。
   - `files.mjs` 仅在显式只读/等待调用（`search`、`memory.mjs` `runRequest`、`"exec/start"`、`"exec/poll"`）合并每调用 `AbortSignal`；`batch.mjs` 的 `'register'`、`'seal'`、`'cancel'`、`'end'` 始终使用独立超时信号而不复用已中止的工具 `signal`，确保取消/收尾 RPC 不会被自身中止；`tool-display.mjs` 新增 `'command'`（支持内联凭据模式脱敏：`Bearer ...`、`token/password/secret/api_key/key=...`、URL `user:pass@`）与 `'process'` 摘要规格。
2. **工作区默认沙箱免审执行、出站网络策略与受管环境隔离**：
   - 允许出站网络，不插入频繁弹窗审批。
   - **环境隔离与工具链可用性**：子进程通过显式 OS 与开发工具链变量白名单继承环境变量（仅放行系统路径、地区设置、主流编译/解释器与无凭据代理地址，前置已准备的 Node 运行时目录以保证开箱执行 `node`），注入 `PYTHONUTF8=1`、`PYTHONIOENCODING=utf-8`、`NO_COLOR=1` 与 Windows 默认 `GIT_SSL_BACKEND=openssl`（调用方未显式覆写安全 Git 配置时），将 `TEMP`、`TMP`、`TMPDIR`、`PIP_CACHE_DIR`、`UV_CACHE_DIR`、`NPM_CONFIG_CACHE`、`XDG_CACHE_HOME`、`GOCACHE` 重定向至独立于工作区的受管 `sandbox-temp`（位于 `<journal>/sandbox-temp`），默认拒绝未列入的宿主机密钥/服务环境变量（如 `LITELLM_MASTER_KEY`、`OPENROUTER_MANAGEMENT_KEY`、`DATABASE_URL`、带 `user:pass@` 的代理 URL 以及 `DEEPSEEK_*`、`OPENCLAW_*`、`ASTERHAND_*`、`*_KEY`/`*_SECRET`/`*_TOKEN`）。禁止调用方通过 `env` 参数覆写 `PATH`、`PATHEXT`、`COMSPEC`、`SYSTEMROOT`、`WINDIR`、`NODE_OPTIONS`、`PYTHONSTARTUP`、`PYTHONPATH`、`PYTHONHOME`、`DOTNET_STARTUP_HOOKS`、`GIT_SSH_COMMAND`、`LD_PRELOAD`、`DYLD_INSERT_LIBRARIES` 等危险变量（拒绝返回 `dangerous_env_denied`）。
   - **Windows 内核级写沙箱（经 Win32 探针实证定案）**：
     - 复用抽取至 `internal/platform/userprocess/spawn_windows.go` 的共享启动原语：Kill-on-close Job Object（`JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` + `PROC_THREAD_ATTRIBUTE_JOB_LIST`）与最小句柄继承清单 `PROC_THREAD_ATTRIBUTE_HANDLE_LIST`（不重复 `internal/localcommands` 实现）。
     - 确定性能力 SID `S_sandbox` 使用 `CreateRestrictedToken` 可接受的 `SECURITY_RESOURCE_MANAGER_AUTHORITY` 格式 `S-1-9-<u1>-<u2>-<u3>-<u4>`（由 `HMAC-SHA256("asterhand-workspace-sandbox-v1", lowerCleanedWorkspaceRoot)` 生成；绝不使用导致 `ERROR_INVALID_PARAMETER (87)` 的 `S-1-15-2/3-*`）。
     - `CreateRestrictedToken(DISABLE_MAX_PRIVILEGE | LUA_TOKEN | WRITE_RESTRICTED)` 限制 SID 列表为 `[S_sandbox, S-1-5-33 (WRITE_RESTRICTED_CODE), LogonSID（若存在）, S-1-1-0 (Everyone)]`，Integrity Level 取 `min(ParentIL, Medium S-1-16-8192)`，`TokenDefaultDacl` 授予 `CurrentUser (GENERIC_ALL)`、`S_sandbox (GENERIC_ALL)` 与 `LogonSID (GENERIC_READ|GENERIC_EXECUTE)`。
     - **为何必须保留 `S-1-1-0 (Everyone)`（已实证）**：不带 `Everyone` 时，Windows `\Device\Null`（`cmd > nul` / Git `/dev/null`）、Python 3.14 DLL 初始化（`0xC0000142`）、Windows PowerShell CLR（`80070005`）及 PowerShell 7 `BCrypt.dll`（`0x8007045A`）在内核加载即失败；改用 `S-1-5-11 (Authenticated Users)` 既无法运行上述 5 类运行时，又会意外放开辅助 NTFS 盘（`F:\` 默认授 `Authenticated Users:Modify`）。保留 `S-1-1-0` 后上述全部运行时正常，且 `%USERPROFILE%`、`C:\`、`C:\Windows`、`Program Files`、Asterhand `Home` 与默认辅助盘写入均被内核拒绝；**唯一剩余风险**是宿主机上被显式授 `Everyone:Modify` 的共享目录仍可写入（已在领域文档与验证边界中如实披露）。
     - **窄化工作区 ACL 与 NTFS staging 重命名继承修复**：仅对 Asterhand 拥有的 `workspace` 与 `<journal>/sandbox-temp` 幂等授予 `(A;OICI;0x001301BF;;;<S_sandbox>)`（即 `Modify`，刻意**不含** `WRITE_DAC` 与 `WRITE_OWNER`，阻止脚本篡改或锁死 ACL），绝不修改用户桌面、文档、系统盘或用户主目录 ACL。`EnsureDirGrant` 递归修复时严格通过 `FILE_FLAG_OPEN_REPARSE_POINT | FILE_FLAG_BACKUP_SEMANTICS` 检查并**跳过所有 `FILE_ATTRIBUTE_REPARSE_POINT != 0` 与 `NumberOfLinks > 1` 项**，绝不跟随 junction/symlink 或改写外部对象 DACL。针对 `internal/workspace/files_windows.go` 中 `heldFile.write()` 在 `staging` 创建 `.new` 后同卷 `renameHandleIn` 导致继承中断的问题，在打开 `.new` 时增加 `windows.WRITE_DAC` 并在 `renameHandleIn` 发布后立即使用显式初始化的 8 字节空 DACL（`InitializeAcl(acl, 8, 2)`，绝对不传 `NULL` `pDacl` 以防生成 `Everyone:FullControl`）调用 `SetSecurityInfo(..., DACL_SECURITY_INFORMATION | UNPROTECTED_DACL_SECURITY_INFORMATION, ...)` 恢复父目录 `S_sandbox` 继承；若 `SetSecurityInfo` 在发布后返回错误，标记该句柄失败并在 `observe()` 中使对应操作转为 `"unknown"`（`s.blocked = true`）待核对。
   - **Windows Schannel TLS 行为处理**：`exec/start` 默认在清理环境中注入 `GIT_SSL_BACKEND=openssl` 以保障 Git HTTPS 正常使用；对显式调用 Windows WinHTTP/Schannel 的工具（原生 `curl.exe` 退出报 `SEC_E_NO_CREDENTIALS` / `0x8009030E`，或 PowerShell `Invoke-WebRequest`/`Invoke-RestMethod` 报 `"The underlying connection was closed"` / `AuthenticationException`），服务端匹配两类失败特征后在结果中附加结构化 `tls_hint: "windows_schannel_restricted"`，引导改用 Python（OpenSSL）、Node.js（BoringSSL）或配置 OpenSSL TLS，而非仅匹配字面 `0x8009030E`。
   - **跨平台 `SandboxBackend` 抽象**：定义 `internal/platform/sandbox` 接口；非 Windows 目标提供干净 `Supported() == false` 存根，并保持工具定义、调度、`sensitive_path_denied` 预校验与 Turn-owned 生命周期跨平台统一。
3. **凭据隔离与敏感目录保护边界（取消自锁，诚实分层）**：
   - **不使用 `HoldSecretFiles()` (`dwShareMode = 0`)**：Windows 上 `local_connection.go`（CLI `turns.stop` 读 `web-login.token`）、`api.go`（`/api/info`、`/api/settings` 读改 `.secret`）、`tasks/engine.go` 与 `executor.go` 在活跃脚本期仍需读取/更新配置凭据，且 `peer.OutboundToken` 以 DPAPI 密文存放在 `asterhand.db` 中并可被同用户进程调用 `CryptUnprotectData` 解封；因此**不采用**会导致平台自锁的 `dwShareMode = 0` 文件锁。
   - **第一阶段防泄漏与防误触机制**：
     1. 环境变量剥离 + 工作区桥令牌 `${ASTERHAND_WORKSPACE_TOKEN}` 零落盘到 `openclaw.json`；
     2. 独立安置 `<journal>/sandbox-temp`，使 `workspace` 与 `<runtime>/openclaw.json` 解耦；
     3. 服务端启动前规范化路径守卫（覆盖 `workdir`、Windows `GetFinalPathNameByHandle` / 跨平台 `EvalSymlinks` 真实路径与悬空链接 `Readlink` 解析、长路径与祖先归一化、引号/空格/括号感知命令词元扫描与 `env` 值检查）：引用落入工作区与 `sandbox-temp` 之外的 Asterhand `Home`，或用户敏感凭据路径（`~/.ssh`、`~/.aws`、`~/.gnupg`、`~/.kube`、`~/.docker`、`~/.npmrc`、`~/.pypirc`、`~/.netrc`、`~/.git-credentials`、`~/.vault-token`、`~/.config/gh`、`~/.config/gcloud`、`%APPDATA%\GitHub CLI`、`~/Library/Keychains`、`~/.local/share/keyrings`）直接返回 `sensitive_path_denied`，并经工作区服务与 HTTP 控制桥原样透传错误码而不折叠为 `io_error`。
     4. **平台分层事实声明**：Windows 零 UAC（保留当前用户以便 `.pyenv`/`go`/`uv`/`npm` 免提权运行）提供**内核级工作区外禁写**与**启动前路径防误触卫兵**，不对同用户恶意脚本或同用户 DPAPI 宣称内核禁读；后续 macOS（Seatbelt `deny file-read*`）与 Linux（`bwrap --tmpfs` 遮蔽）将在第二阶段及多平台实机验证（含 Ubuntu 24.04+ AppArmor unprivileged userns 与 macOS SIP 测试）后提供内核级敏感目录禁读。
4. **独立 `execManager`、后台持久状态与 `exec_active` 写屏障、`sandbox-temp` 配额与事后对账**：
   - 以一等模块 `internal/workspace/exec_manager.go` 替代伪造调度条目 `exec-lease-*`：
     - **持久后台会话操作记录（崩溃可核对且异步终态可追踪）**：启动脚本前预留额外 `MaxOperationBytes` 活动额度；若在 `yieldMs` 内结束则退还该预留并由原工具 `callID` 记账；`m.backend.Start` 与单次 `"exec/start"` HTTP 请求 `r.Context()` 解耦（使用 `context.Background()`，而前台等待切片仍遵循调用方 `ctx` 取消），确保 `yieldMs=0` 立即转入后台与 `yieldMs>0` 超时转入后台的会话能在同轮后续 HTTP 请求间持续运行；一旦超出 `yieldMs` 转入后台，先以 `Operation` 原型持久落盘 `<journal>/<sessionID>.json`（`Tool: "exec_command"`, `State: "started"`, `Phase: "running"`, 固定引用）再向插件返回 `{status: "running", sessionId}`。异步退出/超时/被杀时，由后台收尾器更新并持久化 `<journal>/<sessionID>.json`（`completed` / `cancelled` / `failed` + 详细原因如 `exit_code=<n>` / `timeout` / `workspace_limit` / `sandbox_temp_limit` / `unsafe_links_detected`；若对账或落盘失败则记 `unknown` 并设 `s.blocked = true`），保证即使模型未再轮询，活动记录与崩溃重启 `Open()`（自动把任何 `"started"` 转 `"unknown"` 并置 `s.blocked = true`）都能完整接管并交由 `workspace_operations.resolve` 核对。
     - **前台排他与后台 `exec_active` 写屏障**：前台 `exec_command` 在同步等待窗内（`<= min(timeoutMs, yieldMs)`）申请 `DomainFS("", Subtree, X)` + `DomainMemorySources("sources", Exact, X)`（`Modifying: true`），保证短命令与同批读写严格串行；转入后台完成 `End()` 释放 `DomainFS` 锁后，同轮 `read_file`/`list_dir`/`search_files`/`memory_search`/`memory_get`/`progress_card`/`process_control` 以及最多 `MaxExecSessionsPerTurn = 8` 个**并发在途** `exec_command` 均可并发放行而不自阻塞（普通读写控制工具不占用该名额，脚本退出或被 `process_control(kill)` 终止后立即归还名额，可继续顺序执行直至 500 次单轮总工具预算）；**同时 `scheduler.canGrantLocked` 实施 `exec_active` 写屏障**——只要 `execManager` 存在活动后台脚本，非脚本的受管文件写入（`e.plan.Modifying && e.tool != "exec_command"`，即 `write_file`/`edit_file`/`apply_file_patch`）排队等待 `waitReason = "exec_active"` 直至后台脚本退出或被杀（退出后 `sched.wake()` 唤醒），避免与运行中脚本抢占 `share=0` 句柄或污染 `reserveForCall` 前像。
     - `process_control` 仅声明 `DomainExec("control", Exact, S)`（`Modifying: false`），`poll` 走 `waitSlots`（`24` 槽）做 `<= 2500ms` 有界等待，`kill` 走 `"exec/kill"`（`controlSlots`，`12` 槽），`status`/`log`/`list` 走 `"exec/control"`（`ioSlots`，`12` 槽，`totalSlots = 48`）。
   - **`sandbox-temp` 磁盘配额与超限强杀**：为 `<journal>/sandbox-temp` 施加独立上限 `MaxSandboxTempBytes = 256 MiB` / `MaxSandboxTempFiles = 10,000`；在 `Open`、`AcquireTurnScope` 及 Turn 内最后一个活动脚本结束时安全清理（跳过 reparse point，并在删除前清除非 reparse 项的 `FILE_ATTRIBUTE_READONLY` 只读属性避免 `.git/objects` 或包缓存删不动）。活动脚本期间每 `2s` 巡检工作区与 `sandbox-temp` 用量，任一超限时锁存 `overQuota` 并强杀**该 Turn 内全部活动脚本会话**（`workspace_limit` / `sandbox_temp_limit`）；并在 `reconcileAfterExec` 与文件写/删后维护 `overQuota`，对脚本产生的意外 `FILE_ATTRIBUTE_REPARSE_POINT` / `NumberOfLinks > 1` 硬链接记录 `unsafe_links_detected` 并在可读目录扫描中跳过避免后续工具误算或穿透。
   - 多编码输出解码抽离至共享包 `internal/platform/textcodec`（UTF-8 / UTF-16LE BOM / WHATWG GB18030），供 `localcommands` 与 `sandbox` 共同复用，单流保留上限 `256 KiB`。
5. **统一四级聊天“停止”中断语义与无死锁 `ReleaseTurn`**：
   - **第一级（未获调度授予与等待队列）**：`CancelTurn` / `CancelCall` 立即关闭准入并标记 `cancelled`。
   - **第二级（正在提交原子单文件写 `write_file`/`edit_file`/`apply_file_patch`）**：保持单文件临时写、ACL 重继承恢复与原子重命名边界，关闭同批剩余目标准入并保留已落盘文件。
   - **第三级（协作可中断扫描 `search_files` / `memory_search`）**：`files.mjs` 仅对只读/等待请求合并 per-call `signal`；`memory.mjs` 通过 `AsyncLocalStorage` 传递 `ctx.signal` 至 `assertCurrent()` 与每次桥接 I/O（OpenClaw `manager.sync` 纯内存 SQLite FTS 步待下次 `throwIfAborted()` 或 I/O 响应）；Go `read.go` `SearchWithCallContext` 检查 `ctx.Err()` 与 `schedEntry.isCancelled()`，使 `search_files` 在毫秒级返回 `cancelled`。
   - **第四级（Turn-owned 脚本进程树、无死锁 `CancelTurn` / `ReleaseTurn` / `RuntimeStopped`）**：
     - `CancelCall(id, detail)` 除关闭调度项外还主动强杀仍绑定到该前台 `callID` 的活动脚本，覆盖单独工具 `AbortSignal` 先于 `CancelTurn` 到达的窗口（`exec.mjs` 同时尽最大努力发 `"exec/kill"`）。
     - `CancelTurn(turn)` 首先在 `s.mu` 锁外调用 `s.exec.InitiateTurnStop(turn, "cancelled")` 关闭准入并并行强杀该 Turn 全部在途/后台会话 Job Object，再取消调度项。
     - `ReleaseTurn(turn, stopped)` 先短暂持有 `s.mu` 校验 `s.turn == turn` 与 `stopped` 布尔位：若 `s.turn != turn` 直接返回 `nil`；若 `!stopped` **直接返回 `fail("cleanup_pending")` 而不终止运行中脚本**。确认 `s.turn == turn && stopped == true` 后释放 `s.mu`，在锁外调用 `s.exec.InitiateTurnStop(turn, "cancelled")` 并等待/判定全部进程终止（`ProcessesStopped(turn)`，即 `JobProcessCount == 0` 且管道排空结束）：进程仍活跃时返回 `fail("cleanup_pending")` 供 `Observe` 推进；待全部 OS 进程确已退出且管道排空后执行 `reconcileAfterExec` 与 `sandbox-temp` 清理，即便该收尾/落盘自身发生错误，也按[本机工具能力设计](../../../assistant/runtime-capabilities.md)置 `s.blocked = true`（确保存在 `"unknown"` 记录待人工核对）并清空 `s.turn` 返回 `nil`，绝不令 `Observe` 永久卡死在 `"running"`。`RuntimeStopped()` 同样在持有 `s.mu` 外部调用 `s.exec.StopAll(3 * time.Second)` 并将未完成 `es_*.json` 标记为 `"unknown"`。

### 明确非目标（阶段边界）

- 第二阶段能力：聊天内嵌审批卡、跨工作区全局读写放行（`UnrestrictedExceptProtected`）、目录/命令前缀会话级或永久放行规则、审批时刻 TOCTOU 脚本 SHA-256 绑定。
- 安装与生产级实现 macOS（Seatbelt）及 Linux（`bwrap`）的节点运行时打包与真机验证（本阶段仅交付干净可编译抽象边界与 `Supported() == false` 存根）。
- 修改用户工作区与受管 `sandbox-temp` 外部磁盘的 ACL、脱离对话轮次永久驻留的通用后台守护进程池，或向远端 peer 暴露 `exec_command`。

### 已核实平台与运行时约束

- **Windows `WRITE_RESTRICTED` 令牌兼容性（已纳入 `E1`–`E3` 自动化测试核实）**：
  - `S-1-9-...` + `[S_sandbox, S-1-5-33, LogonSID, S-1-1-0]` 下 `cmd > nul`、Python 3.14.2、PowerShell 5.1、PowerShell 7、Git、随包 Node 24.22.1 均可正常工作；
  - `S_sandbox` 授予 `Modify (0x001301BF)` + `files_windows.go` 同卷移动后 `InitializeAcl(8)+SetSecurityInfo(UNPROTECTED_DACL)` 修复继承后，`write_file` 创建的文件可被 `exec_command` 正常修改且子进程 `icacls` 改权失败（`EXIT=5`）。
- **OpenClaw 2026.10.7 环境变量展开与插件策略（已纳入 `TestWorkspacePluginSDKPreflight` 与真实本地 Gateway 集成测试核实）**：
  - `${ASTERHAND_WORKSPACE_TOKEN}` 在 `openclaw.json` 插件配置中正常展开；
  - 11 项工具不与 OpenClaw `createOpenClawCodingTools` 或 `TOOL_NAME_ALIASES` 重名冲突。

### 验收用例及预期可见结果

1. **工作区脚本编写、修改并执行（含 `write_file` -> `exec_command` -> `read_file` 联动）**：助手通过 `write_file` 在工作区编写脚本，再用 `exec_command` 运行、生成并覆写 `write_file` 创建的文件，`read_file` 可正常读回中文 UTF-8/GB18030 输出与结果；整个过程零审批弹窗。
2. **工作区外写入拦截与凭据防泄漏**：脚本向 `%USERPROFILE%`、`Home`、驱动器根目录写文件受内核 `ERROR_ACCESS_DENIED` 拒绝；试图 `icacls` 修改工作区 DACL 失败；环境内零 `ASTERHAND_*`/`DEEPSEEK_*`/`OPENCLAW_*` 凭据；参数/命令/env 中引用 `~/.ssh`、`~/.aws` 等敏感路径或危险环境变量在启动前触发 `sensitive_path_denied` / `dangerous_env_denied`；活跃脚本期不阻塞 CLI `turns.stop` 或 `/api/info`。
3. **后台脚本共存、`exec_active` 写屏障与轮次强清**：`exec_command` 超出 `yieldMs` 返回 `{status: "running", sessionId}` 并持久登记 `<journal>/es_*.json`（`"started"`）后，同轮 `read_file`、`search_files` 与第 2 个并发 `exec_command` 均可正常执行不被锁死，`write_file` 在脚本运行期有序排队（`waitReason = "exec_active"`）并在脚本退出后自动放行，`process_control(action: "poll" | "log" | "kill" | "list")` 可管理多会话；若未再次轮询即结束或崩溃重启，异步终态与 `"unknown"` 均可通过活动/操作接口核对；若模型未回收即结束回复，`Observe`/`ReleaseTurn(turn, true)` 非阻塞触发并行终止并待进程与管道排空对账后放行。
4. **聊天“停止”四级中断**：长耗时脚本（含孙进程树与多并发会话）或大目录 `search_files` 运行期间触发停止，`search_files` 毫秒级取消返回 `cancelled`，全部脚本进程树在 `< 2s` 内强杀排空、先前工具已写入文件保留、`sandbox-temp` 清空并可立即发送下一条消息。

## 实施与文档同步

### 交付模块与验证闭环

1. **平台共享原语与 `internal/platform/sandbox`**：
   - 新增 `internal/platform/textcodec`（下沉自 `internal/localcommands/{log_encoding,log_text}.go`）；
   - 新增 `internal/platform/userprocess/spawn_windows.go`（与 `internal/localcommands/execution_windows.go` 共享 Kill-on-close Job Object 与 AttributeList 启动逻辑）；
   - 新增 `internal/platform/sandbox/{sandbox.go,env.go,pathguard.go,output.go,acl_windows.go,sandbox_windows.go,sandbox_other.go,sandbox_windows_test.go}`。
2. **工作区服务、NTFS 继承修复、`execManager` 与四级停止**：
   - 更新 `internal/workspace/files_windows.go` 修复 `heldFile.write()` 同卷移动后 `S_sandbox` DACL 继承；
   - 更新 `internal/workspace/{types.go,descriptor.go,descriptor_registry.go,read.go,journal.go,scheduler.go,service.go,call_io.go,transport.go}` 并新增 `internal/workspace/{exec_manager.go,exec_service.go,exec_windows_test.go}`。
3. **OpenClaw 插件、Token 环境变量化、提示词、摘要与 i18n 守卫**：
   - 更新 `internal/executor/{conversation.go,conversation_config.go,conversation_workspace.go}` 与 `internal/executor/workspaceplugin/{openclaw.plugin.json,files.mjs,memory.mjs,tools.mjs}` 并新增 `internal/executor/workspaceplugin/exec.mjs`；
   - 更新 `internal/executor/chatbridge/tool-display.mjs`、`internal/prompt/default-chat.txt`、`internal/i18n/guard-rules.json` 及 `internal/i18n/locales/{zh-CN,en-US}.json`，执行 `sync-i18n.mjs` 与 `check-i18n.ps1`。
4. **全量验证与审计验收**：
   - 已完成全部工程测试（含 `TestWorkspacePluginSDKPreflight`、真实本地 Gateway 测试、`go test -count=1 ./...`、`go vet ./...`、`linux/amd64` 与 `darwin/arm64` 交叉编译、CLI/MCP 集成测试、Node `tests/*.test.mjs` 与 `check-i18n.ps1`）、独立代码质量审计复审及本机 Windows 真实模型人工验收；详细前置条件与支持边界见[验证方案](validation.md)。

### 必须更新的领域文档及实质内容

- [助手需求](../../../assistant/requirements.md)、[助手设计](../../../assistant/design.md)、[本机工具能力与运行时适配](../../../assistant/runtime-capabilities.md)、[ADR-AST-012](../../../assistant/adr/012-workspace-script-execution-boundary.md)
- [执行器需求](../../../executors/requirements.md)、[执行器设计](../../../executors/design.md)
- [日常操作覆盖清单](../../../automation/coverage.md)、[开发路线图](../../roadmap.md)、[文档导航](../../../README.md)、[测试入口](../../../../tests/README.md)

### 日常操作覆盖评估

- 新增模型工具 `exec_command` 与 `process_control` 属于聊天运行时内部由助手发起的执行能力，不引入新的人类设置表单；权限仍由现有“设置 → 助手”的“允许助手调用工具”人工例外开关管理。
- 新增 `scripts.execute` 能力声明、`exec_timeout_seconds`、`exec_output_bytes`、`exec_sessions_per_turn`、`sandbox_temp_bytes` 限制与活动收据均通过既有公共操作（`workspaces.get`、`conversations.capabilities`、`turn_activities.list/get`、`workspace_operations.get`、`messages.list`）在 `contracts/automation-coverage.json` 下向 UI、CLI 与 MCP 统一暴露，不新增仅 UI 业务入口。

## 规模与拆分评估

- 现有受影响生产文件 `internal/workspace/service.go`（基线 643 行 / 第一阶段后 761 物理行）、`internal/workspace/call_io.go`（基线 528 行 / 第一阶段后 580 物理行）、`internal/workspace/files_windows.go`（基线 427 行 / 第一阶段后 508 物理行）及新增独立模块 `exec_manager.go`（448 物理行）、`exec_service.go`（843 物理行）和 `internal/platform/{textcodec,userprocess,sandbox}`、`workspaceplugin/exec.mjs` 均满足单文件 `< 1000` 物理行、单函数 `<= 100` 行软约束。

## 验证与维护

- **仓库测试入口、复现方法和预期结果**：见[验证方案](validation.md)。
- **当前平台与边界**：Windows 零 UAC 下仅内核限制工作区外写入，同用户读取与 DPAPI 受“与当前开发工具链同用户免提权运行”前提约束而不做内核禁读；macOS/Linux 在本阶段保持接口存根，尚未完成实机验证。
