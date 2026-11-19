# 受控提权脚本执行与极速免审验证方案（第二阶段）

本文说明 `workspace-script-escalation`（第二阶段 Tier 2 受控提权执行、内联确认交互、持久化信任与 `turbo` 极速免审模式）的可复现工程验证入口、前置条件、预期结果与平台/真实环境边界；设计细节见[功能计划](plan.md)与[ADR-AST-013](../../../assistant/adr/013-controlled-escalated-script-execution.md)。

## 前置条件

1. Go `1.27.3` 工具链与 Node.js `24.22.1`；普通（非 UAC 管理员）Windows 会话及本地 NTFS 文件系统。
2. 当运行环境启用 `CGO_ENABLED=0` 且无 C 编译器时，Go 测试以 `-count=1` 执行，`-race` 竞态检查另需具备 `CGO_ENABLED=1` 的环境单独补跑并标注边界。
3. 本机真实模型与安装/打包环境验收使用锁定的 OpenClaw `2026.10.7` 运行时并独立登记产品验收结果；工程自动化测试不依赖外部 DeepSeek 在线模型调用。

## 验证分组与复现命令

### 1. Windows Tier 2 执行后端与普通用户令牌隔离

覆盖 `OrdinaryToken` 中等完整性令牌（`S-1-16-8192`）、工作区外声明目录真实读写、具名 JobObject 进程树秒杀、节点私有 `<HomeRoot>` 与 `~/.ssh` 硬拦截、显式提权动词拦截（`-Verb RunAs`/`sudo`）、敏感环境剥离及原生 `TEMP`/Schannel 保留：

```powershell
go test -v -count=1 ./internal/platform/sandbox -run 'TestEscalated'
```

预期结果：
- Tier 2 子进程 `whoami /groups` 包含 `S-1-16-8192` 且可在声明的 `ExternalDirs` 创建外部文件；
- 访问 `<HomeRoot>`、`~/.ssh`、未列明外部 `workdir`、危险环境变量或 `Start-Process -Verb RunAs` / `sudo` 均在启动前返回结构化拒绝错误码；
- 子进程环境不含 `DEEPSEEK_*`、`OPENCLAW_*`、`ASTERHAND_*`，不重定向 `TEMP`/`TMP` 至 `<journal>/sandbox-temp`，亦不强制注入 `GIT_SSL_BACKEND=openssl`；
- 具名 `JobObject` 调用 `Terminate` 后子孙进程树归零退出。

### 2. 五层策略引擎、分片审批、写屏障、TOCTOU 校验与 Turbo 零污染

覆盖 `allow_once`、`allow_session`、`allow_always` 与 `deny` 全生命周期（含 `denied_by_user` 不触发 `s.blocked` 及 `ExecResult` 回传 `escalated`/`approval_mode`/`user_approved`/`approval_decision`/`approval_wait_ms`）、五层优先级（`Hard Deny > User Deny > User Ask > Allow > Standard/Turbo`）、裸通用解释器禁设 `始终允许`、不带外部目录的复合内置命令（`cmd /c ...`）归入 `workspace_escalated`、用户主目录下新建子目录精准建票、`MaxCalls = 500` 单轮调用上限、双目录完整 `ApprovalView.Targets` 展示与未声明外部 `workdir` 建票前拦截、同目录外同名程序按规范化绝对路径严格绑定、裸引号字符串/字面量表达式保持原生求值而不加 `&` 执行、`PATH` 含弯引号（`O’Neil`）时经 UTF-16LE Base64 数据传递正常启动、`.ps1` 本地化命名/开关/数值与引号字面量参数语义保留及表达式/非 ASCII 定界符退出直通防绕过、等待期 `approval_pending` 写屏障、大脚本（`> 4 MiB`）与外部 junction 改指向 `approval_tampered` 拦截、启动前 `FILE_SHARE_READ` 锁持与规则后置发布（含持久化失败保留执行结果与 `rule_persist_error` 告警）、区分纯数据大文件（`> 16 MiB` CSV/ZIP）与代码入口、并发 `WaitApproval` 单次原子启动、认领后与挂起启动恢复门禁（`AdmitResume`）处 `CancelCall` 串行阻断、`End` 票据回收、`sanitizeApprovalReason` 与 ~255 KiB 多行脚本预检性能回归、`turbo` 模式下直通 Tier 2 且零规则残留、切回 `standard` 立即重新询问，以及 Tier 1 外部写入受限（含 `Access to the path` 与外部 `Could not find file`）返回 `escalation_available: true`：

