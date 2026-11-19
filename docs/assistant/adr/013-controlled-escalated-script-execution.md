# ADR-AST-013：受控提权脚本执行（`OrdinaryToken`）、多层信任策略与无审批极速模式

- **状态**：accepted（在第一阶段受限工作区写沙箱之上扩展第二阶段交互式审批提权与 `turbo` 自动单次放行模式）。
- **所属需求**：R03、R04、R23、R27；`AST-TOOLS-06`、`AST-TOOLS-09`、`AST-TOOLS-10`、`EXEC-TOOLS-03`、`EXEC-TOOLS-05`—`08`。
- **设计与计划**：[受控提权脚本执行计划](../../development/features/workspace-script-escalation/plan.md)、[验证方案](../../development/features/workspace-script-escalation/validation.md)、[本机工具能力与运行时适配](../runtime-capabilities.md)。
- **替代关系**：扩展 [ADR-AST-012](012-workspace-script-execution-boundary.md) 中“第二阶段审批提权预留独立扩展口”的演进架构；第一阶段 `WRITE_RESTRICTED` 写沙箱与四级统一停止契约完整保留并继续作为按需模式默认路径。

## 背景

第一阶段 `WRITE_RESTRICTED` 内核写沙箱在保障零审批安全的同时存在两类天然工作流边界：其一，写保护严格锁定当前受管工作区与 `sandbox-temp`，导致整理桌面/下载文件或向指定外部项目写入时必然遭遇内核 `ACCESS_DENIED`；其二，系统 WinHTTP/Schannel 在受限令牌下无法打开默认凭据（影响 PowerShell `Invoke-WebRequest`、`winget` 等依赖系统 HTTPS 的工具）。在保持普通用户非提权安全底线的前提下，产品需要既支持按需弹出简洁卡片由非技术用户逐项授权，也支持为愿意自行承担日常风险的用户提供类似成熟 harness 的零交互“极速免审”运行模式。

## 核心决策

1. **Tier 2 复用 `OrdinaryToken` + 具名 Kill-on-Close JobObject，保持零 UAC 与普通用户权限边界**：
   - 受控提权不引入 Windows UAC 管理员提权弹窗，也不创建系统高权限服务账号，而是直接复用 [token_windows.go](../../../internal/platform/userprocess/token_windows.go) 的 `userprocess.OrdinaryToken()` 构造剥离管理员组与高危特权的 Medium IL（`S-1-16-8192`）普通用户令牌，即使宿主意外以高权限启动亦将助手子进程主动降权；
   - 继续使用具名 JobObject（`Local\Asterhand.Exec.<sessionID>`）与受控环境构造（剥离 `DEEPSEEK_*`/`OPENCLAW_*`/`ASTERHAND_*` 及危险变量覆盖，但保留正常 `TEMP`/`TMP` 与原生 Schannel TLS），且在脚本结束后统一触发工作区 `reconcileAfterExec`（保护工作区文件版本号免受 `restore` 竞态覆盖）。
2. **Go 服务端独立执行五层严格评估与反绕过解析，模式与规则独立修订零重启热生效**：
   - 五层优先级固定为：`System Hard Deny`（在启动前及引用工作区脚本扫描中拦截 `<HomeRoot>` 私有目录、常见用户核心凭据目录、显式 UAC 提权动词 `-Verb RunAs`/`runas`/`sudo` 及过宽根目录/顶级系统或用户目录） → `User Deny` → `User Ask` → `Session Allow` / `Persistent Allow` → 兜底策略（`standard` 模式进入人工确认卡片，`turbo` 模式自动单次放行 Tier 2）；
   - 保守词法分析对未引号包裹复合连接符/重定向及裸通用解释器/代理宿主（如 `powershell`、`cmd`、`python`、`node`）禁止仅凭命令前缀授予永久规则或显示 `始终允许`；按命令调用头部语法区分“直接调用位置”与裸引号/数值/布尔表达式（仅首部处于直接调用位置时才按程序身份固定，裸表达式保留原生求值且退出免审）；对 `command` 类 `allow` 规则按子进程生效 `PATH`/`PATHEXT` 与工作目录解析并严格绑定 `CanonicalExistingPath` 规范绝对路径（屏蔽 PowerShell 内置别名误绑定，绝不退回裸短名匹配），启动时通过 UTF-16LE Base64 数据传递已核验程序路径并以 `DirectRawArgs` 保留本地化命名参数与参数类型语义；
   - `config.LocalToolPolicy` 引入独立的 `ExecutionMode`（`standard` / `turbo`）、`TrustRevision` 与 `TrustRules`，与触发 Gateway 重启的 `LocalToolPolicy.Revision` 隔离；所有配置写操作统一经 `config.Manager.writeMu` 事务提交（普通设置保存保留最新 `LocalTools`），多目录信任规则单次批量落盘并串行热更新；收紧或移除规则立即同步清理所有会话级缓存。
