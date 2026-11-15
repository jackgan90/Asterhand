# 本机工作区脚本与命令执行验证方案（第一阶段）

本文说明 `workspace-script-execution` 第一阶段的可复现工程验证入口、前置条件、预期结果及支持边界；具体设计约束见[功能计划](plan.md)与[ADR-AST-012](../../../assistant/adr/012-workspace-script-execution-boundary.md)。

## 前置条件

1. Go `1.27.3` 工具链与依赖 manifest（若 `go` 不在系统默认 `PATH`，将工具链 `bin` 目录前置加入 `$env:PATH`）。
2. Node.js `24.22.1` 及锁定 OpenClaw `2026.10.7` 准备目录（`<prepared-runtime>`）。
3. 普通（非 UAC 管理员）Windows 登录会话与 NTFS 本地文件系统；若开发盘开启第三方受控文件夹或强杀软拦截，受限环境报错按环境阻塞单独记录。
4. `-race` 竞态检测需要 `CGO_ENABLED=1` 与本地 C 编译器；在无 C 工具链的 `CGO_ENABLED=0` 宿主机上，移除 `-race` 执行同款 `go test` 命令，并将竞态检测结果显式记为 `skipped`。

## 验证分组与复现命令

### 1. 共享文本解码、进程启动原语与 Windows 内核沙箱（`E1`–`E3`）

覆盖 WHATWG UTF-8 / UTF-16LE / GB18030 流式解码、共享 Job Object 进程树启动、`S-1-9-*` + `S-1-1-0` `WRITE_RESTRICTED` 令牌隔离、环境凭据剥离、路径守卫与 `<journal>/sandbox-temp` 安全清理：

```powershell
go test -race -v ./internal/platform/textcodec ./internal/platform/userprocess ./internal/platform/sandbox ./internal/localcommands -count=1
```

预期结果：
- **E1（内核心写限制与主流工具链可执行性）**：受限子进程可运行 `cmd.exe /c "... > nul"`、`node.exe`（随包运行时）、`powershell.exe`、`pwsh.exe`、`git.exe` 及本机 `python.exe`（若存在），可在 `workspace` 与 `sandbox-temp` 创建/重命名/删除文件；尝试向 `%USERPROFILE%`、`Home` 或独立测试目录写入收到内核 `ACCESS_DENIED`；在工作区文件上执行 `icacls ... /grant Everyone:F` 因仅授 `Modify`（无 `WRITE_DAC`）返回拒绝（`EXIT != 0`）；非托管目录原有 ACL 零变更。
- **E2（环境白名单零泄漏、孙进程树清零与 Schannel 提示）**：子进程仅继承 OS/工具链白名单变量与无凭据代理 URL，不含 `DEEPSEEK_*`、`OPENCLAW_*`、`ASTERHAND_*`、未列入的宿主机变量（如 `LITELLM_MASTER_KEY`、`OPENROUTER_MANAGEMENT_KEY`、`CUSTOM_SERVICE_SIGNING_KEY`、`DATABASE_URL`）及含 `user:pass@` 的代理凭据；`Terminate()` 通过 Job Object 杀灭派生孙进程树且 `JobProcessCount == 0`；检测 Schannel/PowerShell HTTPS 受限失败特征并返回 `tls_hint = "windows_schannel_restricted"`，Node HTTPS 本地回环请求成功；`localcommands` 既有测试回归通过。
- **E3（规范化敏感路径守卫、解码与 junction/只读清理）**：`workdir` 逃逸、junction/symlink 跳出、8.3 短名绕过、`~/.ssh` / `~/.aws` / `<home>/config.json` / `<home>/conversations`（含 `~`、`%USERPROFILE%`、`$env:USERPROFILE`、引号包裹、空格与括号路径、外层括号包裹及含空格的工作区 junction）与工作区父目录穿越（`..`）引用，以及危险环境变量（`PATH`/`PYTHONSTARTUP`/`NODE_OPTIONS` 等）篡改均在启动前返回 `sensitive_path_denied` / `dangerous_env_denied`；嵌套在受保护 `<home>` 下的 `workspace` 与 `sandbox-temp` 合法路径（含双/单引号、反引号转义引号、`\"...\"`、`file:///` URI 及带空格/括号文件名）放行；GB18030 与 UTF-16LE 中文输出无乱码且 `256 KiB` 截断正确；`EnsureDirGrant` 严格跳过 junction/硬链接（`NumberOfLinks > 1`）不改外部 DACL，`sandbox-temp` 内建指向外部目录的 junction 与只读（`0444`）缓存文件时，清理正确跳过外部目标并解除只读属性完成删除。

