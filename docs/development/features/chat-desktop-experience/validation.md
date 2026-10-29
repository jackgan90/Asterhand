# 聊天展示与桌面窗口体验验证

验收状态：本机日常聊天展示与桌面交互用户验收通过。多屏/DPI/RDP、Explorer 恢复、故障注入、权限组合与成本专项仍按各自场景验证，不由日常交互验收覆盖。

## 前提

对应[功能计划与 UX01—12 验收标准](plan.md)、[Web 设计](../../../web/design.md)与[窗口 ADR](../../../installation/adr/008-chat-window-desktop-presence.md)。工具版本使用[依赖清单](../../../../packaging/dependencies.json)，Windows 原生测试需要 x64 交互桌面及合格的 WebView2 Evergreen Runtime。输出目录由执行者选择，示例路径由命令生成，不依赖既有本机文件。

普通前端/Go 测试不调用模型；原生测试使用隔离 profile 与测试会话。真实 DeepSeek 的端到端观察单列，不用模拟回复代替用户验收。运行记录应包含命令、版本、环境、passed/failed/skipped/blocked 和原始日志；未执行项目标记 not_run。

## 自动化入口

```powershell
$uiTests = Get-ChildItem -LiteralPath tests -Filter '*.test.mjs' -File
node --test $uiTests.FullName
go test ./...
go vet ./...
```

真实宿主的构建和启用方法沿用[快捷输入验证脚本](../../../../scripts/test-quick-input.ps1)及[脚本指南](../../../../scripts/README.md)。本功能的原生用例不改变日常安装、节点设置、全局显示器配置或 Explorer 进程；实体交互补充以下人工检查。

本功能的真实 WebView2 专项入口：

```powershell
$env:ASTERHAND_CHAT_GATE = '1'
$env:ASTERHAND_CHAT_EXPERIENCE_CAPTURE = [IO.Path]::GetFullPath('./out/chat-desktop-experience/validation')
go test ./internal/quickinput -run '^TestNativeChat(DesktopWindow|ConstrainedPlacement|MathAndStatus)$' -count=1 -timeout=150s -v
Remove-Item Env:ASTERHAND_CHAT_GATE, Env:ASTERHAND_CHAT_EXPERIENCE_CAPTURE
```

`TestNativeChatDesktopWindow` 检查原生窗口样式、WebView2 拖动能力、位置保留、失焦、最小化/恢复和显式隐藏；`TestNativeChatMathAndStatus` 检查实际公式 DOM、随包字体、CSP、复制原文及 CSS 动画，并输出双主题截图。测试自动使用非提权 UI 令牌；需在允许创建隔离 profile 的正常 Windows 用户环境运行。窗口消息和样式检查不替代实体任务栏点击与鼠标拖动。

公式依赖校验使用 `node scripts/verify-frontend-assets.mjs`；源文件见 [KaTeX 清单](../../../../internal/quickinput/assets/vendor/katex/manifest.json)。公式解析/预算、回复状态及既有聊天恢复用例分别位于 `tests/chat-math.test.mjs`、`tests/chat-status.test.mjs`、`tests/chat-ui.test.mjs`；窗口工作区/DPI 逻辑用例位于 `internal/quickinput/window_layout_test.go`。

## 实体桌面验收

`TestNativeChatConstrainedPlacement` 补充验证靠近工作区底部拖动后，展开造成的位置约束不会被隐藏写回，并在缩回后恢复用户锚点。

| 范围 | 操作 | 预期 |
|---|---|---|
| UX01—03 | 呼出后发送一条消息，观察回复、完成、隐藏再打开；系统减少动态效果开/关分别观察 | 四处多余文案消失；点每约 400 ms 变化；完成清空；隐藏停止；错误状态仍可辨识 |
| UX04—07 | 发送含行内、块级、矩阵及长公式的消息，查看历史；在两主题和离线环境重复 | 公式排版、滚动与复制源码正确；无外部下载；不支持内容保留原文 |
| UX08 | 拖动顶部品牌及空白区；再点击历史/新对话/展开、选择正文和输入 | 只在标题区拖动；展开/折叠保留位置；控件与文本选择不触发拖动 |
| UX09 | 双显示器不同 DPI 下拖动，隐藏后同屏/跨屏呼出，改变可用工作区 | 使用目标屏记录/默认位置；布局留在可见工作区；小屏仍可访问输入和标题 |
| UX10—11 | 切到另一应用，再点任务栏；最小化/恢复；Esc/同键/Alt+F4/任务栏关闭 | 未固定模式已移除；失焦保留；任务栏激活同一窗口；显式隐藏移除运行按钮且不停止生成 |
| UX12 | 标准与管理员节点分别测试；在专用测试会话重建宿主、退出节点及重启 Explorer | UI 保持非提权；草稿按确认边界恢复；无残留窗口图标、重复宿主或进程泄漏 |

任务栏检查针对运行窗口按钮；用户手动固定的启动图标不由应用移除。最小化与显式隐藏是两种状态，最小化不应作为快捷键“隐藏”的替代。

## 支持边界

自动化验证状态转换、DOM/资源安全、预算与窗口消息路径；真实鼠标、输入法、多屏 DPI、Explorer 恢复及操作系统任务栏视觉表现需要实体桌面验收。交叉编译不等于 macOS/Linux 桌面支持，本功能只实现 Windows 原生适配。原有隐藏 CPU 成本限制不因本功能完成而自动关闭。
