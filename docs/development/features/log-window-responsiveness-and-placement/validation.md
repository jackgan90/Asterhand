# 日志窗口响应与定位验证

状态：工程验证、独立审计修复复审及本机用户验收已完成。本机日志查看体验和双屏同屏定位已通过用户验收，其他显示布局与平台仍遵循下述验证边界。

## 前提和入口

使用[固定依赖](../../../../packaging/dependencies.json)中的 Go、Node.js 和可用的 Windows WebView2 Runtime。所有原生用例使用临时数据和独立宿主，不执行已注册的用户脚本，不替换在线安装。

```powershell
go test ./internal/quickinput/... ./internal/app/... ./internal/localcommands/...
node --test tests/managed-command-ui.test.mjs tests/managed-commands.test.mjs
pwsh -NoProfile -File scripts/check-i18n.ps1
git diff --check
```

按[托管命令验证](../managed-local-commands/validation.md#自动测试)构建独立宿主，设置 `ASTERHAND_DESKTOP_TEST=1` 与 `ASTERHAND_INPUT_HOST_TEST_DIRECTORY` 后运行 `go test ./internal/quickinput -run TestManagedLog -count=1 -v -timeout=180s`。

## 验证层次

- 协议与替身：定位提示的方向/动作限制、负坐标及数值边界、宿主超时后重建、迟到回执、读取/停止消息与窗口创建并发。
- 几何：不同工作区和 DPI 下的初始居中、已有窗口保留与跨屏调整、超大尺寸裁剪。
- 前端：按钮操作失败后自动刷新不清除错误；后续主动操作可重新开始。
- 真实桌面：冷开先显示加载态、同一 HWND 隐藏/重开、最小化恢复、初始化失败及重建、真实双屏移动与独立命令继续执行。

真实多屏用例需要至少两台逻辑显示器；不存在时明确 skipped，不以几何测试代替。两个主题与两种语言的加载/错误提示需要实际 WebView2 和原生窗口检查。

源码入口：[`log_host_test.go`](../../../../internal/quickinput/log_host_test.go) 验证就绪复用、超时淘汰和慢清理隔离；[`log_runtime_windows_test.go`](../../../../internal/quickinput/log_runtime_windows_test.go) 验证初始化期限、首次显示回执、缓存回收和旧代次隔离；[`log_reuse_windows_test.go`](../../../../internal/quickinput/log_reuse_windows_test.go) 使用真实独立宿主验证 HWND 复用、最小化恢复、profile 创建失败后的重建及实际双屏工作区。

默认测试覆盖负坐标和不同 DPI 的几何策略；实际不同缩放、负坐标显示布局及用户使用中的输入框，需要在相应桌面布局下验收，不把固定 100% 缩放的双屏自动化扩大成全部布局已验证。

资源和发布时序回归：[`log_profile_windows_test.go`](../../../../internal/quickinput/log_profile_windows_test.go) 验证只删除已释放的单个代次、保留未确认浏览器目录；`log_host_test.go` 验证退休 Stop 失败不被后续 Close 丢弃；[`lifecycle_test.go`](../../../../internal/app/lifecycle_test.go) 验证错误进入应用退出结果。设置 `ASTERHAND_DESKTOP_TEST=1` 后额外运行 `go test ./internal/quickinput -run TestNativeDispatchBeforeWindowCreation -count=1 -v`，验证创建前及 HWND 发布期间并发投递的更新均被唤醒；该用例只创建不可见原生窗口，不启动 WebView2。真实日志复用用例同时核对最小化前后用户自定义矩形保持一致。

诊断在节点的 `error.log` 中搜索 `command_log_window`。阶段依次包括原生显示、环境、控制器、页面 ready 和 initialized；失败记录错误键、可用的 HRESULT/Win32 错误码及经过时间，同一宿主通过 `session` 关联。日志不包含脚本参数、命令日志正文或浏览器页面内容。

清理边界还覆盖 environment 已建立、退出观察器尚未订阅成功的失败状态；此时保留 profile、报告退出未确认，并由父进程回收日志宿主的独立 Job。不能仅因缺少观察器就判定浏览器不存在。

工程验证、实体桌面自动化、用户安装后验收与发布状态分别记录。原始执行输出保存在忽略目录，本文不保存机器路径或临时进程标识。
