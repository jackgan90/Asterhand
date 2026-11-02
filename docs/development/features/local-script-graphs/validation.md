# 本地脚本图验证

## 验证状态与边界

工程验证、独立审计修复复审和本机用户验收均已通过。DPI、多屏、睡眠恢复和长期使用需在相应环境中单独验收；Linux、macOS 的交叉编译仅证明构建，平台实测要求见[支持矩阵](../../../testing/platforms.md)。

## 前提

使用仓库依赖清单规定的 Go 与 Node.js。运行 `go test ./internal/localcommands ./internal/app ./internal/quickinput/...`、`go vet ./...`、`node --test tests/*.test.mjs`、`pwsh -NoProfile -File scripts/check-i18n.ps1` 和 `node scripts/verify-frontend-assets.mjs`。真实进程测试在 Windows 上运行，其他平台不作为 Windows 执行验证。

依赖与失败调度入口为 [graph_test.go](../../../../internal/localcommands/graph_test.go)，Windows 实际脚本与子进程清理入口为 [graph_windows_test.go](../../../../internal/localcommands/graph_windows_test.go)。[边界回归](../../../../internal/localcommands/graph_regression_test.go)覆盖最大合法图、转义后膨胀的节点名称、每节点双流分块上下文、原始日志完整分页和真实 IPC 编码，以及进程创建前取消与校验原因切换语言；还会实际创建长脚本路径，验证大于 2 MiB 的合法运行记录可以重开，超限保存保留原记录。

前端 [图行为测试](../../../../tests/script-graphs.test.mjs)覆盖图校验、编辑草稿与锁定、节点独立解码和日志分栏；[命令表单测试](../../../../tests/local-command-management.test.mjs)覆盖脚本选择器及图错误完整重译。可用 `go test ./internal/localcommands -run 'TestGraph|TestWindowsGraph' -count=1 -v` 和 `node --test tests/script-graphs.test.mjs tests/local-command-management.test.mjs` 复现专项。

## 核对场景

在 Web 新建脚本图：开始 → 准备 → 两个并行步骤 → 汇合 → 结束。各脚本输出自身标识并使用显式退出码。保存后从快捷输入运行，检查汇合仅在两前置成功后执行。图并发改为 1 后检查串行；一个步骤非零退出时检查另一步停止、汇合未执行、根状态失败及节点原因可读。

分别覆盖节点超时、整图超时、用户停止、运行中编辑拒绝、关闭/重开日志窗口、中文分块输出、节点筛选和双栏、重新执行后的代次切换。异常退出后核对未结束记录，不应自动重跑；业务副作用由用户检查。

图编辑器和日志窗口按中英双语、两种主题检查，切换语言保留草稿、焦点和日志阅读位置。桌面回归还需通过实际操作核对图编辑、快捷输入启动和独立日志窗口，自动化进程/DOM 验证仅覆盖各自测试场景。
