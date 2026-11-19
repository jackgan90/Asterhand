# 受控提权脚本与外部目录执行计划（第二阶段 / Tier 2）

## 基本信息

- **功能名称**：`workspace-script-escalation`（受控提权脚本与外部目录执行，Tier 2）
- **目标与用户价值**：在保留第一阶段（[工作区受限脚本执行](../workspace-script-execution/plan.md)）默认零审批沙箱底座的前提下，为需要读写指定外部文件夹（如帮用户批量处理 `D:\Data` 下的文档或报表）或执行依赖 Windows Schannel / SSPI 凭据链与原生安装组件的命令（如 `git clone/push`、`winget`、PowerShell 原生 HTTPS 请求）的场景提供受控提权通道。面向**非技术普通用户**同时提供两档运行模式——默认的**按需询问（Standard）**模式（零术语内联确认卡片与一键撤销管理）与可选的**自动允许（极速 / Turbo）**无审批模式（零弹窗直通普通用户态全能力执行且不污染长期/会话白名单），并全模式统一落实启动前防篡改（TOCTOU）校验、OS 非提权 UAC 隔离（`OrdinaryToken()`）以及启动前参数/环境变量/引用工作区脚本的节点与凭据路径硬拦截。
- **状态与阶段范围**：
  - **第二阶段**（Stage 2，独立计划）：已完成实现、工程验证、独立审计修复复审与本机真实模型用户验收；架构取舍记录于 [ADR-AST-013](../../../assistant/adr/013-controlled-escalated-script-execution.md)，可复现验证与平台边界见[验证方案](validation.md)。
- **关联 Rxx / Cxx / 领域需求 ID**：R03、R04、R23、R27；`AST-CHAT-04`、`AST-TOOLS-01`—`10`；`EXEC-TOOLS-01`—`08`、`EXEC-SCHED-01`—`12`；`AUTO-19`、`AUTO-20`（[自动化需求](../../../automation/requirements.md)）。
- **相关领域文档**：
  - [助手需求](../../../assistant/requirements.md)、[助手设计](../../../assistant/design.md)、[本机工具能力与运行时适配](../../../assistant/runtime-capabilities.md)、[持续会话设计](../../../assistant/conversations.md)、[ADR-AST-012](../../../assistant/adr/012-workspace-script-execution-boundary.md)、[ADR-AST-013](../../../assistant/adr/013-controlled-escalated-script-execution.md)
  - [执行器需求](../../../executors/requirements.md)、[执行器设计](../../../executors/design.md)、[细粒度并发设计](../workspace-tool-concurrency/design.md)
  - [日常操作覆盖清单](../../../automation/coverage.md)、[Web 交互设计](../../../web/design.md)
- **关联 ADR**：[ADR-AST-013](../../../assistant/adr/013-controlled-escalated-script-execution.md)（受控提权审批、`TOCTOU` 防篡改绑定、多层信任策略及无审批 `turbo` 模式边界）。

## 用户体验设计（面向非技术用户：零术语、少打扰、随时可撤回）

普通用户不关心进程令牌、完整性级别或哈希校验，只关心三件事：**助手想做什么、影响电脑哪里、点击后能否不反复被打断**。

### 1. 紧凑原位审批卡片（仅待确认时展开，用完即收起）

参考成熟 AI 助手（Claude Desktop / Cursor / Antigravity）的行内确认交互：当脚本需要越出助手专属文件夹或使用本机系统功能时，仅在**等待确认期间**于对应工具行下方原位展开紧凑确认卡片；**一旦点击决策或取消，卡片立即自动收起回单行工具活动记录**，绝不让冗余操作栏留在历史消息流中。界面严禁出现 `Tier 2`、`WRITE_RESTRICTED`、`Schannel`、`OrdinaryToken`、`SHA-256`、`TOCTOU`、`通配符` 等技术术语，仅呈现普通用户关心的三要素：

1. **申请标题与目标标签**：
   - 访问外部文件夹：`请求访问文件夹` + 完整路径胶囊（涉及多个外部目录时通过 `targets` 逐一并排展示所有目录，与实际写入的信任规则严格同源）；
   - 运行本机程序：`请求运行程序` + 可读程序名与实际规范化可执行文件身份（按子进程生效 `PATH`/`PATHEXT` 与工作目录解析，始终允许严格绑定至完整规范路径而不以短名串用）；
   - 其他系统能力：`请求使用系统功能`。
2. **自然语言用途、当前账户权限说明与可选命令查看**：
   - 仅展示 1 行助手提交并经服务端清洗限长（`<= 200` 字符）的日常用途说明（例如 `整理并汇总其中的 Excel 报表`），并附带大白话风险范围提示（明确“允许后将以当前 Windows 账户权限运行，脚本可能访问所列位置之外的普通文件”）；
   - 不主动摊开长命令行；右侧提供静默折叠按钮 `查看命令` / `收起命令`，仅用户主动点击时才展开已脱敏的单框命令预览。
3. **短促直白的决策按钮栏**：
   - 按钮文案绝不拼接长路径或解释文字，按使用频率排列：

### 2. 四项一键决策与智能防打扰作用域

| 按钮 | 文案定位 | 实际作用域与体验效果 |
|---|---|---|
| **主推荐按钮** | `本对话允许` | 点击一次后，**当前对话**后续同范围调试、多步骤脚本读写与重试均自动放行，不再重复打断；开启“新对话”后自动失效（当仅工作区内提权时显示 `允许`） |
| **次选按钮** | `仅本次` | 仅放行当前这一次执行；审批确定后、启动前由服务端自动核对程序路径与工作区脚本未被篡改 |
| **长期记住按钮** | `始终允许` | 记住该文件夹或具体程序，跨对话免询问生效；按钮悬停提示“后续可在「设置 → 助手」中随时移除”，不占用额外正文高度 |
| **温和拒绝按钮** | `不允许` | 不报错中断本轮对话；将拒绝事实直接回传助手，让助手改用助手专属文件夹内方案或给出手动步骤继续作答 |

