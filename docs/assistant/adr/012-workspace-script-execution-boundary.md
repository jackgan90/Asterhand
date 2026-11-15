# ADR-AST-012：本机工作区受限脚本执行、敏感目录保护与统一中断边界

- **状态**：accepted（覆盖第一阶段零审批工作区沙箱与统一四级停止契约；第二阶段审批提权策略保留独立扩展口）。
- **所属需求**：R03、R04、R23、R27；`AST-TOOLS-01`—`08`、`EXEC-TOOLS-01`—`06`、`EXEC-SCHED-01`—`12`。
- **设计与计划**：[本机工作区脚本与命令执行计划](../../development/features/workspace-script-execution/plan.md)、[本机工具能力与运行时适配](../runtime-capabilities.md)、[细粒度并发设计](../../development/features/workspace-tool-concurrency/design.md)。
- **替代关系**：扩展 [ADR-AST-010](010-local-runtime-capability-boundaries.md) 与 [ADR-AST-011](011-default-assistant-tools.md) 中“仅限工作区文件工具、不开放脚本执行”的初始边界；在不改变文件安全底座和人工开关门控的前提下，开放默认沙箱下的工作区脚本能力。

## 背景

在仅有读写与补丁工具时，助手无法直接运行脚本验证代码、转换数据或组合系统工具链，实用能力明显受限。用户要求在参考 Claude Code、OpenAI Codex CLI 与 OpenClaw 等成熟方案后，优先保障完整脚本能力且避免频繁审批打断体验，同时做好凭据与敏感目录安全防线、统一聊天“停止”按钮下对各类在途工具与脚本子进程的行为，并确保后续扩展到 macOS 与 Linux 时上层体验一致。

## 核心决策

1. **受信任 Go 服务经纪执行，禁止 Gateway 直接派生进程**：
   OpenClaw Gateway 进程环境持有模型 key（如 `DEEPSEEK_API_KEY`）与网关令牌。若由插件直接在 Node 内部 `child_process.spawn`，不仅难以安全隔离环境变量与 Windows 句柄继承，也会破坏 Go 服务对工作区配额、调度锁与停止排空的全权控制。因此继续保持 OpenClaw 内置 `exec`/`process`/`code_execution` 禁用，由 `asterhand-workspace` 注册统一工具 `exec_command` 和 `process_control`，经本地认证控制桥（`"exec/start"`、`"exec/poll"` 走 `waitSlots`，`"exec/kill"` 走 `controlSlots`，`"exec/control"` 走 `ioSlots`）委托 Go `workspace.Service` 统一启动、隔离与收尾；控制桥令牌通过 `${ASTERHAND_WORKSPACE_TOKEN}` 注入进程环境而不再明文写入 `openclaw.json`，且仅在只读/等待查询动作绑定每次调用 `AbortSignal`，`'end'`/`'cancel'` 始终独立完成收尾。
