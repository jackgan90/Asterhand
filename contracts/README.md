# 当前机器契约

版本前缀 /v1。节点服务使用 TLS 1.3、固定证书和独立 bearer token；/v1/pair 仅接受一次性邀请。配对后 /v1/info、/v1/jobs、/v1/jobs/{id}、events、answer、cancel 与 cancel-request 均按 peer/project/origin 检查。

字段权威在 internal/model/model.go，状态在 internal/tasks/engine.go；请求 ID 8—100 字符，goal 最多 16000 字节，HTTP JSON 请求最多 256 KiB。事件分页 200 条，按单调 seq 查询；snapshot watermark 配合补读。

本地 /api 采用会话与 CSRF，/internal/tool 采用 attempt 随机 token，模型不可提供 job/peer 身份。远端无设置、key 或人工核对关闭接口。

本地设置保存 `agent_runtime` 和 `llm:{provider,model,credential_ref,endpoint?}`，GET 返回可用 runtimes/providers 描述及 has_key；旧 model POST 保留兼容。文件、job、peer 协议不携带模型凭据。runtime 接口与能力见 [执行器设计](../docs/executors/design.md)，模型迁移见 [助手设计](../docs/assistant/design.md)。
