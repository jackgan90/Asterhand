# 决策记录

本目录只记录本领域决策；当前有效行为见 [requirements](../requirements.md) 和 [design](../design.md)。

- [001-openclaw-deepseek — OpenClaw 与默认 DeepSeek](001-openclaw-deepseek.md)

新增决策使用 [ADR 模板](../../development/templates/adr.md)。历史决定通过新 ADR 替代，不改写历史理由。

- [002-agent-exec-runtime — agent exec 与受限回调插件](002-agent-exec-runtime.md)
- [003-provider-runtime-bundle-boundaries — 模型、agent 与安装 bundle 分层](003-provider-runtime-bundle-boundaries.md)
- [004-provider-neutral-public-flows — 公共配置、目录保护和包描述收口](004-provider-neutral-public-flows.md)
- [005-conversation-runtime-and-transport-boundaries — 持续会话的运行时、存储与通信边界](005-conversation-runtime-and-transport-boundaries.md)

- [ADR-AST-006：可选的会话提示词策略](006-optional-conversation-prompt-policy.md)。
- [ADR-AST-007：保留原生 Harness 设定，仅适配产品冲突](007-preserve-native-harness-prompts.md)。
- [ADR-AST-008：提示词诊断与聊天展示分离](008-keep-prompt-diagnostics-out-of-chat.md)。
- [ADR-AST-009：模型按需配置，诊断与功能准入分离](009-on-demand-model-readiness.md)。
