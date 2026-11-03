# 本地快捷命令验证

对应[功能计划](plan.md)、[本地命令设计](../../../executors/local-commands.md)、WEB-CMD-01—08 和 EXEC-CMD-01—05。使用[锁定工具链](../../../../packaging/dependencies.json)。以下命令从仓库根目录运行，输出目录均为可生成的忽略目录，不依赖作者本机安装。

## 自动化检查

```powershell
go test ./...
go vet ./...
go build ./...
$uiTests = Get-ChildItem -LiteralPath tests -Filter '*.test.mjs' -File
node --test $uiTests.FullName
```

- [注册库测试](../../../../internal/localcommands/registry_test.go)：名称合并、描述保留、独立启动配置、冲突、重启保存、删除不删脚本、失败写入不发布、损坏配置不覆盖、分页与状态更新、并发启动和旧修订拒绝。
- [Windows 启动测试](../../../../internal/localcommands/launch_windows_test.go)：真实 bat/cmd/PowerShell，中文及空格路径、空参数、元字符、PowerShell 字面引号、工作目录、节点私有环境不继承、父进程结束后继续执行。批处理使用 `%~n` 读取时保留尾部反斜杠；含字面双引号的批处理参数在保存和启动时拒绝。
- [Web API 测试](../../../../internal/app/commands_api_test.go)：登录、CSRF、CRUD、旧修订拒绝，以及不存在 HTTP 启动路由；通用来源检查继续由现有应用测试覆盖。
- [文件选择 API 测试](../../../../internal/app/commands_picker_test.go)：认证、CSRF、请求字段、取消、类型边界、同时仅一个选择窗口和请求中止；[表单测试](../../../../tests/local-command-management.test.mjs)验证回填、取消保留原值、重复点击及会话结束后的迟到结果。
- [无模型节点启动测试](../../../../internal/app/commands_api_windows_test.go)：真实应用调用普通用户启动器，检查脚本未提权、不继承节点私有环境、未创建模型运行目录，并在节点关闭后继续产生结果。
- [命令宿主测试](../../../../internal/quickinput/commands_test.go)、[IPC 测试](../../../../internal/quickinput/hostipc/commands_test.go)：请求方向、字段、ID/修订、重复投递、独立确认和旧 epoch；宿主来源校验沿用快捷输入桥测试。
- [前端测试](../../../../tests/local-commands.test.mjs)：单行命令识别、文本引用、排序/别名/缺失项、独立序号、错误和释放后不重发，以及错误按命令保留、成功清除与迟到回执不覆盖新状态。既有聊天、编辑器和高度测试验证接线兼容。
- 现有内嵌资源闭包测试检查新增模块、样式在主 Web 和输入宿主中均可加载，避免源码可见但构建遗漏。

实际终端测试需交互桌面，会短暂打开 cmd 与 PowerShell 控制台，脚本写入临时结果后自动退出：

```powershell
$env:ASTERHAND_COMMAND_TERMINAL_TEST = '1'
go test ./internal/localcommands -run '^TestWindowsTerminalAndBatchQuoteBoundary$' -count=1 -v
Remove-Item Env:ASTERHAND_COMMAND_TERMINAL_TEST
```

## 真实 WebView2

Windows 文件选择使用单独的普通用户进程；以下专项实际打开系统文件选择窗口，检查显示后的窗口位于目标屏幕工作区中央，再自动选择临时 bat/cmd/ps1，覆盖中文空格路径、取消、请求中止和私有环境不继承：

```powershell
$env:ASTERHAND_SCRIPT_PICKER_TEST = '1'
go test ./internal/quickinput -run '^TestNativeScriptPicker$' -count=1 -timeout=90s -v
Remove-Item Env:ASTERHAND_SCRIPT_PICKER_TEST
```

实现及回归入口：[选择器](../../../../internal/quickinput/script_picker_windows.go)、[原生选择测试](../../../../internal/quickinput/script_picker_windows_test.go)。该测试覆盖系统窗口与进程边界，浏览器回填可另用受控选择响应验证；Browse 的本机使用体验已通过用户验收，多屏/DPI 仍单独验证。

需 Windows x64、WebView2 Runtime 和允许创建普通用户进程/profile 的桌面。测试使用隔离注册库与真实页面/IPC 编解码，启动回调为计数替身；脚本创建进程由上面的真实启动测试单独覆盖，不称为已安装产品端到端验收。

```powershell
$env:ASTERHAND_CHAT_GATE = '1'
$env:ASTERHAND_COMPOSER_CAPTURE = [IO.Path]::GetFullPath('./out/local-quick-commands/captures')
go test ./internal/quickinput -run '^TestNativeLocalCommands(Workflow|Scrollbar)$' -count=1 -timeout=150s -v
Remove-Item Env:ASTERHAND_CHAT_GATE, Env:ASTERHAND_COMPOSER_CAPTURE
```

