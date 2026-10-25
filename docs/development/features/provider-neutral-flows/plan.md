# 提供者无关的公共流程

## 目标与范围

关联 R17、C12/C13、EXEC-07/08/09、INST-11/12/13。公共配置、CLI、授权目录保护和安装准备只依赖适配器及包描述，不按厂商名称分支。

## 设计

- Config.runtime_options 按 runtime ID 保存 JSON，由对应适配器解析校验；CLI 使用 agent-runtime/runtime-options。
- 旧 flat JSON 仅由迁移模块识别，保存时移除，身份及凭据文件保持。
- Runtime.ProtectedPaths 声明宿主、入口、工具插件及模型插件目录；公共授权目录校验拒绝重叠及可解析目录别名。
- dependencies.json schema 2 使用 components、bundle.targets/default_target；准备脚本核对完整直接依赖集合，包检查遍历声明的目标。
- 运行时工厂为编译期注册；不下载并执行未知代码插件。

权威文档：[执行器](../../../executors/design.md)、[安装](../../../installation/design.md)、[助手](../../../assistant/design.md)、[公共流程决策](../../../assistant/adr/004-provider-neutral-public-flows.md)。

## 验证与规模

见 [公共流程验证](validation.md)。配置迁移、目录校验与描述分派分别保持窄职责；CLI 和配置事务的规模理由见 [任务可靠性](../task-reliability/plan.md)、[双节点交互](../two-node-usability/plan.md)。生成锁文件不计为手写代码。
