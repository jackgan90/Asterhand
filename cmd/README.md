# 命令入口

状态：仅目录占位，尚无 Go module 或程序代码。

- `asterhand/`：计划 CLI，承载 setup、doctor、repair、启动与管理入口。
- `asterhandd/`：计划常驻任务服务与 Web API 入口。

业务逻辑留在 internal，CLI 与 Web 共用环境准备能力。是否最终合并为单一二进制在 M0 根据部署验证决定。
