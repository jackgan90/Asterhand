# 工作区工具并发验证方案

状态：实现、工程验证、独立审计修复复审与本机真实模型用户验收已完成（`WC01—WC21`、10 类故意错误/变异检测、离线 SDK 预检与原生 OpenClaw Gateway 多批次并发集成测试通过）。两台 Windows 跨节点长对话验收仍按独立双机计划确认。

## 环境与三源互证原则

- **核心调度与组合层**：Go 单元测试使用确定性通道屏障、独立预期真值表与固定随机 seed，不联网、不依赖固定 `sleep` 碰运气。
- **Windows 文件与外部竞争层**：在真实 Windows NTFS 隔离目录与受控外部句柄竞争下验证 `FILE_SHARE_READ` 纯读并发、外部写入/删除拦截、相对父句柄抗目录替换及原子预算。
- **原生运行时层**：使用依赖清单锁定的 Node `24.22.1` 与 OpenClaw `2026.10.7`，兼备无 Gateway、不联网的离线 SDK 预检与独立 Gateway + 本地 SSE 多工具批次实测。
- **三源互证**：同时比对（1）`ToolDescriptor` 声明集合、（2）低层文件/索引实际进入与退出事件轨迹、（3）测试侧独立编写的业务预期（禁止调用生产 `prepare` 或 `conflicts` 生成预期）。并行断言要求双方同时到达受保护执行屏障后才放行；互斥断言要求持锁方停在检查点期间竞争方确认为 `queued` 且零低层 I/O 进入。

## 组合与故障验收矩阵（WC01—WC21）

