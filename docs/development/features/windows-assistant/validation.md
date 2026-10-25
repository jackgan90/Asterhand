# Windows 跨节点助手验证方法

## 前提与入口

使用 [依赖清单](../../../../packaging/dependencies.json) 中的 Go、Node、OpenClaw 和模型插件版本。普通 Go/JS 测试不需要真实模型凭据；Windows 权限与进程测试需当前用户可创建私有目录和子进程。

基础命令：

~~~powershell
go test -count=1 -timeout=120s ./cmd/... ./internal/... ./packaging/... ./web/...
go vet ./cmd/... ./internal/... ./packaging/... ./web/...
node --test tests/plugin-contract.test.mjs tests/web-state.test.mjs
~~~

从仓库根运行；明确产品源码目录可避免遍历构建产物。

## 行为覆盖

| 行为 | 仓库测试入口 |
|---|---|
| 持久化、幂等与原子事务 | [存储测试](../../../../internal/store/store_test.go)、[操作证据](../../../../internal/store/operation_test.go) |
| 文件创建、回读、权限边界 | [文件工具测试](../../../../internal/tasks/tools_test.go) |
| 远端执行、A 关闭后补读、问答 | [双节点集成](../../../../internal/app/integration_test.go)、[交互测试](../../../../internal/app/interaction_test.go) |
| 配对、固定证书、撤销 | [节点信任测试](../../../../internal/peer/peer_test.go) |
| Windows 进程树清理 | [进程测试](../../../../internal/executor/process_windows_test.go) |
| 页面排序与发送状态 | [Web 状态测试](../../../../tests/web-state.test.mjs) |

真实模型验证由 [模型检查脚本](../../../../scripts/verify-live.ps1) 执行 probe、文件、问答和取消，调用计费模型并创建唯一测试文件。安装验证由 [安装脚本测试](../../../../scripts/test-install.ps1) 执行，命令和参数见 [依赖维护](../../../../packaging/upgrade.md)。

物理双节点操作使用 [人工验收规程](../../../testing/acceptance.md)，以实际文件和任务状态判断成功。FakeExecutor、交叉编译、单机多实例不能替代真实设备测试，未验证平台见 [支持范围](../../../testing/platforms.md)。
