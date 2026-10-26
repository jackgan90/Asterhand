# ADR 007：快捷输入使用独立非提权宿主

- 状态：已接受，实施与验证范围见[进程隔离计划](../../development/features/quick-input-process-isolation/plan.md)。
- 替代：[ADR 006](006-global-hotkey-input-surface.md) 的进程内宿主和管理员降级决策；其本地编辑、配置事务、隐私与交互约束继续有效。

## 背景

节点可能按系统策略以管理员身份运行。进程内 WebView2 共享该令牌，原有标准用户检查会同时阻止面板与快捷键。独立 STA 线程不能建立权限和故障边界。可行性验证已覆盖受限 Medium 宿主初始化、输入法、管理员快捷键转发及 UI 进程异常后的重建。

## 决策

主进程继续持有配置、注册事务、原生内存草稿和恢复策略。独立 GUI 程序 `AsterhandInputHost.exe` 仅承载原生窗口、STA、WebView2 和页面桥。普通用户也使用该进程结构；Windows 启动适配层在管理员环境创建受限非提权 Medium 令牌，复核权限，设置新令牌的用户/SYSTEM 默认 DACL，不改变系统 UAC 或已有目录/桌面 ACL。

父子使用仅继承明确端点的匿名管道，协议有版本、代次、长度和队列边界。只允许输入面板所需的定型操作，不提供任意执行或通用原生代理。配套宿主必须来自相同版本目录并通过完整性与构建身份检查。UI 与浏览器进程树纳入自有生命周期，不按进程名清理其他应用。

草稿只存内存。主进程接受文本后确认，UI 故障恢复最后已接受的文本；未送达主进程或未完成的 IME 输入不承诺恢复。主动销毁保留快照屏障，主进程退出结束草稿生命周期。托盘依据实际注册和面板状态显示快捷键。

平台公共层只包含进程会话、激活意图、IPC、草稿和恢复语义。Windows 令牌、句柄、Job 与 WebView2 保留在 Windows 实现；macOS/Linux 继续明确不支持，须完成各自原生实现和验证再开放。

## 取舍与验证

增加一个可执行文件、IPC 与进程管理成本，换取权限兼容和明确的故障恢复边界。受限令牌不是同用户文件的完整沙箱；继续遵守页面来源、导航和桥权限限制，保持浏览器沙箱启用。[微软权限建议](https://learn.microsoft.com/en-us/microsoft-edge/webview2/concepts/security#recommended-privilege-level-for-webview2-host-applications)

验收覆盖标准用户、提权管理员、UAC 关闭管理员、窗口激活、实体 IME、草稿确认竞争、协议故障、父子异常退出和正式包完整性。原有资源预算不变；早期异常未再出现不能标为根因已修复。支持与未验证环境分别记录，详见[验证说明](../../development/features/quick-input-process-isolation/validation.md)。