#### 面向普通用户的“防误授”智能作用域收窄

1. **用工具处理外部文件夹时，按“文件夹”授权而非按“解释器”授权**：
   - 当命令实质上是借助 `python`、`node`、`powershell`、`pwsh`、`cmd` 等通用脚本运行环境批量处理用户指定目录（如 `D:\Data`）时，系统自动把 `本对话允许` 与 `始终允许` 的作用对象提炼为**文件夹 `D:\Data`**，而不是授权整个 `python *` / `powershell *`；
   - 这样用户在同一个对话里无论是换用 Python 统计、Node 转换还是 PowerShell 归档 `D:\Data`，点一次即可全程免打断，同时不会把免询问信任规则扩大到其他未获批目录（脚本运行期仍遵循当前 Windows 账户权限边界）。
2. **无具体外部文件夹的通用解释器提权，收敛到当前对话工作区**：
   - 当模型仅因系统环境/网络能力为工作区内 `python`/`powershell` 请求提权而未绑定具体外部目录或具名原生工具（如 `git`、`winget`、`uv`）时，`本对话允许` 仅覆盖**当前对话在助手专属文件夹内执行脚本**，且卡片自动**隐藏 `始终允许` 按钮**，避免普通用户误点一次就把 `powershell *` 设为永久全局放行。
