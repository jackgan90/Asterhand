# 依赖升级与本地候选流程

首版只实现 DeepSeek + OpenClaw。脚本不提交、推送或发布，不改变全局开发工具，不自动重启用户节点。

以下命令从仓库根运行。out 中的路径均为本流程生成的示例输出，可自行选择；Go 使用 PATH 中与清单一致的版本。运行真实检查前，需将 LiveHome 替换为自己配置的验收节点数据目录，PreviousPackageDirectory 指向上一兼容版本的完整安装包目录。

## 1. 准备精确版本

复制 dependencies.json 到 out 下的新候选文件，修改组件精确版本；Go、Node、Tailscale 和 Microsoft VC++ 同时填写官方版本化 URL 和核对的 SHA-256。schema 2 的 components 列表描述 id、manager、package、version、source、integrity、license；bundle.targets 描述 runtime/provider/model/credential_ref，default_target 选择默认项。windows_native_modules 声明安装时必须加载验证的原生模块。准备和包诊断按描述遍历。新增提供者先实现并注册适配器，再加入组件与目标；未知安装技术显式拒绝。OpenClaw 和模型插件版本可以不同，但必须通过真实验证。保留原清单和 lock。

```powershell
.\scripts\prepare-dependencies.ps1 -CatalogPath .\out\candidate-dependencies.json -OutputDirectory .\out\prepared-upgrade -RefreshLock
```

按原锁重建时省略 CatalogPath/RefreshLock。可用 NodeArchive 指定本地官方 zip，仍校验摘要。输出独立 Node、runtime/node_modules、候选 dependencies.json、runtime-lock.json 和 prepared.json。npm ci 校验包完整性，默认 ignore-scripts；新版本若需要安装脚本，先审核再修改明确的准备步骤。

RefreshLock 会重新解析传递依赖，相同直接版本也可能产生新锁。审查版本、来源、integrity、许可证和接口变化后，显式把候选 dependencies.json/runtime-lock.json 放回源码 packaging。准备脚本不会修改源码。

## 2. 工程验证与构建

```powershell
.\scripts\verify-dependencies.ps1 -Go go -NodeDistribution .\out\prepared-upgrade\node-distribution\node-v24.22.1-win-x64 -RuntimeRoot .\out\prepared-upgrade\runtime -OutputDirectory .\out\upgrade-engineering
.\scripts\build.ps1 -Go go -NodeDistribution .\out\prepared-upgrade\node-distribution\node-v24.22.1-win-x64 -RuntimeRoot .\out\prepared-upgrade\runtime -OutputDir .\out\upgrade-candidate
```

Node 目录名按实际版本调整。缺真实模型/安装参数时相应检查为 skipped，upgrade_ready=false。程序和 manifest 内嵌源码摘要；源码变更后必须重新构建，不能沿用旧候选的真实验证。

## 3. 完整验证

在隔离目录配置候选，或在现有验收节点任务全部结束后停止旧进程并启动新候选。现有节点继续使用自己的 key，不复制到其他节点；更换候选后重新 probe。

```powershell
.\scripts\verify-dependencies.ps1 -Go go -NodeDistribution .\out\prepared-upgrade\node-distribution\node-v24.22.1-win-x64 -RuntimeRoot .\out\prepared-upgrade\runtime -OutputDirectory .\out\upgrade-full -LiveHome '<验收节点数据目录>' -LiveAddress http://127.0.0.1:17432 -PackageDirectory .\out\upgrade-candidate -PreviousPackageDirectory .\out\previous-candidate
```

LiveHome 必须属于当前用户并与地址对应；脚本只读取本机 Web 登录 token，模型 key 保留在节点进程内。该命令消耗少量模型额度，在授权目录创建唯一测试文件，执行 probe、文件、问答和取消；存在未结束任务时拒绝运行。失败只取消本次测试任务，保留证据。

安装测试使用独立中文含空格目录和 Windows PowerShell 5.1，合成凭据检查旧配置迁移、身份与凭据字节保留。Tailscale、登录和防火墙变更跳过。upgrade_ready 仅表示本机工程层通过；双 Windows/NAT、用户验收、提交和发布独立处理。

## 回退边界

版本目录保留，数据目录独立。安装器不自动停止或热替换现有进程，升级后由用户控制停止和启动。数据库遇到较新 schema 会拒绝打开；二进制回退不保证数据兼容，升级前应停机备份。当前没有自动回滚命令。
