# CLI 与 MCP 使用

命令的 `notify_on_completion` 为布尔值，默认 true。通过 `commands.patch` 与当前命令修订传入 `"notify_on_completion": false` 可关闭完成提醒，传入 true 可重新开启；省略字段保留当前值。创建、复制、读取以及 `drafts.get/update/commit` 使用同一开关。通知只显示在运行节点的 Windows 桌面，运行结果仍用 `runs.wait/get` 和 `logs.read` 读取；`settings.get` 的 `notifications` 字段可检查桌面展示是否可用。

在 Codex 等外部客户端中配置 Asterhand 的 MCP 服务，或直接调用 CLI。提示词指定工作对象。两者使用同一个可执行文件，按 Windows 当前用户连接本机节点。先用当前版本启动一次节点，使客户端可以找到本机管道和证书。

## CLI

以下路径为占位值。使用默认节点目录时可以省略 `--home`。已安装节点（Windows）优先使用安装根目录无版本号的稳定 CLI 转发入口 `<安装目录>\asterhand-cli.exe`（可从 `capabilities` 返回的 `cli_path` 读取，默认位于 `%LOCALAPPDATA%\Programs\Asterhand\asterhand-cli.exe`），它会自动读取同目录 `installation.json` 并向当前活动版本 `asterhand.exe` 注入 `--home <Data>`；开发或源码直跑环境可直接调用 `asterhand.exe`。帮助、schema 和生成操作 ID 不要求节点在线；命令、脚本图、运行与日志的完整字段正则、取值范围、节点约束及 Windows Job 语义均由 `automation schema <操作>` 直接给出。

```powershell
& '<安装目录>\asterhand-cli.exe' automation help
& '<安装目录>\asterhand-cli.exe' automation schema commands_patch
& '<安装目录>\asterhand-cli.exe' automation new-id
& '<安装目录>\asterhand-cli.exe' automation commands_list --home '<节点数据目录>'
& '<安装目录>\asterhand-cli.exe' automation workers_list --caller codex --args '{"include_closed":false}'
& '<安装目录>\asterhand-cli.exe' wait <turn_id> --caller claude-code --timeout 1800
```

业务参数可用 `--args '<JSON>'` 单行传入（适合简短参数），也可用 UTF-8（无 BOM）JSON 文件传给 `--input <文件>` 或 `--input -` 从 stdin 读取（两者互斥；包含非 ASCII 字符或长文本时优先使用 `--input` 文件或 stdin）；受管 Coding Harness 调用可通过 `--caller <harness_id>` 沿用三开关受限权限。stdout 是单个 JSON 响应；stderr 是诊断、首次写入前生成的 `{"operation_id":...}` 以及 `wait` 状态切换单行进度（在 PowerShell 中调用时注意避免因 `stderr` 输出触发 `NativeCommandError`）。跨进程重试时应先生成并保存操作 ID，再显式传 `--operation-id`，以便进程或连接中断后核对原请求。

例如先用 `commands_get` 读取目标的 `id` 和 `revision`，再提交如下 `commands_patch` 参数；图的步骤、边和属性也可在同次 patch 内修改，所有修改通过验证后一次保存。

```json
{
  "command_id": "<命令ID>",
  "expected_revision": "<刚读取的修订>",
  "description": "由 agent 更新的说明"
}
```

`commands_run` 使用同样的 `command_id` 和 `expected_revision`，返回独立 `run_id`。用 `runs_get` / `runs_wait` 查询，`runs_stop` 明确列出 `run_ids`；读日志用 `logs_read`，参数包含 `command_id`、`run_id`，可加 `step_id`、`stream` 和 `encoding`。把 `next` 原样用于后续请求，不修改游标。`at_end` 只表示到达当前日志末尾，`finished` 才表示执行已结束且解码尾部已处理。

长耗时多 Harness Worker 轮次（如 10–30 分钟）推荐在后台 shell 执行 `asterhand-cli.exe wait <turn_id> [<turn_id2> ...] [--caller <harness_id>] [--workspace-id <id>] [--timeout 1800] [--poll-seconds 30] [--wake-on-commentary] [--after-commentary-seq N] [--disconnect-grace 60] [--quiet]`：该命令内部循环切片等待并在节点短暂重启恢复期间按 `--disconnect-grace` 退避重试，直到轮次到达终态、`reconciliation_required`、`waiting_input`（且尚未自动转人工）、命中 `--wake-on-commentary` 新旁白或总超时才退出并向 stdout 输出单个紧凑 JSON。

退出码：0 成功，2 参数/版本问题，3 授权问题，4 冲突，5 结果未知，6 容量限制，7 节点不可用或实例改变，8 阻塞等待总超时（`wait_timeout`，stdout 仍保留最新快照 `data`），其余失败为 1。业务判断以 JSON 的 `error.code` 和收据为准。

