# 本机持续聊天验证说明

状态：实现、工程验证及日常聊天用户验收已完成；其余专项按下文独立记录。文档链接/一致性审计与产品验证分开。关联[实施计划](plan.md)、[持续会话设计](../../../assistant/conversations.md)和 [Web 设计](../../../web/design.md)。

## 用户验收状态

2026-10-28，用户完成日常聊天验收，确认以下功能均符合预期：

| 场景 | 状态 |
|---|---|
| 真实消息发送 | passed |
| 历史记录查询 | passed |
| 新建对话 | passed |
| 聊天面板展开与收起 | passed |

以上是实际用户操作结果。矩阵中的实体 IME、故障竞争、大历史、停止、重启/升级、并行任务和多屏/DPI/RDP 等专项仍按各自证据记录，不由日常交互通过自动更新整项 LC 状态。

## 前置条件与证据

- 首版目标为 Windows x64 交互桌面，使用[依赖清单](../../../../packaging/dependencies.json)锁定的 Go、Node、OpenClaw/provider 和 WebView2；实际运行记录全部版本与源码指纹。
- 使用独立节点数据、运行时 state/workspace 和动态端口；不读取个人 OpenClaw home，不停止已安装节点，不更改用户 UAC、ACL 或网络配置。
- 凭据在专用设置表单配置。真实模型验证显式选择并设费用/次数上限；普通工程测试用 fixture，不调用计费 API。
- 先完成计划 G1—G5；原生 Gateway 试验使用独立 state、workspace、用户环境和本地模型夹具，桌面验证使用独立测试宿主。
- 结果分 passed、failed、skipped、blocked；未开始为 not_run。代码、工程验证、真实模型、用户验收、安装和发布分别登记。

## 可行性门槛的验证方法

| 门槛 | 隔离操作 | 必须保留的证据 |
|---|---|---|
| G1 会话/收据 | 使用锁定包的正式接口创建会话；同 ID/同内容与异内容提交；在持久接收前后丢弃响应并重启 Gateway | 创建/发送收据对应原生会话与消息，去重窗口、重启行为、公开 API 映射；无可靠证据时记录 blocked |
| G2 历史 | 多页文本、超长单条、压缩/归档/重启；移除模型 key 和断开模型网络后读取；验证维护策略与旧格式 | 正文数量/ID/字节一致性、分页游标变化、缺失原因，保留策略不会静默删除应用会话 |
| G3 权限 | 检查最终工具注册/有效策略；输入文件操作、shell、skill 与 `/reset` 等管理指令；伪造会话引用 | 实际不可调用或调用前拒绝，无文件/进程/配置副作用，无凭据或内部上下文泄漏 |
| G4 生命周期 | 正常启动/停止、启动冲突、强制结束自有 Gateway、UI 与父节点；并发运行既有文件任务 | 进程归属、独立目录、可核对终态、无残留和无跨实例误停 |
| G5 控件/成本 | 在真实 WebView2 内加载候选离线控件；严格 CSP、原输入桥、长历史、流式/隐藏与主题 | 可替换通信和渲染证明、依赖来源/版本/摘要/许可证、成本分解、冻结的有限等待预算 |

包内文档路径见[会话设计](../../../assistant/conversations.md)；在实际准备的依赖目录读取，不能把某台开发机的缓存路径作为前提。公开上游文档可辅助定位，所有通过结论对应锁定包实测。

## 产品与故障验收矩阵

以下 LC 编号只用于本功能的可追溯验收；不是已有自动化测试名。

