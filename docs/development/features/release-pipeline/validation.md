# 发布与安装验证

## 自动化入口

- [test-release.ps1](../../../../scripts/test-release.ps1)：禁用平台拒绝、本地内容篡改、标签提交冲突和注释标签解析、上传失败保留草稿、相同草稿重试、同大小远端篡改拒绝、公开版本拒绝覆盖；CLI 边界用可控替身，真实上传另外验证。
- [test-installer-system-dependencies.ps1](../../../../scripts/test-installer-system-dependencies.ps1)：复用兼容 VC++、缺失时安装、旧版升级、错误签名拒绝、安装退出码与后置检查、原生模块路径越界；模拟 Windows 系统安装边界，不修改本机运行库。
- [test-install.ps1](../../../../scripts/test-install.ps1)：Windows PowerShell 5.1 首次/重复安装、相对路径、中文空格目录、节点启动、身份与凭据保留；系统网络安装跳过。已有包可通过 PreviousPackageDirectory 增加升级验证。
- [test-release-download.ps1](../../../../scripts/test-release-download.ps1)：只下载公开的安装脚本，通过隔离 PATH 验证无开发工具安装、组件检查、配置未初始化和重复安装数据保留。
- [公开下载工作流](../../../../.github/workflows/release-install-smoke.yml)：在新 Windows 托管运行器运行完整 Tailscale 首次安装，工作流摘要显示具体系统和结果。

```powershell
.\scripts\test-release.ps1 -OutputDirectory .\out\release-contract
.\scripts\release.ps1 -Mode Local -OutputDirectory .\out\local-release
.\scripts\test-release-download.ps1 -Repository <owner>/<public-repository> -Tag <published-tag> -OutputDirectory .\out\public-install -RequireFreshDependencies
```

每次使用新输出目录。RequireFreshDependencies 要求没有既有 Asterhand/Tailscale，且会安装系统网络组件，需要 Windows 管理员许可。已有开发机可用 SkipTailscale，只验证匿名下载与应用安装；不得把该结果记为系统依赖新装通过。

VC++ 安装前后版本单独记录；原生模块由真实捆绑 Node 加载。工作流默认保留已有 VC++；启用 fresh_vc_runtime 时，仅在一次性的 GitHub 托管运行器移除预装运行库，确认 DLL 缺失后用 RequireFreshVCRuntime 检查新装。关闭该选项时只证明复用或升级；移除失败必须明确报告，并换用未安装该运行库的干净虚拟机完成验证。模拟边界测试不能替代缺失状态的真实安装。

## 真实 GitHub 验证

在专用公开测试仓库运行 Prerelease 流程，记录版本 URL 与附件集合。每个附件必须与本地 SHA-256/大小一致。公开后使用不带 GitHub 凭据的安装请求下载；在 Windows 托管运行器运行上述工作流。重试公开标签应在准备依赖前拒绝，确保公开版本不被覆盖。

## 结果与限制

发布目录中的 validation.json 只记录实际执行的自动化检查。干净系统、真实模型、两机测试分别记录；安装测试不配置模型、不配对，不消耗模型额度。后续公开下载验收结果记录在对应 GitHub 工作流摘要，不能回写覆盖已公开的发布附件。

原始日志保存在本次输出目录，文档只维护复现方法。Windows 托管系统和 Windows 10/11 桌面 UAC/重启体验分别验证；Linux/macOS 未启用。开发、用户验收、源码提交、推送和正式发布仍为独立状态。