## MCP

在支持 stdio MCP 的客户端中优先配置安装根目录的稳定转发入口 `asterhand-mcp.exe`（或开发环境下直连 `asterhand.exe`）。下例中的路径为占位值；具体外层配置位置由使用的客户端决定。

```json
{
  "mcpServers": {
    "asterhand": {
      "command": "C:\\<安装目录>\\asterhand-mcp.exe",
      "args": []
    }
  }
}
```

工具名称与 CLI 操作名称统一使用下划线规范名（`[a-zA-Z0-9_-]`），输入参数来自同一 schema；写工具的 `operation_id` 为可选字段——省略时由 `asterhand-mcp` 自动按节点规范时钟生成并随成功响应或断连 `result_unknown` 报错原样返回；若需要调用前预先固化跨进程重放身份，仍可先调用 `operations_new_id`（对应 `node_start` 传 `{"operation":"node_start"}`）再显式传入。MCP 请求编号只识别本次通信，不能替代业务操作 ID。结果在 `structuredContent` 与有界 `content[].text` 中双通道返回，业务错误设置 `isError`；守护进程短暂重启期间已注册工具清单保留不清除，仅在显式鉴权拒绝或策略撤销时即时收回。敏感操作不在工具列表内。

## 续改未保存内容

例如在 Codex 中说“把我正在编辑的脚本图描述补全并保存”。agent 调用 `drafts.list` 按业务目标找到编辑器，对它调用 `drafts.get`。修改返回内容后，用 `drafts.update` 提交 `draft_id`、`expected_revision` 和完整 `content`。要保存或发送，用最新修订调用 `drafts.commit`。这些写操作使用与其他工具相同的 `operation_id`。

未打开编辑器时，使用 `commands.get/patch` 等已保存对象接口。多个窗口匹配时应明确指定目标。编辑器关闭、内容变化或正在组字时，重新读取状态后决定下一步。详见[编辑器接口](draft-handoff.md)。

## 批量、长内容和恢复

批量删除命令：先用 `commands.list` 遍历全部分页并固定命令 ID/修订；按需筛选，或全选这个快照。每批最多 32 项，向 `batches.submit` 提交以下参数，随后用返回的 `batch_id` 调用 `batches.get`。CLI 用 `automation batches.submit --input <JSON文件> --operation-id <操作ID>`，MCP 调用同名工具并附 `operation_id`。更多项按相同方式分块，逐项检查收据；运行中及修订冲突的命令保留，不删除外部脚本。

```json
{
  "on_error": "continue",
  "items": [
    {"operation": "commands.delete", "arguments": {"command_id": "<命令ID>", "expected_revision": "<对象修订>"}}
  ]
}
```

`batches.submit` 接收最多 32 个 `{operation, arguments}` 项及 `on_error: stop|continue`，预检查全部项目的操作类别与授权，最多四项同时分派；`batches.get` 分页返回每项身份和收据。遇错停止不撤销已接纳项目，节点重启不自动续发未分派项目。`read.many` 接收最多 100 项只读请求，逐项返回结果和错误。单图修改是原子的，多对象批次没有整体事务或回滚。

普通响应最多 64 KiB，完整命令/草稿读取和候选校验允许 1 MiB。大图可对 `commands.get` 或 `drafts.get` 指定 `format: json_fragments`，按游标拼接 `text` 后解析 JSON；内容或修订改变会拒绝旧游标，重新读取整份快照。消息、任务正文和事件也有片段接口。所有序列/字节偏移使用字符串或不透明游标，不把大整数转为 JavaScript 普通数值。

写入中断后，用 `operations.get` 查询原 `operation_id`。重复写只返回原收据；同 ID 改参数会冲突。`queued` 表示节点内存托管，`accepted`/原生回执才表示运行时接收；不要把接纳当作执行成功。未找到且超过十分钟接纳窗口的请求也不会被当成新操作重做。收据、历史和大日志的保留预算见[设计](design.md)。

普通查询不会启动离线节点。显式 `node.start` 需要已授权的安装记录，并使用 `start:` 身份：离线执行 `automation new-id node.start`，或在线调用 `operations.new_id` 并传 `{"operation":"node.start"}`。启动只复用原安装和原节点目录，重复未知请求只检查健康状态，不另起实例。未安装的开发节点不自动寻找或运行任意程序。`node.stop` 要求当前实例 ID；存在活跃工作时还须显式 `stop_active: true`。

`tasks.resolve` 与 Web 的关闭未知结果操作共用同一任务状态检查。提供处置原因，结果记录为失败、执行结果未知。认证、凭据、加密和配对仍由人类在原入口处理。

完整操作分类见[覆盖清单](coverage.md)，可复现测试和真实设备验收见[验证说明](../development/features/agent-automation/validation.md)。
