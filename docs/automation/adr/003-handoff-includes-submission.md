# ADR-AUTO-003：交出草稿即允许提交

状态：superseded，由[外部客户端直接操作](004-external-clients-without-ui-workflow.md)替代。

人类将未完成的草稿交给指定 agent，已经表达了允许它完成这份工作的意图。这一次交出允许接手、修改并提交该草稿。保存图、发送消息、提交任务分别由对应业务操作执行，仍检查调用者权限、目标与修订。人类取回后，原租约失效。

本决定替代 [ADR-AUTO-002](002-draft-handoff.md) 中要求再次允许提交的部分。具体交互见[草稿交接设计](../draft-handoff.md)。
