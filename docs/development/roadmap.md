# 开发路线图

## 本机助手工具能力优先

[受控提权脚本与外部目录执行（第二阶段 / Tier 2 + 极速免审）](features/workspace-script-escalation/plan.md)已完成实现、工程验证、独立审计修复复审与本机真实模型用户验收：在保留第一阶段零审批工作区沙箱之上，面向非技术用户提供快捷输入原位交互确认卡片（零术语、用完即收起）与“按需询问 / 自动允许（极速）”双选（含零污染单次自动授权与空态隐藏的一键移除信任清单）；Go 服务端通过 `OrdinaryToken()` + 具名 `JobObject`、五层优先级评估、分片 `exec/approval_wait`、`approval_pending` 写屏障及启动前 `TOCTOU` 防篡改校验保障零 UAC 管理员提权与核心凭据隔离，并经独立 `TrustRevision` 实现 Gateway 零重启热切换；验证入口见[验证方案](features/workspace-script-escalation/validation.md)。

[工作区脚本与命令执行（第一阶段）](features/workspace-script-execution/plan.md)已完成实现、工程验证、独立审计修复复审与本机真实模型用户验收：在保持零频繁审批、出站网络可用及完整主流脚本工具链能力的前提下，通过 Go 服务经纪的 Windows `WRITE_RESTRICTED` 内核写沙箱、工作区控制桥令牌零落盘与敏感凭据保护、Turn 级进程树租约、`reconcileAfterExec` 配额与版本协调、四级统一聊天“停止”中断及跨平台 `SandboxBackend` 边界接入 `exec_command` 与 `process_control`；验证入口与双机/跨平台专项边界见[验证方案](features/workspace-script-execution/validation.md)。

[工作区工具细粒度并发](features/workspace-tool-concurrency/plan.md)已完成实现、工程验证、独立审计修复复审与本机真实模型用户验收：保留同工作区工具轮次串行，通过公开插件 SDK 委托九项统一工具，按 `(rootID, relPath)` 资源声明调度轮内文件、记忆与进度并行、同批盲覆写冲突拦截及公平等待，配套逐次低层效果校验、排空式取消、离线重名/别名预检与 `9×9` 组合及变异检测；见[验证方案](features/workspace-tool-concurrency/validation.md)。

[聊天历史顺序与分页修复](features/chat-history-timeline/plan.md)已完成实现、工程验证与本机用户验收：文字/工具按顺序显示，工具状态原位更新，原生跨页与重启读取保留最初消息。

[聊天工具调用目标摘要展示](features/chat-tool-call-summaries/plan.md)已完成实现、工程验证与本机用户验收：由工具定义白名单声明可展示目标参数并生成有界单行 `summary`（兼容历史旧工具名，排除写入正文、编辑前后文本和补丁差异正文），聊天界面按紧凑同行最多两行显示目标与状态并同步至 CLI/MCP。

[本机工具计划](features/local-runtime-tools/plan.md)首批已交付，工程回归、独立审查和用户验收完成；采用[正式文件 I/O 桥接](features/local-runtime-tools/preflight.md)，专项验证边界见[验证说明](features/local-runtime-tools/validation.md)。首批包含文件读写/发现、本地记忆、工具活动/产物及文件恢复，暂不执行脚本。之后分批完善本机网络、媒体、隔离执行、委派与后台工作；本机常用原生能力基本完善后再评估远程聊天。其他 harness 保留能力适配边界，本批仅实现 OpenClaw。旧 route/execute 兼容入口暂保留。

本地命令统一脚本图与多实例执行已实现并通过用户验收；范围和验证见[统一命令计划](features/unified-command-runs/plan.md)。

路线图区分现有能力和后续工作。具体行为以各领域 requirements/design 为准，验证方式见 [测试指南](../../tests/README.md)，平台边界见 [支持矩阵](../testing/platforms.md)。

## 日常自动化、CLI 与 MCP

[本地命令批量删除](features/batch-delete-local-commands/plan.md)：提供 Web“管理”模式中的多选、全选、逐项删除结果及 CLI/MCP 等价组合，批量删除及管理界面调整已通过本机用户验收。

