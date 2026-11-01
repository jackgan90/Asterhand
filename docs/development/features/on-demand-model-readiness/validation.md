# 按需模型配置验证

## 前置条件

Windows，使用[依赖清单](../../../../packaging/dependencies.json)固定的 Go、Node.js 和 OpenClaw。普通回归使用临时节点、合成凭据和受控执行器，不读取生产凭据或修改安装。Windows 私有目录 ACL、链接及子进程测试需要正常用户权限。

## 自动化入口

```powershell
go test ./...
go vet ./...
go build -o ./out/on-demand-model-readiness/ ./cmd/asterhand ./cmd/asterhand-launcher ./cmd/asterhand-input-host
$uiTests = Get-ChildItem -LiteralPath tests -Filter '*.test.mjs' -File
node --test $uiTests.FullName
```

| 契约 | 测试入口与断言 |
|---|---|
| 基础节点启动 | [应用测试](../../../../internal/app/model_readiness_test.go)：无 key、无目录启动，健康、设置、任务和节点查询可用；不创建运行目录 |
| 直接执行与源证据失效 | 同上：从未 probe 也可本机执行及 A 无目录时路由 B；旧 bundle 诊断过期但本地能力有效；仅显式测试调用 probe |
| 配置判断 | [能力测试](../../../../internal/executor/availability_test.go)：缺失/空/不可读凭据、缺宿主/入口、provider 版本漂移和未知选择；聊天不依赖任务工具；状态检查不执行进程 |
| 原子接收 | [存储测试](../../../../internal/store/admission_test.go)：已有接收与取消墓碑先于配置拒绝；不同摘要冲突；无额外 attempt |
| 远端投递 | [投递测试](../../../../internal/app/admission_test.go)：本机和 TLS 远端明确拒绝停止重试、取消先到仍确认 cancelled；网络故障保持原目标/ID；本机接收等待配置事务 |
| 协议证据 | [peer 测试](../../../../internal/peer/admission_test.go)：只有提交端点的匹配 ID/摘要/错误码响应证明未接收；查询、取消、未知码、超限、截断和错误摘要均不产生该证据 |
| 终局拒绝与迟到请求 | [TLS 迟到提交测试](../../../../internal/app/delayed_admission_test.go)暂停第一次请求，发送方中断后重试得到拒绝，恢复配置并释放旧调用，断言无接收或文件副作用；[存储测试](../../../../internal/store/rejection_test.go)覆盖拒绝跨重启、摘要冲突、写入/提交失败和 schema 升级保护 |
| Web 重新提交 | [拒绝恢复测试](../../../../tests/task-admission.test.mjs)覆盖完整匹配才释放 pending、草稿保留、新请求 ID、未知错误及 pagehide；[共享 fixture](../../../../tests/fixtures/task-rejection-requests.json)同时由 Go 与 JS 测试检查 route 归一化、字段省略和 Unicode/HTML 转义 |
| 诊断与设置 | [诊断测试](../../../../internal/config/diagnostic_test.go)、[设置测试](../../../../internal/app/settings_readiness_test.go)：旧配置迁移、失败持久化、过期、元数据保留及有效配置失效 |
| 前端与桌面 | [交互测试](../../../../tests/web-interactions.test.mjs)、[状态测试](../../../../tests/web-state.test.mjs)、[桌面状态测试](../../../../internal/desktop/status_test.go)：直接进入主页、保存不要求 probe、拒绝与未知文案分开、能力优先及旧节点 ready 兼容、托盘节点状态 |

## 锁定原生运行时

已准备固定版本依赖时，以本地模型 fixture 检查聊天发送与无凭据历史：

```powershell
$env:ASTERHAND_CHAT_RUNTIME = '1'
$env:ASTERHAND_CHAT_NODE = '<固定 Node.js 目录>/node.exe'
$env:ASTERHAND_CHAT_ENTRY = '<固定运行时目录>/node_modules/openclaw/openclaw.mjs'
go test ./internal/executor -run 'TestNativeConversation(SendBeforeReply|ReadOnlyAdapter)$' -count=1 -v
```

路径指向依赖准备入口生成的固定版本文件；不要指向另一个滚动安装。只读适配器用例先以合成 key 检查真实包的 `ConfiguredCapabilities` 和 `Describe`，确认聊天可用、无工具插件只限制任务、不产生诊断成功或运行目录，然后删除 key 验证只读历史。这样覆盖实际 OpenClaw 较大的包描述，避免小型 fixture 掩盖读取上限问题。包元数据检查每文件最多 512 KiB，超限、截断与尾部多余 JSON 有独立测试。发送测试使用受控本地模型，包含真实 Gateway/bridge；不代表 DeepSeek 在线服务或实体双机验收。原生发送支持边界同时见[本机聊天验证](../local-node-chat/validation.md)。

## 人工验收与支持边界

本机聊天使用已通过用户验收。以下保留全新节点、设置、任务与双机等独立场景的复验步骤，不以日常聊天验收替代专项覆盖。

1. 在隔离节点不配置 key，启动并打开主页、设置、历史；节点正常运行且不会自动弹模型设置。
2. 填写有效模型配置后直接发送聊天；配置授权目录后直接提交文件任务。确认没有先行模型工具测试；真实调用的鉴权、额度或网络失败应显示在该请求上。
3. 仅更新应用源码并安装，确认旧诊断过期，已配置功能继续可用。显式测试仍可运行并展示结果，退出时可取消。
4. 两台 Windows 配对后，在 B 缺模型配置时提交任务，A 显示明确拒绝并停止投递；配置修复后由用户重新提交。响应丢失时保持原请求对账，不重新生成任务。
5. 检查旧节点没有 capabilities 字段时的兼容提示；新节点能力不可用即使 ready 为 true 也不能提交新请求。

自动化覆盖隔离实例与受控运行时；真实 API、实体双机、安装升级、物理托盘/快捷键和多屏环境仍需对应人工验收。发布验证保留 [verify-live.ps1](../../../../scripts/verify-live.ps1) 的显式 probe 与真实文件任务。