3. **分片 HTTP 等待协议（`2.5s`）、单次原子认领、写屏障与 `TOCTOU` 防篡改校验**：
   - 为避免人类确认耗时触发 45s 控制桥 `WriteTimeout`，`"exec/start"` 创建 `ap_<rand>` 票据置 `PhaseAwaitingApproval` 并立即返回 `approval_required`，由插件循环调用 `"exec/approval_wait"`（每次 `<= 2500ms`，最长等待 `5 分钟`）；同票据通过 `claimed` + `execDoneCh` 原子单次认领执行权，无等待者直接 `End` 立即回收票据并解除屏障；
   - 等待期间调度器通过 `awaitingApproval` / `approval_pending` 写屏障延迟同轮并发受管文件修改；批准后 `WaitApproval` 先基于原始请求 `tk.req` 重新解析工作目录、外部目录、主程序及最高 `16 MiB` 工作区内引用脚本与可验证可执行入口（排除纯数据大文件，双向核对增删与 SHA-256 摘要，拒绝无法验证的符号链接或超限脚本），再通过 `verifyAndLockPreSpawn` 以 `FILE_SHARE_READ` 持有锁定直至子进程退出，并结合 `SpawnAttachedJob` 的 `AdmitResume` 在挂起恢复线程前与取消状态串行仲裁；全部直通与票据启动均先完成 `TOCTOU` 终检与启动、再发布会话/持久允许规则（若持久保存失败则保留执行结果并独立返回 `rule_persist_error` 告警，终检未通过报 `approval_tampered` 且零规则残留）；拒绝统一返回非阻塞 `denied_by_user` 并与实时/历史共享活动投影映射为 `"denied"`（且同步透传只读 `approval_mode`）。
4. **“极速免审（`turbo`）”定位为无白名单污染的自动单次授权（`Auto-Approve Ephemeral`）**：
   - `turbo` 模式下无论模型是否传递 `escalated: true`，只要未命中硬底线、显式 `User Deny` 或 `User Ask`，即一步直通 Tier 2 普通用户态；每次放行仅记录只读 `approval_mode = "turbo_auto"`，绝不污染会话或持久允许列表；切回 `standard` 立即恢复按需确认。
5. **面向非技术用户的极简双面交互与闭环审核契约**：
   - 聊天卡片仅在 `awaiting_approval` 且 `pending` 时于原工具行内展开（通俗标题+目标胶囊+用途说明+可选“查看命令”+`本对话允许`/`仅本次`/条件`始终允许`/`不允许`），处置后立即收起为单行活动记录；Web `设置 → 助手` 仅保留双选确认方式与“空态自动隐藏、单行一键移除”的已始终允许项目清单，不暴露复杂正则/通配符规则编辑器。
   - `approve_tool`（原生闭合动作）与 `/api/workspace/permission` trust 字段按 `AUTO-19`/`AUTO-20` 保留在 `human` 人工例外集合，不向内置模型或普通 CLI 开放自批准。

## 备选与取舍

| 方案 | 结论 |
|---|---|
| 通过 `ShellExecute("runas")` 弹出系统 UAC 对话框 | 会阻塞无人值守执行或使高权限子进程脱离可控进程树；固定在启动前返回 `elevation_verb_denied` |
| Web 设置页暴露细粒度路径通配符与命令规则编辑器 | 增加非技术用户理解与误配负担；收口为按需/极速双选及内联确认沉淀后的一键移除清单 |
| 扩展现有修改信任规则即重启 OpenClaw Gateway | 导致长连接断开与数秒冷启动卡顿；改由独立 `TrustRevision` 与 `SetWorkspaceTrustPolicy` 毫秒热更新 |
| `turbo` 模式将每次经过的路径写入持久白名单 | 切回 `standard` 后仍残留宽泛自动放行风险；改用零持久记录的 `Auto-Approve Ephemeral` 语义 |

## 后果

- 按需模式下绝大多数工作区安全脚本保持零打扰，遇到外部目录或系统工具链时提供一次点击、可按对话或长期复用的顺滑交互，拒绝不阻断对话与任务连续性。
- `turbo` 模式实现与成熟 harness 一致的无确认高速体验，同时依靠 `OrdinaryToken` 降权、环境变量零继承与启动前（含引用工作区脚本）静态拦截确保子进程无法获取管理员身份、不继承节点凭据环境（运行期同用户普通文件访问边界沿用 [ADR-AST-012](012-workspace-script-execution-boundary.md) 所界定的 Windows OS ACL 事实）。
- 未来 macOS（Seatbelt 非沙箱放行）与 Linux（`bwrap` 额外 `--bind`）可复用同一策略引擎与 UI 交互契约完成平台实机实现。
