# 桌面启动与托盘验证

## 前置条件

使用 [依赖清单](../../../../packaging/dependencies.json) 中的 Go、Windows x64 和当前用户的交互桌面。PowerShell 脚本最低为 Windows PowerShell 5.1，完整候选包构建使用 PowerShell 7。安装测试在独立的程序和数据目录执行，跳过 Tailscale 与防火墙配置；真实模型与两台 Windows 验收独立进行。下列输出目录由命令生成，使用前选择新的隔离位置。

## 验证范围

- [启动器测试](../../../../internal/launch/run_windows_test.go) 和同目录安装/状态测试覆盖安装记录与路径验证、loopback 身份检查、重定向拒绝、并发只启动一个节点、已有节点复用、早期进程超时保留、服务锁竞争、无控制台子进程和日志追加。
- [启动锁互通测试](../../../../internal/launch/mutex_windows_test.go) 由真实安装器函数在独立 PowerShell 进程持锁，验证 Go 启动器等待、释放后恢复；覆盖 ASCII、中文空格、U+0130、非 BMP 字符与 junction 目录别名。分别执行 Windows PowerShell 5.1 和可用的 PowerShell 7；宿主缺失明确记录 skipped，生成物只写临时目录。
- [应用生命周期测试](../../../../internal/app/lifecycle_test.go) 覆盖退出确认、停止后的接收屏障、等待已进入的请求、模型验证与任务收尾。现有应用双节点测试覆盖 A 退出后 B 已接收任务继续，使用 FakeExecutor。
- [托盘测试](../../../../internal/desktop/tray_windows_test.go) 覆盖图标帧、Win64 结构、消息循环及托盘关闭。可见 Shell 注册需要显式开启；TaskbarCreated、DPI 和会话结束使用合成消息，真实 Explorer 重启和注销仍由人工验收。
- [安装快捷方式测试](../../../../scripts/test-installer-desktop.ps1) 覆盖 11 组 Auto/Create/Skip/Keep、归属与稳定目标、升级及手动删除、Unicode/重定向路径、旧包兼容和激活锁边界；桌面与 Startup 映射到隔离目录，不改真实系统入口。
- [原生启动回归](../../../../scripts/test-desktop-launch.ps1) 使用真实基础节点进程，覆盖四个并发启动器、CLI status、start.ps1 复用、日志追加与 token 不泄漏、Web 停止释放资源、重启身份配置保留、端口占用失败与恢复。测试不打开浏览器，不使用真实模型；托盘由节点实际创建，脚本不判断其视觉效果。
- [图标资源测试](../../../../scripts/test-icon-resources.ps1) 检查透明 PNG、多尺寸 ICO 和可执行文件图标嵌入后读取，输出尺寸预览；分别在 PowerShell 5.1 和 7 执行。

## 工程复现

在仓库根目录运行，Go 与 Node 使用清单版本：

```powershell
go test ./...
go vet ./...
node --test tests/plugin-contract.test.mjs tests/web-state.test.mjs
powershell.exe -NoProfile -File .\scripts\test-installer-desktop.ps1 -OutputDirectory .\out\desktop-installer
powershell.exe -NoProfile -File .\scripts\test-icon-resources.ps1 -Go go -OutputDirectory .\out\desktop-icons-ps51
pwsh -NoProfile -File .\scripts\test-icon-resources.ps1 -Go go -OutputDirectory .\out\desktop-icons-ps7
```

可见托盘用例默认跳过。在当前用户交互桌面显式运行：

```powershell
$env:ASTERHAND_DESKTOP_TEST = '1'
try { go test ./internal/desktop -count=1 } finally { Remove-Item Env:\ASTERHAND_DESKTOP_TEST }
```

用 [构建入口](../../../../scripts/README.md) 生成候选后，指定其中的节点和原生启动器：

```powershell
powershell.exe -NoProfile -File .\scripts\test-desktop-launch.ps1 -NodeExecutable <candidate-asterhand.exe> -LauncherExecutable <candidate-AsterhandLauncher.exe> -OutputDirectory .\out\desktop-launch
pwsh -NoProfile -File .\scripts\test-install.ps1 -PackageDirectory .\out\candidate -OutputDirectory .\out\desktop-package-install
powershell.exe -NoProfile -File .\scripts\test-install-paths.ps1 -Installer .\install.ps1 -PackageDirectory .\out\candidate -OutputDirectory .\out\desktop-package-paths
```

升级检查另向 test-install.ps1 提供 `-PreviousPackageDirectory <previous-package-directory>`；未提供时只记录新装和重复安装结果。完整包验证还要核对固定启动器、脚本及图标均随包交付。测试报告按实际执行记录 passed、failed、skipped、blocked，未运行完整包或前置平台不可用时保持未验证。

## 人工桌面验收

1. 首次交互安装分别选择创建和跳过桌面图标；验证非交互 Auto 跳过、Create 显式创建。确认安装结束后无节点进程；双击图标后 Web 打开、托盘出现，无控制台窗口。
2. 连续双击，并与 start.ps1 同时启动；确认同一数据目录仅一个节点和一个托盘。关闭浏览器后节点仍运行；托盘双击和菜单能重新打开 Web，浏览器会话过期后也能恢复登录。
3. 从本机 Web 退出，再从托盘退出，分别确认监听端口释放、子执行进程退出、托盘消失；在途任务持久化为待核对，已有文件保留。取消忙时退出确认应保持原任务运行；模型验证中的退出同样需要确认并完成取消。
4. A 将真实任务交给 B 后退出 A，确认 B 继续；退出 B 后确认 A 周期探测显示暂不可达，B 重启按既有期限和对账规则处理。Tailscale、节点身份和配对保留。
5. 不创建桌面图标时从脚本启动，托盘行为相同；升级后快捷方式仍指向固定启动器，原节点身份与历史保持。手动删除桌面图标后升级不重建；显式 Create 可以恢复。同名其他安装的快捷方式保留。运行中安装应拒绝激活，正常退出后再安装成功。
6. 分别使用中文含空格程序/数据目录和重定向桌面；选择登录后启动，确认与桌面图标选择独立，登录不弹浏览器，移除 Startup 入口后不再自启。旧包没有原生启动器时验证兼容脚本可用，Create 给出明确错误。
7. 制造端口占用和启动未就绪，检查错误提示与 launcher.log/error.log/startup.log，确认未强杀原端口进程、未重复启动、日志继续追加且无登录 token。恢复条件后重试同一启动入口。
8. 测试 Windows 深浅主题、100%/125%/150%/200% 缩放、Explorer 重启恢复、锁屏继续和注销/关机收尾。新图标可能位于 Windows 隐藏图标区域；系统强制结束会话仍需重启后按持久记录对账。

原始日志存放在执行者选择的忽略输出目录，发布验证结果附到对应版本发布物；长期文档只保留方法与边界。FakeExecutor、单机双实例和合成 Windows 消息不代替两台 Windows + OpenClaw + DeepSeek 验收。工程检查通过、真实桌面通过和用户接受分别记录。
