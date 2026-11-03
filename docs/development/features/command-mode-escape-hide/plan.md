# 命令模式下 Esc 直接隐藏快捷窗口

## 范围与依据

状态：实现、相关前端回归、真实 WebView2 显隐流程、连续呼出中间帧验证及本机日常安装用户验收完成。

将快捷输入的 Esc 行为统一为隐藏整个窗口，包括输入 `/` 后显示命令列表、搜索命中和无匹配空态。关联 [WEB-CMD-06、WEB-CHAT-03](../../../web/requirements.md)，遵循 [Web 设计](../../../web/design.md)与[本地命令设计](../../../executors/local-commands.md)。

取消命令列表独立的 Esc 收起状态；命令模块仅处理列表方向键，Esc 交给编辑器现有的提交草稿和原生隐藏操作。隐藏保留输入查询，重新呼出后继续显示对应列表。输入法正在组合、事件标记 `isComposing` 或键码 229 时继续优先处理输入法，不隐藏、不执行命令。正常文本 Esc、命令搜索、方向键、Enter/鼠标执行和正在运行的脚本保持原有行为。

## 重新呼出的布局顺序

编辑器统一控制命令面板和布局模块的可见性：显示及焦点恢复时先恢复命令列表 DOM 与期望高度，再启动布局观察和首次同步测量；隐藏时先停止布局观察并取消待执行帧，再隐藏列表。命令面板不再独立订阅显示/焦点消息，避免编辑器先按隐藏列表计算小高度、随后又补算完整高度。

缓存查询和列表内容未变、工作区与 DPI 未变时，连续隐藏/呼出沿用原窗口高度，不提交中间缩小请求。已有工作区约束、首次获取命令、真实列表变化和屏幕变化继续允许合理的高度调整，不以延时或动画掩盖不完整布局。

## 验收与验证

- 输入 `/`、命中查询及无匹配查询后各按一次 Esc，整个快捷窗口隐藏，无需再按第二次。
- 隐藏不清空查询、不发送聊天、不执行命令；重新呼出恢复查询及对应列表。
- 连续隐藏/呼出时，每条高度请求和每个可见中间帧都保持完整高度，列表与底栏不被短暂裁切；覆盖单项、多项滚动、无匹配和展开态。
- 输入法组合期间 Esc 不隐藏；组合结束后正常 Esc 隐藏。
- 列表方向键、Enter 和鼠标启动、普通文本 Esc 保持可用。

自动化入口为[编辑器与桥接回归](../../../../tests/quick-input-bridge.test.mjs)、[输入按键模型](../../../../tests/quick-input.test.mjs)、[命令界面回归](../../../../tests/managed-command-ui.test.mjs)和[真实窗口命令流程](../../../../internal/quickinput/surface_commands_windows_test.go)：

```powershell
node --test tests/quick-input.test.mjs tests/quick-input-bridge.test.mjs tests/local-commands.test.mjs tests/managed-command-ui.test.mjs
pwsh -NoProfile -File scripts/check-i18n.ps1
go test ./internal/quickinput -count=1
go vet ./internal/quickinput
$env:ASTERHAND_CHAT_GATE = '1'
go test ./internal/quickinput -run '^TestNative(CommandEscapeHidesSurface|CommandReopenFrames|LocalCommandsWorkflow|LocalCommandsScrollbar)$' -count=1 -timeout=150s -v
Remove-Item Env:ASTERHAND_CHAT_GATE
```

原生回归需要 Windows 交互桌面、非提权 UI 进程和符合依赖清单的 WebView2 Runtime，使用隔离配置和受控命令响应。检查真实窗口可见性与草稿，不执行用户脚本。合成 IME 事件验证保护条件，实体输入法仍按[本地命令验收](../local-quick-commands/validation.md)检查。原始命令、环境和结果保存在忽略输出目录，工程验证与日常安装用户验收分别记录。

[逐帧呼回回归](../../../../internal/quickinput/surface_command_reopen_windows_test.go)加载[中间帧记录场景](../../../../tests/fixtures/command-reopen-regression.js)，在每次呼回前开始记录所有高度请求，并在显示消息后记录连续渲染帧，检查内容区高度、底栏和命令列表边界。即使先缩小再恢复且最终尺寸正确，也必须判定失败。

## 规模评估

修改命令面板的按键、可见条件与恢复顺序，移除已无用途的收起状态，不拆分文件。`commandPanel` 为 209 行，沿用[原功能计划](../local-quick-commands/plan.md#规模和验证边界)的实例闭包内聚例外：请求、渲染、事件和释放共同维护单一面板状态，内部按键处理保持短小。`inputEditor` 为 156 行，沿用既有编辑器工厂例外，显示顺序与草稿、IME、主题和宿主订阅共用同一实例，新增可见性函数单独保持短小。既有 `TestNativeLocalCommandsWorkflow` 为 106 行，本次未改动其主体；连续检查共享同一注册库、窗口及错误状态，保留顺序便于核对完整流程。新增和调整的内部操作函数低于 100 行，所有改动文件低于 1000 行。
