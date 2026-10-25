# 测试入口

使用 Go 1.27.3 运行 `go test ./...` 和 `go vet ./...`。测试源在 internal、cmd、packaging 各包的 *_test.go；临时目录独立，普通测试无网络模型费用。

覆盖 SQLite 事务和幂等、文件回读和链接边界、交互去重、恢复待核对、真实 HTTP/TLS 双实例、A App 停止后 B 继续、配对重放与撤销、Web 来源保护、Windows Job Object 子进程清理。

公共流程测试覆盖公共运行时选项迁移/快照隔离、目录别名保护、异名包目标分派、描述完整性和 CLI 拒绝时不写配置。Node 插件契约与 PowerShell 通用组件准备守卫由 verify-dependencies.ps1 编排。

可靠性回归覆盖：事件边界与受限收尾、文件证据原子回滚、旧满日志节点重启、取消与问答交错、重复控制请求、持久化失败停止调度、显式路径重启保留、旧自动路径升级和执行配置验证摘要。隔离数据库/进程测试不调用真实模型。

双节点交互测试覆盖 peer 元数据身份验证、改名保留信任、单向地址修复、监听配置及就绪保持回归。`node --test tests/plugin-contract.test.mjs tests/web-state.test.mjs` 覆盖路由转执行的统一排序、去重和发送按钮状态，并由完整验证入口执行。

桌面生命周期测试覆盖启动记录和目录验证、loopback 身份检查、并发启动只创建一个节点、早期子进程超时保留、无控制台子进程与日志追加；应用测试覆盖忙时退出确认、停止后的接收屏障、模型验证取消、持久化和在途任务待核对。测试入口在 [启动器测试](../internal/launch/run_windows_test.go)、[托盘测试](../internal/desktop/tray_windows_test.go) 和 [应用生命周期测试](../internal/app/lifecycle_test.go)。

默认 Go 测试跳过需要交互桌面的可见托盘用例；在 Windows 当前用户桌面设置 `ASTERHAND_DESKTOP_TEST=1` 后运行 `go test ./internal/desktop -count=1`，验证真实 Shell 图标注册和合成 TaskbarCreated、DPI、会话结束消息。合成消息不代表已经实测 Explorer 重启、不同显示缩放或注销。

Windows PowerShell 5.1 运行 `test-installer-desktop.ps1` 验证 11 组隔离快捷方式/激活边界，`test-desktop-launch.ps1` 验证真实基础节点进程的启动、复用和退出；`test-icon-resources.ps1` 同时支持 PowerShell 5.1/7。命令和输出约定见 [脚本指南](../scripts/README.md)，完整范围和人工桌面步骤见 [桌面验证说明](../docs/development/features/windows-desktop-lifecycle/validation.md)。

单机多实例包含 FakeExecutor，不能代替真实双 Windows。方法见 [双节点交互验证](../docs/development/features/two-node-usability/validation.md) 与 [任务可靠性验证](../docs/development/features/task-reliability/validation.md)，用户步骤见 [操作指南](../docs/installation/quickstart.md)。

发布测试见 [发布与安装验证](../docs/development/features/release-pipeline/validation.md)：`test-release.ps1` 覆盖草稿失败恢复和附件不可覆盖；`test-release-download.ps1` 通过公开安装脚本验证匿名下载、无开发工具安装、配置分离及重复安装。Tailscale 首次安装在独立 Windows 环境执行，不修改开发者现有网络配置。

固定入口的默认 Latest、显式版本/标签、本地清单推导和错误响应由 `test-installer-release-selection.ps1` 在 Windows PowerShell 5.1 验证。GitHub API 边界使用可控响应，真实下载回归另行运行；复现方法见 [固定安装入口计划](../docs/development/features/stable-installer-entry/plan.md)。

本地一键安装与在线覆盖使用 [test-install-lifecycle.ps1](../scripts/test-install-lifecycle.ps1)（PowerShell 5.1）验证完整安装器、隔离目录和真实基础节点，使用 [test-local-install-entry.ps1](../scripts/test-local-install-entry.ps1)（PowerShell 7）验证开发入口与 README 下载引导的边界及失败保护。前置条件、首次启动替代边界、浏览器抑制和复现命令见 [脚本指南](../scripts/README.md#本地一键安装)；完整助手与干净双机安装仍按人工验收执行。

[test-installer-bootstrap.ps1](../scripts/test-installer-bootstrap.ps1) 可独立在 Windows PowerShell 5.1 和 PowerShell 7 运行，验证一行在线安装入口及版本/标签参数，网络和安装内容使用受控替代，不执行真实安装。
