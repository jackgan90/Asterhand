asterhand 提供 setup/start/open/status/doctor/repair/version。入口默认启动当前用户节点；完整安装包自动发现相对目录中的固定运行时。

`asterhand-launcher` 构建为 Windows GUI 程序 AsterhandLauncher.exe，安装于固定程序根目录，读取同目录 installation.json 启动当前版本；`--no-browser` 仅启动后台节点，`--quiet` 用于脚本调用，错误仍追加 launcher.log 并返回非零退出码。入口完成健康检查后退出，托盘由节点进程拥有。

`start` / `setup` 取得数据目录锁后初始化；Windows 交互会话显示托盘，Ctrl+C、托盘和 Web 停止共享收尾。启动 stdout 只打印无凭据的本机地址，重新登录使用 `open --home <数据目录>` 或托盘。`open` / `status` 只读检查运行记录、节点身份与健康，不初始化新配置。桌面入口细节见 [安装设计](../docs/installation/design.md)。

`bundle-check --directory <包目录>` 是不读取节点数据或凭据的只读包诊断，按清单逐一检查目标。

高级运行时配置使用 `--agent-runtime <已注册 ID> --runtime-options <JSON 文件>`，选项对象由所选 adapter 校验；显式修改会使模型就绪状态失效。旧 --node/--openclaw/--plugin/--provider 参数已移除，旧持久配置仍自动迁移。普通安装包用户无需设置路径。

`--runtime-options` 是完整的显式选项对象，保存来源标记，普通重启、repair 和安装升级均保留；自动发现只迁移自动管理的选项。有效执行配置摘要变化后必须重新 probe。高级用户要恢复自动发现，可停止节点后删除该 runtime 的选项和对应 runtime_options_explicit 标记，再启动；不要编辑运行中节点的配置。

当前 OpenClaw 选项对象包含 node_path（Node 可执行文件）、entry_path（agent 入口）、plugin_path（Asterhand 工具插件目录）和 provider_paths（provider ID 到插件目录的映射），均使用绝对路径。选项文件不包含 key；凭据通过节点设置表单保存。
