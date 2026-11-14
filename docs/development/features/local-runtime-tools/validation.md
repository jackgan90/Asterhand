# 本机工具能力验证方案

状态：首批功能已完成工程回归、独立审查与用户验收。工程验证覆盖 Windows 文件服务、真实固定包 Gateway + 本地 SSE 模型、公共服务、Web 组件，以及隔离基础节点的安装与在线重启。真实 DeepSeek 全矩阵、原生桌面专项、旧完整助手升级、完整容量故障与性能测量未形成独立的完整验证记录，保留为后续专项验证范围。

关联[计划](plan.md)、[设计](../../../assistant/runtime-capabilities.md)、[持续会话基线验证](../local-node-chat/validation.md)。

## 前置与证据

- Windows x64，使用依赖清单固定的 Go、Node、OpenClaw/provider；如升级，记录来源、摘要、许可与旧会话兼容结果。
- 独立测试节点/state/workspace/端口；不修改已安装用户节点、不启动其工作负载、不读取个人秘密。
- 原生验证用真实 Gateway + 本地模型夹具；真实 DeepSeek 验收独立执行，使用专用设置配置凭据，限定轮次、重试和时间预算。
- 每份执行记录包含源码版本、差异摘要、测试命令、OS/依赖版本、passed/failed/skipped/blocked、证据位置。未执行保持 not_run。
- 生成物放在当前构建配置解析出的输出目录的独立子目录；文档不依赖具体本机目录。长期保留测试目的、前置和入口，原始日志保留在忽略目录或发布附件。

## 必验矩阵

G1 沿用清单固定的 Node 24.22.1、OpenClaw/provider 2026.10.7 及依赖准备/校验流程，没有升级依赖。G2 验证真实模型请求九项 schema、越界与 exec 拒绝；G3 的宿主直写方案仍 blocked，正式桥通过 Windows 句柄、竞争与原生操作验证；G4 验证活动分页/状态更新、原生历史重启及取消；G5 验证无 embedding 凭据的关键词实际命中和重启后的新会话检索。各项不代表下表所有故障与产品验收已完成，具体入口和边界见下文。宿主限制探针见[实施门槛](preflight.md)。

