# 快捷输入进程隔离验证

本说明定义[进程隔离计划](plan.md)与[ADR 007](../../../installation/adr/007-quick-input-process-isolation.md)的验证入口。源码、包契约和独立审计已完成，2026-10-26 本机用户验收通过。

本机验收覆盖当前 Windows 节点的实际使用。隐藏 CPU 预算继续记为 failed；独立标准用户、UAC 开启配置、干净安装及多屏/DPI/RDP 等未完成的专项矩阵保持待验证。以下规程用于这些环境的补充验收和后续回归。

## 自动化入口与前提

使用[依赖清单](../../../../packaging/dependencies.json)和 Go 模块锁定的工具链。基础回归：

```powershell
go test ./...
go vet ./...
node --test tests/quick-input.test.mjs tests/quick-input-bridge.test.mjs tests/desktop-settings.test.mjs
```

| 测试入口 | 验证目的 |
|---|---|
| `internal/quickinput/hostipc` | 分配前限制帧长，拒绝非法 UTF-8/代理项、错方向、未知消息与字段；最大转义文本可传输 |
| `internal/quickinput/hostbundle` | 固定同目录程序、应用版本/源码指纹/摘要校验；Windows 读句柄在启动期间禁止替换 |
| `host_editor_test.go`、`host_session_test.go` | 合并编辑、主进程 ACK、快照屏障、清空 epoch、有限恢复、旧代次回调及退休会话迟到编辑失效 |
| 既有 controller/API/JS 测试 | 配置事务、冲突回滚、录制租约、停止、正文与初始化握手 |
| `host_process_windows_test.go` | 实际 GUI 子进程令牌复核、初始化、主题/快照、终止 UI 后重建并保留草稿 |
| `host_owner_windows_test.go` | 分别在宿主等待初始化和 UI 就绪后强制结束测试父进程，确认事先持有句柄的自有进程全部退出 |
| `surface_cost_windows_test.go` | 独立宿主与浏览器全树成本、完整 IPC 快照往返和连续启停后的残留 |

真实桌面测试使用隔离目录，不替换安装中的节点。先解锁 Windows 并留在当前交互会话：

```powershell
pwsh -NoProfile -File scripts/test-quick-input.ps1 -Go <go-executable> -OutputDirectory <isolated-output-directory> -Desktop -Cycles 100
```

脚本自动生成开发 GUI 宿主和清单，记录 Go JSON 与成本报告；未指定 Desktop 时原生用例 skipped。管理员运行时，直接进程内 WebView2 的旧测试明确跳过，独立宿主测试仍执行，避免在高权限进程创建浏览器。原生测试需真实 Windows 用户令牌和文件权限；工具沙箱中的访问拒绝不得冒充标准用户兼容性结果。100 次启停超时为 30 分钟，性能门槛没有放宽。

单独运行独立宿主用例时，先执行 `scripts/build-input-host.ps1 -Go <go-executable> -OutputDirectory <host-directory>`，设置 `ASTERHAND_DESKTOP_TEST=1` 与 `ASTERHAND_INPUT_HOST_TEST_DIRECTORY=<host-directory>`，再运行 `go test ./internal/quickinput -run '^TestIsolatedHost' -count=1 -v -timeout=3m`。测试仅终止自己创建的进程。

## 实体输入与故障恢复

`host_interactive_windows_test.go` 提供显式选择的人工辅助程序。设置上述宿主变量及 `ASTERHAND_INPUT_INTERACTIVE=1`，运行 `go test ./internal/quickinput -run '^TestIsolatedInteractiveBroker$' -count=1 -v -timeout=17m`。使用原始工具窗口样式和真实系统热键；测试父窗口提供结束 UI、重新打开和退出辅助验证的操作，不修改节点设置。

1. 按 Ctrl+Alt+J，直接用实体键盘完成中文候选、英文、换行、粘贴、选择区和撤销；检查文本、字节上限及组合输入。
2. 检查 Escape、同键和失焦收起，从 High/Medium 前台应用分别呼出并确认焦点恢复。
3. 输入后等待主进程接受，主动结束 UI，再次呼出应恢复已接受文本并提示未确认输入的边界；主进程任务不受影响。
4. 冷恢复时继续操作其他窗口，面板应隐藏预热；无后续活动的有效显式请求可显示。
5. 单独验证主动禁用的快照失败、未完成 IME、renderer/browser 故障、连续崩溃及恢复期间退出。自动化草稿测试和实体输入验收分别登记。

Escape 可停止外部桌面自动化工具；这不影响产品本身的收起行为。人工验证可以全部通过实体键盘及鼠标完成。

## 权限、包与桌面矩阵

分别在标准用户、管理员账户未提权、UAC 开启并提权、UAC 关闭管理员环境运行，确认宿主非提权 Medium、管理员组不授予访问、浏览器沙箱保持启用。SYSTEM、非交互会话、目录访问不足及外部 WebView2 策略覆盖准确降级，不修改系统 UAC/ACL。独立普通用户环境缺失时记录 skipped，不能用受限令牌测试替代。

正式包通过 `scripts/build.ps1` 构建，并由同包 `asterhand.exe bundle-check --directory <package-directory>` 校验。检查宿主缺失、清单损坏、版本/源码指纹错配、摘要错误及含空格/非 ASCII 的路径。源码安装走同一构建契约。安装到实际节点、重启与升级验收需分别安排，隔离构建不代表已经安装。

多屏、负坐标、100/150/200% DPI、拔屏、锁屏/RDP、干净 Runtime 新装和高资源压力分别验收。macOS/Linux 保持 unsupported；公共代码与命令的交叉编译只验证平台依赖隔离，不代表原生 UI 支持。

## 性能与证据边界

沿用[原验证说明](../global-hotkey-quick-input/validation.md)的冻结预算：冷初始化 P95 ≤10 秒、桥往返 P95 ≤200 毫秒、增量 private commit ≤350 MiB、隐藏 CPU ≤单核 1%、100 次完整启停后自有残留为 0。成本包括独立宿主及浏览器树。快照往返表示完整 IPC/页面屏障耗时，不代表可见激活到可输入时间；后者和主服务 ready 延迟需另测。

隐藏 CPU 是已知未达标项，隔离重构没有豁免该门槛。采样包含隐藏初始化后的运行成本，需保留采样窗口、全部进程和原始值；稳态优化另行验证，不能仅改变采样条件宣称修复。早期出现的 WebView2 `0x80000003` 未获得可定位根因；后续未复现不记为修复。

每次运行记录源码指纹、应用/Go/Node/Runtime 版本、权限环境、命令及 passed/failed/skipped/blocked。原始日志、JSON 和截图保存到指定忽略输出目录；发布证据随对应版本提供。源码实现、自动化检查、实体输入确认、完整产品验收、安装和发布分别登记。