3. **整盘与系统目录防误批**：
   - 禁止模型直接请求将整块磁盘根目录（如 `C:\`、`D:\`）、系统核心目录（`C:\Windows`、`Program Files`）或裸用户主目录根（`%USERPROFILE%`）作为目录授权目标，返回提示要求缩小到具体任务子文件夹（如 `D:\Data` 或桌面指定目录）。

### 3. 防漏看提醒与零重启即时生效

- **面板收起或失焦时的温和提醒**：当出现待确认请求而用户刚好收起了快捷输入面板或切换了其他窗口时，聊天状态栏同步显示 `等待你确认操作`，并复用桌面宿主既有温和任务栏提醒（不强抢当前前台输入焦点）；用户展开或回到快捷输入面板后自动定位到该待确认卡片。
- **零重启毫秒级生效**：无论是在卡片点击 `始终允许`，还是在 Web「设置 → 助手」移除规则，均立即同步至 Go 服务内存策略与持久化配置，无需重启 OpenClaw Gateway 或打断当前聊天会话。

### 4. Web「设置 → 助手」极简模式切换与权限清理

在现有 Web「设置 → 助手」“允许助手调用工具”开关开启后，仅暴露普通用户真正需要且直观的两部分（不设计繁琐的自定义正则/前缀规则配置表单，多层策略优先级引擎保留在后端配置与领域模型中）：

- **电脑访问确认（两档直观单选，即时生效）**：
  - **`按需询问（推荐）`**（`execution_mode = "standard"`）：访问助手专属文件夹以外的位置时，先在聊天中询问你。
  - **`自动允许（极速）`**（`execution_mode = "turbo"`）：不再弹出询问，以当前 Windows 账户权限直接放行普通脚本（可能访问助手文件夹之外的普通文件，管理员提权与显式敏感路径仍会拦截）；**自动放行不记入长期允许列表**，切回后立即恢复原状。
- **防遗忘轻提示**：
  - 快捷聊天面板顶部会话栏（`.chat-session-bar`）右侧在 `turbo` 开启期间常驻轻量状态徽标 **`极速免审`**（英文 `Turbo`，悬停提示当前处于自动允许模式并可在「设置 → 助手」切回），不打断输入、不占消息流空间。
- **已始终允许的项目（空时自动隐藏，零视觉噪音）**：
  - 仅当用户曾点击过 **`始终允许`** 留下长期允许规则时展示本区；每行只显示可读名称（例如 `文件夹 · D:\Data`、`程序 · git`）和右侧单个 **`移除`** 按钮，点击即刻撤销并同步清除对应会话内缓存。

## 范围与核心架构设计

### 1. 工具契约扩展与 Tier 1 → Tier 2 按需升级路径

- **扩展 `exec_command` 入参**（[exec.mjs](../../../../internal/executor/workspaceplugin/exec.mjs)）：
  - `escalated`（`boolean`，默认 `false`）：默认继续走第一阶段零审批沙箱，保障日常工作区任务零打扰；
  - `escalationReason`（兼容 `escalation_reason`，`string`）：当 `escalated: true` 时必填，服务端过滤控制字符并限制单行 `<= 200` 字符，作为卡片“用途说明”；
  - `externalPaths`（兼容 `external_paths`，`string[]`，上限 `8` 项）：声明需要读写的外部文件夹或文件路径，参与规则匹配、作用域提炼与规范化审计；
  - `workdir` 在 `escalated: true` 模式下仍默认使用受管工作区，允许指向真实存在且非 symlink 的外部目录；`standard` 模式下外部 `workdir` 必须在建票前校验其已覆盖在显式声明的 `externalPaths` 之内，否则拒绝建票（`undeclared_external_workdir`）。
- **Tier 1 受阻后的平滑升级信号与审批元数据反馈**：
  - 第一阶段沙箱继续优先拦截越界、Schannel 受限与外部路径拒绝；在其返回结构体中对适合受控提权的失败（如工作区外写入 `ACCESS_DENIED` / `.NET Could not find file` 指向外部路径、未列入核心凭据目录但属外部目标的 `outside_workspace`、或 `tls_hint: "windows_schannel_restricted"`）附带 `escalation_available: true` 引导标识；
  - 助手随后携带清晰的 `escalationReason` 与最小 `externalPaths` 重新调用 `exec_command({escalated: true, ...})`；若目标已被当前对话 `本对话允许` 或持久 `始终允许` 覆盖，则零弹窗直接启动，实现“一次授权、后续自动升档”；
  - `ExecResult`（含同步结束、后台转入、轮询、`process_control` 摘要以及用户拒绝/超时返回）携带只读 `escalated`、`approval_mode`（`once`/`session_allow`/`rule_allow`/`turbo_auto`/`denied`/`timeout`）、`user_approved`、`approval_decision` 与 `approval_wait_ms` 字段，明确区分“人工等待点击”与“命中既有规则或极速免审”，避免模型把子进程运行时长 `duration_ms` 误判为未弹卡。

### 2. Windows Tier 2 执行后端（`OrdinaryToken` + 命名 JobObject）

- **进程令牌与进程树控制**：
  - 复用 [token_windows.go](../../../../internal/platform/userprocess/token_windows.go) 已通过验证的 `userprocess.OrdinaryToken()` 构造普通用户非提权令牌：保持中等完整性 `S-1-16-8192`、开启 `DISABLE_MAX_PRIVILEGE | LUA_TOKEN` 剥离管理员组与高危特权（仅保留 `SeChangeNotifyPrivilege`）、零 Windows UAC 弹窗；
  - 复用 [job_windows.go](../../../../internal/platform/userprocess/job_windows.go) 创建具名 Kill-on-close JobObject（`Local\Asterhand.Exec.<sessionID>`，配合 `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` 与 `PROC_THREAD_ATTRIBUTE_HANDLE_LIST` 最小句柄继承列表），确保与第一阶段共用完全一致的“停止回复”和节点退出秒杀收尾能力。
- **环境隔离与普通用户态全能力释放**：
  - 环境构造延续 [env.go](../../../../internal/platform/sandbox/env.go) 的显式白名单与秘密剥离规则（继续严密阻断 `DEEPSEEK_*`、`OPENCLAW_*`、`ASTERHAND_*`、含 `user:pass@` 代理 URL、未列入的宿主机服务/模型密钥以及 `PATH`/`PYTHONSTARTUP`/`NODE_OPTIONS`/`GIT_SSH_COMMAND` 等危险覆盖参数）；
  - 与第一阶段的区别在于，Tier 2 模式下**保留用户正常环境中的 `TEMP`/`TMP` 与原生包管理器缓存目录**（不强行重定向至受 `256 MiB` 约束的 `<journal>/sandbox-temp`，以承载外部目录批处理、安装包下载及构建缓存），且**不再强注 `GIT_SSL_BACKEND=openssl`**，完整释放系统 WinHTTP/Schannel HTTPS（PowerShell `Invoke-WebRequest`/`Invoke-RestMethod`、`winget`）及系统 Git 凭据管理器 / OpenSSH 能力。
- **配额与文件版本联动边界**：
  - Tier 2 会话仍受单轮最长执行 `600s`、每流输出缓冲 `256 KiB`、单轮最多 `8` 个并发脚本会话与 `500` 次单轮总工具预算约束；
  - 外部目录改动由操作系统普通用户 ACL 管辖、不纳入节点 `256 MiB` 工作区配额或版本前像库；但会话退出后 Go 服务端**仍执行工作区 `reconcileAfterExec`**，保证如果提权脚本同时改写了助手工作区内已有文件，文件修订版本号正常递增并触发 `revision_conflict` 保护，防止后续工具或恢复误覆盖。

### 3. 多层信任规则引擎与严格优先级（`Hard Deny` → `User Deny` → `Ask` → `Allow`）

评估逻辑全部由 Go 服务端独立执行，模型及运行中脚本无权读取或修改策略存储（[config.go](../../../../internal/config/config.go)）：

1. **第一层：`System Hard Deny`（系统硬底线，启动前强制拦截，任何用户确认或持久规则均不可覆写）**：
   - Asterhand 节点私有目录 `<HomeRoot>`（`workspace` 与 `sandbox-temp` 子树除外）：保护 `config.json`、`*.secret`、`asterhand.db`、`node.key`、`web-login.token` 及 `<runtime>/openclaw.json` 等核心路径不在命令参数、`workdir`、`env`、`externalPaths` 或所引用的工作区脚本文件中出现（启动前静态扫描引用脚本）；运行期外部普通目录与进程访问依据 [ADR-AST-012](../../../assistant/adr/012-workspace-script-execution-boundary.md) 继续遵循当前 Windows 普通用户 OS ACL 事实；
   - [pathguard.go](../../../../internal/platform/sandbox/pathguard.go) 定义的高敏感用户凭据目录与文件（`~/.ssh`、`~/.aws`、`~/.gnupg`、`~/.kube`、`~/.docker`、`~/.npmrc`、`~/.pypirc`、`~/.netrc`、`~/.git-credentials`、`~/.vault-token`、`~/.config/gh`、`~/.config/gcloud`、Keychains / keyrings 等）；
   - 过宽目录范围（`broad_scope_denied`）：整块磁盘根（如 `C:\`、`D:\`）、系统关键目录（`C:\Windows`、`Program Files`、`ProgramData`）以及裸 `%USERPROFILE%`/`%APPDATA%`/`%LOCALAPPDATA%` 顶层目录直接拒绝作为目录规则目标；同时区分由 `PATH`/系统关键目录解析出的主程序（如不带外部路径的 `cmd /c "..."` 复合命令归为 `workspace_escalated` 而非误判 `C:\Windows\System32` 宽目录）以及用户主目录下尚不存在的显式命名子目录（允许建票询问精确子目录而非向上坍缩成裸 `%USERPROFILE%`，仅在用户主目录根直放单文件时继续拒为宽范围）。
2. **第二层：`User Deny`（用户禁止规则）**：
   - 检查规则中全部 `action == "deny"` 条目（含路径规范化子树匹配 `evalRealPath` 与命令匹配）；任一项命中即直接以 `{status: "policy_denied", escalation_denied: true}` 拒绝，不展示确认卡片。
3. **第三层：`User Ask`（用户强制每次询问规则）**：
   - 任一目标命中显式 `action == "ask"` 条目时，屏蔽所有会话级与持久 `allow` 自动放行，强制进入待确认卡片等待用户明确点击。
4. **第四层：`Session Allow` 与 `Persistent Allow`（本对话允许 / 持久信任）**：
   - 仅当本次请求的**全部**涉及目标（所有声明与解析出的外部目录均被已授权目录规则覆盖；具名外部二进制需被精确规范路径 `CanonicalExistingPath` 命中，绝不以裸短名称跨目录放行；通用解释器无外部目录时仅覆盖会话内工作区提权）全部满足允许条件时，方可在无需人工点击的情况下直接启动 Tier 2。
   - **保守单条直接调用解析与启动身份绑定**：按照实际 PowerShell 单条直接调用语法严格检查 `command`（仅允许处于命令调用位置的首部目标——未引号命令名或单前导 `&` 调用算符后的可执行项；无 `&` 的裸引号字符串 `'...'`/`"..."` 与数字/一元前缀/`$true`/`$false` 等表达式首部、换行 `\r`/`\n`/`\v`/`\f`、非 ASCII PowerShell 空白/控制/智能引号/Unicode 破折号、未引号包裹的 `;`/`|`/`&`/反引号/`>`/`<`/`(`/`)`/`{`/`}`/`[`/`]`/`@`/`#`/`,`、`--%` 停止解析符、非 `-参数:$true`/`$false` 布尔字面量的 `$` 变量、引号内闭合紧邻续接及双引号内 `$`/反引号/`""` 转义均判为复合表达式，且按 PowerShell 字面反斜杠规则处理 `"..."`），并依据子进程生效环境（`EffectiveSearchPath` 含前置节点目录与 `PATHEXT` + `.ps1`）解析主程序（屏蔽 PowerShell 内置别名误绑定）；直通/已批准的单个非宿主程序或 `.ps1` 脚本在启动阶段通过 UTF-16LE Base64 数据解码表达式传递已核验 `DirectExe`（避免规范路径中的弯引号等 Unicode 引号被 PowerShell 误切分），配合原样校验后的 `DirectRawArgs` 参数尾部完整保留本地化命名参数、数值/布尔实参与引号字面量类型语义，复合或未通过校验语法则回退原生命令解析并退出程序自动放行。