[统一自动化计划](features/agent-automation/plan.md)的实现、工程验证、独立审核及用户验收已完成。CLI/MCP 已覆盖[日常业务操作](../automation/coverage.md)和未保存草稿，客户端与 Web 保留原有呈现和交互；验证方法及专项环境边界见[验证说明](features/agent-automation/validation.md)。后续补充了[命令与脚本图 Schema 自描述增强](features/command-schema-self-description/plan.md)（工程验证及本机用户验收通过）。

## 本地命令完成通知

[本地命令完成通知](features/local-command-completion-notifications/plan.md)：Windows 桌面右下角显示跟随主题的完成卡片，默认可读停留 3 秒，提供按内容调整的紧凑高度、自然进退动画、有限堆叠、溢出排队和命令级默认开启的通知开关；公共规则与平台展示分离，macOS/Linux 后续接入。实现、相关工程验证、独立审计修复复审及本机用户验收已完成，专项环境边界见[验证说明](features/local-command-completion-notifications/validation.md)。

普通应用遮挡通知的修复已通过桌面层级、命中和宿主恢复回归，以及安装后的本机用户验收。

## 已实现的 Windows 助手基础

脚本图编辑历史的实现、工程验证与本机用户验收已完成，包含实体中文输入法切换后的节点名称设置与重命名，见[撤销重做计划](features/graph-edit-history/plan.md)。

