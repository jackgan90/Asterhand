# 助手设计

当前实现：OpenClaw 2026.10.7、对应 DeepSeek provider、Node.js 24.22.1；默认 `deepseek/deepseek-flash`。精确来源和摘要见 [依赖清单](../../packaging/dependencies.json)。

## 执行与路由

统一 Web 的每次输入形成持久 route job，A 的 OpenClaw 负责理解意图。普通问题可直接回答；动作通过 `asterhand_submit(goal,target_id,project_id)` 保存 outbox。B 持久接收后创建独立 execute job，由 B 的 OpenClaw 执行；本机动作也经相同边界。

明确选择器优先；与文字目标冲突时要求重新选择。默认选择器允许文字指定已知节点，未知或重名节点先澄清。文件名、内容等执行细节问题交给执行节点，转发目标时保留“先询问”的要求。

每个 attempt 启动一个 `agent exec` 进程，使用独立 config/state/workspace，JSON 结果和 stdin 目标，不启动 Gateway。模型内部工具循环由 OpenClaw 提供，任务生命周期由 Asterhand 监督。没有跨 attempt 长期会话或 memory 同步。

## 工具与权限

插件只允许 `asterhand_create_text`、`asterhand_read_text`、`asterhand_ask_user`；route 仅允许 submit/ask，probe 仅允许无副作用检查。配置使用显式工具白名单及内置工具组拒绝；受认证回调再次按 attempt/mode 校验。文件权限、节点身份均由 Go 绑定，模型不能传入另一个身份。

OpenClaw 内置 shell、任意文件访问、代码模式和自动再次委派关闭。角色指令不替代服务端边界，任务内容不能改写开发规范或扩大授权。

## 配置与凭据

用户在本机设置页输入 DeepSeek key。Windows 使用当前用户 DPAPI；数据目录 ACL 仅该用户和 SYSTEM。页面只返回 has_key。key 仅注入对应 runtime 子进程环境，不写入任务或 OpenClaw JSON；错误输出做脱敏。A、B 各自配置，配对不传 key。

保存有效执行配置的变化将 ModelVerified 置为 false，名称和网络设置保持既有就绪；点击检查会通过真实模型调用 `asterhand_probe`，仅收到回调才标为就绪。替换、删除 key 后必须重新验证。运行时或网络在检查后失效时，执行返回具体失败。

### 模型 provider 边界

`internal/llm.Provider` 提供 Describe/Validate，`Selection` 包含 provider、model、credential_ref 和可选 endpoint。当前唯一实现为 DeepSeek，仅接受清单中的三个模型；自定义 endpoint 尚未实现，传入时明确拒绝。Web 从设置 API 返回的 provider/runtime 描述生成选项，不维护另一份硬编码模型列表。

配置保存为 `llm` 与 `agent_runtime`。读取旧 flat `model` 时归一化为 DeepSeek/OpenClaw，保留模型 ID、节点身份、授权目录和 DPAPI 文件，下一次保存去掉旧字段。凭据引用是受限标识符，不是路径；`deepseek` 仍对应原有 deepseek.secret。通用凭据存储支持分开替换、删除；选择 provider 不会复制另一 provider 的凭据。设置 API 只返回 has_key，不返回明文。

provider 描述不执行模型调用。OpenClaw 的 provider bridge 把选择转换为插件 ID、插件路径及环境变量；其他 agent 需提供自己的转换。模型 key 只在 adapter 内读取并注入子进程。当前 adapter 对 final/error/stderr 中已知 key 和回调 token 脱敏。

就绪记录绑定源码摘要、依赖清单摘要和有效执行配置摘要（runtime ID、LLM Selection、该 runtime 的选项）。新候选、自动路径迁移或任何有效配置变化使旧记录失效；缺少执行摘要的旧就绪记录也要求重新 probe。成功 probe 后重新绑定。自动验证报告还核对正在运行节点的源码摘要，防止用旧程序验证新源码。

扩展步骤见 [边界 ADR](adr/003-provider-runtime-bundle-boundaries.md)。首版仍只交付 DeepSeek + OpenClaw，其他 provider 和 Hermes 尚未实现或验证。

收口公共流程：新节点默认模型/runtime 从包描述读取；runtime_options 按 runtime ID 隔离，由适配器解析。公共 CLI、设置目录保护、安装准备和包诊断不出现具体提供者选择分支；专用 API 和环境变量只位于 adapter/bridge。历史 OpenClaw flat 字段单独兼容迁移，不改变凭据字节或节点身份。见 [ADR-AST-004](adr/004-provider-neutral-public-flows.md)。

Linux 密钥层为权限受限本地文件，未宣称加密；当前 Linux 基础包不含助手运行时。按 [助手验证方法](../development/features/windows-assistant/validation.md) 区分工程测试和真实双机验收。

## 就绪保持

保存节点名称、公布地址或监听地址不撤销已完成的模型验证。模型选择、凭据写入/删除、运行时选择或授权目录变化则撤销并清除旧 passed 探测展示；程序包或运行时选项变化按执行配置摘要失效规则处理。重启后 GET probe 与持久 ModelVerified 一致，不能显示“检查通过”而实际禁止提交。
