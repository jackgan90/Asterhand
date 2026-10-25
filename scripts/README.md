# 构建脚本

Go 1.27.3：`go test ./...`、`go vet ./...`、`go build ./cmd/asterhand`。完整包在 Windows PowerShell 7 执行。先按 [依赖维护流程](../packaging/upgrade.md) 生成 prepared.json；从该文件读取依赖目录，Go 使用 PATH 中与清单一致的版本：

```powershell
$prepared = Get-Content .\out\prepared-upgrade\prepared.json -Raw | ConvertFrom-Json
.\scripts\build.ps1 -Go go -NodeDistribution $prepared.node_distribution -RuntimeRoot $prepared.runtime_root -OutputDir .\out\candidate
```

RuntimeRoot 包含 package.json、与 packaging/runtime-lock.json 完全匹配的 package-lock.json 和按锁安装的 node_modules；安装 OpenClaw 和 @openclaw/deepseek-provider 2026.10.7。NodeDistribution 使用依赖清单中已验证摘要的官方 Windows 发行目录。

构建脚本验证版本与锁文件，产生 Windows 完整 zip、Linux 基础 tar.gz、安装脚本、release-manifest.json、SHA256SUMS 和操作指南。Linux 只表示交叉编译，发布前必须保留该标记。out 下 staging 供人工检查，按需清理自己生成的目录。

prepare-dependencies.ps1（隔离准备）、verify-dependencies.ps1（统一报告）、verify-live.ps1（真实 probe/文件/问答/取消）、test-install.ps1（PowerShell 5.1 安装/升级回归）、dependency-tools.ps1 / test-dependency-tools.ps1（共用清单检查与拒绝测试）。完整命令见 [升级流程](../packaging/upgrade.md)。

schema 2 清单使用通用 components、bundle.targets/default_target；准备和锁检查遍历声明，拒绝未声明的直接依赖及未知安装技术。test-dependency-tools 同时覆盖任意名称的 fixture，安装回归从节点配置读取 provider/runtime/credential_ref，不写死厂商与凭据文件名。

Windows 测试需要当前用户 DPAPI、私有目录和进程权限。受限执行环境导致的 ACL 拒绝记为环境失败，不删掉权限测试绕过。安装回归跳过 Tailscale/防火墙，不替代干净双机验收。

`test-install-paths.ps1` 在 Windows PowerShell 5.1 中使用真实候选，强制 PowerShell 当前目录与 .NET 进程目录不同，覆盖相对/绝对包、自动/显式清单、中文空格目标目录、重复安装数据保留及非文件 provider 拒绝：

```powershell
powershell.exe -NoProfile -File .\scripts\test-install-paths.ps1 -Installer .\install.ps1 -PackageDirectory .\out\candidate -OutputDirectory .\out\install-path-check
```

这些示例目录是由构建和测试命令生成的输出，由执行者选择；不依赖仓库作者的本机文件。可用 BaselineInstaller 参数提供旧安装器比较路径行为。测试不联网、不启动服务、不调用模型；test-install.ps1 在完整安装检查前自动执行路径回归。
