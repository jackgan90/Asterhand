# 执行器与工具设计

状态：接口语义提案，当前没有接口代码或具体 harness 实现。

## 最小契约

| 操作 | 语义 |
|---|---|
| capabilities | 明确实际支持的事件、交互、取消、查询与恢复能力 |
| start | 接受带 attempt_id 的执行说明，启动或返回明确失败 |
| query | 查询现有执行，不隐式启动或恢复 |
| observe_events | 提供可转换成 Asterhand 事件的进度 |
| answer_input | 回答仍有效的指定请求 |
| resolve_approval | 将对应人类决定应用到特定操作，不扩大权限 |
| cancel | 请求停止，返回真实确认程度 |
| reconcile / resume | 先核对；只有明确支持并授权时才继续现有工作 |

具体类型、方法名及不支持错误在 M0 固定。capabilities 描述事实，不用空实现伪造能力。不支持恢复时返回明确结果，由任务服务进入待核对。

## 首版 OpenClaw adapter

由 B 的任务服务持有 attempt 与运行时会话映射，使用独立执行上下文；A 的连接不持有 B 的执行生命周期。OpenClaw 管内部模型/工具循环，adapter 负责生命周期事件与交互转换。

runtime 的自动重试必须限制在不会额外启动 job 的边界内。工具副作用重复执行需使用稳定 operation_id 或明确对账，不能只保证 HTTP submit 去重。

## 文件工具

首版逻辑操作为创建 UTF-8 文本与读取文本，参数包含 project_id、相对路径、内容和明确写入策略。默认 create-only，文件已存在返回可理解冲突，不覆盖用户文件。

目标节点解析授权根目录，在执行处校验最终路径，防止路径穿越、绝对路径绕过、符号链接与 junction 逃逸。测试目录与源码 / 凭据目录分离。若路径校验与写入存在竞争，使用可验证的安全文件访问策略，M1 需针对平台实测。

记录实际位置、字节数、内容摘要和执行关联。文件创建后读取验证，结果预览由实际文件取得。相同内容的已有文件本身不足以证明本 attempt 已成功，必须有操作关联证据。

编码固定 UTF-8；hello-world 场景精确为 `hello world`，不隐式增加 BOM 或换行。设置文本大小上限，数值随 M0 契约固定。

首次问文件名的场景由 B 产生持久 interaction，得到回答后才能写入。验收延迟是隔离验收配置下的确定性工具行为，不让模型猜测等待时间，不在正常工具界面开放任意延迟脚本。

## 后续连接器

Everything 按 exact_name、scope、max_results 接口返回覆盖范围、索引状态、截断和错误，使用参数化 CLI / SDK 调用；非首版必做。

Coding harness 后续按能力接入，才讨论模型认证、工作区 worktree、diff、验证和进程树管理。首版保留这些扩展方向，不提供空的 Codex adapter。

FakeExecutor 与独立进程 fixture 的设计见 [测试框架](../testing/design.md)。