5. **第五层：兜底策略（随运行模式切换）**：
   - **`standard` 模式（默认按需询问）**：未命中以上规则的新提权请求，进入人工待确认卡片流程（`Default Ask`）；
   - **`turbo` 模式（极速无审批）**：未命中前三层（`System Hard Deny` / `User Deny` / 显式 `User Ask`）的脚本或外部目录请求直接自动放行至 Tier 2（`Auto-Approve Ephemeral`），仅在只读活动审计中记为 `approval_mode = "turbo_auto"`，**绝不写入 `Session Allow` 或持久 `TrustRules`**。

#### 零重启规则与模式存储设计

- 在 `config.LocalToolPolicy` 中扩展 `ExecutionMode string`（`"standard"` 默认 | `"turbo"`）、`TrustRules []TrustRule` 与独立的 `TrustRevision string`（采用受保护 `<Home>/config.json` `0600` 原子事务写入），与控制 OpenClaw Gateway 工具启用开关的 `LocalToolPolicy.Revision` 分离：所有配置写操作统一经 `Manager.writeMu` 事务提交（普通设置与模型诊断更新显式保留最新 `LocalTools`），多目录信任规则单次批量落盘并由 `ConversationRuntime` 串行同步至 `workspace.Service`，切换 `ExecutionMode` 或增删信任规则仅推进 `TrustRevision` 并在 Go 服务端毫秒级热生效，不触发耗时的 Gateway 重启。
- 在用户于 Web 设置中**移除规则**、将某目标改为 **`deny` / `ask`** 或**从 `turbo` 切回 `standard`** 时，服务端立即停止自动放行并同步清除所有活跃对话内存中覆盖相关目标的 `本对话允许` 缓存条目，保证收紧立刻生效。

### 4. 分片审批等待协议、超时停止、待审批可见性与 TOCTOU 防篡改绑定

1. **分片异步等待、单次原子认领与长轮次可见性**：
   - [transport.go](../../../../internal/workspace/transport.go) 的本地 `http.Server` 存在 `45s` `WriteTimeout` 约束；而人类阅读并点击聊天确认卡片通常耗时数十秒至数分钟。
   - 因此需要人工确认时，`"exec/start"` 创建内存票据 `ap_<rand>` 并置对应调用操作为 `PhaseAwaitingApproval`，立即返回 `{status: "approval_required", approval_id}`；插件 `exec.mjs` 循环调用 `"exec/approval_wait"`（走 `waitSlots`，每次切片 `<= 2500ms` 并响应 `AbortSignal`）。
   - 同一票据通过 `WaitApproval` 单次原子认领（`claimed` + `execDoneCh`），并发重复等待共享同一执行结果而不重复启动进程；若调用未继续等待即进入 `Service.End`，服务端自动回收未结清票据并释放 `awaitingApproval` 写屏障。
   - 会话活动聚合（`Activities` 先按会话/轮次过滤再排序 + 独立有界 `PendingApprovals` 保底合并 + `reconcile` 增量游标）确保即使单轮次活动超过 20 条，待确认卡片仍始终立即可见并支持点击即刻收起。
2. **票据超时、温和拒绝与活动状态一致投影**：
   - 单张待确认票据设 **`5 分钟`最长等待上限**；超时返回 `{status: "approval_timeout", escalation_denied: true}`，聊天活动投影为 `failed`（`detail: "approval_timeout"`，界面显示“确认超时 / Timed out”）。
   - 用户点击 **`不允许`** 或命中 **`policy_denied`**：返回非阻塞 `denied_by_user` / `policy_denied` 让模型继续以沙箱方案作答，插件向 `End` 透传原因细节，前后端共享 `ProjectToolActivity` 统一映射为活动状态 `"denied"`（界面显示“未允许 / Not allowed”）并携带只读 `approval_mode`（如 `turbo_auto`）。
   - 用户点击聊天 **`停止回复`**（`CancelTurn` / `CancelCall`）或应用退出（`RuntimeStopped`）：立即广播唤醒并撤销对应挂起票据（`< 50ms`），已批准进入运行态的 Tier 2 进程同样由四级停止框架并行强杀命名 JobObject 并排空对账。
