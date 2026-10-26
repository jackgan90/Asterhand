# 平台与支持范围

以下为 Windows 助手版本 0.1.0-alpha.1 的支持边界。精确依赖由 [依赖清单](../../packaging/dependencies.json) 和 [运行时锁文件](../../packaging/runtime-lock.json) 管理，验证操作见 [测试指南](../../tests/README.md)。

| 平台或能力 | 支持与验证边界 |
|---|---|
| Windows x64 | 完整助手目标；工程测试、真实模型文件/问答/取消及 PowerShell 5.1 安装升级已验证 |
| 两台 Windows / Tailscale | 用户已于 2026-10-25 确认通信和指令执行正确并接受该版本；物理故障场景未单独提供逐项记录 |
| Windows PowerShell 5.1 | 安装器最低版本；PowerShell 7 用于构建与依赖维护 |
| Windows 全局快捷输入 | 独立非提权宿主已实现，默认 Ctrl+Alt+J、两种主题；主进程保留已接受草稿，2026-10-26 本机用户验收通过。隐藏 CPU 预算保留 failed，其余权限配置、多屏/会话和干净 Runtime 新装边界见[隔离验证](../development/features/quick-input-process-isolation/validation.md) |
| Linux amd64 | 保留实验源码；当前发布注册表禁用，未提供正式安装附件，原生安装与权限未验证 |
| macOS | 基础 CLI 已交叉构建；快捷输入明确 unsupported，未原生构建/实测，发布注册表禁用 |
| 手机浏览器 | 已有响应式布局；远程认证访问及物理设备未验证 |
| 文件工具 | 创建与读取；拒绝覆盖。未提供删除工具，删除请求执行失败属于能力边界 |
| 运行时恢复与审批 | 未提供原生 resume 或远端审批；未知执行状态进入人工对账 |

## 依赖职责

Go 只用于开发构建；SQLite 驱动编入程序。Windows 安装包包含 Node.js、OpenClaw、DeepSeek provider 与相应许可证。Microsoft VC++ x64 运行库缺失或版本不足时按固定清单补齐，并实际加载原生 Node 模块检查 DLL 依赖；Tailscale 缺失时按固定清单引导安装，已有安装复用。模型凭据在各节点设置页配置。

发布安装的自动化验证见 [公开下载工作流](../../.github/workflows/release-install-smoke.yml)。Windows 托管运行器从无 Asterhand/Tailscale 状态验证依赖新装，并隔离开发工具 PATH；其系统版本与检查结果写入工作流摘要。Windows 10/11 桌面全新安装、UAC 和重启按人工规程验收。

其他模型选项、新 provider、其他 OS 或网络拓扑需要独立验证。交叉编译、单机多实例和用户接受分别记录；用户接受不会自动扩展支持矩阵。
