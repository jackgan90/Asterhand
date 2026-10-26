# Web 与快捷输入维护性验证

本说明覆盖同源客户端、页面生命周期、任务/节点/设置控制器、DOM 视图与 WebView2 桥的等价重构。支持范围仍以[平台矩阵](../../../testing/platforms.md)为准；不调用真实模型的 fixture 不替代[产品验收](../../../testing/acceptance.md)。

## 前提与基础回归

使用[锁定工具链](../../../../packaging/dependencies.json)。Windows 测试需要当前用户 ACL、目录链接、进程和交互桌面权限；权限受限时记录 blocked，保持权限测试原样，在可用环境重新验证。

在仓库根目录执行：

```powershell
$uiTests = Get-ChildItem -LiteralPath tests -Filter '*.test.mjs' -File
node --test $uiTests.FullName
go test ./...
go vet ./...
pwsh -NoProfile -File scripts/test-quick-input.ps1 -Go go -OutputDirectory ./out/web-ui-validation/desktop -Desktop
```

- [客户端测试](../../../../tests/api-client.test.mjs)：登录/会话、实例 CSRF 隔离、认证乱序时的 Cookie/CSRF 配对、失败认证释放队列、HTTP 状态、网络失败和写入不自动重发。
- [生命周期测试](../../../../tests/app-lifecycle.test.mjs)：重复启动只保留一个流和节点计时器，停止后的回调、读取与错误失效。
- [操作序列测试](../../../../tests/web-interactions.test.mjs)：未确认提交和回答复用 ID、编辑保护、确认取消、设置保存、页面离开/恢复及写入中断边界；[状态测试](../../../../tests/web-state.test.mjs)继续验证排序、去重和 busy。
- [桌面设置测试](../../../../tests/desktop-settings.test.mjs)：录制租约开始/取消/轮询的异步交错，以及保存在途时恢复、operation_id 对账/复用与旧完成不释放新事务互斥。
- [桥测试](../../../../tests/quick-input-bridge.test.mjs)：ready/init、草稿修订、IME 快照顺序、主题、字节上限、隐藏及订阅释放；[文本测试](../../../../tests/quick-input.test.mjs)保留 Unicode 与选区边界。
- [主 Web 资源测试](../../../../internal/app/web_assets_test.go)从 HTML 入口追踪静态 import 闭包，对每个内嵌模块检查 GET/HEAD、精确内容、JavaScript MIME 和 nosniff；[浮层资源测试](../../../../internal/quickinput/surface_assets_windows_test.go)检查实际闭包全部通过原生 GET 白名单，并拒绝未知路径、外源和写入。

模拟 DOM 不计为真实浏览器证据。Go 默认跳过需要交互桌面的测试；显式 Desktop 后仍区分视觉输出、成本循环等另需参数的 skipped 项。[原有快捷输入验证](../global-hotkey-quick-input/validation.md)中的隐藏 CPU 预算和物理 IME、多屏/DPI 等未验证边界保持原结论。

## 独立构建与浏览器冒烟

可按[脚本指南](../../../../scripts/README.md)生成完整包，或直接构建节点与内嵌同一资源的测试程序。后者提供真实 HTTP、Cookie/CSRF、SQLite、TLS 配对和 FakeExecutor，免付费完成交互回归：

```powershell
New-Item -ItemType Directory -Path ./out/web-ui-validation/build -Force | Out-Null
go build -o ./out/web-ui-validation/build/asterhand.exe ./cmd/asterhand
go test -c -o ./out/web-ui-validation/build/web-browser-fixture.test.exe ./internal/app
$env:ASTERHAND_WEB_FIXTURE_DIR = [IO.Path]::GetFullPath('./out/web-ui-validation/browser')
& ./out/web-ui-validation/build/web-browser-fixture.test.exe '-test.run=^TestWebBrowserFixture$' '-test.timeout=30m'
```

测试程序启动两个临时节点，`session.json` 包含本次隔离节点的登录 URL、B 配对码与授权目录。该文件只用于本机测试，勿写入报告或提交。程序和所有前端资源均已内嵌，可从独立目录运行，无需源码、npm 或开发服务器。另一个终端可在 fixture 目录的 `command` 文件写入 `offline` / `online`，停止/恢复 A 的原端口；节点重新创建会丢弃内存登录会话，需重新登录。此入口验证服务停止与恢复，不将其记为保持登录会话的短暂网络故障。写入 `stop` 正常清理两个节点及临时数据。示例目录均由当前运行生成，重复验证使用新的输出目录。

浏览器操作与预期：

1. 用生成链接登录，确认地址栏已去除登录片段、节点名称与版本出现；独立打开入口不持 Cookie 时仍要求登录。检查模块加载与安全头，页面保留原布局和文案。
2. 本机提交后等待执行阶段问题，输入答案但暂不发送；经过多个刷新周期后输入、焦点和主输入草稿保留。发送后实际文件字节与卡片产物一致；详情文本和事件保持按文本显示。
3. 配对 B，选择远端节点及项目并提交；验证任务排序、执行节点和文件结果。生成、复制配对码；撤销配对分别选择取消与确认，检查实际节点列表。
4. 打开设置，保存无关名称/网络字段，检查模型就绪保持与重启提示；关闭、重新打开。快捷输入保存/默认/检查/录制沿用专用事务，不启动付费 probe。
5. 用 `offline` 停止隔离 A，观察连接中断；`online` 后重新登录，观察持久任务恢复。保持服务运行时的短暂传输中断另行验证 EventSource 原生重连，不叠加客户端重连循环。页面离开/返回后只恢复一次订阅，主输入草稿保留，旧读取不覆盖当前页面。
6. 停止确认选择取消，服务继续；显式确认后才退出。关闭页面或收起浮层时已接收任务继续，此路径另由现有双实例 Go 测试覆盖。

真实 WebView2 的[行为测试](../../../../internal/quickinput/surface_behavior_windows_test.go)验证主题/选区、IME 快照、隐藏恢复、错误代次、外源与禁网；[初始化测试](../../../../internal/quickinput/surface_windows_test.go)确保新桥模块在独立虚拟 HTTPS 来源中实际加载成功。它们与浏览器冒烟分别记录。

原始命令、工具版本、结果、截图和限制保存到当前运行的忽略输出目录；执行状态用 passed / failed / skipped / blocked，真实双 Windows + OpenClaw + DeepSeek、用户验收、提交和发布各自记录。