| ID | 关联需求 | 操作 / 场景 | 预期结果与验证层 |
|---|---|---|---|
| LC01 | AST-CHAT-01、WEB-CHAT-01、TEST-CHAT-01 | 快捷键呼出，输入并发送，完成一次有上下文的追问 | 自动展开、真实流式回复、追问使用当前会话；真实 OpenClaw + 模型及桌面必测 |
| LC02 | WEB-CHAT-02 | 实体中文 IME 选词时 Enter/Ctrl+Enter/Esc；多行、粘贴、撤销及 UTF-8 上限 | 组合期不发送；Shift+Enter 换行；显式发送才调用；超限保留草稿；真机与 fixture 分开 |
| LC03 | WEB-CHAT-01、WEB-CHAT-03 | 生成中折叠/隐藏/失焦；重新呼出；固定后切其他应用；查看旧消息 | 轮次继续、恢复最新状态；固定保持显示；滚动不被强制拉底，不抢焦点 |
| LC04 | AST-CHAT-02、WEB-CHAT-04 | 新建两段不同主题对话，重启 UI 和节点，再从列表选择 | 会话分离、默认恢复最近选择、已接受消息保留；未接受草稿的丢失边界准确 |
| LC05 | AST-CHAT-02、WEB-CHAT-04 | 载入多于 50 条历史并翻到最早，穿插长消息；分页时新回复完成 | 无缺漏/重复，旧游标失效有重载，完整长消息可按片段读取；无跨会话串页 |
| LC06 | AST-CHAT-03 | 重复点击发送；分别丢弃首次跨桥、服务端 ID 分配及 runtime 接受响应；同 ID 重试、异内容复用 ID；重建 UI/重启服务 | 原操作身份可查询且不生成第二次提交；同请求最多一次接收运行，异内容冲突；刻意同文不同 ID 仍为两次输入；未知结果不自动重放 |
| LC07 | AST-CHAT-03、WEB-CHAT-02 | 发送后立即编辑新草稿；ACK 迟到；确认前重建 UI/结束父进程 | ACK 仅清空原修订；UI 恢复主进程已接受的内存草稿；父进程在 runtime 接收前崩溃不伪造消息可恢复 |
| LC08 | AST-CHAT-04 | 停止生成、重复停止、完成与停止交错、取消确认丢失 | 区分停止请求/实际终态，保留可恢复部分正文；迟到停止不影响下一轮 |
| LC09 | AST-CHAT-04、INST-CHAT-01 | UI 崩溃、Gateway 崩溃、节点正常退出和强制结束；停止证据可靠但收据/正文保存未知 | UI 故障不取消；恢复先核对，未知不重跑；有界清理自有树；确认停止且不会恢复执行后释放槽，历史未知仍可见 |
| LC10 | AST-CHAT-03、AST-CHAT-04 | 创建绑定前后、runtime 接受后目录回写前、正文保存时注入存储失败/磁盘满 | 不重复创建，不发虚假持久 ACK，不把未保存回复记为已保存；保持可诊断现场 |
| LC11 | EXEC-CHAT-02、NODE-CHAT-02 | 模型请求工具/代码/管理命令；伪造目标、角色、原生 session、旧宿主和消息代次 | 实际拒绝，无权限提升；首版不连接其他节点，不泄露其他会话 |
| LC12 | WEB-CHAT-05 | 回复含 HTML/脚本、危险 URL、图片/媒体引用、伪装控制事件和超长代码块 | 安全展示，无外部自动请求/原生执行；复制成功；不从文本推断状态 |
| LC13 | AST-CHAT-05、EXEC-CHAT-01、EXEC-CHAT-03 | fixture 使用异名 runtime、不同原生 ID/分页语义，能力缺失或协议不兼容 | 公共 UI/会话服务无厂商分支；缺失能力明确拒绝或诚实降级；不宣称支持新 harness |
| LC14 | AST-CHAT-06、NODE-CHAT-01 | fixture 使用不同 node/role/绑定；修改显示名/默认目标；错身份 endpoint | 已有会话归属不变，缓存/游标隔离，错身份拒绝；不证明多节点产品已实现 |
| LC15 | AST-CHAT-08、NODE-CHAT-03 | 本地通信 fixture 丢事件、重放/乱序、慢消费、断开；目标暂不可达 | 有界重连、游标失效重取快照、去重顺序正确，未知写不换 ID/目标；无远程端点交付声明 |
| LC16 | AST-CHAT-07 | 既有任务运行或等待问答/核对时聊天；聊天忙时提交第二轮 | 两类准入独立；第二轮忙时保留草稿；原任务、配对、问答、取消和文件证据无回归 |
| LC17 | AST-CHAT-05、INST-CHAT-02 | 活跃或未确认停止时改配置；确认停止但历史收据未知时再改；空闲换模型/key/runtime；无 key/无模型网络读历史 | 在途明确 busy；已停止的历史未知不单独阻塞变更；就绪失效；旧绑定不迁移；只读历史不调用模型 |
| LC18 | INST-CHAT-02 | 隔离完整包升级保留身份、凭据、会话目录和原生历史；尝试旧程序访问较新数据 | 升级可恢复，未知较新 schema 拒绝写入，不原地破坏数据；实际安装单独验收 |
| LC19 | WEB-CHAT-03、TEST-CHAT-02 | 多屏、负坐标、100/150/200% DPI、锁屏/RDP与支持权限配置 | 面板在工作区内，固定与焦点行为正确；缺环境逐项 skipped，不扩大支持声明 |
| LC20 | AST-CHAT-08、TEST-CHAT-02 | 长历史/长回复、慢消费者、隐藏生成、100 次打开关闭 | 缓存/IPC/缓冲在设计上限内，无持续增长/遗留；UI 与 Gateway 成本分开 |

