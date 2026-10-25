# 发布维护指南

## 发布入口

维护者使用 Windows、PowerShell 7、Git 和官方 [GitHub CLI](https://cli.github.com/)。先用 `gh auth login` 登录有目标仓库发布权限的账号；也支持已有有效 Git Credential Manager 登录或进程环境中的 GH_TOKEN。凭据不写入发布目录。

从干净且已提交的源码执行；正式仓库中必须能访问对应源码提交。发布脚本不会替维护者提交或推送源代码。

```powershell
# 自动下载固定 Go/Node、安装锁定 npm 依赖、构建验证，然后上传草稿。
.\scripts\release.ps1 -Repository jackgan90/Asterhand -Mode Draft

# 公开测试版；应用版本仍由源码 Version 常量确定。
.\scripts\release.ps1 -Repository <owner>/<release-test-repository> -Mode Prerelease -Tag <unique-test-tag> -AllowDirty

# 正式版：要求干净源码、非预发布应用版本和匹配的 v<version> 标签。
.\scripts\release.ps1 -Repository jackgan90/Asterhand -Mode Stable
```

默认模式是 Draft；只有 Prerelease/Stable 会公开。开发期使用单独的公开测试仓库，创建过一次 README 或初始提交即可。测试仓库标签可指向其默认分支；清单记录来源提交、源码摘要与 dirty 状态，不额外上传未提交源码快照。`-AllowDirty` 只用于测试，不用于 Stable。`-TargetCommit` 可显式指定目标仓库的提交；Stable 必须与本地干净 HEAD 相同，源码由该提交追溯。

`-Mode Local` 只生成附件，不连接 GitHub。`-Go` 可使用已有且版本匹配的构建器；省略时下载清单中固定版本。`-Gh` 可指定 CLI 路径。`-OutputDirectory` 必须是新目录，避免混入旧产物；缺省输出到仓库忽略目录中。

仓库内的输出目录必须被 Git 忽略；也可选择仓库外的新目录。构建目录生成独立 go.mod 边界，避免 `go test ./...` 遍历解压的工具链和第三方模块。Go 编译/模块缓存复用显式环境变量，未指定时使用忽略的共享缓存；依赖版本与完整性仍由清单和 go.sum 校验。

需要分两步操作时，对目标模式加 `-PrepareOnly`，先完成全部本地准备与验证而不连接 GitHub；随后使用相同模式、仓库、标签与 `-ResumeDirectory`，省略 PrepareOnly，完成上传。准备与上传之间源码和附件必须保持不变。

运行工具与组件版本以 [依赖清单](../../packaging/dependencies.json) 为准。Node、Go 下载使用固定 HTTPS 地址和 SHA-256；npm 使用 [锁文件](../../packaging/runtime-lock.json) 与完整性检查，禁用生命周期脚本。依赖升级须另外完成 [真实运行验证](../../packaging/upgrade.md)，一次成功上传不替代依赖兼容性验收。

Windows 准备阶段还下载并校验 Microsoft VC++ x64 与 Tailscale 官方安装器的固定摘要和有效供应商签名，确认新机器所需的外部下载当前可用。安装器读取同一清单；VC++ 版本不足时补齐，Tailscale 已存在时复用。应用激活前实际加载清单声明的原生 Node 模块，避免仅检查 Node 版本而漏掉 DLL 依赖。

## 附件与恢复

附件包含 Windows 完整 ZIP、绑定仓库与标签的 install.ps1、清单、SHA256SUMS、依赖/平台描述、验证摘要、许可证、说明和公开下载测试脚本。第三方详细许可位于完整包内。Microsoft VC++ 与 Tailscale 不重新分发，安装器从固定供应商地址获取并检查 Authenticode 签名。VC++ 部署与版本检测依据 [Microsoft 部署文档](https://learn.microsoft.com/en-us/cpp/windows/redistributing-visual-cpp-files)。

上传先创建 Draft，逐项上传并下载回读核对大小和 SHA-256。任何步骤失败均保持草稿；不会用不同内容覆盖已存在附件，也不会修改已公开 Release。全部校验后才公开。网络中断后保留本地输出，可使用相同参数和 `-ResumeDirectory <原输出目录>` 继续；恢复前重新核对源码摘要、参数及本地附件摘要。源代码或附件变动时使用新输出目录及新标签。

已有标签必须解析到预期目标提交，带注释的标签逐层解析；标签冲突在上传前拒绝，发布前再次检查。

草稿验证完成后改为公开模式，需要以对应公开模式重新准备输出，或者从 GitHub 的草稿界面显式发布已经校验的版本。脚本的恢复模式保持原发布意图，不把旧草稿自动升级为公开版本。

GitHub 没有独立的 Release 测试服务。Draft 只有具备权限的账号可见，公开下载验收应使用隔离测试仓库里的 Prerelease。Release 和 Actions 日志/产物有不同的保存规则；发布流程不依赖临时 CI 文件作为长期附件。官方接口见 [Release REST API](https://docs.github.com/en/rest/releases/releases) 与 [附件 API](https://docs.github.com/en/rest/releases/assets)。

## 平台扩展契约

[release-targets.json](../../packaging/release-targets.json) 声明 id、os、arch、profile、host_os、enabled、prepare、builder、verify、installer。当前 `windows-amd64` 启用；Linux/macOS 为禁用条目。

prepare 接收 Workspace、Go（可选），生成 prepared.json，返回 go、node_executable、node_distribution、runtime_root。builder 接收 Go、NodeDistribution、RuntimeRoot、OutputDir、Repository、ReleaseTag，输出 release-manifest.json（schema 1）与对应平台包、安装脚本。包条目必须包含 os、arch、profile、file、size、sha256。verify 接收 PackageDirectory、OutputDirectory，失败时抛出错误。当前准备器和发布验证为 Windows 专用；接入其他平台时实现并注册对应 prepare/builder/verify/installer，host_os 限制原生构建环境，不能仅修改 enabled。不同 OS 的安装脚本独立，不在 install.ps1 中实现其他系统分支。

新平台必须定义最低系统、CPU 架构、依赖安装及重试规则、许可证和原生干净环境验证。未完成时保持 disabled，不把交叉编译产物列为受支持的可安装包。

## 下载验收

[公开安装工作流](../../.github/workflows/release-install-smoke.yml) 可手动指定公开仓库及标签。在新 Windows 托管运行器确认 Asterhand/Tailscale 不存在后，仅通过 install.ps1 安装全部组件；测试进程 PATH 中不提供 Go、Node、npm、Git 或 gh。工作流摘要保存系统版本、首次安装、Tailscale 待登录、配置未初始化与重复安装结果。

托管运行器自带开发工具，其 PATH 隔离验证的是安装没有这些工具的依赖；它不等同于 Windows 10/11 桌面全新镜像。桌面系统仍应按 [验收规程](../testing/acceptance.md) 验证 UAC、重启和系统策略。详细自动化入口见 [发布验证说明](../development/features/release-pipeline/validation.md)。

VC++ 运行库通常由托管镜像预装，默认回归会记录已有版本的复用。启用 fresh_vc_runtime 后，工作流只在一次性的 GitHub 托管实例中使用 Microsoft 签名的原安装器及已注册 MSI 移除预装 x64 运行库，确认 DLL 缺失后再运行公开安装脚本；准备步骤显式拒绝在自托管运行器执行。检查报告单独记录安装前后 DLL 版本。若卸载后 DLL 仍被系统保留，强验证会失败，须换用原本未安装该运行库的干净虚拟机验证；不得把普通回归记为缺失运行库的新装。