2. **第一阶段 Windows `WRITE_RESTRICTED`（`S-1-9-*` + `S-1-1-0 Everyone`）零审批写沙箱与诚实安全边界**：
   - **限制 SID 实证定案**：Win32 实测表明，若限制 SID 缺少 `S-1-1-0 (Everyone)`，`\Device\Null`、Python 3.14 DLL 初始化、PowerShell CLR/BCrypt 与 Git `/dev/null` 均无法工作；而 `S-1-5-11 (Authenticated Users)` 既不修复上述运行时又放开非系统盘写权限；`S-1-15-2/3-*` 会导致 `CreateRestrictedToken` 返回 `ERROR_INVALID_PARAMETER (87)`。因此采用合法的 `S-1-9-<u1>-<u2>-<u3>-<u4>` + `S-1-5-33 (WRITE_RESTRICTED_CODE)` + `LogonSID` + `S-1-1-0 (Everyone)` 配合 Medium Integrity 与 `TokenDefaultDacl`，仅向受管 `workspace` 与 `<journal>/sandbox-temp` 授予不含改权位的 `Modify (0x001301BF)` ACE（`EnsureDirGrant` 跳过 `FILE_ATTRIBUTE_REPARSE_POINT` 与 `NumberOfLinks > 1` 项），并在 `files_windows.go` 同卷原子发布后用初始化的 8 字节空 DACL（非 `NULL` DACL）调用 `SetSecurityInfo(UNPROTECTED_DACL)` 恢复父目录继承（若恢复失败则标为 `"unknown"` 待核对），彻底解决受管文件对脚本不可写与脚本篡改 ACL 问题。
   - **放弃 `HoldSecretFiles` 独占共享锁与如实界定同用户读边界**：早期设想在活跃脚本期对 `*.secret`/`node.key`/`web-login.token` 持 `dwShareMode=0` 锁，经核查会直接自锁 CLI `turns.stop`（`local_connection.go`）、`/api/info`、`/api/settings`、`tasks/engine.go` 与 `executor.go`，且同用户进程仍可访问 `asterhand.db` 并调用 `CryptUnprotectData` 解封 DPAPI 密文。环境变量隔离方面，`BuildEnvironment` 采用显式 OS 与开发工具链变量白名单继承（默认拒绝任意未列入的宿主机变量如 `*_KEY`、`DATABASE_URL` 及包含 `user:pass@` 凭据的代理 URL），不再依赖宽泛 `os.Environ()` 黑名单过滤。此外，在 Windows 上也实测验证了两种零 UAC 非提权内核禁读候选，均存在本质不兼容：在 `WRITE_RESTRICTED` 令牌下 `SepAccessCheck` 对纯读请求跳过 `TokenRestrictedSids`，使针对 `S_sandbox`/`S-1-5-33` 的 DACL `DENY` ACE 不拦截脚本读取（而向正常组加 `DENY` 会阻断宿主 `ssh.exe`/`asterhand.exe`）；若将子进程完整性级别降至 `Medium (S-1-16-8192)` 以下（`S-1-16-8191` 或 `Low S-1-16-4096`）以启用强制完整性标签 `NO_READ_UP`，则导致 `powershell.exe` 5.1、`pwsh.exe` 7、`node.exe` 与 `git.exe` 在启动时均报 `0xC0000142 (STATUS_DLL_INIT_FAILED)`，且 `ssh-keygen` 默认 `0600` 私钥权限不含 `WRITE_OWNER`。因此全面取消 `dwShareMode=0` 文件自锁与零 UAC 内核禁读尝试；在不通过 UAC 管理员创建独立低权账号以保全用户个人工具链（`.pyenv`、`uv`、`npm`、`go`）的前提下，Windows 第一阶段诚实定位为：**内核强制阻止向工作区/`sandbox-temp` 之外写入（含个人目录及驱动器，唯宿主显式授 `Everyone:Modify` 目录例外）+ OS/工具链白名单环境变量隔离（零节点/宿主外部秘密）+ 工作区与 `sandbox-temp` 解耦 + 启动前规范化 `sensitive_path_denied` 防误触卫兵**；不宣称可从内核阻断同用户恶意代码读取外部文件或 DPAPI。
   - **出站网络与跨平台对齐**：保留出站网络；为 Git 注入默认 `GIT_SSL_BACKEND=openssl`，对 Schannel/PowerShell 受限 HTTPS 失败返回清晰引导；上层契约、工具与 `internal/platform/sandbox` 接口跨平台一致，未来 macOS（Seatbelt）与 Linux（`bwrap`）在第二阶段及实机验证后同步提供敏感路径内核遮蔽读保护。
