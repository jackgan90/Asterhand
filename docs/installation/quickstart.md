# 首版安装与 hello-world 验收

版本：0.1.0-alpha.1。Windows x64 是完整助手目标。支持边界见 [平台矩阵](../testing/platforms.md)，可下载版本以 GitHub Release 为准。

## 1. 在 B 安装

从同一版本发布物取得 `install.ps1`、`release-manifest.json`、`asterhand-0.1.0-alpha.1-windows-amd64.zip`。发布前可将这三份本地候选文件一起复制到 B。不要使用尚未存在的下载地址。

在该目录打开 PowerShell，执行本地包安装：

```powershell
.\install.ps1 -PackagePath .\asterhand-0.1.0-alpha.1-windows-amd64.zip
```

相对路径按当前 PowerShell FileSystem 目录解析；也可向 PackagePath 和 ManifestPath 传绝对路径。默认清单从包所在目录读取。升级时安装器、ZIP 与 manifest 必须来自同一构建，完整性以 manifest 校验为准。

若 Windows 阻止执行从浏览器下载的脚本，先核对随包 SHA256SUMS，再使用 `Unblock-File .\install.ps1` 解除该文件的下载标记；不需要修改整机执行策略。仍受组织策略限制时按组织要求运行，不自动绕过。

正式 Release 可用后，也可只下载该 Release 的 install.ps1 并运行，脚本会获取固定版本清单和平台包。程序、Node.js 和 OpenClaw 已打包；B 不需要安装 Go、npm 或 coding harness。

默认程序目录 `%LOCALAPPDATA%\Programs\Asterhand`，数据目录 `%APPDATA%\Asterhand`。重复安装保留节点身份、配对、key 和数据库。可用 `-InstallDir`、`-DataDir` 指定独立目录；程序和数据分开。

## 2. 网络与初次配置

安装器检测 Tailscale。缺失时展示系统网络服务安装范围，请求 Windows 管理员许可；登录在 Tailscale 的浏览器页面完成。A、B 加入同一 Tailnet。已有 Tailscale 会复用，不静默升级。

网页中填写：

1. 节点名称，例如 `Windows B`。
2. 授权目录，例如 `D:\AsterhandWorkspace`。不存在时自动创建；不要使用凭据、应用数据或运行时目录。
3. 点击“使用 Tailscale 地址”，同时填写 `100.x.x.x:17433` 节点监听地址和 `https://100.x.x.x:17433` 公布地址。
4. DeepSeek 模型和 API key。key 在对应机器的设置表单输入，Windows 使用当前用户 DPAPI 加密。
5. 保存。若显示监听配置需要重启，先点击“停止本机节点”，再运行安装目录的 start.ps1。
6. 点击“验证模型与工具”。这会消耗一次小型模型调用；通过后显示助手就绪。只修改节点名称或网络地址会保留既有模型验证结果。

本机页面使用 `127.0.0.1:17432`，不向局域网暴露登录入口。用户在 B 本机打开 B 页面可以独立处理任务。跨节点 API 使用 TLS 与带外配对证书；无需浏览器忽略证书告警。

若 Windows 防火墙阻止 TCP 17433，运行安装目录下的 `configure-firewall.ps1`；它明确询问后只开放 Tailscale 地址范围到 Asterhand 程序的该端口。升级后程序路径改变时重新运行该脚本。Tailnet ACL / grants 也要允许 A↔B。不做公网端口转发。

## 已有节点升级

在任务完成后，打开本机设置，记下页面显示的数据目录，并点击“停止本机节点”。将新候选的 install.ps1、release-manifest.json 和 Windows ZIP 三个文件一起复制到机器；进入该目录，运行安装命令。默认安装沿用原数据；曾指定自定义 DataDir 的节点必须继续传入同一目录。

升级后运行安装目录 start.ps1 打开原节点，确认原名称、授权目录、配对和历史任务仍在。点击“使用 Tailscale 地址”并保存；出现重启提示时再停止并启动一次。运行安装目录 configure-firewall.ps1 更新规则。新程序首次启动需要重新验证一次模型与工具。

两端均升级后，等待 20—40 秒并刷新。双方节点列表应显示对端当前名称、在线状态及真实 Tailscale 地址。既有配对会通过已固定的证书和节点身份回查新地址，无需重新配对；至少一个方向可达时自动修复另一个方向的旧地址。探测允许 10 秒完成冷连接与身份校验，超时会显示具体错误并在后续周期重试。

验证时使用新文件名（例如 hello-world-on-B.txt）：从 A 指定 B 创建精确的 hello world，确认任务立即排在列表顶部，完成后刷新仍在顶部且只有一张任务卡。然后从 B 指定 A 创建 hello-world-on-A.txt，检查两台机器各自的实际文件。修改任一节点名称后，等待 20—40 秒检查另一端显示新名称，本机应始终保持模型就绪。

## 3. 在 A 配对 B

A 以同样步骤安装、配置并验证独立助手。在 B 点击“配对另一台设备”→“在本机生成配对码”，把完整配对码带外复制到 A 的配对表单。

配对码 10 分钟有效，包含证书与一次性凭据，只交给自己的设备。成功后 A 的节点列表显示 B 与授权目录。配对不传送 DeepSeek key。

## 4. 从 A 创建 B 上的文件

在 A 页面选择 `Windows B` 和 B 的授权目录，发送：

> 让节点 B 在授权目录下新建 hello-world.txt，内容精确为 hello world，不添加换行或 BOM。

等待 B 已接收、执行中、完成并验证。页面显示 B 的实际文件路径、11 字节大小、内容和 SHA-256。在 B 的资源管理器打开文件检查；A 的授权目录不应出现该文件。已存在同名文件会拒绝覆盖，请换新文件名或使用新的验收目录。

## 启停与诊断

- 启动：安装目录 `start.ps1`。已经运行时打开现有节点，不创建第二个服务。
- 停止：本机设置页“停止本机节点”。在途任务重启后按记录核对，不自动重跑。
- 可选登录后启动：安装时显式加 `-LoginAtStartup`；移除 Windows Startup 中的 Asterhand 快捷方式即可关闭。
- 诊断：安装版本目录中运行 `asterhand.exe doctor --home <数据目录>`；不输出 key。
- 修复：`repair` 重新发现并检查同包运行时；缺少组件时重新运行安装器，不删除数据。
- 仅 LAN 或隔离安装测试：`-SkipTailscale`；安装但不启动：`-NoStart`。这两种运行不证明跨 NAT 可达。
- 未知执行状态显示“需要核对”。检查文件、执行记录后在 B 本机填写证据并关闭；该操作记录人工处置，不伪造成功。

其他验收：A 服务退出后 B 继续、问题回答去重、执行中重启的对账，按项目 `docs/testing/acceptance.md` 的 V04—V06。Linux 基础节点是单独的交叉编译产物；Mac 未验证。
