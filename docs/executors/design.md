# 执行器与工具设计

范围：OpenClaw adapter、公共配置和目录保护；未接入具体 coding harness。验证方法见 [公共流程契约](../development/features/provider-neutral-flows/validation.md)。

## 当前契约

`Executor.Execute(ctx, Run) (Result,error)` 是实际最小接口。Run 绑定 attempt ID、mode、callback、一次性 token 和 goal；Result 提供 ok/status/final/error。启动、查询、事件、回答和恢复由任务服务实现，不为未支持的 native resume 提供空接口。

`Runtime` 接口嵌入 Executor，提供 Describe、Validate、Discover、Check 和 ProtectedPaths。静态工厂只登记实际实现的 adapter，当前为 OpenClaw；Selected 在每次 attempt 按当前配置解析 adapter。环境管理使用同一工厂发现和诊断，应用入口不直接实例化具体 agent。

将运行时专用选项放在 Config.runtime_options[runtime ID] JSON 中，由该适配器解析和校验；公共配置与 CLI 不声明厂商专用字段。CLI 用 --agent-runtime 和 --runtime-options（JSON 文件）选择及配置；旧 --node/--openclaw/--plugin/--provider 参数由此替代。历史 flat JSON 字段仅由兼容迁移模块读取，保存时移除，身份和凭据文件不改写。

适配器 ProtectedPaths 返回已知运行时、宿主、工具插件和模型插件保护目录。公共授权目录校验统一拒绝与这些目录及节点数据目录重叠，解析现有目录/祖先的符号链接与 junction 后再比较；目录声明或解析失败显式拒绝。更换运行时选项时对既有授权目录重新校验。

用 Config.runtime_options_explicit[runtime ID] 标记显式选项。CLI 的 --runtime-options 将整个选项对象标为显式，普通启动/repair 保留它；自动选项随包目录发现和升级。旧无标记配置仅当宿主、入口、工具插件和当前模型插件均匹配同一历史包布局时视为自动，其余非空配置保留为显式。新 CLI 即使指定另一个完整包也保持显式；旧格式无法区分人为填写的完整包布局与旧自动值，按此兼容规则处理。

能力声明区分 route/execute/probe、工具回调、普通问答、进程取消与原生事件/resume/审批。后三项当前明确 false；任务事件、持久问题及重启对账由 Asterhand 提供。`/api/settings` 返回能力描述与支持的模型 provider，服务端拒绝不支持的组合。

OpenClaw adapter 在启动前校验 attempt ID、模式、本机回调和凭据引用。stdout 采用已验证 CLI envelope：status 为 ok/error/timeout，且 ok 标志必须一致；未知或矛盾状态返回适配失败。输入通过 stdin 异步写入，取消能同时中断阻塞写入和进程树。失败日志、最终文本与错误字段对已知凭据脱敏。

契约 fixture 以真实子进程模拟协议边界，覆盖成功/错误/无效输出、取消和阻塞输入；真实 OpenClaw 检查另行执行。新增 agent 要实现同一工具桥接和能力契约，并运行相同类别的契约及真实模型验证，不假设 Hermes 与 OpenClaw 的 API 相同。

Windows 在写入目标前把 Node 加入 kill-on-close Job Object；取消和服务退出结束整棵子进程树。真实进程 fixture 验证后代终止。Linux 使用进程组，其产物仅交叉编译。每个 attempt 的 runtime state 留在节点数据目录用于现场核对。

OpenClaw CLI 固定 `agent exec --message-file - --json --code-mode direct`，每个 attempt 独立运行。有效执行预算 10 分钟，墙钟 2 小时；问答等待由 Go 回调承载。 stdout/stderr 各最多保存 1 MiB，不展示无限模型输出。

## 文件工具

Go `os.OpenRoot` 约束路径解析，拒绝绝对路径、穿越、NTFS 流、尾随点/空格和越界 junction。文件 create-only：`O_EXCL`，不覆盖已有内容。文本有效 UTF-8，最多 64 KiB；不自动加换行或 BOM，父目录须已经存在。

调用绑定 job/attempt/native tool-call ID，摘要相同的重试读取原操作结果，摘要不同返回冲突。写前取得普通事件预算并持久化 prepared 操作；写后 fsync、close、回读校验，再把完成操作与任务产物/事件同事务提交，包含大小、完整 SHA-256 和最多 8000 字节预览。旧分步提交留下的 completed 操作可按原证据幂等补齐任务产物；prepared 仍进入待核对，不根据同内容旧文件伪造成功。

普通问题持久化 interaction，绑定 attempt 与 native tool-call ID；回答后才释放等待回调。取消时等已接收回调退出再释放执行槽，防止上一个写入尚未结束就开始下一个任务。

工具层不提供权限审批工具或任意 shell。远端 peer 无本地人工处置权限。后续 Everything、VCS、coding harness 需单独实现并申明能力。
