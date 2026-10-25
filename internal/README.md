Go 服务按 config、store、model、tasks、executor、peer、app、environment、network 分包。权限和文件动作在 Go 校验，模型只经受认证回调提出工具请求。测试与对应包相邻。

launch 负责安装记录验证、只读状态探测、串行启动与启动失败诊断；desktop 负责原生系统托盘、打开本机入口、消息循环和图标资源。两者不承担任务执行。app 的生命周期入口统一停止接收、关闭监听、等待请求与执行收尾后关闭数据库；tasks 的接收/启动屏障防止退出时产生新执行。相关设计见 [桌面生命周期计划](../docs/development/features/windows-desktop-lifecycle/plan.md)。
