# 本地命令完成通知验证

## 前提与命令

使用[锁定依赖](../../../../packaging/dependencies.json)中的 Go 与 Node.js。Windows 原生用例需要普通交互桌面和 WebView2；测试生成自己的脚本、数据目录与宿主，不使用已注册的用户命令。

```powershell
go test ./internal/notifications ./internal/localcommands ./internal/automation ./internal/app ./internal/quickinput/... -count=1
node --test tests/*.test.mjs
node scripts/sync-i18n.mjs --check
go test ./... -count=1
go vet ./...
git diff --check
```

原生显示测试（设置开关前应确认是可操作的测试桌面）：

```powershell
$env:ASTERHAND_DESKTOP_TEST = '1'
go test ./internal/quickinput -run TestNativeNotificationRendering -count=1 -v
pwsh -NoProfile -File scripts/build-input-host.ps1 -Go go -OutputDirectory out/completion-notification-validation/input-host
$env:ASTERHAND_INPUT_HOST_TEST_DIRECTORY = (Resolve-Path out/completion-notification-validation/input-host).Path
go test ./internal/quickinput -run 'TestNotification(HostIndependentProcess|ExitingCardKeepsExitDuringReposition)' -count=1 -v -timeout=120s
Remove-Item Env:ASTERHAND_DESKTOP_TEST, Env:ASTERHAND_INPUT_HOST_TEST_DIRECTORY
```

可选 `ASTERHAND_NOTIFICATION_PREVIEW=1` 延长原生测试显示，依次检查霜蓝英文和星核中文。为允许桌面检查工具发现卡片，该预览在完成不抢焦点断言后临时使用测试窗口的任务栏样式；正式宿主仍使用 TOOLWINDOW/NOACTIVATE，不显示任务栏或 Alt+Tab 项。

## 自动覆盖

- [配置兼容](../../../../internal/localcommands/notification_setting_test.go)：新建和旧 schema 4 缺字段默认开启，false 持久化，旧编辑器缺字段保留，null/错误类型拒绝，读取不改写文件。
- [完成事件](../../../../internal/localcommands/completion_test.go)：自然成功/失败一次性发布、主动停止及显式关闭过滤、状态读取不重播。发布位置在最终运行记录及历史索引保存成功后，既有存储失败重试测试继续覆盖该入口。
- [公共队列](../../../../internal/notifications/queue_test.go)：3 个显示名额、3 秒可读时间、进入时间排除、悬停/断连/容量缩小保留时间、100 条等待上限、溢出和过期汇总、动作失败及迟到回执。
- [前端](../../../../tests/completion-notifications.test.mjs)与[编辑器](../../../../tests/local-command-management.test.mjs)：纯文本、安全动作、外观更新、默认开关/保存/脏状态、原运行选择及保留其他分屏。
- [机器字段](../../../../internal/automation/notification_test.go)：创建、patch、草稿及返回 schema 的 boolean 类型，缺省/false/null 语义。覆盖登记、权限及 CLI/MCP 测试沿用[自动化验证](../agent-automation/validation.md)。
- [原生显示](../../../../internal/quickinput/notification_windows_test.go)：真实 WebView2 三卡片、进入/退出回执、主题语言更新、不抢前台、单行/长标题/错误行的实际高度，以及负坐标/DPI 下不同高度卡片的间距。
- [独立宿主](../../../../internal/quickinput/notification_process_windows_test.go)：正式启动参数、进程/协议校验、宿主退出后重建、恢复可见身份和最终退出；[日志宿主](../../../../internal/quickinput/log_host_test.go)核对原 run ID 穿过 IPC。
- [显示与退出动画](../../../../internal/quickinput/notification_animation_windows_test.go)：旧延迟显示计时器不能绕过当前内容的尺寸确认；三卡显示时，中间卡片先退出，顶部卡片紧接退出，移除中间卡片后仍能收到顶部卡片的退出回执，防止补位占住显示名额。
- [会话状态](../../../../internal/quickinput/notification_session_windows_test.go)：初始锁屏、断连及未知状态暂停，电源恢复不覆盖锁屏，睡眠不被会话变化解除；原生查询用例核对 Windows 当前交互会话，实体锁屏顺序仍需人工验收。
- [实际应用装配](../../../../internal/app/command_notifications_windows_test.go)：将 `go test -c` 编译的 app 测试程序放到上述 `input-host` 目录旁，设置 `ASTERHAND_NOTIFICATION_APP_TEST=1` 后运行 `-test.run=TestCommandNotificationsRealWindowsApp -test.v`。隔离节点执行真实 Windows 成功、失败、超时、关闭通知和主动停止脚本，确认实际通知出现并按时退场；无需模型凭据。

## 桌面验收与支持边界

本功能已通过本机 Windows 用户验收，包括命令编辑页两个开关的对齐，以及普通通知紧凑显示、长标题和错误信息按需增高。以下专项环境场景仍按各自的验证条件单独记录，本机验收不替代未执行的专项测试。

实际多屏、100%/150%/200% 混合缩放、任务栏移动、拔屏、锁屏睡眠、减少动态效果、系统延长通知时长和读屏需在对应环境分别验收。卡片外部及间隙应能点击桌面；其他应用输入不受干扰；鼠标移入期间卡片不自动换位。点击通知应定位保留期内的原实例，删除/过期时显示明确错误。

同时完成 10 次运行并关闭部分卡片，检查退出结束才补入、连续进退无重叠和裁剪；观察空闲时窗口隐藏、WebView2 暂停且没有持续计时轮询。核对热键关闭和浏览器关闭后仍提示。仅本机几何/自动化测试不能替代上述实体布局和用户验收。macOS/Linux 只提供接口与 `unsupported` 状态。

相关 Go 包、全量前端测试、国际化检查、静态检查、文档链接检查以及上述原生显示、独立宿主和实际应用装配场景已完成工程验证。独立审查覆盖功能、计划一致性、边界与资源、性能、代码结构及共享功能回归；交错退出、初始会话状态和延迟显示绕过尺寸确认的问题已修复并增加回归，针对性复审通过。原始失败复现、正式交错退出、会话状态组合、原生会话查询、不同内容高度的窗口复用和三卡显示均经独立复跑。

全仓库 Go 测试中的 `TestShortcutLifecycleAndWScriptInterop` 在当前测试环境因 Windows PowerShell 输出中文路径编码而失败，未改动基线也可复现；不将其记为通过。未记录的专项实体场景不视为通过。原始命令输出保存在可重新生成的构建验证目录，长期文档不依赖该目录中的日志。