### 2. 工作区调度器、NTFS 继承修复、`execManager` 配额与四级停止

覆盖 `write_file` -> `exec_command` 同文件互操作、后台会话不自阻塞同轮读写、无死锁 `CancelTurn`/`ReleaseTurn`、`sandbox-temp` `2s` 配额看门狗与 Tier 3 搜索毫秒取消：

```powershell
go test -race -v ./internal/workspace -count=1
```

预期结果：
- `files_windows.go` 同卷重命名发布后文件正确继承 `S_sandbox` `Modify` ACE：由 `write_file` 创建并在 `workspace_versions.restore` 恢复的文件均可直接被同轮 `exec_command` 修改并由 `read_file` 读回；若模拟发布后 `SetSecurityInfo` 失败，对应操作转为 `unknown`（`blocked=true`）。
- 同步 `exec_command` 与同批文件/记忆工具串行；经 HTTP `Transport`（`POST /` `"exec/start"`，含 `yield_ms = 0` 立即转入与 `yield_ms > 0` 等待超时转入）转入后台持久登记 `<journal>/es_*.json`（`"started"`）并返回 `{status: "running", sessionId}` 后，不受单次 HTTP `r.Context()` 结束取消影响而继续运行，并在同轮 HTTP `"exec/poll"` 正常返回终态；期间同轮 `read_file`、第二个 `exec_command`（最多 `8` 个并发在途会话；单轮顺序执行 `10+` 次短命令均正常放行，满 `8` 个并发时第 `9` 个返回 `exec_session_limit`，调用 `process_control(kill)` 终止其一后可立即复用空出槽位）与 `process_control`（`poll`/`log`/`kill`/`list`）均并发放行，`write_file` 排队等待 `waitReason = "exec_active"` 直至后台脚本退出后自动执行；即使不再调用 `process_control(poll)`，异步退出/超时终态也会持久化并出现在 `Activities`；若模拟在后台脚本未退出时崩溃并重启 `Open()`，遗留 `"started"` 会话转入 `"unknown"` + `blocked=true` 且可通过 `ResolveRequest` 核对解除；`*sandbox.Error`（如 `sensitive_path_denied`、`dangerous_env_denied`）通过 `Service.StartExec` 与 HTTP `Transport`（`POST /` `"exec/start"`）原样透传错误码而不折叠为 `io_error`。
- `CancelTurn` 在 `search_files` 大目录扫描期间使其在远低于 5 秒扫描预算的毫秒级窗口内返回 `cancelled`；`CancelCall` 立即强杀对应前台脚本；`ReleaseTurn(turn, false)` 返回 `cleanup_pending` 且不误杀在途脚本；`ReleaseTurn(turn, true)` 在 `-race` 下无死锁完成进程树强杀与对账（即使强制注入收尾落盘错误也会标记 `blocked=true` 并释放 `s.turn`，不令 `Observe` 永久卡死）。
- `sandbox-temp` 或 `workspace` 被脚本写超 `256 MiB / 10,000` 项上限时，`2s` 看门狗主动终结同一轮次内**全部**活动脚本会话并锁存 `overQuota`；脚本改动已有文件后携带旧修订调用 `workspace_versions.restore` 触发 `revision_conflict`，意外生成的 junction 被计入 `unsafe_links_detected` 并在可读列表/搜索中安全跳过。

### 3. OpenClaw SDK 离线预检、UI/i18n 守卫与本地 Gateway 原生集成

首先执行无外网的静态/半真实测试与全量 Node UI 回归：

