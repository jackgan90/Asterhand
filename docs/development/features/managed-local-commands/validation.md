# 托管本地命令验证

## 环境与边界

执行平台为 Windows amd64，使用[依赖清单](../../../../packaging/dependencies.json)锁定的 Go 工具链、Node.js 与 WebView2 Runtime。原生测试要求普通交互桌面；受限进程令牌、不可用的本机网络或无桌面环境应记录 blocked/skipped，不能作为产品测试通过。

本功能无需模型或远程节点。macOS/Linux 只检查接口编译隔离，尚不支持托管启动或原生日志窗口。真实睡眠/休眠、DPI/多屏移动、全新安装和升级的数据保留需要专项验收；交叉编译、替身与单机原生测试不替代这些环境。

## 自动测试

- [运行控制器测试](../../../../internal/localcommands/run_manager_test.go)：并发准入、双流原始字节完整性、结果持久化、编辑保护、超时、中断、旧执行请求、替换、配置清理和恢复时的执行范围确认。
- [故障与预算回归](../../../../internal/localcommands/managed_regression_test.go)：大量小帧、极限配置快照的序列化预算、清理标记失败后切回托管及重启、活跃耗时。
- [编码检查点测试](../../../../internal/localcommands/log_encoding_test.go)：通过 Node 的真实文本解码器校验四种编码逐字节跨页、双流交错及 BOM 状态；UTF-8/UTF-16 还覆盖随机无效字节。运行该 Go 测试时 Node 必须在 PATH 中，缺失会明确 skip。
- [宿主并发测试](../../../../internal/quickinput/log_host_test.go)：读取 worker 饱和后返回 busy，窗口保持可用且停止请求仍可处理。
- [Windows 托管进程测试](../../../../internal/localcommands/managed_windows_test.go)：真实脚本双流重定向、根进程退出后后代仍占用、按执行对象强制停止及退出确认。
- [原有 Windows 启动测试](../../../../internal/localcommands/launch_windows_test.go)与[应用命令测试](../../../../internal/app/commands_api_windows_test.go)：非托管参数、工作目录、普通用户身份与退出后继续运行回归。
- [原生日志窗口测试](../../../../internal/quickinput/log_windows_test.go)：独立宿主关闭/重建不结束受管脚本；真实 WebView2 的双语言双主题、纯文本显示和工具栏；[原生编码验证](../../../../internal/quickinput/log_codec_windows_test.go)还校验真实存储页的四种编码上下文，覆盖 GBK 四字节序列和不完整/无效字节边界。它不代替用户目视、DPI 和多屏验收。
- [前端状态与解码测试](../../../../tests/managed-commands.test.mjs)、[注册表单测试](../../../../tests/local-command-management.test.mjs)：跨块双流解码、超时换算、停止可用状态与已有浏览脚本交互。
- [页面交互回归](../../../../tests/managed-command-ui.test.mjs)：翻页与刷新定时器交错后持续轮询，键盘选择在列表缓存命中时也更新执行按钮。
- [嵌入模块闭包](../../../../internal/quickinput/surface_assets_windows_test.go)：两个 HTML 入口及唯一声明的共享状态模块别名均可读取；其他来源、路径、查询和写请求继续拒绝。

从仓库根目录运行（Go 可使用已校验编译器的绝对路径）：

```powershell
go test ./... -count=1
go vet ./...
node --test tests/*.test.mjs
node scripts/sync-i18n.mjs --check
git diff --check
```

原生日志宿主测试使用独立构建目录，避免覆盖已安装节点。以下命令中的 `out/managed-command-validation` 为可重新生成的测试输出目录：

```powershell
pwsh -NoProfile -File scripts/build-input-host.ps1 -Go go -OutputDirectory out/managed-command-validation/input-host
$env:ASTERHAND_DESKTOP_TEST = '1'
$env:ASTERHAND_INPUT_HOST_TEST_DIRECTORY = (Resolve-Path out/managed-command-validation/input-host).Path
go test ./internal/quickinput -run TestManagedLog -count=1 -v -timeout=120s
Remove-Item Env:ASTERHAND_DESKTOP_TEST, Env:ASTERHAND_INPUT_HOST_TEST_DIRECTORY
```

构建的同一个宿主可执行物通过独立 `--log-host` 进程承载日志窗口，沿用 `input-host.json` 校验。日志脚本、快捷输入宿主与日志宿主各有独立 Job。无新增外部依赖或许可项。

## 用户验收

1. 在“本地命令”注册托管脚本，设置或清空超时；核对类型旁的说明、秒/分钟/小时换算和非托管切换的删除提示。
2. 运行同时输出 stdout/stderr、包含中文和无换行片段的脚本；从快捷列表与管理列表打开日志窗口。核对任务栏、窗口拖动、自由调整大小、最大化/最小化、重复打开激活与不同命令多窗口。
3. 运行中重复启动、修改或删除应被拒绝。停止后显示执行中断；超时显示执行出错并注明超时。两种情况都应保留已捕获输出，且可以再次执行。
4. 输出足够多的日志，检查最早/上一页/下一页/回到最新，确认窗口内存不随全量日志增长。向上滚动暂停跟随，选择编码后重新解码当前片段。
5. 关闭/重开日志窗口、重建日志宿主、退出/重启助手，核对最近结果及日志；退出助手停止托管脚本，非托管脚本继续。新一次执行替换旧记录。
6. 保存为非托管后，旧窗口内容清空、最近结果与日志删除；取消未保存编辑应保留。删除命令仅删除注册和执行记录，不删除脚本文件。

脚本应以退出码表达成功/失败，并等待业务子进程。stderr 内容本身不判失败；强制停止不撤销已发生的文件修改，尚在脚本自身缓冲中的内容不保证捕获。Windows PowerShell 5.1 输出编码及 CLIXML 行为由脚本决定，日志窗口保留原始输出并提供显式编码选择。
