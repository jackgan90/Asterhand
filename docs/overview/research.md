# 研究来源与复用线索

原草稿调研日期为 2026-10-24，未在实际设备验证；主分支文档可能不同于发布版本。本文件保存来源，不将历史描述升级为本项目已实现能力。首次实施必须固定版本并实测。

## 已收敛的复用方向

- OpenClaw：首版助手运行时，正式接口与能力见 [执行器设计](../executors/design.md)。
- Hermes：历史备选，首版已选 OpenClaw，不在首版同时接入。
- Codex / Claude Code / ACP：后续 coding adapter 研究线索，不是首版前置。
- A2A：长期互操作方向；当前先用版本化 HTTP + SSE。
- EnvoyMesh / HAPI：架构与交互参考；不因功能列表认定满足本项目可靠性要求。
- Headscale / iroh / ntfy：按未来网络和通知缺口评估，当前不新增网络基础设施。
- Everything：后续 Windows 工具连接器线索，非当前文件创建验收前置。

原草稿关于 A2A 内存 TaskStore、OpenClaw paired-device remote-exec 断连行为、Hermes dispatcher、移动后台及微信插件限制等结论均属特定时点线索。实际采用前逐项核对对应版本，不能据此直接实现接口。

## 补充官方入口

- [OpenClaw Windows](https://docs.openclaw.ai/platforms/windows)
- [OpenClaw Secrets](https://docs.openclaw.ai/gateway/secrets)
- [Go 平台与构建](https://go.dev/doc/install/source)
- [Go cgo](https://pkg.go.dev/cmd/cgo)

外部资料用于理解接口与方案；产品支持以 [平台矩阵](../testing/platforms.md) 为准，依赖变化时重新验证。

## 原草稿来源清单 S01—S30

以下为技术方案的来源链接。实施时应复核实际版本，尤其不要把主分支能力自动当作已安装版本的能力。

| 编号 | 资料 | 链接 |
|---|---|---|
| S01 | OpenClaw Gateway architecture | [打开资料](https://docs.openclaw.ai/concepts/architecture) |
| S02 | OpenClaw Multi-agent routing | [打开资料](https://docs.openclaw.ai/concepts/multi-agent) |
| S03 | OpenClaw A2A | [打开资料](https://docs.openclaw.ai/channels/a2a) |
| S04 | Hermes A2A | [打开资料](https://hermes-agent.nousresearch.com/docs/user-guide/messaging/a2a) |
| S05 | Hermes A2A TaskStore 源码 | [打开资料](https://github.com/NousResearch/hermes-agent/blob/main/plugins/platforms/a2a/protocol.py) |
| S06 | Hermes Profiles | [打开资料](https://hermes-agent.nousresearch.com/docs/user-guide/profiles) |
| S07 | Hermes Kanban Multi-Gateway | [打开资料](https://hermes-agent.nousresearch.com/docs/user-guide/features/kanban-multi-gateway) |
| S08 | OpenClaw Codex harness | [打开资料](https://docs.openclaw.ai/plugins/codex-harness) |
| S09 | OpenClaw ACP agents | [打开资料](https://docs.openclaw.ai/tools/acp-agents) |
| S10 | Hermes Codex App-Server Runtime | [打开资料](https://hermes-agent.nousresearch.com/docs/user-guide/features/codex-app-server-runtime) |
| S11 | Codex App Server 官方文档 | [打开资料](https://developers.openai.com/codex/app-server) |
| S12 | Claude Code 程序化运行 | [打开资料](https://code.claude.com/docs/en/headless) |
| S13 | A2A 规范 | [打开资料](https://a2a-protocol.org/latest/specification/) |
| S14 | Agent Client Protocol | [打开资料](https://agentclientprotocol.com/protocol/v1/overview) |
| S15 | Apple iOS Background Execution Limits | [打开资料](https://developer.apple.com/forums/thread/685525) |
| S16 | Android 前台服务后台启动限制 | [打开资料](https://developer.android.com/develop/background-work/services/fgs/restrictions-bg-start) |
| S17 | OpenClaw WeChat | [打开资料](https://docs.openclaw.ai/channels/wechat) |
| S18 | Hermes Weixin | [打开资料](https://hermes-agent.nousresearch.com/docs/user-guide/messaging/weixin) |
| S19 | Hermes Platform Support | [打开资料](https://hermes-agent.nousresearch.com/docs/getting-started/platform-support) |
| S20 | EnvoyMesh 主仓 | [打开资料](https://github.com/allenpeng0705/EnvoyMesh) |
| S21 | HAPI FAQ | [打开资料](https://hapi.run/docs/guide/faq) |
| S22 | Headscale 主仓 | [打开资料](https://github.com/juanfont/headscale) |
| S23 | Tailscale 协调服务不可用时的行为 | [打开资料](https://tailscale.com/docs/reference/coordination-server-down) |
| S24 | iroh FAQ | [打开资料](https://docs.iroh.computer/about/faq) |
| S25 | MCP 架构 | [打开资料](https://modelcontextprotocol.io/docs/learn/architecture) |
| S26 | OpenClaw DeepSeek provider | [打开资料](https://docs.openclaw.ai/providers/deepseek) |
| S27 | OpenClaw Skill Workshop | [打开资料](https://docs.openclaw.ai/tools/skill-workshop) |
| S28 | Everything ES CLI | [打开资料](https://www.voidtools.com/support/everything/command_line_interface/) |
| S29 | OpenClaw Codex 远程执行 placement | [打开资料](https://docs.openclaw.ai/plugins/codex-harness/placement) |
| S30 | ntfy iOS 即时通知 | [打开资料](https://docs.ntfy.sh/config/#ios-instant-notifications) |

[S01]: https://docs.openclaw.ai/concepts/architecture
[S02]: https://docs.openclaw.ai/concepts/multi-agent
[S03]: https://docs.openclaw.ai/channels/a2a
[S04]: https://hermes-agent.nousresearch.com/docs/user-guide/messaging/a2a
[S05]: https://github.com/NousResearch/hermes-agent/blob/main/plugins/platforms/a2a/protocol.py
[S06]: https://hermes-agent.nousresearch.com/docs/user-guide/profiles
[S07]: https://hermes-agent.nousresearch.com/docs/user-guide/features/kanban-multi-gateway
[S08]: https://docs.openclaw.ai/plugins/codex-harness
[S09]: https://docs.openclaw.ai/tools/acp-agents
[S10]: https://hermes-agent.nousresearch.com/docs/user-guide/features/codex-app-server-runtime
[S11]: https://developers.openai.com/codex/app-server
[S12]: https://code.claude.com/docs/en/headless
[S13]: https://a2a-protocol.org/latest/specification/
[S14]: https://agentclientprotocol.com/protocol/v1/overview
[S15]: https://developer.apple.com/forums/thread/685525
[S16]: https://developer.android.com/develop/background-work/services/fgs/restrictions-bg-start
[S17]: https://docs.openclaw.ai/channels/wechat
[S18]: https://hermes-agent.nousresearch.com/docs/user-guide/messaging/weixin
[S19]: https://hermes-agent.nousresearch.com/docs/getting-started/platform-support
[S20]: https://github.com/allenpeng0705/EnvoyMesh
[S21]: https://hapi.run/docs/guide/faq
[S22]: https://github.com/juanfont/headscale
[S23]: https://tailscale.com/docs/reference/coordination-server-down
[S24]: https://docs.iroh.computer/about/faq
[S25]: https://modelcontextprotocol.io/docs/learn/architecture
[S26]: https://docs.openclaw.ai/providers/deepseek
[S27]: https://docs.openclaw.ai/tools/skill-workshop
[S28]: https://www.voidtools.com/support/everything/command_line_interface/
[S29]: https://docs.openclaw.ai/plugins/codex-harness/placement
[S30]: https://docs.ntfy.sh/config/#ios-instant-notifications