```powershell
go test -v ./internal/executor -run TestWorkspacePluginSDKPreflight -count=1
node ./scripts/sync-i18n.mjs --check
$uiTests = Get-ChildItem -LiteralPath tests -Filter '*.test.mjs' -File
node --test $uiTests.FullName
pwsh -NoProfile -File ./scripts/check-i18n.ps1 -Go go
```

随后指向本地已准备的锁定运行目录运行真实 OpenClaw `2026.10.7` Gateway 本地受控 SSE 测试（无真实在线模型费用）：

```powershell
$env:ASTERHAND_CHAT_RUNTIME = '1'
$env:ASTERHAND_CHAT_NODE = '<prepared-runtime>/node-distribution/node-v24.22.1-win-x64/node.exe'
$env:ASTERHAND_CHAT_ENTRY = '<prepared-runtime>/runtime/node_modules/openclaw/openclaw.mjs'
$env:ASTERHAND_CHAT_EVIDENCE = '<output-dir>/workspace-script-validation'
go test -v ./internal/executor -run 'TestNativeWorkspace' -count=1
```

预期结果：
- `openclaw.json` 中工作区桥配置采用 `"${ASTERHAND_WORKSPACE_TOKEN}"` 而非明文密钥；Gateway 通过环境变量注入成功鉴权，且 CLI/Web 在脚本运行期仍可正常读取状态与执行 `turns.stop`。
- 11 个注册工具无碰撞，内置 `exec`/`process`/`code_execution` 保持拒绝，提示词规则与 `tool-display.mjs` 摘要规格一致。
- 本地 Gateway SSE 场景通过：`exec_command` 执行脚本读写工作区并可回读；执行长脚本期间调用 `Cancel` 杀灭整个进程树并保留旧文件，紧随其后的下一轮对话正常完成。

### 4. 全量静态守卫、自动化契约与链接检查

```powershell
go test -count=1 ./...
go vet ./...
$env:GOOS='linux'; $env:GOARCH='amd64'; go build ./internal/platform/textcodec ./internal/platform/sandbox ./internal/workspace
$env:GOOS='darwin'; $env:GOARCH='arm64'; go build ./internal/platform/textcodec ./internal/platform/sandbox ./internal/workspace
Remove-Item Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
go build -trimpath -o <output-dir>/automation-check/asterhand.exe ./cmd/asterhand
$env:ASTERHAND_AUTOMATION_BINARY = [IO.Path]::GetFullPath('<output-dir>/automation-check/asterhand.exe')
go test ./internal/app -run '^TestCLIAndRealMCPStdioSharePipeBusinessOperation$' -count=1
```

## 未覆盖与边界声明

- **Windows 零 UAC 读取边界**：非提权当前用户令牌保证 `%USERPROFILE%`/外部目录不被写入，但无法从 OS 内核阻止同用户恶意代码读取外部文件或解封 DPAPI；显式赋予 `Everyone:Modify` 的宿主机目录仍可被写。
- **未覆盖平台**：macOS（Seatbelt）与 Linux（`bwrap`）在第一阶段仅提供 `internal/platform/sandbox` 接口、设计映射与存根，`Supported()` 返回 `false`，未在对应操作系统上构建或验证（包含 Ubuntu 24.04+ AppArmor 限制与 macOS SIP 影响）。
- **Windows Schannel 限制**：在 `WRITE_RESTRICTED` 第一阶段内，部分依赖系统 WinHTTP/Schannel/SSPI 的工具（如原生 PowerShell `Invoke-WebRequest`、系统 `curl.exe`）访问 HTTPS 会失败并返回提示；Python 与 Node.js 的 HTTPS 请求应正常通过，默认环境变量 `GIT_SSL_BACKEND=openssl` 引导 Git 走 OpenSSL 路径。
- **真实模型与双机物理验收**：本机 Windows 节点的 OpenClaw + DeepSeek 真实在线多轮脚本执行与停止体验已完成人工验收；两台实体 Windows 节点的全新安装与跨节点协同验收按产品发布流程独立执行。
