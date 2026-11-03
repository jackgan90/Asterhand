# 日志工具栏自动刷新稳定性

## 范围与依据

状态：修复、相关前端回归、双主题双语言 WebView2 工程验证及本机日常安装用户验收完成。

修复本地脚本图执行时“回到最新”等日志分页按钮反复变暗、变亮的问题。关联 [WEB-CMD-11、WEB-GRAPH-06](../../../web/requirements.md)，共享行为由 [Web 设计](../../../web/design.md)和[脚本图设计](../../../executors/script-graphs.md)维护。单脚本、合并日志及节点分屏共用同一日志读取器和工具栏，修复同时适用。

保持现有 500 毫秒刷新间隔、日志格式、读取预算、执行调度和停止行为。后台补读已有日志时保留按钮状态与焦点；初次加载、用户主动翻页、回到最新或切换编码时仍阻止重复操作。相同按钮文字和禁用状态不重复写入 DOM，切换语言时更新真实变化的文字。

## 设计

日志读取器分别记录实际读取是否在途，以及是否需要在界面显示操作忙碌。自动补读已有内容只占用读取状态，避免周期性改变按钮外观；首次加载和主动操作仍报告加载状态。自动读取保持串行，主动翻页可以替代在途的自动读取，旧响应由请求代次丢弃，不能覆盖用户选择的历史页或最新页。隐藏和销毁继续取消旧代次，读取队列仍限制并发。

工具栏按当前页边界、主动加载状态与语言计算显示内容，只提交实际变化；不通过隐藏禁用样式或延长刷新间隔掩盖问题。不新增配置、消息或架构决策。

## 验收与验证

- 脚本图持续执行，无新输出和持续输出两种情况下，合并日志和节点分屏的按钮都保持稳定，日志继续刷新。
- 自动读取尚未完成时点击历史翻页或“回到最新”，操作正常接收；迟到自动响应不改变用户选择的内容。
- 初次加载及主动翻页期间禁用重复操作，请求完成或失败后恢复可用状态。
- 切换中英文后按钮文字更新，保持已有按钮、焦点、阅读位置及跟随状态。

回归入口为 [日志读取与工具栏测试](../../../../tests/log-reader.test.mjs)、[日志窗口刷新测试](../../../../tests/managed-command-ui.test.mjs)和[脚本图界面测试](../../../../tests/script-graphs.test.mjs)：

```powershell
node --test tests/log-reader.test.mjs tests/managed-command-ui.test.mjs tests/script-graphs.test.mjs
pwsh -NoProfile -File scripts/check-i18n.ps1
node scripts/verify-frontend-assets.mjs
```

真实 WebView2 回归 [TestNativeLogToolbarRefresh](../../../../internal/quickinput/log_toolbar_windows_test.go) 在隔离宿主加载真实日志模块、样式及[受控轮询场景](../../../../tests/fixtures/log-toolbar-stability.js)，覆盖两种主题和两种语言，检查按钮 DOM 不重复改写、读取期间透明度保持不变、焦点保留及有/无新输出。需要 Windows 交互桌面及符合依赖清单的 WebView2 Runtime：

```powershell
$env:ASTERHAND_DESKTOP_TEST = '1'
go test ./internal/quickinput -run '^TestNativeLogToolbarRefresh$' -count=1 -timeout=90s -v
Remove-Item Env:ASTERHAND_DESKTOP_TEST
go test ./internal/quickinput -count=1
go vet ./internal/quickinput
```

日常安装按[脚本图日志体验验证](../script-graph-log-experience/validation.md)运行持续输出和等待型脚本，观察按钮稳定性并操作翻页。原始命令、环境和结果保存到执行者指定的忽略目录；工程测试、原生窗口检查和日常安装用户验收分别记录。

## 规模评估

沿用读取器和单屏工具栏的独立模块，文件均低于 1000 行。`logReader` 为 110 行，`logPane` 为 108 行；两个工厂闭包共享单屏请求代次、解码器、DOM 和销毁状态，沿用[日志体验计划](../script-graph-log-experience/plan.md#规模评估)的内聚例外。新增在途状态仍属于原有读取职责，内部操作函数保持低于 100 行，拆开工厂只会额外暴露这些实例状态。