3. **审批时刻快照与启动前统一 TOCTOU 防篡改验证**：
   - **创建票据时刻（`t_request`）固定不可变指纹**：
     1. 完整参数指纹 `(Command, NormalizedWorkdir, SortedEnv, CanonicalExternalPaths)` 及未创建代码路径集合 `absentPaths`；
     2. 解析后主可执行文件的规范真实路径（`CanonicalExistingPath`）及其基础文件身份（大小、修改时间纳秒与工作区/限额 `<= 64 MiB` 内 SHA-256 摘要）；
     3. 精确区分脚本/可执行目标与纯数据文件参数：对工作区及外部目录中被引用的脚本文件与被解释器/命令执行的入口文件（最高 `16 MiB`）及非系统可执行目标（最高 `64 MiB`）校验非符号链接并计算精确 **SHA-256 哈希**（拒绝无法核验的超限脚本或符号链接，且静态扫描引用脚本内容中的显式提权动词与受保护路径；仅作数据参数的大 CSV/归档等普通数据文件免于脚本大小与哈希拦截）。
   - **等待期工作区防竞态写屏障**：当轮内任一调用处于 `PhaseAwaitingApproval` 等待确认时，调度器 `canGrantLocked` 延迟同轮其他修改工具（`write_file`/`edit_file`/`apply_file_patch`）执行（复用 `waitReason` 等待协调机制），避免同批并发修改误改即将执行的脚本。
   - **启动前锁定核验、规则后置发布与串行取消门禁（`t_spawn`）**：`ApproveToolCall` 仅原子记录决策并唤醒等待者；`spawnExecSession` 在调用 `Backend.Start` 前预登记可取消会话并结合 `SpawnAttachedJob` 的 `AdmitResume` 门禁，在 `CREATE_SUSPENDED` 后、恢复主线程（`ResumeThread`）前与调用级/轮次级取消状态串行判定（已取消则直接终止挂起 JobObject 而不恢复执行）；所有启动路径先经 `verifyAndLockPreSpawn` 以 `FILE_SHARE_READ` 打开并锁定被执行脚本及主程序（阻断启动窗口内篡改并保持至会话进程完全退出）、完成完整 TOCTOU 复核后启动后端；仅在终检与启动均未取消且成功后发布 `Session Allow` 或批量持久化 `TrustRules`，且当进程已启动但规则持久化失败时，保留完整脚本执行结果并在 `ExecResult.rule_persist_error` / `warning` 单独上报，避免调用方误判未执行而重试产生重复副作用。

### 5. 无审批极速模式（Turbo）架构、成熟方案对齐与 UAC 边界

- **与成熟 Harness 的共性对齐**：
  - 业内主流框架均将无审批模式定位为**取消交互式弹窗**，而非彻底废弃安全底线：例如 Claude Code（`bypassPermissions` 仍强制执行 `deny` 规则与禁 `root`/自配置保护）、Antigravity（`Turbo` 仍严格遵守 `Deny > Ask > Allow` 优先级）、Codex CLI（`approval_policy = never` 与 OS 沙箱正交解耦）及 OpenClaw（`security = full, ask = off` 仍保留高危内联求值门控与策略合并）。
  - 本计划的 `turbo` 模式吸收该共性，在 Asterhand 中落实为：**自动单次授权 + 零白名单污染 + 直通 Tier 2 普通用户态 + 硬底线/用户 Deny 与 OS UAC 隔离不撤防**。
- **“一步到位”直通 Tier 2 与零白名单污染**：
  - 在 `turbo` 模式下，只要请求未命中 `System Hard Deny`、`User Deny` 或用户显式配置的 `User Ask`，**无论模型是否显式传递 `escalated: true`**，Go 服务均直接以 Tier 2 `OrdinaryToken()` 启动执行——省去先撞第一阶段写沙箱 `ACCESS_DENIED` 或 Schannel 限制报错后再多花一轮对话重新调用 `exec_command` 的延迟与 Token 浪费；若命令同时改动助手工作区内文件，退出后仍执行 `reconcileAfterExec` 维持文件版本保护；
  - 每次自动放行均为**单次临时授权（Ephemeral）**：不写入任何对话内存 `Session Allow`，也不写入 `<Home>/config.json` 持久 `TrustRules`。此外，`turbo` 模式下仅针对**会造成永久宽泛信任的授权入库场景**设置的普通目录层级限制（如在桌面或下载目录临时批量处理）直接放行，用户随时切回 `standard` 后即刻恢复“按需询问”，零历史残留权限。