[原生流程测试](../../../../internal/quickinput/surface_commands_windows_test.go)检查十二项列表的溢出滚动、高亮与键盘滚入、一项过滤后无滚动条、中文别名、双主题、合成 IME 保护、鼠标及多次 Enter、按键长按抑制、缺失标记/提示/配置定位、无匹配、Esc 一次隐藏整个窗口、重新呼出保留查询及展开态几何；`TestNativeCommandEscapeHidesSurface` 单独覆盖 `/`、匹配和无匹配时的原生隐藏与输入法保护，运行命令见[命令模式 Esc 计划](../command-mode-escape-hide/plan.md)。截图用于布局复核，合成事件不替代实体输入法和鼠标体验。

其中[行内错误检查](../../../../internal/quickinput/surface_commands_feedback_windows_test.go)验证成功无提示、错误在行内右侧且不增高、深浅主题可辨认、跨刷新/过滤/收起保留，以及其他命令成功不清除、本命令成功清除。

[连续呼回逐帧检查](../../../../internal/quickinput/surface_command_reopen_windows_test.go)在单项、多项滚动、无匹配和展开态反复隐藏/呼出，记录每条高度请求及显示后的连续帧。要求未变化的缓存内容不产生临时小高度，任一中间帧均不裁切命令列表或底栏；最终恢复正确高度仍不足以通过该回归。

[布局逐帧测试](../../../../internal/quickinput/surface_commands_scrollbar_windows_test.go)通过[页面夹具](../../../../tests/fixtures/command-scrollbar-regression.js)暂停原生尺寸请求，检查空注册库、一项、三项、十五项、重开和无匹配时的等待帧与尺寸就绪帧。输入区取得焦点，逐帧比较展开前后的高度与下边界，确保焦点下划线不短暂上跳；同时检查临时滚动条。测试还复现旧缩小请求晚于新展开请求的交错，并确认限高后仍能滚动且底栏可见。该测试检查真实 WebView2 的几何和样式，不用固定延时作为产品中的尺寸确认依据。

## Web 与用户验收

本地快捷命令、Browse、行内错误反馈及展开布局的本机使用体验已通过用户验收。以下步骤作为后续回归清单；未单独记录的跨环境专项不计入本机验收结论。

可用[隔离浏览器 fixture](../web-ui-maintainability/validation.md#独立构建与浏览器冒烟)检查真实 HTTP、登录和管理页面；不要在测试报告保存生成的登录链接。测试完向 fixture 的 `command` 文件写入 `stop` 清理临时节点。

1. 打开“本地命令”，使用路径右侧 Browse，确认选择窗口在当前屏幕工作区居中；选择临时 ps1，确认绝对路径回填；再次打开后取消，原值保留。多屏环境将浏览器移到另一屏再打开，检查居中及不同 DPI 下的位置。注册英文名 `backup`、中文别名和描述；添加中文/空参数并保存，刷新后回显一致。
2. 相同启动配置再注册 `save`，列表保持一条、名称合并、原描述保留；不同参数、工作目录或窗口模式保留独立项。编辑/删除别名后刷新，再删除命令，确认脚本仍在磁盘。
3. 同时打开两个管理页面，在一页保存后用另一页旧表单保存，应拒绝且保留表单内容。关闭未保存表单可选择继续编辑；返回已有命令不会静默覆盖输入。
4. 在安装候选中关闭模型配置，呼出快捷输入，输入 `/`，用键盘/鼠标启动本地脚本。确认无模型请求；普通聊天仍按自身配置状态工作。
5. 用带延时并写入标记的受控脚本连续启动多次；隐藏/重开输入框、退出节点后标记仍按脚本预期产生。后台无交互窗口；终端可显示输出并输入。脚本自行收尾。
6. 删除原脚本等待状态更新，应保留红色条目，执行显示指定缺失提示；打开配置定位该命令，更换路径后恢复。工作目录错误单独提示；启动回执未知时不自动重试。
7. 实体中文输入法选词时 Esc 优先取消候选；非组合时在 `/`、命中列表及无匹配空态各按一次 Esc，整个快捷窗口隐藏，重新呼出保留查询并恢复列表。检查列表滚动条拖动、长别名、深浅主题、屏幕边缘及不同 DPI 下操作。安装后的体验与自动化工程验证分别记录。

## 记录与支持边界

原始命令、系统/工具版本、passed/failed/skipped/blocked、日志和截图保存在当次忽略输出目录；发布证据随对应版本附带。普通 Go 回归默认跳过交互桌面专项。当前支持 Windows 自带 cmd 和 Windows PowerShell 5.1，不承诺第三方 shell、UNC、远程执行或模型调用。企业组策略、其他权限组合、实体 IME 和多屏/DPI 分别验收；无需在线模型与双机来证明本地命令逻辑，项目原有模型/双机验收范围保持独立。
