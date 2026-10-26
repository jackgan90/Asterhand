# WebView2 宿主依赖

Go 模块 github.com/wailsapp/go-webview2 v1.0.23 为 MIT，其 webviewloader 子目录为 ISC；go.mod/go.sum 固定模块与完整性，构建收集两个许可证。默认使用纯 Go loader，不要求 CGO、C 编译器或外置 WebView2Loader.dll。只使用 loader 与 COM callback bridge，未使用其生成的扩展接口 vtable。

本地 COM 接口依据 Microsoft.Web.WebView2 SDK 1.0.2792.45 的 WebView2.h，来源为[官方 NuGet](https://www.nuget.org/packages/Microsoft.Web.WebView2/1.0.2792.45)，包 SHA-256 为 02ffd77440c09b5ff60f5f06defdc3fb07fb5630077c7cf6115bf6be793f91e3。SDK 属 Microsoft 分发条款，未作为产品文件重新分发。新增接口必须校验继承槽位、GUID、BOOL 与出参指针；uintptr 包装必须保持输出指针逃逸及生命周期。

共享 Evergreen Runtime 的兼容下限、验证版本和安装材料固定在[清单](dependencies.json)。version 是完整离线安装器文件外壳版本，minimum_runtime_version 与 tested_runtime_version 分别表示 Runtime 下限和已验证版本，三者不能混用。固定 URL、SHA-256 和有效 Microsoft Authenticode 签名共同校验，不使用滚动 URL 的当前响应冒充固定输入。

完整安装器只在准备目录下载，不附入 Release。用户安装复用合格 Runtime，否则在停止旧节点前补齐。标准权限运行安装器为 per-user；不主动提权、修改共享更新策略、降级、自动重启系统或卸载共享 Runtime。运行中的节点不因 Evergreen 更新自动重启。实际加载版本由桌面状态返回，未来 Runtime 仍需能力查询与回归。

发布准备下载并核验固定材料；干净设备、供应商退出码/重启与 offline 验收见[快捷输入验证](../docs/development/features/global-hotkey-quick-input/validation.md)。常规脚本测试 mock 供应商执行，不能代替干净 Windows 新装。

Windows 包同时包含 `AsterhandInputHost.exe` 与 `input-host.json`。宿主为 GUI 子系统程序，与节点使用相同应用版本和源码指纹；清单摘要在图标写入后计算，`bundle-check` 和每次启动都验证。开发时使用 `scripts/build-input-host.ps1` 在节点可执行文件同目录生成配套宿主；带源码指纹的节点必须传入相同 `-SourceDigest`。缺失或不匹配时不搜索其他目录的宿主。

宿主使用非提权 Medium 令牌、限定的环境和继承句柄，在恢复执行前加入父进程自有的 kill-on-close Job。其 WebView2 保持默认浏览器沙箱；不接受继承的 `WEBVIEW2_*` 环境覆盖，并拒绝为本程序或通配符设置的浏览器参数、运行时路径及 profile 路径注册表覆盖。失败只影响快捷输入；不修改用户策略或扩大安装目录权限。权限与真实桌面验证见[隔离验证](../docs/development/features/quick-input-process-isolation/validation.md)。