- **Turbo 模式下依然生效的三道硬边界（含 Windows UAC 机制说明）**：
  1. **OS 普通用户态与 Windows UAC 隔离（绝不获得管理员身份）**：
     - 进程始终经 `userprocess.OrdinaryToken()` 启动（强制中等完整性 `S-1-16-8192`、`LUA_TOKEN` 禁用管理员组、特权仅留 `SeChangeNotifyPrivilege`）。**即使宿主 Asterhand 意外运行在管理员上下文，子进程也会被主动降回非提权普通用户态**，避免同类产品因宿主提权而导致模型命令静默穿透 UAC 修改全系统的隐患；
     - 脚本直接尝试修改受保护系统目录（`C:\Windows`、`Program Files`）、写 `HKLM` 注册表、安装系统服务或通过 `CreateProcess` 启动带 `requireAdministrator` 清单的程序时，Windows 内核直接返回 `ERROR_ACCESS_DENIED (5)` 或 `ERROR_ELEVATION_REQUIRED (740)` 硬拒绝，不会弹窗也不能越权；
     - **显式提权动词拦截（防无人值守挂起与脱离进程树控制）**：若脚本显式调用 `Start-Process -Verb RunAs`、`runas.exe`、Windows `sudo` 等走 `ShellExecuteEx("runas")` 路径的提权动词，系统 `Appinfo` 服务会在受 UIPI/安全桌面隔离的桌面弹出 UAC 对话框——AI 既无法自点击确认（会导致命令行长期挂起直至超时），又会在真人误点击“是”时使高权限子进程脱离当前非提权 `KillOnCloseJob` 而失控；因此启动前守卫在包括 `turbo` 在内的**全部模式**下直接拒绝显式提权动词，返回结构化 `elevation_verb_denied`。
  2. **`System Hard Deny` 节点自保与核心凭据防线**：
     - `turbo` 放行的是工作区外普通文件操作与系统网络工具链，而非放行模型读取/改写节点自身。Asterhand `<HomeRoot>`（工作区除外，含 `config.json`、`*.secret`、`asterhand.db`、`web-login.token`、`<runtime>/openclaw.json`）、环境变量密钥剥离、危险 `env` 禁覆写以及 `~/.ssh`/`~/.aws`/`~/.gnupg`/`~/.kube` 等核心凭据路径直指拦截继续 100% 生效。
  3. **用户显式 `User Deny` 与 `User Ask` 规则**：
     - 配置层通过 `TrustRules` 显式保存的 `deny`（禁止访问）与 `ask`（每次询问）规则优先级高于 `turbo` 模式兜底，命中即按对应策略拦截或确认（Web 设置页仅向非技术用户提供极简的确认方式双选与“已始终允许的项目”一键移除，不暴露规则编辑器）。

### 6. 明确非目标（安全与范围边界）

- 不接入 Windows UAC 管理员提权对话框，不创建高权限账户或以 `SYSTEM` 运行助手脚本；
- 不向远端配对节点（Peer）开放跨节点自动提权执行或免密远端审批；
- 本阶段完整实现并验证 Windows `OrdinaryToken` + 命名 JobObject 提权路径，macOS（Seatbelt 非沙箱放行范围）与 Linux（`bwrap`/主机执行隔离范围）仅保持上层契约、策略模型与 `SandboxBackend` 扩展接口一致，实机后端留待多平台阶段验证交付。

## 实施步骤、覆盖清单与模块规模评估

### 1. 分层实施步骤与模块划分（保持单文件 `< 1000` 行、单函数 `<= 100` 行）

遵循代码风格与拆分原则，新增职责独立拆分至新文件，不堆砌进已接近上限的既有文件（`exec_service.go` 当前 `945` 行、`service.go` 当前 `764` 行）：

1. **沙箱后端提权能力**（`internal/platform/sandbox`）：
   - 扩展 `sandbox.go` / `SpawnSpec` 增加 `Escalated`、`ExternalDirs`、`JobName`、`DirectExe`、`DirectArgs` 及 `OpenSharedReadLocked`（`FILE_SHARE_READ` 只读共享防写锁）；
   - 新增 `internal/platform/sandbox/escalated_windows.go` 与 `resolve_exec.go`：实现 Tier 2 外部目录与 `workdir` 规范路径核验、子进程一致 `EffectiveSearchPath` 命令解析（屏蔽 PowerShell 内置别名）、Tier 2 白名单环境构建（保留原生 `TEMP`/`TMP` 与包缓存、保留系统 TLS/Schannel）以及 `OrdinaryToken()` + 具名 JobObject 启动，并在启动前校验中拦截显式提权动词（`RunAs`、`runas.exe`、Windows `sudo` 等）。
2. **策略引擎、防篡改终检与审批票据模块**（`internal/workspace`）：
   - 新增 `internal/workspace/exec_policy.go`：封装 `ExecutionMode`（`standard` | `turbo`）、`TrustRule` 结构与规范化、`System Hard Deny -> User Deny -> User Ask -> Session/Persistent Allow -> Default Ask / Turbo Auto-Approve` 评估引擎、外部 `workdir` 建票前校验、精确可执行路径绑定、单次线性扫描用途裁剪及面向非技术用户的“按文件夹/具名程序自动收窄”逻辑；
   - 新增 `internal/workspace/exec_verify.go`：承担可验证代码目标识别（区分被执行/脚本目标与纯数据文件参数）、脚本静态安全预检（O(n) `pathguard` 扫描 `elevation_verb_denied` / `sensitive_path_denied`）、最高 `16 MiB` 脚本与 `64 MiB` 可执行文件 SHA-256 快照采集，以及统一 `verifyAndLockPreSpawn`（`FILE_SHARE_READ` 持有锁定直至子进程退出 + 完整 TOCTOU 复核）；
   - 新增 `internal/workspace/exec_approval.go`：管理 `approvalTicket` 状态机、`StartApproval`、单次原子认领 `WaitApproval`（`<= 2500ms` 切片轮询、`5 分钟`超时）、`ApproveToolCall`（仅原子提交决策并在 TOCTOU 校验通过后批量写入持久/会话规则）、`PendingApprovals` 有界查询及 `End` 票据回收；
   - 最小限度接入 `exec_service.go`、`exec_manager.go`、`journal.go`（先过滤后排序）、`scheduler.go`、`call_io.go`、`transport.go`（新增 `"exec/approval_wait"` 路由及 `waitSlots` 配额）与 `types.go` / `service.go`。