LC01—12、LC16—18 的实际产品行为需要真实锁定运行时/Windows 验证；工程 fault fixture 补足可控竞态，不能代替实际运行时收据、工具面、持久化和用户操作。LC13—15 只证明抽象与传输契约。LC19 按发布支持环境逐项记录，缺失必测环境阻止相应支持声明。

## 自动化入口与覆盖边界

下列命令覆盖工程回归；真实模型、实体 IME 和安装验收仍分别执行，不能仅凭单元测试把整项 LC 标为 passed。

```powershell
go test ./...
go vet ./...
$uiTests = Get-ChildItem -LiteralPath tests -Filter '*.test.mjs' -File
node --test $uiTests.FullName
```

相关现有源码：[运行时契约](../../../../internal/executor/contract_test.go)、[任务可靠性](../../../../internal/tasks/reliability_test.go)、[应用生命周期](../../../../internal/app/lifecycle_test.go)、[宿主协议](../../../../internal/quickinput/hostipc/protocol_test.go)、[草稿恢复](../../../../internal/quickinput/host_session_test.go)、[真实 WebView2](../../../../internal/quickinput/surface_behavior_windows_test.go)、[UI 成本](../../../../internal/quickinput/surface_cost_windows_test.go)。

原生桌面回归使用已有命令，参数中的路径由验证者选择且必须为独立生成物目录：

```powershell
pwsh -NoProfile -File scripts/test-quick-input.ps1 -Go <go-executable> -OutputDirectory <isolated-output-directory> -Desktop -Cycles 100
```

宿主准备、权限和真实桌面前提见[进程隔离验证](../quick-input-process-isolation/validation.md)。既有主 Web 的真实浏览器回归见[维护性验证](../web-ui-maintainability/validation.md)，安装/构建命令见[脚本指南](../../../../scripts/README.md)。

| 自动化源码 | 覆盖内容 | 对应验收边界 |
|---|---|---|
| [会话服务测试](../../../../internal/conversation/service_test.go) | 丢失接收响应、重复/冲突、重建服务、目录回写失败、发送前存储失败、未知历史后续核对、节点槽、配置保护、新 schema 拒绝与缺失能力 | LC06—10、LC13—17 的领域状态；存储故障不等同于真实磁盘耗尽 |
| [父进程聊天测试](../../../../internal/quickinput/chat_test.go) | 首次响应前保存操作身份、旧宿主死亡、原草稿恢复、后续修订优先与乱序选择 | LC06—07 的桥接恢复 |
| [创建和拒绝恢复](../../../../internal/conversation/creation_test.go)、[启动取消](../../../../internal/executor/conversation_test.go) | 旧 history 创建快照与并发 create/submit、明确拒绝后释放槽、取消等待启动与关闭中断启动所有者 | LC06、LC08—10 的竞态回归 |
| [面板状态测试](../../../../tests/chat-ui.test.mjs)、[连接状态测试](../../../../tests/chat-gateway.test.mjs) | 草稿切换取消/过期确认、完成时保留旧页、200 条淘汰锚点、游标 reset、折叠懒加载；重连认证屏障、明确未写出/原生拒绝白名单与写后未知 | LC02、LC04—08、LC15、LC20；DOM fixture 补足确定时序，真实布局仍由桌面验证覆盖 |
| [消息与客户端测试](../../../../tests/chat-model.test.mjs) | 10,000 条分页、200 条/2 MiB 窗口、Unicode 片段完整性、Go JSON 转义后字节上限、重复/乱序事件、迟到回执 | LC05、LC12、LC15、LC20 的有界展示和通信；不代替运行时大库性能 |
| [原生运行时测试](../../../../internal/executor/conversation_integration_test.go) | 真实锁定 Gateway + 本地 HTTP 模型夹具：两轮上下文、重复提交、工具列表为空、长正文、重启无 key 读取；生产 DeepSeek 插件无 key 只读启动，安装路径变化不改逻辑绑定 | LC01、LC04—06、LC11、LC17—18 的原生接口；不构成 DeepSeek 计费 API 或完整安装升级验收 |
| [真实聊天面板](../../../../internal/quickinput/surface_chat_windows_test.go) | Medium 权限 WebView2、生产 JS/CSP、封闭桥与 IPC、发送后继续编辑、脚本组合输入防误发、安全 Markdown、固定和密集长文本 | LC02、LC07、LC12、LC20 的自动化桌面覆盖；实体输入法与跨应用焦点仍需人工 |
| [原型门槛](../../../../internal/quickinput/surface_chat_gate_windows_test.go) | 原输入编辑器、CSP、浏览器无正文存储与候选长历史展示 | G5 可行性，不替代成品工作流 |
| [真实模型可选测试](../../../../internal/executor/conversation_live_test.go) | 明确授权后最多提交两轮短文本，每轮 45 秒；验证记住口令、重启后移除临时 key 读历史 | LC01 的真实模型补充；调用可能计费，不能由普通工程测试隐式启动 |