| ID | 需求 | 场景 | 预期与证据层 |
|---|---|---|---|
| LT01 | AST-TOOLS-01、EXEC-TOOLS-01 | 同一聊天创建/读取/修改/移动/删除多个文件 | 真实字节、大小、摘要与工具结果一致；固定原生包 + DeepSeek |
| LT02 | EXEC-TOOLS-02 | 绝对越界、穿越、UNC/device、ADS、大小写、尾随字符、symlink/junction/硬链接，含检查后替换链接 | 执行处拒绝且目录外无读写；真实 Windows 文件系统，不只字符串单测 |
| LT03 | EXEC-TOOLS-02、06 | 诱导 shell、Code Mode、插件、管理命令、切换 full 权限、MCP commands.run、外部网络工具 | 最终模型 schema 与执行拒绝一致，无可调用旁路；原生包及适配器 |
| LT04 | AST-TOOLS-02 | 空目录、深目录、10,000 项、超大文本、非法 regex、慢扫描、取消/分页 | 有界结果、截断原因和可继续依据；不以 shell 解析用户输入 |
| LT05 | AST-TOOLS-03 | 保存记忆、新会话检索、重启、无 embedding key、外部记忆链接 | 仅允许的本地记忆命中；无意外 cron/后台写入/外部 embedding 调用 |
| LT06 | AST-TOOLS-04 | 文本→工具→文本→工具→最终回复，进度卡更新、重复乱序事件 | 中间消息不结束轮次；状态由结构化证据决定；进度声明与实际结果分开 |
| LT07 | AST-TOOLS-04、EXEC-TOOLS-04 | 关闭/重建 UI、丢事件、过期游标、重启后查看 | 活动可按公共身份补读，丢失明确标记；无完整正文复制到第二数据库 |
| LT08 | EXEC-TOOLS-03、04 | 写入前、原生写后结果落盘前、快照落盘失败、磁盘满、Gateway 强制退出 | 未有前像不执行；部分完成可核对；未知操作不重跑、不误释放租约 |
| LT09 | EXEC-TOOLS-03、04 | 两会话、公共 mutate/restore 与记忆写入竞争；前像持久化后由外部进程原地写/替换文件或抢先创建不存在目标，覆盖 write/edit/patch 的覆盖、删除、移动 | 节点内串行；外部竞争使原操作条件失败或被实测排他机制阻止，实际被覆盖内容均有对应前像；原生正常写入仍成功；停止/超时不早释保护 |
| LT10 | AST-TOOLS-06、EXEC-TOOLS-03 | 覆盖/删除/移动后恢复；在恢复摘要检查后、实际写回前安排外部原地写/替换/抢先创建；重复恢复/异内容身份 | 条件失败或排他访问阻止竞争，外部新内容不被静默覆盖；有前像及不存在标记，重复返回同收据，部分失败逐项返回 |
| LT11 | AST-TOOLS-05 | 伪造 artifact ID、相同相对路径跨 workspace、生成后文件被换成链接或已变更 | 每次授权/身份/修订复核；拒绝任意宿主路径；stale 可区分 |
| LT12 | EXEC-TOOLS-05 | 每项预算边界、重复写、长工具输出、慢消费者、100 次打开关闭 | 有界存储/输出/缓存；UI 隐藏停止刷新但轮次继续；满额有可诊断结果 |
| LT13 | EXEC-TOOLS-01、06 | 异名 runtime fixture，支持但无法 enforce、缺失工具、权限扩大/收紧 | 公共层无厂商工具名分支；无法落实时工具不可用；撤权不虚报完成 |
| LT14 | AST-TOOLS-05、06 | CLI/MCP 查询/搜索/读取/下载/恢复；设置只保留人工开关，伪造内置工具身份访问普通自动化 | 同结果、分页、冲突、预算和错误；窄身份不能调用脚本或改授权 |
| LT15 | AST-TOOLS-01、EXEC-TOOLS-06 | 新节点、旧配置缺字段/无修订、已有明确关闭、保存其他设置/重启、切换模型/凭据 | 未设置时默认开启，已有明确选择保持；已有历史与工作区保留；在途策略正确收尾 |
| LT16 | 全部 | 已有本地命令、外部 CLI/MCP、旧 route/execute 文件任务、纯文本会话 | 旧授权与行为无回归；聊天工具快照不阻塞无关命令/任务槽 |
| LT17 | AST-TOOLS-04、05 | 工具结果含 HTML/脚本/危险 URL/超长字符串及伪造事件 | 安全展示，无自动执行/外部请求；产物引用只取受信任元数据 |
| LT18 | EXEC-TOOLS-02、03 | 修改人格/skill/记忆文件，覆盖同名受信任插件路径的尝试 | 允许工作区文本演化；权限不扩大、代码不加载，修改可恢复 |

LT02、LT03、LT08—11、LT15 必须包含真实固定包及 Windows 验证；fixture 不替代系统路径或原生 hook 能力。LT01/LT05/LT06 还需要真实 DeepSeek 用户工作流；远程节点、Mac/Linux 与其他 harness 不属于本批支持声明。

## 可执行入口

复用现有[原生聊天测试](../../../../internal/executor/conversation_integration_test.go)、[提示词插件](../../../../internal/executor/promptplugin/index.mjs)、[投影](../../../../internal/executor/chatbridge/projection.mjs)、[会话服务测试](../../../../internal/conversation/service_test.go)、[自动化行为覆盖](../../../automation/coverage.md)；新增工具/版本/权限测试如下节所列。常规回归命令：

~~~powershell
go test ./internal/workspace ./internal/executor ./internal/conversation ./internal/automation ./internal/app
go test ./...
go vet ./...
$uiTests = Get-ChildItem -LiteralPath tests -Filter '*.test.mjs' -File
node --test $uiTests.FullName
pwsh -NoProfile -File scripts/check-i18n.ps1
~~~