| 能力 | 设计与维护入口 |
|---|---|
| 命令模式下 Esc 一次隐藏整个快捷窗口，保留查询和输入法保护，连续呼出不先缩小再撑大（含中间帧的工程验证及本机用户验收通过） | [命令模式 Esc](features/command-mode-escape-hide/plan.md) |
| 日志后台补读保留分页按钮文字、可用状态和焦点，主动翻页优先于在途自动读取（工程验证及本机用户验收通过） | [日志工具栏稳定性](features/log-toolbar-refresh-stability/plan.md) |
| 快捷输入按前台状态处理快捷键：前台隐藏，失焦/遮挡或最小化后跟随当前工作屏幕恢复激活（工程验证及本机用户验收通过） | [快捷键焦点修复](features/quick-input-shortcut-focus/plan.md) |
| 普通消息发送、异常气泡与独立停止回复（工程验证及本机用户验收通过） | [聊天发送与停止回复](features/chat-send-intent/plan.md) |
| Web 管理本机脚本、快捷输入 `/` 列表与模糊搜索、普通用户独立启动（工程验证及本机用户验收通过） | [本地快捷命令](features/local-quick-commands/plan.md)、[验证说明](features/local-quick-commands/validation.md) |
| 本地脚本图编辑、静态串并行调度、节点失败停止和并行日志查看（工程验证、独立审计修复复审及本机用户验收通过） | [本地脚本图](features/local-script-graphs/plan.md)、[验证说明](features/local-script-graphs/validation.md) |
| 脚本图结构节点隐藏设置、日志自动/固定多分屏、独立翻页与状态颜色（工程验证及本机用户验收通过） | [脚本图日志体验](features/script-graph-log-experience/plan.md)、[验证说明](features/script-graph-log-experience/validation.md) |
| 本地与远端自然语言文件任务、问答、取消 | [Windows 跨节点助手](features/windows-assistant/plan.md) |
| 模型配置、执行器契约、依赖锁定 | [模型、运行时与依赖边界](features/provider-runtime-boundaries/plan.md) |
| 无模型配置启动、按功能准入与可选模型工具诊断（工程验证、独立审计及本机聊天验收通过，其余专项见验证说明） | [按需模型配置](features/on-demand-model-readiness/plan.md)、[验证说明](features/on-demand-model-readiness/validation.md) |
| 运行时专用选项、受保护目录、通用包描述 | [提供者无关的公共流程](features/provider-neutral-flows/plan.md) |
| 日志预算、取消竞争、文件证据与配置迁移 | [任务可靠性与配置保留](features/task-reliability/plan.md) |
| Windows 安装、升级、相对路径与数据保留 | [安装路径解析](features/installer-path-resolution/plan.md) |
| 任务排序、节点名称/地址同步、就绪保持 | [双节点交互与连接同步](features/two-node-usability/plan.md) |
| 统一发布、单脚本下载安装与平台扩展接口 | [可复现发布与独立安装](features/release-pipeline/plan.md) |
| 仓库固定安装入口、默认 Latest 与显式版本选择 | [固定安装入口](features/stable-installer-entry/plan.md) |
| 原生双击启动、可选桌面入口、节点托盘和统一退出 | [Windows 桌面启动与托盘生命周期](features/windows-desktop-lifecycle/plan.md) |
| 当前源码双击安装、显式停止与安装后启动、PowerShell 下载引导 | [本地一键安装与在线引导](features/local-install-entry/plan.md) |
| Ctrl+Alt+J 全局呼出、本地自定义与冲突检测、科技风文本浮层（首版用户验收完成） | [本机全局快捷输入](features/global-hotkey-quick-input/plan.md) |
| 独立非提权输入宿主、主进程草稿、有限恢复和真实可用状态（本机用户验收完成） | [快捷输入进程隔离](features/quick-input-process-isolation/plan.md) |
| 原生 Web 同源客户端、生命周期、任务/节点/设置职责与独立 WebView2 桥；保持现有功能和平台范围 | [Web 与快捷输入维护性重构](features/web-ui-maintainability/plan.md) |
| 本机持续聊天：真实消息发送、历史查询、新对话、展开/收起（用户验收通过） | [持续聊天](features/local-node-chat/plan.md) |
| 收起态输入按实际文本增高与删除回缩、单行起步与十行滚动上限；默认恰好容纳，额外一行可供实测调整（工程验证、独立复审及本机输入体验用户验收通过） | [自动高度](features/compact-input-auto-height/plan.md)、[验证说明](features/compact-input-auto-height/validation.md) |
| 聊天连接中、已连接、无法连接三态提示（工程验证及本机桌面用户验收通过） | [连接状态](features/chat-connection-status/plan.md) |
| 聊天启动诊断、共享冷启动与已有状态恢复（工程验证及本机聊天使用验收通过） | [启动恢复](features/chat-startup-recovery/plan.md) |
| 聊天启动操作队列与统一发送体验（工程验证及本机桌面用户验收通过） | [操作队列](features/chat-operation-queue/plan.md) |
| 聊天发送反馈、快捷键与正文展示；提示词保留原生身份和角色设定，局部补充能力约束 | [聊天交互](features/chat-prompt-interaction/plan.md)、[原生提示词保留](features/native-harness-prompt-preservation/plan.md) |
| 精简提示、动态回复、本地数学公式、标题拖动和任务栏入口（本机日常交互用户验收通过） | [聊天展示与桌面窗口体验](features/chat-desktop-experience/plan.md) |
| 正常发送状态与气泡稳定、提示词诊断退出聊天展示、后台完成任务栏提醒（本机用户验收通过） | [聊天发送稳定性与后台完成提醒](features/chat-completion-attention/plan.md) |
| 快速本地安装：应用与运行时分开更新、目录候选、源码核对及持续进度（工程验证、独立复审和本地提速效果用户验收完成） | [快速本地安装](features/fast-local-install/plan.md)、[验证说明](features/fast-local-install/validation.md) |
| Web 管理页与快捷面板统一视觉、随面板主题切换、面向普通用户精简文案（用户验收通过） | [Web 管理页视觉升级](features/web-console-visual-refresh/plan.md) |
| 运行期界面与状态/错误提示中英双语实时无损切换、系统语言优先/浏览器语言次选自动匹配、单一权威词表与四层静态硬编码守卫（工程验证、独立复审及本机双语切换用户验收通过） | [运行期国际化框架](features/internationalization-framework/plan.md)、[验证说明](features/internationalization-framework/validation.md)、[国际化开发指南](i18n-guide.md) |
| 日志窗口快速显示与关闭复用、初始化失败恢复和同屏定位（工程验证、独立审计修复复审及本机日志体验与双屏用户验收通过） | [日志窗口响应与定位](features/log-window-responsiveness-and-placement/plan.md)、[验证说明](features/log-window-responsiveness-and-placement/validation.md) |
| 前端信息精简第一阶段：状态图标化、日志窗口与设置页重组、首次显示语言、日志窗口前台、面板圆角与合并日志按行标注（工程验证、本机用户验收及独立评审复审通过） | [前端信息精简与显示修复](features/frontend-cognitive-simplification/plan.md) |
| 前端信息精简第二阶段：脚本图编辑器节点式交互，画布平移缩放与全屏、右键菜单、端口拖线、原地改名、属性面板，负坐标编辑与保存前布局平移（工程验证、本机用户验收及独立评审复审通过） | [前端信息精简与显示修复](features/frontend-cognitive-simplification/plan.md) |
| Windows 正式安装节点运行期桌面图标开关：Web「设置 → 通用」即时切换与 CLI/MCP `settings.desktop_shortcut` 等价操作，外部同名快捷方式冲突保护与升级保留（工程验证及本机用户验收通过） | [运行期桌面图标开关](features/runtime-desktop-shortcut/plan.md) |

