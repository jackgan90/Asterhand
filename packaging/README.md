# 发布与依赖

dependencies.json 锁定直接组件、来源和摘要；runtime-lock.json 是 npm 生成的完整传递依赖锁，不受手写文件行数限制。go.mod/go.sum 锁定 Go 依赖。

当前清单 schema 2：components 是具名组件列表（manager/package/version/source/integrity/license），bundle.targets 是待检查的 runtime/provider/model/credential_ref 组合，default_target 指定新节点默认选择。公共准备脚本、锁检查和包诊断遍历描述；厂商映射留在实际 adapter/bridge。当前只实现 npm 安装技术和 DeepSeek + OpenClaw 生产适配器。

由同一 dependencies.json 驱动 Go 内嵌诊断、构建和安装。[升级流程](upgrade.md) 提供隔离准备、清单错误拒绝、契约/真实模型/安装报告；未运行项会阻止 upgrade_ready，不自动更新活动运行时。

Windows 完整包保留 Node LICENSE、npm 模块原有 LICENSE/NOTICE；Go 依赖许可随包归档。Tailscale 不再分发，安装时从官方地址校验后安装。项目 MIT License 原文保持不变。

start.ps1 是隐藏启动和 health 检查入口；configure-firewall.ps1 是独立明确确认的 Tailnet TCP 17433 规则操作。源码打包使用 scripts/build.ps1，用户操作见 docs/installation/quickstart.md。