3. **配置、会话闭合协议与 OpenClaw 插件集成**：
   - 更新 `internal/config/config.go`、`internal/config/selection.go` 与新增 `internal/config/local_tools.go` 持久化 `LocalToolPolicy.ExecutionMode`、`TrustRules` 与独立 `TrustRevision`（零 Gateway 重启热刷新）；
   - 更新 `internal/conversation/wire/protocol.go`、`activities.go`、`service.go`、`workspace.go` 及 `internal/executor/conversation_history.go`：新增 `wire` 闭合动作 `"approve_tool"`、`ToolActivity` 脱敏 `Approval` 字段（仅含可读类型/目标/大白话用途/命令预览/按钮适用性与状态）、活动状态 `"awaiting_approval"`/`"denied"` 及响应中的当前 `execution_mode`；
   - 更新 `internal/executor/workspaceplugin/exec.mjs` 与 `internal/prompt/default-chat.txt`：接入 `exec_command` 提权参数、`"exec/approval_wait"` 分片等待与取消联动、以及“用大白话填写提权原因”的助手行为引导；保持 [tool-display.mjs](../../../../internal/executor/chatbridge/tool-display.mjs) 脱敏规范一致。
4. **面向非技术用户的聊天卡片、Turbo 状态提示与设置页 UI + i18n 守卫**：
   - 新增快捷聊天模块 `internal/quickinput/assets/chat-approval.mjs`（联动 `chat-activities.mjs`、`chat-renderer.mjs`、`chat-model.mjs`、`chat-ui.mjs`、`index.html`、`chat.css`），仅在待确认期间渲染紧凑大白话审批卡片与 `查看命令` 折叠区、操作按钮（`本对话允许` / `仅本次` / `始终允许` / `不允许`），决策落定后自动收起为单行工具行，并在 `.chat-session-bar` 展示 `极速免审`（`Turbo`）轻量防遗忘徽标及收起态温和提醒；通过 `tool.id` 活动复用、按决策键缓存 `<button>` 引用及胶囊变化对比，使按钮 DOM 引用在 500ms 轮询、`relocalize()` 及瞬态气泡向落盘消息交接期间保持不变，防止按压中途重建丢失点击；
   - 扩展 `web/tool-settings.mjs`、`web/index.html`、`web/style.css` 与 `internal/app/workspace_api.go`（复用人工例外路由 `"/api/workspace/permission"`）：提供“按需询问 / 自动允许（极速）”两档直观切换，并在“已始终允许的项目”非空时展示可读列表与一键 `移除` 按钮（空时隐藏，零配置表单负担）；将 `toggling` 状态与信任写请求 `saving` 隔离并按规则 ID 原位复用 `<li>` 行，使变更模式或逐条移除规则在途期间 `#assistant-tools-trust` 保持展开不坍塌跳动；
   - 同步更新 `internal/i18n/locales/{zh-CN,en-US}.json` 并运行 `sync-i18n.mjs` 与 `check-i18n.ps1`，确保 `G1`–`G4` 静态守卫全部通过。

### 2. 日常操作与自动化覆盖一致性（`AUTO-19` / `AUTO-20`）

- **人工例外（严禁外部 CLI/MCP 旁路批准或开启极速无审批）**：
  - 会话闭合动作 `"approve_tool"`（在聊天内点击 `仅本次` / `本对话允许` / `始终允许` / `不允许`）与 Web 权限开关及模式/规则管理接口 `"/api/workspace/permission"`（切换 `execution_mode` 或移除长期允许规则）直接扩展或放宽助手访问宿主计算机的边界，依据 `AUTO-19`、`AUTO-20` 与[日常操作覆盖清单](../../../automation/coverage.md)强制归类为 **`"human"` 人工例外**，同步登记至 [automation-coverage.json](../../../../contracts/automation-coverage.json) 的 `native_actions` 与 `web_endpoints`，禁止模型或外部自动化脚本代点确认、开启 `turbo` 模式或写入 `allow` 规则。
- **等价只读观察与停止（UI / CLI / MCP 一致）**：
  - `turn_activities.list/get`、`workspace_operations.get` 与 `messages.list` 对外统一暴露 `awaiting_approval` 状态、脱敏后的可读请求范围/用途摘要、只读模式标记（如 `turbo_auto`）与最终决策结果；
  - `workspaces.get` 与 `conversations.capabilities` 对外只读暴露 Tier 2 提权能力状态（`scripts.escalate`）、当前 `execution_mode`（`standard` | `turbo`）、`trust_revision` 与已配置信任规则数量（不泄露无关宿主秘密）；
  - `turns.stop` 与现有 `CancelTurn` 一致，可随时安全终止处于等待确认中或已提权运行的会话。

### 3. 必需同步更新的领域文档

- [助手需求](../../../assistant/requirements.md)、[助手设计](../../../assistant/design.md)、[本机工具能力与运行时适配](../../../assistant/runtime-capabilities.md)、[持续会话设计](../../../assistant/conversations.md)、[ADR 索引](../../../assistant/adr/README.md)（新增 `ADR-AST-013`）
- [执行器需求](../../../executors/requirements.md)、[执行器设计](../../../executors/design.md)
- [日常操作覆盖清单](../../../automation/coverage.md)、[覆盖契约](../../../../contracts/automation-coverage.json)、[Web 交互设计](../../../web/design.md)、[开发路线图](../../roadmap.md)、[文档导航](../../../README.md)、[测试入口](../../../../tests/README.md)

### 4. 模块规模与软阈值评估

依据[代码风格与规模约束](../../code-style.md)，检查本特性新增与修改文件的物理行数及函数规模：

- **手写产品与测试文件均低于 1000 行**：核心新增及改动源码控制在 `exec_service.go`（`945` 行）、`exec_policy.go`（`937` 行）、`exec_verify.go`（`925` 行）、`exec_escalation_reaudit_windows_test.go`（`901` 行）、`pathguard.go`（`840` 行）、`exec_manager.go`（`810` 行）与 `web/style.css`（`810` 行）以内，均未触发千行拆分门槛。
- **生成代码与扁平词表例外**：
  - `web/i18n-catalog.mjs`（`1783` 行）与 `internal/quickinput/assets/i18n-catalog.mjs`（`1413` 行）是由 `scripts/sync-i18n.mjs` 确定性生成的只读模块，属生成代码分类排除项；
  - `internal/i18n/locales/zh-CN.json` 与 `internal/i18n/locales/en-US.json`（各 `1019` 行）为[国际化开发指南](../../i18n-guide.md)规定的单一权威扁平键值词表，受 `manifest.json -> <id>.json`、`//go:embed` 单文件加载及 `G1` 键对称守卫约束，拆分单个语种 JSON 会增加加载映射复杂度而无职责收益，暂保留单文件并与团队既有国际化规划一并维护。
