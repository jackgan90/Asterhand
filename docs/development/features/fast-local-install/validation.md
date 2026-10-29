# 快速本地安装验证

契约及验收场景见[计划](plan.md)，当前安装行为以[安装设计](../../../installation/design.md)为准。

## 环境与方法

Windows x64、PowerShell 7、依赖清单锁定的 Go/Node；正式安装器另用 Windows PowerShell 5.1 验证。每次验证使用新建的隔离程序、数据和输出目录；系统 VC++/WebView2 已安装，跳过 Tailscale，不修改开发者桌面或活跃节点。基础节点与模拟边界测试不代表 OpenClaw/DeepSeek 的真实模型和双机验收。

覆盖真实源码指纹、应用与依赖缓存失效、路径/链接拒绝、激活失败保护及正常生命周期；另用新版完整构建验证共享组装与 PS5.1 兼容。需要允许 Windows 私有文件权限、进程身份读取和 junction 的运行环境；权限拒绝应记为环境阻塞，不能删除用例绕过。原始日志放在执行者选择的生成目录。

## 复现入口

以下示例中的 `$go` 指向依赖清单规定版本的编译器，`$preparedPath` 指向按[依赖维护流程](../../../../packaging/upgrade.md)生成的 prepared.json。`$validation` 必须是本次新输出位置；所有程序、数据、Known Folder 替代目录都留在该目录。

```powershell
$go = '<pinned-go.exe>'
$preparedPath = '<prepared.json>'
$validation = './out/fast-install-validation'
$prepared = Get-Content $preparedPath -Raw | ConvertFrom-Json
$repo = (Get-Location).Path
. ./scripts/dependency-tools.ps1
. ./scripts/local-build-cache.ps1
. ./scripts/local-runtime-cache.ps1
$source = Get-SourceFingerprint $repo
$application = Join-Path $validation 'application'
./scripts/build-application.ps1 -Go $go -Node (Join-Path $prepared.node_distribution 'node.exe') -OutputDirectory $application -SourceDigest $source
New-LocalApplicationReceipt (Resolve-Path $application).Path $source (Get-LocalRuntimeKey $repo)
./scripts/build.ps1 -Go $go -NodeDistribution $prepared.node_distribution -RuntimeRoot $prepared.runtime_root -OutputDir (Join-Path $validation 'full')
./scripts/test-fast-local-install.ps1 -OutputDirectory (Join-Path $validation 'boundaries')
./scripts/test-local-install-entry.ps1 -OutputDirectory (Join-Path $validation 'entry')
./scripts/test-local-candidate.ps1 -ApplicationDirectory $application -PreparedPath $preparedPath -Go $go -PackageDirectory (Join-Path $validation 'full') -OutputDirectory (Join-Path $validation 'lifecycle')
./scripts/test-local-source-rebuild.ps1 -LifecycleDirectory (Join-Path $validation 'lifecycle') -Go $go -OutputDirectory (Join-Path $validation 'source-rebuild')
powershell.exe -NoProfile -ExecutionPolicy Bypass -File ./scripts/test-install-lifecycle.ps1 -PackageDirectory (Join-Path $validation 'full') -OutputDirectory (Join-Path $validation 'ps5-lifecycle')
```

- [边界回归](../../../../scripts/test-fast-local-install.ps1)直接调用真实指纹、缓存与安装校验函数，验证 txt/Go/Web/插件、根 Go 文件、新增顶层包与资源、删除/重命名、生成目录排除、本地 Go replace 拒绝、陌生根目录、实际路径重叠、安装锁、损坏收据、版本/源码身份不匹配、路径越界及同源码 runtime generation 换代。运行时准备为受控替身，文件、摘要与 junction 为真实操作。
- [开发入口回归](../../../../scripts/test-local-install-entry.ps1)覆盖 Full 原有复用与失败保护、默认 Fast 构建/复用及安装器内部源码变化。源码 guard 必须在独立脚本作用域仍可调用，并在变化时阻止激活。
- [真实目录候选回归](../../../../scripts/test-local-candidate.ps1)使用真实程序、固定运行时、native module load、基础节点、标准启动器和正常退出；验证自定义数据、身份/token/文件与快捷方式状态保留、无效候选/收据版本或源码伪造/源码 guard 不停止旧节点，以及新 generation 的实际链接切换。提供 PackageDirectory 时验证 Fast→Full→Fast，省略时明确记 skipped。系统组件安装被拒绝，Known Folders 使用替代目录，浏览器被抑制。
- [源码重建与性能回归](../../../../scripts/test-local-source-rebuild.ps1)只接受已通过上述隔离生命周期的目录，复制源码后运行真实默认入口，再只修改副本中的内嵌 txt 并重复执行。检查 source_sha256 改变、应用重建、运行时持续命中，以及不变源码的应用复用；每次真实启动后校验源码并记录阶段和总耗时，结束正常停止测试节点。
- 正式 ZIP 必须由当前共享 builder 直接生成；[PS5.1 生命周期](../../../../scripts/test-install-lifecycle.ps1)、[桌面契约](../../../../scripts/test-installer-desktop.ps1)、[在线引导](../../../../scripts/test-installer-bootstrap.ps1)、[版本选择](../../../../scripts/test-installer-release-selection.ps1)、[系统依赖](../../../../scripts/test-installer-system-dependencies.ps1)和[WebView2](../../../../scripts/test-installer-webview2.ps1)按各自脚本声明的 PowerShell 版本运行。系统依赖/网络专项为替身，不执行系统安装。

性能以暖缓存普通源码修改的运行时复制量和 ZIP 次数为硬条件；源码变化需要应用构建，纯重复可复用应用。首次导入、联网准备和 Full 独立计量。查看每次入口生成的 timings.json 与 install.log，不能用替身执行耗时代表真实安装。

## 当前状态

实现与独立复审已完成，本地安装提速效果已通过用户实际使用验收。工程验证覆盖真实指纹/缓存/路径边界、Fast/Full 开发入口、隔离基础节点生命周期、同源码新 generation、源码重建/热重复、共享 builder 的真实完整包构建及 PS5.1 生命周期与 Fast/Full 互操作。

联网重新准备的真实 repair、干净机器系统组件安装、桌面兼容性专项、真实 OpenClaw/DeepSeek 及双机验收未在本次工程验证中执行。上述专项与发布验证仍需按相应流程单独完成。
