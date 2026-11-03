# 快捷输入失焦后的快捷键呼回

## 目标与范围

状态：修复、工程验证及本机快捷键用户验收完成。双屏呼回保留迁移到当前活动窗口所在屏幕并激活的行为；其他桌面专项继续按既有验证说明检查。

修复聊天窗口被其他窗口遮挡后，再按全局快捷键反而隐藏的问题。关联 [WEB-CHAT-03](../../../web/requirements.md) 与 [INST-28](../../../installation/requirements.md)，实现遵循 [Web 设计](../../../web/design.md) 和 [安装设计](../../../installation/design.md)。沿用现有窗口、非提权宿主与快捷键协议，无新增架构决策。

全局快捷键仅在聊天窗口可见且处于前台时隐藏窗口；窗口失焦、隐藏或最小化时均恢复并激活，位置跟随当前活动窗口所在屏幕。双屏下即使聊天在另一屏仍可见，也迁移后激活，不触发隐藏。Esc 在聊天窗口接收按键时继续沿用输入法候选优先与隐藏行为；系统关闭和锁屏等现有隐藏入口保留。草稿、会话和正在生成的回复不受显隐影响。

## 实现

原生 UI 线程在处理快捷键时同时读取窗口可见性和系统前台窗口，避免把 `IsWindowVisible` 当作焦点判断。失焦时保存当前前台窗口，复用原有显示、位置修正、最小化恢复与 WebView2 输入焦点处理；随后再次隐藏时恢复到此次呼回前的窗口。

更新 Web 与安装领域需求、设计中的快捷键条件，文档导航和路线图提供本计划入口。既有桌面窗口体验验证继续覆盖 Esc、系统关闭、任务栏和位置行为。

## 验收与验证

- 呼出聊天，切换到覆盖它的窗口：聊天仍可见并保留任务栏入口；再按快捷键，激活同一个聊天窗口并恢复输入焦点。
- 聊天处于前台时再次按快捷键：隐藏并回到此次呼回前的窗口；再次按键恢复原窗口和草稿。
- 最小化聊天后按快捷键：恢复并激活，不能移除运行入口。
- 在屏幕 A 呼出聊天，切换到屏幕 B 的其他窗口后按快捷键：同一聊天窗口迁移到屏幕 B 并获得焦点；再次按键才隐藏。
- 聊天接收 Esc 时隐藏；再次呼出后草稿仍在。

Windows x64 原生回归入口为 [快捷键焦点测试](../../../../internal/quickinput/surface_shortcut_windows_test.go)，使用隔离 WebView2 profile 和测试覆盖窗口，不调用模型。需有交互桌面及符合依赖清单的 WebView2 Runtime；若 Windows 拒绝后台测试进程激活，测试会等待最多 30 秒，需在交互桌面激活隔离的测试聊天窗口后继续：

```powershell
$env:ASTERHAND_CHAT_GATE = '1'
go test ./internal/quickinput -run '^TestNativeChat(ShortcutActivation|DesktopWindow|ConstrainedPlacement)$' -count=1 -timeout=150s -v
Remove-Item Env:ASTERHAND_CHAT_GATE
go test ./internal/quickinput ./internal/desktop
go vet ./internal/quickinput ./internal/desktop
pwsh -NoProfile -File ./scripts/check-i18n.ps1
```

原生回归检查真实窗口可见性、前台句柄、最小化状态、WebView2 焦点及草稿；本机快捷键修复及双屏呼回策略已通过用户验收，多屏 DPI、屏幕移除、Explorer 恢复等专项继续按[桌面窗口体验验证](../chat-desktop-experience/validation.md)检查。其他桌面平台继续沿用现有支持边界。运行命令、环境与原始输出保存在执行者指定的忽略目录，不写入长期文档。

## 规模评估

修改局限于原生窗口的快捷键判断，新增原生交互测试单独成文件；不拆分现有文件。改动文件与新增函数保持在工程建议规模内。
