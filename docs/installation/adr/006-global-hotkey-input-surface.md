# ADR-006：节点内全局快捷键与独立快捷输入宿主

- 状态：accepted。

## 决策

Windows 节点保留已有托盘 OS 线程作为全局快捷键的注册所有者，通过受控消息调度注册/注销。文本浮层由节点内独立 STA UI 线程承载 WebView2，关闭浮层不退出节点，预期初始化或 renderer 错误只使面板降级。整个宿主进程崩溃仍按既有节点重启对账处理，不宣称进程内崩溃隔离。

输入界面加载随包资源，使用独立虚拟 HTTPS 来源及受限原生消息，不持有 Web 登录 token，不调用通用任务或设置 API。Web 设置继续使用现有认证、Host/Origin/CSRF 和停止屏障；桌面配置独立于模型、任务和 peer 数据。

使用固定版本的 Go WebView2 绑定及纯 Go loader，保持 CGO_ENABLED=0。Evergreen Runtime 作为共享系统依赖，安装材料固定校验，诊断记录实际版本。profile 归属当前节点数据目录，草稿仅保留在本次进程内存。非 Windows 明确返回 unsupported，保持窄平台接口。

## 原因与后果

专用 UI goroutine 完成 COM 清理后保持 OS 线程锁直到退出，让 Go 销毁该线程，释放 WebView2 的原生线程缓存。该线程不返回普通调度池；重复启停必须覆盖主进程内存与整个自有子进程树，不能只检查 HWND 或浏览器退出。

宿主仅使用绑定的 loader/COM bridge，核心扩展接口按官方 SDK 的完整继承布局实现窄调用，并保留 uintptr 输出指针生命周期。启动探测拒绝 Beta/Dev/Canary 通道替代正式 Runtime；隐藏面板尝试挂起以减少渲染开销，CPU 预算仍作为首版已记录的性能限制保留失败。

复用前端技术支持科技风和内置主题，独立 STA 避免托盘同步菜单与 WebView2 回调重入。新增 Runtime 带来包准备、权限、进程及资源验证成本，需通过宿主和安装门禁；不引入完整桌面框架或常驻监督进程。文本执行、角色、skill 与跨页面草稿交接后置。

## 关联

[快捷输入计划](../../development/features/global-hotkey-quick-input/plan.md) · [桌面入口决策](004-desktop-launcher-and-tray.md) · [Web 设计](../../web/design.md) · [安装设计](../design.md)
