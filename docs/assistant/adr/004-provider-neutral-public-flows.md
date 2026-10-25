# ADR-AST-004 公共流程只依赖适配器与包描述

日期：2026-10-24。状态：accepted。补充并收口 [ADR-AST-003](003-provider-runtime-bundle-boundaries.md) 中仍遗留在公共配置和安装流程的厂商绑定；一次性 agent exec 与首版提供者范围保持。

## 决定

公共 Config 仅存储 runtime_options[运行时 ID] 的 JSON，专用字段在适配器定义，CLI 使用通用选择和 JSON 文件入口。旧 flat OpenClaw 字段仅在兼容读取器中迁移，已有显式新选项优先，保存后不再输出旧字段。默认 runtime/model 来自包描述；历史凭据引用仍由迁移规则保留。

适配器返回 ProtectedPaths。公共安全检查解析现有目录别名，统一验证授权根与节点数据/声明目录的双向重叠，设置及启动配置变更均使用同一检查。

依赖清单 schema 2 描述 components、bundle.targets 和 default_target。准备/校验脚本遍历组件，包诊断逐一分派目标；新提供者需要适配器/bridge、工厂注册、描述及测试，不在公共流程添加名称判断。静态工厂仍是审核边界，不加载任意远程代码。组件安装器按 npm 等技术类型分派，支持新的安装技术需单独实现。

## 验证与代价

用不在生产工厂中的任意名称 fixture 验证目录、包目标分派和依赖准备；fixture 不能代表新提供者已兼容。首版仍只实现 DeepSeek + OpenClaw，并运行真实模型和旧包升级回归。公共 JSON 改为 adapter 所有，类型错误由 adapter 显式拒绝；旧 CLI 专用参数停止支持，改用通用 runtime-options 文件。兼容旧持久配置，不承诺旧二进制读取新配置。