```powershell
go test -v -count=1 ./internal/workspace -run 'TestExecEscalation'
```

预期结果：
- `StartExec` 需要确认时返回 `approval_required` 与 `approval_id`，活动状态进入 `awaiting_approval`（多目录均完整列于 `Approval.Targets`，未在 `ExternalPaths` 声明的外部 `workdir` 建票前返回 `undeclared_external_workdir`）；
- 同名可执行文件仅严格匹配已批准的规范化真实路径，另一目录同名程序重新要求确认；
- 并发 `WaitApproval` 仅触发 1 次 `Backend.Start`；未进入等待直接 `End` 立即归零 `awaitingApproval` 且 `TurnSettled == true`；
- 并发 `write_file` 在票据等待期阻塞于 `waitReason = "approval_pending"`；若等待期间 `> 4 MiB` 工作区脚本遭修改或外部 junction 改指向，`WaitApproval` 在启动前返回 `approval_tampered` 且不写入任何会话或持久规则；
- `turbo` 模式下 `ApprovalMode` 记为 `turbo_auto`，`Status().AllowedItems` 保持为空，显式 `User Ask`/`User Deny` 与硬底线继续生效；切回 `standard` 后同目标立即重新弹出待确认卡片。

### 3. 对话运行时热更新、长轮次可见性、活动投影与 UI/i18n 回归

覆盖配置写事务并发保护（普通设置保存不覆盖撤权或 `standard` 回切）、`ConversationRuntime` 串行信任热同步、Gateway 零重启、长轮次（第 23 条活动）`reconcile` 保底返回待审批项与共享 `ProjectToolActivity`（`denied` / `approval_timeout` / `turbo_auto`）、`approve_tool` 闭合协议校验、插件 `exec.mjs` 分片轮询 `"exec/approval_wait"`、`endDetail` 透传与输入尺寸限制、快捷输入多目录胶囊与拒绝/超时文案渲染、审批卡按钮在后台轮询与瞬态/落盘消息切换下的 DOM 引用稳定（防止按压中途重建吞掉首次点击）、设置页逐条移除“始终允许”规则期间保存区不坍塌跳动以及全量 i18n/自动化契约守卫：

```powershell
go test -v -count=1 ./internal/config -run 'TestLocalTools|TestConcurrentTrust'
go test -v -count=1 ./internal/conversation -run 'TestActivitiesProjection|TestLongTurnPendingApproval'
go test -v -count=1 ./internal/executor -run 'TestTrustPolicy|TestConcurrentTrustPolicySync|TestWorkspacePluginSDKPreflight'
node ./scripts/sync-i18n.mjs --check
pwsh -NoProfile -File ./scripts/check-i18n.ps1 -Go go
node --test tests/workspace-plugin.test.mjs tests/chat-approval.test.mjs tests/workspace.test.mjs tests/automation-coverage.test.mjs
```

### 4. 全量仓库静态检查、后端测试与跨平台编译

```powershell
go test -count=1 ./...
go vet ./...
$env:GOOS='linux'; $env:GOARCH='amd64'; go build ./internal/platform/textcodec ./internal/platform/sandbox ./internal/workspace
$env:GOOS='darwin'; $env:GOARCH='arm64'; go build ./internal/platform/textcodec ./internal/platform/sandbox ./internal/workspace
Remove-Item Env:\GOOS, Env:\GOARCH
```

## 人工真实体验与验收边界

以下场景需在目标 Windows 桌面与真实模型配置下完成人工实测确认，不以单测替代：
1. 在“按需询问（推荐）”模式下引导助手读写工作区外普通目录（如测试数据文件夹）或运行依赖系统 HTTPS 的工具，观察快捷输入内联确认卡片在无技术术语下弹出、点击“本对话允许”后卡片即时折叠且后续同对话操作自动放行；
2. 切换“设置 → 助手”为“自动允许（极速）”，确认快捷输入标题旁显示轻量“极速免审”状态标识、外部整理脚本一步执行成功而无需逐次点击、核心私有目录与 UAC 提权仍被拒绝；切回“按需询问（推荐）”后无历史规则残留并立即恢复询问；
3. 对通过“始终允许”写入设置列表的条目在设置页点击“移除”，确认无需重启聊天服务且下一轮访问立即重新弹窗；
4. macOS 与 Linux Tier 2 跨平台边界仅通过源码契约与交叉编译验证构建，实机行为待多平台阶段独立实测。
