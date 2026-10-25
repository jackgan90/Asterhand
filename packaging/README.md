# 发布与依赖

dependencies.json 锁定直接组件、来源和摘要；runtime-lock.json 是 npm 生成的完整传递依赖锁，不受手写文件行数限制。go.mod/go.sum 锁定 Go 依赖。

当前清单 schema 2：components 是具名组件列表（manager/package/version/source/integrity/license），bundle.targets 是待检查的 runtime/provider/model/credential_ref 组合，default_target 指定新节点默认选择。公共准备脚本、锁检查和包诊断遍历描述；厂商映射留在实际 adapter/bridge。当前只实现 npm 安装技术和 DeepSeek + OpenClaw 生产适配器。

由同一 dependencies.json 驱动 Go 内嵌诊断、构建和安装。[升级流程](upgrade.md) 提供隔离准备、清单错误拒绝、契约/真实模型/安装报告；未运行项会阻止 upgrade_ready，不自动更新活动运行时。

Windows 完整包保留 Node LICENSE、npm 模块原有 LICENSE/NOTICE；Go 依赖许可随包归档。Tailscale 不再分发，安装时从官方地址校验后安装。项目 MIT License 原文保持不变。

start.ps1 是固定 GUI 启动器的脚本入口，传递 NoBrowser 并检查启动器退出码；AsterhandLauncher.exe 负责 health 确认和重复启动复用。桌面与登录后启动快捷方式指向固定启动器，后者使用 --no-browser；托盘嵌入节点进程。旧包没有启动器时安装器沿用该包自带脚本，不为它生成桌面入口。

图标源与多尺寸 ICO 在 [图标资源](../internal/desktop/assets/README.md) 中维护；构建将图标写入节点与启动器 PE 资源，随后计算包摘要。configure-firewall.ps1 仍为独立明确确认的 Tailnet TCP 17433 规则操作。源码打包使用 [构建指南](../scripts/README.md)，用户操作见 [安装指南](../docs/installation/quickstart.md)。
