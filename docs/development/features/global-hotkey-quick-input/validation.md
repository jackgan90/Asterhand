# 快捷输入验证

独立宿主、主进程草稿、权限矩阵与退出清理的当前验证入口见[进程隔离验证](../quick-input-process-isolation/validation.md)。

## 交付与验收状态

Windows 首版开发、独立实现审计及修复复审、用户验收已完成。功能包含默认 Ctrl+Alt+J、本机 Web 自定义与持久化、实际全局注册冲突检测、内存文本面板和两种主题。隐藏 CPU 冻结门槛继续保留 failed，作为首版性能限制；逐项工程验证与用户验收分别记录。

## 前置条件与入口

普通回归使用清单锁定的 Go、Node 和 PowerShell 7，无模型费用。真实桌面用例要求 Windows x64 当前用户交互会话和符合清单要求的 WebView2 Runtime；测试使用独立临时目录和自己的注册/窗口，不终止其他应用。运行时版本可从桌面设置状态的 runtime_version 读取。

```powershell
go test ./...
go vet ./...
node --test tests/plugin-contract.test.mjs tests/web-state.test.mjs tests/quick-input.test.mjs tests/desktop-settings.test.mjs
pwsh -NoProfile -File scripts/test-quick-input.ps1 -Go go -OutputDirectory .\out\quick-input-validation -Desktop -Cycles 100
```

省略 Desktop 时，真实注册、WebView2 与资源成本用例明确 skipped。Cycles 默认 0；100 次启停需显式选择，允许 20 分钟完成。它保持窗口隐藏，正常退出等待本 profile 的 BrowserProcessExited，再检查本测试进程的子进程树；不会按进程名结束 WebView2。

## 自动化覆盖与边界

| 入口 | 覆盖 |
|---|---|
| [控制器与草稿](../../../../internal/quickinput/controller_test.go) | 新组合冲突、旧组合仍有效、候选消息屏蔽、写入失败回滚、释放失败后新配置权威、清理期间禁用、幂等重试、CAS、无效配置保全恢复、录制所有权与到期、禁用快照失败、UTF-8 上限与过期版本 |
| [原生快捷键](../../../../internal/desktop/hotkey_windows_test.go) | 子进程真实占用/退出释放；Ctrl+Alt+J 的 WM_HOTKEY 派发、录制回送和 MOD_NOREPEAT |
| [真实 WebView2](../../../../internal/quickinput/surface_behavior_windows_test.go) | Unicode 草稿、快照、组合输入状态、主题保留选区、错误代次/字段拒绝、外部导航和网络拒绝、Web 存储未使用、关闭后隔离 profile 中无测试草稿 |
| [私有 profile](../../../../internal/quickinput/surface_windows_test.go) | 纯 Go 宿主、可信页面 ready、实际 Runtime 版本、BrowserProcessExited 与文件锁释放 |
| [API](../../../../internal/app/desktop_api_test.go) | 认证、Origin/CSRF、保存主题不修改模型与节点配置；[静态模块](../../../../internal/app/web_assets_test.go)验证入包及 MIME/nosniff |
| [JS 文本](../../../../tests/quick-input.test.mjs) | 16,000 ASCII 字节、5,333 中文、4,000 emoji 边界，非法 Unicode、LF、粘贴选择区替换，无截断 |
| [桌面设置异步交互](../../../../tests/desktop-settings.test.mjs) | 旧录制的迟到回复不覆盖或取消新录制；开始请求返回前失焦，释放迟到租约且不重新录制 |
| [安装边界](../../../../scripts/test-installer-webview2.ps1) | 新版复用、缺失/较旧补齐、签名错误、下载/摘要失败、重启要求、成功退出却无 Runtime；供应商安装为 mock |

composition 事件自动化检查状态机，物理中文候选窗口仍需真机验收。单机故障注入、合成会话消息与真实两机结果分别记录。

控制器回归还覆盖故障后由托盘恢复保存主题，以及无配置变更时返回已完成清理后的状态。原生回归验证主动关闭立即清除窗口可见状态，随后等待 BrowserProcessExited，不留下等待清理的空白浮层。

## 资源成本协议

[成本测试](../../../../internal/quickinput/surface_cost_windows_test.go)适用于普通 Windows x64 开发桌面、Evergreen Runtime、单个独立 profile。冷初始化从 STA 启动计时到页面 ready 和初始草稿发送；热指标是已加载页面的快照桥往返，不将隐藏页面指标称为前台可编辑延迟。

冻结工程预算：冷初始化 P95 ≤10 秒，桥往返 P95 ≤200 毫秒，自有进程树增量 private commit ≤350 MiB，隐藏两秒采样 CPU ≤单核 1%，100 次完成后遗留自有子进程为 0。实际环境、原始样本与结果写入生成物；失败保留原值，不修改预算宣称通过。

surface-cost.json 包含次数、P95、内存、CPU、遗留进程与预算。全系统 WebView2 内存不计为本应用所有；跨设备成本、可见激活 P95、服务 ready 基线及禁用长时间 CPU 需独立测量。

首版保留一项性能限制：隐藏面板虽使用 WebView2 挂起，进程组仍有初始化和后台 CPU 开销，当前样本未达到单核 1% 的冻结预算。该门槛继续记录 failed，不改阈值、不算通过；首版交付放行与性能通过分别判断。后续优化沿用原预算复测，具体环境与原始数值保存在对应执行报告。

## 浏览器与外观

使用隔离基础节点且保持模型未配置，实际完成：进入设置、录制新组合、保存、刷新/重启保留组合与主题、禁用/启用、恢复默认、检查占用。另开页面验证版本冲突与录制互斥；正常停止测试节点后再运行默认键测试，避免测试节点自己占用。

[视觉捕获](../../../../internal/quickinput/surface_capture_windows_test.go)同时设置 ASTERHAND_DESKTOP_TEST=1 与 ASTERHAND_SURFACE_CAPTURE 输出目录，运行 TestNativeSurfaceVisualCapture，短暂显示测试窗口并生成两种主题 PNG。它使用真实 compositor，未给产品桥增加执行脚本或写文件权限。

## 专项验证范围

以下设备与故障场景尚无逐项可复现执行记录，保持 not_run，由后续专项记录维护：

- 物理中文候选的 Enter/Esc、撤销/重做、超限组合与粘贴，不同键盘及前台软件焦点体验。
- 多屏、负坐标、100/150/200% DPI、拔屏、锁屏/RDP、UAC、管理员/SYSTEM/非交互会话。
- 干净 Windows 完整包与 Runtime 新装、离线/中断/重启、升级保留及共享 Runtime 边界。
- 强制结束进程的提交边界、renderer 真实崩溃恢复、物理掉电。
- 两台 Windows + OpenClaw + DeepSeek 相关路径按[产品验收](../../../testing/acceptance.md)执行，既有版本验收不自动覆盖本次变更。

实现、工程验证、用户验收、提交与发布保持分开。首版用户验收结论不自动扩展设备和故障矩阵的工程验证状态。原始执行记录保存于忽略目录，本说明维护复现方法与支持边界。