原生包夹具环境变量与命令沿用[持续会话验证](../local-node-chat/validation.md#自动化入口与覆盖边界)。设置 ASTERHAND_CHAT_RUNTIME=1、ASTERHAND_CHAT_NODE 为固定 node.exe、ASTERHAND_CHAT_ENTRY 为固定 openclaw.mjs、ASTERHAND_CHAT_EVIDENCE 为独立构建输出子目录，运行 `go test ./internal/executor -run TestNativeWorkspace -count=1 -v -timeout=8m`。每个测试创建隔离 state/workspace，不读取真实节点凭据。Windows 使用普通用户进程；中文快捷方式回归使用 UTF-8 控制台（`chcp 65001`）。安装升级沿用[脚本指南](../../../../scripts/README.md)的隔离安装流程，真实节点切换另获确认。

性能保持[既有桌面预算](../local-node-chat/validation.md#性能与支持边界)，新增搜索、快照、恢复分别测量 P50/P95、扫描字节和峰值内存；同时比较禁工具基线，不把 Gateway/索引排除后报告整体达标。快照不得对每次小改递归复制整个 workspace。

## 审查与验收状态

原计划及 P0 探针已独立审计；产品代码按 quality-audit 完成六维度与十三项结构审查，发现的问题按原触发条件修复和针对性复审。简化后的设置入口与默认开启策略也完成独立复审，已确认问题全部关闭。首批用户验收通过；提交和发布是后续独立状态，工程覆盖仍以下表为准。

## 已提供的工程测试与专项验证边界

| 入口 | 覆盖 | 边界 |
|---|---|---|
| [Windows 文件服务](../../../../internal/workspace/service_windows_test.go)、[分页/保留](../../../../internal/workspace/paging_windows_test.go) | LT02/04/08—12/15：真实句柄、外部竞争、junction/硬链接、Unicode 长路径、恢复、unknown 重开、活动序号、分页和版本保留 | 磁盘满、完整存储预算及所有失败窗口仍需专项故障验收 |
| [准入与故障恢复](../../../../internal/workspace/admission_windows_test.go) | LT02/08/12：已固定父目录原地变 junction、拒绝操作无新目录、10,000 项下删除、目录计数、外部超限后缩小、终态落盘失败后核对 | 日志路径故障注入覆盖写入失败；不等同于真实磁盘耗尽 |
| [原生工具](../../../../internal/executor/conversation_workspace_integration_test.go) | LT01/03/05/06/07/18：九项工具、读写编辑/补丁移动新增删除、搜索、记忆实际命中、进度、重启历史、新会话记忆、越界与 exec 拒绝 | 本地 SSE 模型夹具；不单独证明真实 DeepSeek 全矩阵 |
| [原生取消](../../../../internal/executor/conversation_workspace_cancel_test.go) | LT08/09/15：已写文件保留、工具收尾、下一轮继续及旧轮迟到取消 | 系统退出、睡眠和完整磁盘故障待专项验证 |
| [公共操作](../../../../internal/app/automation_workspace_windows_test.go)、[回执恢复](../../../../internal/app/automation_workspace_recovery_windows_test.go) | LT10/13/14：异名 runtime、scope、重复身份、过期修订、租约冲突及回执中断 | 实际 CLI/MCP 客户端完整工作流仍按自动化验收执行 |
| [工具设置](../../../../tests/workspace.test.mjs)、[有序聊天](../../../../tests/chat-timeline.test.mjs)、[设置交互](../../../../tests/web-interactions.test.mjs)、[聊天模型](../../../../tests/chat-model.test.mjs) | LT06/07/17：开关单独保存、失败核对、迟到响应、重开、双语、工具原位更新和中间消息 | 工具/文字顺序与跨页回归及本机用户验收已通过，见[历史修复计划](../chat-history-timeline/plan.md)；其他专项仍按矩阵执行 |
| [默认工具配置](../../../../internal/config/local_tools_test.go)、[运行时配置](../../../../internal/executor/conversation_default_tools_test.go) | LT15：新节点、旧默认迁移、明确关闭保持、保存其他设置/重启、提示词与工具白名单一致 | 配置与生成结果检查；真实模型验收仍按 LT01 执行 |
| [隔离候选安装](../../../../scripts/test-local-candidate.ps1) | LT15/16 的基础节点部分：真实应用及运行时安装、损坏候选拒绝、在线重启、数据/token/身份保留与运行时 generation 更换 | 已验证；不调用模型，Known Folder 使用隔离目录，完整 ZIP 互操作及旧完整助手会话升级另验 |

常规全套 Go/前端检查覆盖 LT16 的旧功能回归，不能替代真实模型、安装或两机验收。性能 P50/P95、完整容量与磁盘故障尚未完成测量；在完成前不宣称性能目标或全部 LT 场景通过。