原生 Gateway 测试先按安装/构建说明准备依赖清单锁定的 Node/OpenClaw/provider，然后指定其入口：

```powershell
$env:ASTERHAND_CHAT_RUNTIME = '1'
$env:ASTERHAND_CHAT_NODE = '<locked-node.exe>'
$env:ASTERHAND_CHAT_ENTRY = '<locked-openclaw-directory>\openclaw.mjs'
go test ./internal/executor -run '^TestNativeConversation' -count=1 -v -timeout=4m
$env:ASTERHAND_CHAT_GATE = '1'
go test ./internal/quickinput -run 'TestNativeChatPanelWorkflow|TestChatSurfaceDesignGate' -count=1 -v -timeout=3m
```

真实模型另行设置 `ASTERHAND_CHAT_REAL_MODEL=1` 和 `ASTERHAND_CHAT_CREDENTIAL_HOME=<通过设置表单配置过的测试节点数据目录>`，运行 `go test ./internal/executor -run '^TestRealConversationTwoTurns$' -count=1 -v -timeout=4m`。测试只读指定节点的配置/DPAPI 凭据，把加密测试凭据和运行时数据放在独立临时节点；不输出密钥，不改动源节点，不需要运行源节点。测试结束取消这些环境变量，普通回归保持不调用模型。两轮是应用提交上限，provider 自身重试仍由锁定运行时策略约束。

当前工程覆盖包含 Go/前端回归、上述存储故障、真实 Gateway 本地模型夹具、生产 provider 只读启动、真实 DeepSeek 两轮上下文及移除临时 key 后重启读取历史、真实 WebView2 工作流与生产宿主崩溃恢复。日常聊天用户验收已通过；完整 LC01—20 专项仍有待验收项：实体中文 IME、多屏/DPI/RDP、跨应用固定/焦点/复制、实际包升级、并行真实文件任务与聊天尚需对应环境验收。源码已接入与用户验收、安装、提交、发布分别记录。

## 性能与支持边界

沿用[快捷输入冻结预算](../global-hotkey-quick-input/validation.md)：冷初始化 P95 ≤10 秒、桥往返 P95 ≤200 毫秒、自有 UI 进程树增量 private commit ≤350 MiB、隐藏 CPU ≤单核 1%、100 次启停后遗留自有子进程为 0。原隐藏 CPU 未达标项继续记录 failed，不通过换采样窗口或改阈值宣称修复。

测试分别记录空白已预热输入框、聊天面板展开、模型生成、隐藏生成和历史分页状态。Gateway 冷启动、稳态内存/CPU、模型首字延迟、传输到绘制延迟和节点启动影响单列，不能把 Gateway 排除后宣称整体成本达标。G5 冻结新增预算后才能进入正式接入；实测失败保留原值并评估设计。

长历史至少覆盖 10,000 条可见文本消息的 fixture，以及输入上限、超长单条回复与持续流式事件；初始页和可见缓存不得随总历史线性膨胀。真实运行时历史性能另测，fixture 不替代其存储性能。资源限额的淘汰只针对可重读 UI 数据，不删除 harness 权威历史。

本功能不承诺节点强制中断后无缝续跑、不承诺保存所有未落盘片段，也不承诺跨 harness 无损恢复。远程聊天、角色管理、单机多节点产品、其他 OS、工具和多媒体保持未实现。文档审计结论不提升这些能力状态。