3. **前台互斥、后台持久状态与 `exec_active` 写屏障、无死锁 Turn-owned 收尾**：
   持续聊天启动的任何脚本均从属当前对话 `turn`，通过独立的 `execManager`（而非伪造 `exec-lease-*` 调度锁条目）管理每轮最多 8 个并发会话：前台同步等待期间持有工作区读写排他；超出 `yieldMs` 转入后台时先把 `<journal>/<sessionID>.json`（`"started"`）持久落盘并固定引用再释放 `DomainFS` 锁，使同轮只读、控制与多脚本并发不被自阻塞，同时由调度器 `exec_active` 屏障有序延迟受管 `write_file`/`edit_file`/`apply_file_patch` 直至活跃脚本退出（防止 `share=0` 争抢与前像竞态）；异步终态统一回写活动记录，崩溃遗留 `"started"` 在重启 `Open()` 自动转 `"unknown"` (`blocked=true`)，任何配额超限均由 `2s` 看门狗强杀该轮全部活动脚本并由退出后的 `reconcileAfterExec`（刷新 `publishGen`、记录 `unsafe_links_detected`、`overQuota` 与 `revision_conflict` 保护）兜底。
4. **统一四级“停止”与严格锁序**：
   所有工作区工具统一纳入四级中断语义：排队项直接取消；原子单文件写保持单文件边界；`search_files` 与 `memory_search` 通过 `AbortSignal` 与 `searchInternal` 协作循环实现尽早中止（明确承认 OpenClaw 原生 `manager.sync` 内核同步阶段不可抢占的窗口）；脚本进程树在 `CancelCall`、`CancelTurn`、`ReleaseTurn`（严格校验 `s.turn == turn && stopped == true`，且进程确认归零排空后即便对账/落盘出错亦置 `s.blocked=true` 并放行 `s.turn` 以防 `Observe` 永久卡死）与 `RuntimeStopped` 时均在 `s.mu` 锁外并行终止并异步执行有界排空（`<= 1500ms`），消除锁重入死锁。

## 备选与取舍

| 方案 | 结论 |
|---|---|
| 不带 `S-1-1-0 Everyone` 的 `WRITE_RESTRICTED` | 实测导致 `cmd > nul`、Python 3.14、PowerShell、pwsh 与 Git 全部无法启动，不可用 |
| 对 `web-login.token`/`.secret` 持 `dwShareMode=0` | 自锁 CLI 停止、设置读写与任务调度，且防不住 `asterhand.db` 同用户 DPAPI 解密，放弃 |
| 在 Windows 弹出 UAC 提权创建独立低权本地账号 | 破坏个人工具链可用性与无干扰安装原则；保留 `SandboxBackend` 窄接口供未来演进，不作当前选择 |
| Windows 零 UAC 下采用受限 SID `DENY` ACE 或 `< Medium` 完整性标签 `NO_READ_UP` 内核禁读 | 实测 `WRITE_RESTRICTED` 对纯读跳过受限 SID，而降至 `S-1-16-8191`/`Low` 会令 PowerShell 5.1、`pwsh` 7、Node.js 与 Git 启动报 `0xC0000142` 且 `0600` 私钥缺少 `WRITE_OWNER`，与核心工具链不兼容，放弃 |
| 转后台后继续永久占用 `DomainFS(workspace, Subtree, X)` | 锁死同轮 `read_file`/`write_file` 与并发第 2 个脚本，破坏 `yieldMs` 体验；改用前台锁 + 后台并行 `execManager` + 事后对账 |

## 后果

- Windows 下无需 UAC、无需修改工作区外系统 ACL 即可让主流脚本工具链直接生效，且 `write_file` 创建与版本恢复的文件均可无缝被脚本读写。
- 安全承诺透明可核对：准确区分全平台统一边界与 Windows 零 UAC 下核写/卫兵 vs macOS/Linux 内核防读的平台差异，以及 `Everyone:Modify` 与 Schannel 限制。
- 取消、轮次结束与关闭保持无死锁与有界收尾，兼容 `workspace_versions.restore` 的 `revision_conflict` 保护与 `sandbox-temp` 磁盘配额上限。
