# 测试入口

使用 Go 1.27.3 运行 `go test ./...` 和 `go vet ./...`。测试源在 internal、cmd、packaging 各包的 *_test.go；临时目录独立，普通测试无网络模型费用。

覆盖 SQLite 事务和幂等、文件回读和链接边界、交互去重、恢复待核对、真实 HTTP/TLS 双实例、A App 停止后 B 继续、配对重放与撤销、Web 来源保护、Windows Job Object 子进程清理。

公共流程测试覆盖公共运行时选项迁移/快照隔离、目录别名保护、异名包目标分派、描述完整性和 CLI 拒绝时不写配置。Node 插件契约与 PowerShell 通用组件准备守卫由 verify-dependencies.ps1 编排。

可靠性回归覆盖：事件边界与受限收尾、文件证据原子回滚、旧满日志节点重启、取消与问答交错、重复控制请求、持久化失败停止调度、显式路径重启保留、旧自动路径升级和执行配置验证摘要。隔离数据库/进程测试不调用真实模型。

双节点交互测试覆盖 peer 元数据身份验证、改名保留信任、单向地址修复、监听配置及就绪保持回归。`node --test tests/plugin-contract.test.mjs tests/web-state.test.mjs` 覆盖路由转执行的统一排序、去重和发送按钮状态，并由完整验证入口执行。

单机多实例包含 FakeExecutor，不能代替真实双 Windows。方法见 [双节点交互验证](../docs/development/features/two-node-usability/validation.md) 与 [任务可靠性验证](../docs/development/features/task-reliability/validation.md)，用户步骤见 [操作指南](../docs/installation/quickstart.md)。

发布测试见 [发布与安装验证](../docs/development/features/release-pipeline/validation.md)：`test-release.ps1` 覆盖草稿失败恢复和附件不可覆盖；`test-release-download.ps1` 通过公开安装脚本验证匿名下载、无开发工具安装、配置分离及重复安装。Tailscale 首次安装在独立 Windows 环境执行，不修改开发者现有网络配置。

固定入口的默认 Latest、显式版本/标签、本地清单推导和错误响应由 `test-installer-release-selection.ps1` 在 Windows PowerShell 5.1 验证。GitHub API 边界使用可控响应，真实下载回归另行运行；复现方法见 [固定安装入口计划](../docs/development/features/stable-installer-entry/plan.md)。