| ID | 场景 | 验证入口与通过条件 |
|---|---|---|
| WC01 | 未知工具、未知资源域/访问模式、参数别名解析、越界路径、白名单与描述/测试登记一致性 | `TestDescriptorRegistryAndValidation`、`TestWorkspacePluginSDKPreflight`；副作用前拒绝，四方清单严格一致 |
| WC02 | `S/S`、`S/X`、`X/X`、`exact/children/subtree`、路径组件边界（`a` vs `ab`）、大小写与不同 `rootID` | `TestResourceClaimTruthTable`、`TestOrderedToolPairMatrix`（全量 `9×9` 有序工具配对）；与独立真值表完全一致 |
| WC03 | 不同文件 `read_file/read_file`、`write_file/write_file`、`read_file/write_file` 及 `progress_card` 并行 | `TestParallelExecutionOverlap`、`TestNativeWorkspaceConcurrency`；双调用同时到达实际 I/O 屏障，结果与收据准确 |
| WC04 | 同文件读写/写写、同批盲覆写冲突检测、多文件补丁部分重叠、补丁移动源/目标交叉 | `TestConflictingBatchOrderAndAtomicity`、`TestSameBatchWriteConflictAndOrderedPipelines`、`TestNativeWorkspaceConcurrency`；一次取得全部资源，无半批持锁死锁，有序管线严格保序，同批后序盲覆写 `write_file` 以 `conflicting_batch_write` 拒绝且不破坏前序结果 |
| WC05 | 长读持锁 + 排队写 + 后续新读 + 不相关目录读写 + 队首取消 | `TestWriterFairnessAndBypass`；后续同文件新读排在写者之后不插队，不相关文件直接绕过执行，队首取消唤醒后继 |
| WC06 | 准备后钩子改写参数（`write_file(a)` 改为 `write_file(b)`）且后序 `read_file(b)` 先请求准入、准备中断、封存后参数篡改、重复登记/领取 | `TestBatchSealOrderingAndTamperRejection`、`TestNativeWorkspaceConcurrency`；整批封存前零准入，`read_file(b)` 等待改写后的前序写，篡改参数拒绝，重复请求幂等 |
| WC07 | 并发创建共同缺失父目录、已有父目录下兄弟文件并发写入 | `TestConcurrentMissingParentCreation`；缺失父目录串行创建且容量只计一次，已有目录下兄弟写入并行 |
| WC08 | `list_dir`（`children S`，目录尾斜杠 `/`）与深层子目录写入并行、同批删除后列目录保序、`search_files`（`subtree S`）与子树外修改并行及分页限制 | `TestDirectoryAndSearchScopeConcurrency`、`tests/workspace-plugin.test.mjs`、`TestNativeWorkspaceConcurrency`；边界外修改无需等待，同批先删后列不残留已删项且子目录带 `/`，分页与扫描预算有界 |
| WC09 | 修改工具内部先读后写、子操作越界访问、纯读句柄尝试写入（读锁升级） | `TestCallScopedSubOperationsAndIdentityIsolation`；授权内读取无自锁，越界路径或未声明写动作触发 `undeclared_effect` |
| WC10 | 错误 `callId`、过期运行代次、缺失身份、非启动期无 ID 请求拦截、跨调用句柄隔离 | `TestCallScopedSubOperationsAndIdentityIsolation`、`TestBootstrapBridgeGateAndContextCancellation`；每次低层访问校验精确身份或显式启动标记，绝不串写前像或结果 |
| WC11 | Windows 同文件 `FILE_SHARE_READ` 并发纯读、外部进程写/删拒绝、新硬链接与父目录替换保护 | `TestWindowsSharedReadAndExternalContention`；双纯读句柄并发定位读取成功，外部写/删被系统句柄锁拒绝 |
| WC12 | 多调用并发逼近工作区字节/条目、版本存储与活动元数据上限及尾部收尾余量 | `TestConcurrentQuotaAndJournalPinning`、`TestActivityQuotaHeadroomAndSequenceCursor`；准入原子预留防超额，未完成删除不提前释放额度，进行中 `End()` 不因未来调用余量误报 `activity_limit` |
| WC13 | 编辑/补丁发布前实际膨胀复核、落盘或发布失败单次归还预留 | `TestConcurrentQuotaAndJournalPinning`、`TestActivityQuotaHeadroomAndSequenceCursor`；超限或初始落盘失败在副作用前拒绝并精确归还单次预留，已完成文件据实记录 |
| WC14 | 并发修改前像保存、版本恢复读取与版本 GC 交错，排队/运行/终态转换的单调事件水位 | `TestConcurrentQuotaAndJournalPinning`、`TestActivityQuotaHeadroomAndSequenceCursor`；活跃/`unknown`/恢复中的前像不被 GC 删除，阶段转换均推进游标不漏读 |
| WC15 | 同批 `write_file("memory/...")` → `memory_search`、并发双 `memory_search`、记忆检索与普通文件写入并行及无身份记忆请求拒绝 | `TestMemoryCancellationUnknownAndControlSlots`、`tests/workspace-plugin.test.mjs`、`TestNativeWorkspaceConcurrency`；在受监督调用内同步刷新索引并命中同批新写入内容，脱离上下文请求以 `missing_call_identity` 拒绝 |
| WC16 | `registered/queued/preparing/running/draining` 各阶段取消、公共只读 `ctx` 取消与原生提前返回后的底层排空 | `TestMemoryCancellationUnknownAndControlSlots`、`TestBootstrapBridgeGateAndContextCancellation`、`TestNativeWorkspaceCancellationRetainsCompletedFiles`；在途 I/O 未退出前绝不释放冲突锁或轮次租约 |
| WC17 | `end` 失败/丢失、单调用日志失败时其他活跃调用排空、进程退出转 `unknown` 与重启核对 | `TestMemoryCancellationUnknownAndControlSlots`、`TestStoppedRuntimeReleasesTurnWhenTerminalPersistenceFails`；关闭新修改准入，排空后释放运行态 `busy` 并保持 `unknown` 待核对 |
| WC18 | 等待队列满（64）、活动名额满（8/4/1）、数据通道打满时的保留控制名额及批次内存回收 | `TestMemoryCancellationUnknownAndControlSlots`、`TestActivityQuotaHeadroomAndSequenceCursor`；慢请求占满数据槽时 `cancel/end/seal` 仍立即通过控制通道完成，完成后批次表清零 |
| WC19 | 公共 CLI/MCP `workspace_files.read/list/search/mutate`、`workspace_versions.restore` 与模型工具并发争用及取消透传 | `TestAutomationWorkspaceSharesVersionsScopeAndReceipts`、`TestWorkspaceReceiptRecoversOnlyDurableDomainEvidence`、`TestBootstrapBridgeGateAndContextCancellation`；公共纯读与不冲突工具并行，冲突时公平等待且支持 `ctx` 取消，`phase/wait_reason` 与 schema 一致 |
| WC20 | 同批工具后发先至完成、`queued` 排队状态投影、图文混排、刷新与跨页读取 | `tests/chat-timeline.test.mjs`、`TestHistoryToolEnrichmentMapsQueuedPhase`、`TestNativeWorkspaceConcurrency`；聊天消息保持模型原始工具顺序，排队与完成状态按 ID 原位更新 |
| WC21 | 工具未启用、人工开关切换、多会话轮次串行、既有一次性任务与本地快捷命令回归 | 全量回归套件；轮次级隔离、权限修订与既有业务保持不变 |