## 后续验证与功能

- 脚本图编辑器在 Windows 中文输入法下的原地改名候选确认，按[前端信息精简计划](features/frontend-cognitive-simplification/plan.md)的验证说明在实体输入法中复核。

- [托管本地命令](features/managed-local-commands/plan.md)：实现、工程检查、独立审计修复复审及用户验收已完成，真实环境专项和平台支持边界见功能验证说明。新增托管/非托管类型、独立 Asterhand 日志窗口、最近执行结果和完整日志、按命令单实例、强制中断及可选超时；退出节点停止托管脚本，非托管行为保持。首个实现为 Windows，注册策略、执行/计时、窗口/传输及文件操作留出窄平台边界，macOS/Linux 接入后独立验收。

- 运行期国际化的本机双语切换已通过用户验收；实体输入法、多屏/DPI 及双机跨节点任务专项按[验证说明](features/internationalization-framework/validation.md)独立确认。

- 收起态自动高度的本机输入体验及默认恰好容纳策略已通过用户验收；实体输入法、鼠标滚动条、多屏/DPI等专项按[验证说明](features/compact-input-auto-height/validation.md)单独验证，后续调整额外一行策略时重新检查相关体验。

- 快速本地安装的真实联网依赖修复与干净环境安装继续按[验证说明](features/fast-local-install/validation.md)完成专项验证。

- 持续聊天的实体 IME、多屏/DPI/RDP、跨应用交互、实际升级、并行真实文件任务与成本专项继续按[验证说明](features/local-node-chat/validation.md)记录；日常聊天用户验收已完成。

- 快捷输入的隐藏 CPU 成本优化，以及其余权限配置、多屏/DPI/锁屏/RDP、干净 Windows Runtime 安装和双机专项验证，范围见[隔离验证](features/quick-input-process-isolation/validation.md)与[快捷输入验证](features/global-hotkey-quick-input/validation.md)。
- Windows 桌面专项验收：Explorer 重启、注销、深浅主题与 DPI，以及两台 Windows + OpenClaw + DeepSeek 下的退出与恢复；工程测试和合成系统消息的边界见 [桌面启动验证](features/windows-desktop-lifecycle/validation.md)。
- Linux/macOS 平台准备器、构建器、安装入口与原生系统验证；平台注册表在验证前保持禁用。
- 手机远程访问的认证、部署方式和物理设备测试。
- 物理断线与重启、复杂 NAT 环境、长历史规模的系统化可复现测试。
- 协议故障代理、可控时钟和持续集成；现有覆盖见 [测试设计](../testing/design.md)。
- 根据实际需求扩展工具和模型/运行时适配器。具体 coding harness、自动调度、全网会话同步等见 [后续方向](../overview/future.md)。

这些条目需要分别确定范围与授权。新增功能先建立语义明确的功能计划，再同步领域文档、实现和验证方法。用户验收、本地提交、推送与发布各自确认，不以路线图状态替代授权。
