# 批量删除验证

## 验收状态

本地命令批量删除及管理界面调整已通过本机用户验收，包含按需显示多选控件、删除成功不留提示、普通操作按钮与管理状态高亮，以及搜索框旁的刷新入口。工程检查已覆盖前端行为、定向 Go 回归、国际化、构建和真实 Edge 双语双主题；全量 Go 回归及公共 CLI/MCP 跨进程专项不因本机界面验收而标记通过。

## 前提与自动回归

使用[项目工具链](../../../../packaging/dependencies.json)，在仓库根目录运行。测试只使用隔离数据，不连接已安装节点、不删除用户命令、不调用模型。

```powershell
node --test tests/command-selection.test.mjs tests/local-command-management.test.mjs tests/automation-coverage.test.mjs
go test ./internal/app -run 'TestCommandBatchDelete|TestLocalCommandsAPI|TestBatchKeeps|TestBatchPreflight|TestAutomationCoverage|TestWebModules' -count=1
go test ./internal/localcommands -run 'TestRegistryIndependent|TestRunHistory|TestConcurrentAdmission' -count=1
pwsh -NoProfile -File scripts/check-i18n.ps1
go vet ./...
go build -o ./out/batch-delete-validation/asterhand.exe ./cmd/asterhand
```

- [选择行为测试](../../../../tests/command-selection.test.mjs)：默认隐藏、管理/完成切换与清空勾选、提交中禁止退出、退出后保留未知请求身份、任意选择、半选/全选/取消、搜索外选择、确认取消、分块、部分失败、传输中断、固定身份重试、未提交项、语言切换、后台结果节点保留与会话结束。
- [管理页测试](../../../../tests/local-command-management.test.mjs)：通过真实控制器确认管理模式切换及删除其他命令不丢编辑草稿与撤销历史；删除当前脏命令确认后清空表单。
- [HTTP 测试](../../../../internal/app/commands_batch_delete_test.go)：真实注册存储、成功与修订冲突混合、重复收据、脚本保留、登录/Origin/CSRF、数量/重复项/未知字段及公共操作权限等价。
- [运行占用测试](../../../../internal/app/commands_batch_delete_windows_test.go)：注入受控执行器保持活跃实例，验证 Web 与公共操作都拒绝删除，空闲项仍可删除；它验证运行锁规则，不作为真实脚本执行的证据。

完整回归使用 `go test ./...` 和 `node --test` 加全部 `tests/*.test.mjs` 文件列表。受限环境的本机网络、命名管道、进程完整性级别与目录链接失败，应保留原失败并在具备权限的 Windows 重新运行，不计为 passed。

## 浏览器与人工验收

[浏览器验证入口](../../../../tests/command-selection-browser.mjs)使用真实 HTML/CSS/模块和内存 API。设置 `ASTERHAND_PLAYWRIGHT_MODULE` 为已有锁定运行依赖中 `playwright-core/index.mjs`，`ASTERHAND_BROWSER_EXECUTABLE` 为本机 Edge，`ASTERHAND_COMMAND_SELECTION_OUTPUT` 为本次独立输出目录，然后运行 `node tests/command-selection-browser.mjs`。该入口检查双主题 × 中英界面、默认隐藏及恢复完整列表宽度、新建与管理的普通描边及管理态高亮、刷新图标位置与双语名称、刷新中的重复点击/语言切换及失败后恢复、管理按钮与新建同行、完成后清空勾选、关闭重开、35 项跨筛选全选、取消、32+3 分块、部分失败和编辑对象清空；浏览器启动受阻时记 blocked。

在正常安装节点中另行验收：

1. 建立几个用于验收的命令，确认新建和管理都为普通描边，刷新图标位于搜索框右侧，默认不显示复选框和批量工具栏；点击“新建”同一行的“管理”，确认按钮变为高亮的“完成”并显示批量控件，退出后恢复普通描边。勾选任意项，切换搜索词，核对已选数量；编辑对象不随勾选改变。
2. 全选全部命令、取消全选，核对半选状态与零选择时删除按钮禁用；点击“完成”后控件隐藏，再进入时没有勾选，编辑草稿和图历史保留。关闭窗口重开也应恢复普通模式。
3. 删除确认选择取消，核对目录保持；再次确认删除后核对成功项消失、脚本文件仍存在，页面不保留成功提示；全部成功时结果区隐藏。
4. 包含运行中的命令时，核对该项失败并保留，空闲项成功；多窗口改动修订后删除旧快照应显示冲突。
5. 删除其他命令期间保留当前未保存的文本与图历史；删除当前命令时确认放弃草稿。

真实浏览器、安装后的用户验收和公共 CLI/MCP 的跨进程验证分别记录，不用 DOM fixture 或应用内 Dispatch 的通过替代。
