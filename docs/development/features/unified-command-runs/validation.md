# 统一命令验证

## 自动检查

前置：仓库规定的 Go、Node 和 PowerShell 7；Windows 真实进程检查不调用模型。执行 `go test ./...`、`go vet ./...`；使用 `Get-ChildItem tests -Filter '*.test.mjs'` 枚举全部 Node 测试并传入 `node --test`；运行 `pwsh -NoProfile -File scripts/check-i18n.ps1`。文档本地链接检查覆盖根文档、docs、scripts、tests、packaging 及模板，新增目标必须包含在交付中。

测试重点：同命令接纳竞争与别名；图内失败只影响本实例；最后接纳顺序与乱序结束；下一轮发布失败不删旧日志；崩溃点在记录、索引、清理及终态之间；恢复所有未完成节点；停止全部遇到新启动、重复请求和旧轮请求；运行/保留/输出容量；多实例有界状态列表与最大节点名称；跨实例相同节点 ID 的分页、解码、迟到响应；选择与主题/语言保持；旧配置拒绝及脚本文件不变。

主要源码入口：

- [多实例执行回归](../../../../internal/localcommands/concurrent_runs_test.go)：乱序完成、停止集合、发布/终态写盘失败、恢复阻塞、日志容量与摘要预算。
- [异常恢复回归](../../../../internal/localcommands/concurrent_recovery_test.go)：首条/中间记录损坏、索引损坏、日志缺失时继续核对其他有效身份，保留错误数据；逐节点记录丢弃输出的事实。
- [Windows 双实例回归](../../../../internal/localcommands/concurrent_windows_test.go)：真实平台进程独立输出、单实例停止、其他实例正常完成、Job 退出、重开保留日志与新轮清理。
- [注册回归](../../../../internal/localcommands/registry_test.go)：拒绝旧 schema、独立配置与图数据深拷贝。
- [前端多实例回归](../../../../tests/concurrent-command-logs.test.mjs)：同名节点的实例身份、固定选择、迟到读取、编码和单屏停止。
- [日志读取回归](../../../../tests/log-reader.test.mjs)：分页、完成快照、后台读取与用户导航竞争。

## Windows 与桌面

沿用 [原托管命令验证](../managed-local-commands/validation.md)的真实 Job/普通用户令牌、输出编码、超时、停止、子进程与窗口检查，fixture 使用新图配置。新增至少两个同命令实例：分别读日志、停止一个、另一个正常完成，再次启动检查旧轮失效；窗口关闭重开后仍能读取当前轮全部记录。

设置 `ASTERHAND_DESKTOP_TEST=1`，运行 `go test ./internal/quickinput -run '^TestManagedLogNativeSurfaceThemesAndText$' -count=1 -v`，验证真实 WebView2 的双主题双语言、跨实例三分屏、按实例停止和九组存储分页解码。设置 `ASTERHAND_CHAT_GATE=1`，运行 `go test ./internal/quickinput -run '^TestNativeLocalCommandsWorkflow$' -count=1 -v`，验证快捷列表重复执行、错误提示、缺失脚本和隐藏恢复。截图可选设置 `ASTERHAND_COMPOSER_CAPTURE` 为本次验证产物目录。测试需可交互 Windows 桌面及可用的普通用户令牌。

实体使用验证：默认三节点图保存和执行、同命令多次触发、状态计数、跨实例/节点固定多选和自动分屏、单屏翻页不跳动、停止全部、退出后重开。安装后人工验收与工程自动化分开记录；未实际运行的桌面、睡眠或多屏条件不得记为通过。

## 旧测试数据处理

当前格式不读取或转换旧配置。开发环境更新前停止旧节点及命令，确认目标数据目录，删除该目录的旧 `local-commands.json` 和 `command-runs`，保留脚本文件及所有其他节点数据。不是运行时通用迁移或自动恢复功能；配置格式损坏时仍明确报错并保留原文件。