## 必选故意错误与变异检测（10/10）

`TestMutationDetectionSuite` 在隔离实例中逐项注入以下 10 类缺陷，断言对应防线明确捕获并报告具体失败原因（禁止以崩溃或超时充当捕获）：

1. 移除写入工具的 `X` 模式（降为 `S`，`mutation_01`）→ 冲突检测与低层效果守卫捕获缺失写互斥。
2. 将 `S/X` 误判为兼容（`mutation_02`）→ 独立真值表与并发轨迹屏障捕获非法读写重叠。
3. 漏声明缺失祖先父目录锁（`mutation_03`）→ 并发父目录相交校验检出竞争缺口。
4. 忽略低层访问的 `callId` 身份校验（`mutation_04`）→ 空身份/跨调用身份隔离断言立即检出旁路。
5. 工具返回时未等待在途子 I/O 即释放资源（`mutation_05`）→ 取消排空屏障检出后继冲突调用提前进入。
6. 多调用非原子检查旧配额（`mutation_06`）→ 并发配额压测检出超额放行。
7. 版本清理未固定其他活跃调用的前像引用（`mutation_07`）→ 并发 GC 交错测试检出活跃前像丢失。
8. 忽略批内登记顺序允许后续新读插队（`mutation_08`）→ 写者公平性与批次顺序轨迹断言失败。
9. 退化为全工作区单锁串行执行（`mutation_09`）→ 强制双调用同时到达执行屏障的重叠测试明确失败。
10. 未封存即放行准入或封存后忽略参数指纹漂移（`mutation_10`）→ 批次封存屏障与篡改拒绝测试明确检出。

此外，`TestDeterministicRandomScheduleReplay` 使用固定种子集生成随机多工具批次与完成交错，与独立串行参考模型逐轮比对最终工作区树摘要、容量账本与操作状态；任意失败输出复现 seed。

## 可复现命令

~~~powershell
# 核心调度、9x9 矩阵、Windows 句柄竞争、并发配额、随机重放与 10 类变异检测
go test ./internal/workspace -count=1 -v

# 离线公开 SDK 零冲突/归属预检 + 会话与公共自动化测试
go test ./internal/executor ./internal/conversation ./internal/app ./internal/automation -count=1

# 锁定 Node 24.22.1 + OpenClaw 2026.10.7 原生多工具批次并发与取消集成测试
$env:ASTERHAND_CHAT_RUNTIME = "1"
$env:ASTERHAND_CHAT_NODE = "<prepared-node-distribution>/node.exe"
$env:ASTERHAND_CHAT_ENTRY = "<prepared-runtime>/node_modules/openclaw/openclaw.mjs"
go test ./internal/executor -run "TestWorkspacePluginSDKPreflight|TestNativeWorkspace" -count=1 -v -timeout=8m

# 前端时间线乱序完成保序、插件桥接契约、自动化覆盖与全仓静态检查
node --test tests/workspace-plugin.test.mjs tests/chat-timeline.test.mjs tests/chat-model.test.mjs tests/automation-coverage.test.mjs
go test ./...
go vet ./...
~~~

Windows 下 `go test -race` 依赖本机可用的 C 编译工具链（CGO）；若环境未安装 GCC/Clang，按项目规范记录该专项为 `blocked` 并保留确定性通道屏障与固定 seed 交错证据，不将无 `-race` 的普通测试冒充为 race 通过。