- **闭包组件与既有处理器函数说明**：`internal/quickinput/assets/chat-approval.mjs` 已将无状态纯辅助函数外提使 `approvalCard` 收敛至 `<= 100` 行；`chatPanel`（`chat-ui.mjs`）、`nodeSettings`（`node-settings.mjs`）、`toolSettings`（`tool-settings.mjs`）与 `App.settings`（`internal/app/api.go`）延续既有闭包工厂/单一接口分发结构，各自内部状态守卫与事件闭包保持紧耦合（单段子回调均 `<= 35` 行），拆成跨文件状态对象反而增加生命周期协调成本，本次按现状保留并记录理由。

## 验收用例与验证计划

### 核心用户验收场景

1. **外部文件夹批处理（单次授权、整对话免反复打断）**：让助手整理工作区外某个指定测试文件夹（如 `D:\Data`）中的文件并输出汇总；首次申请展开紧凑大白话卡片（展示 `D:\Data` 与用途说明，零技术术语），点击**`本对话允许`**后卡片立即收起，助手同对话内连续多次运行 Python / PowerShell 读写该文件夹均直接完成、不再次弹窗；新建对话后再访问相同目录会重新询问。
2. **系统 HTTPS / 原生工具命令提权执行**：在聊天中请求执行依赖 Windows 系统凭据或 HTTPS（如 PowerShell `Invoke-RestMethod` 或 `git` 远端操作）的命令，按需升级并允许后可在 `OrdinaryToken` 下正常执行并返回输出，且进程仍不带 `DEEPSEEK_*`/`OPENCLAW_*`/`ASTERHAND_*` 环境变量。
3. **`仅本次` 与 TOCTOU 防篡改拦截**：挂起审批后，在点击`仅本次`前通过外部编辑改动工作区内待执行脚本文件内容或替换可执行文件/外目录 junction 指向，点击批准后服务端在启动前立即拦截并报 `approval_tampered`，绝不执行被篡改版本。
4. **`不允许` 温和退回不中断对话**：面对外部访问请求点击`不允许`后，卡片收起、本轮回复不报错崩溃，助手收到拒绝状态并继续通过沙箱内方式或人工步骤建议正常回复用户。
5. **`始终允许` 跨对话生效与设置页一键撤回 + `Deny` 绝对优先**：点击`始终允许`后无需重启网关，新对话直接免审访问；在 Web「设置 → 助手」“已始终允许的项目”一键`移除`后立即恢复询问（并清除活跃对话会话允许缓存），列表为空时自动隐藏该区块；配置层设置针对子目录或程序的`deny`规则时，即便外层父目录为`allow`也优先拦截。
6. **硬底线防护与聊天“停止”**：即使携带 `escalated: true`，尝试指向 Asterhand 节点数据目录（`config.json` / `*.secret` / `asterhand.db`）、`~/.ssh` 私钥路径或整块磁盘根 `C:\` 均被硬性拒绝；等待审批期间或 Tier 2 脚本运行期间点击“停止回复”，挂起票据与子进程树在 `< 2s` 内干净终止。
7. **极速无审批（Turbo）模式零打扰与回切零残留**：在 Web「设置 → 助手」切换为`自动允许（极速）`后无需重启网关，快捷聊天顶部出现`极速免审`（`Turbo`）轻提示徽标；跨外部文件夹读写与系统 HTTPS 操作免确认卡片直通执行，但对 Asterhand `<Home>`、`~/.ssh`、已设 `deny` 规则的目标及 `RunAs`/`sudo` 显式提权或系统目录写入仍被拦截；切回`按需询问（推荐）`后查看“已始终允许的项目”，列表保持干净无自动新增白名单，新操作恢复询问。

### 工程自动化测试矩阵

- **Go 单元与并发安全测试**：
  - `internal/platform/sandbox`：`OrdinaryToken()`（含管理员宿主降权验证）提权进程环境秘密隔离、原生 `TEMP` 保留、显式提权动词 `RunAs`/`sudo` 拦截、系统目录与凭据硬底线拒绝、外部目录与可执行文件 `evalRealPath` 解析；
  - `internal/workspace`：多层规则优先级矩阵（`System Hard Deny` > `User Deny` > `User Ask` > `Session/Persistent Allow` > `Default Ask` / `Turbo Auto-Approve`）、`turbo` 模式下直通 Tier 2 且零污染 `SessionAllow` / `TrustRules`、复杂命令行注入符号拒绝走前缀放行、`仅本次` SHA-256 + `evalRealPath` TOCTOU 篡改检测、`approval_wait` 切片轮询与 5 分钟超时、`CancelTurn`/`ReleaseTurn` 无死锁退场及 `reconcileAfterExec` 版本冲突保护。
- **契约与静态守卫测试**：
  - `TestAutomationCoverage`（Go）与 `tests/automation-coverage.test.mjs`（Node）核实 `"approve_tool"` 与 `"/api/workspace/permission"`（含 `execution_mode` 切换与规则移除）严格登记为 `"human"` 例外；
  - `TestWorkspacePluginSDKPreflight` 与真实本地 OpenClaw Gateway 集成测试验证 `exec_command` 新入参 schema、提示词约束、脱敏展示及异步等待收尾；
  - `scripts/check-i18n.ps1` 与快捷聊天 / Web 设置 Node UI 测试覆盖大白话文案双语同步、卡片展开与收起、`极速免审` 状态徽标渲染、按钮幂等锁定及模式与移除规则即时切换校验（正式验收报告须包含真机 Windows + OpenClaw + DeepSeek 实测结果，并明确区分 macOS / Linux 未覆盖平台边界）。
