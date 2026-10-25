# ADR-NODE-002：首版跨 NAT 连接采用 Tailscale

状态：已接受。日期：2026-10-24。用户确认“采用 Tailscale，纳入安装引导”。取代首版仅假定用户已自行准备 Tailnet 的部署前提。

## 决策

跨 NAT 的首版标准路径使用 Tailscale。install.ps1 检测安装状态；缺失时提供固定版本官方安装包及登录引导。用户在 Tailscale 自己的登录页面完成身份认证，不向 Asterhand 提交 Tailscale 账户密码或 auth key。

配置页检测本机 Tailscale BackendState 与 IPv4，并提供填入节点地址。A、B 加入同一 Tailnet；允许两节点 TCP 17433。配对和任务读取实际访问 Asterhand HTTPS 服务，检测成功才显示可达。

Tailscale 尝试 NAT 穿透直连，无法直连时可使用 DERP 中继。Asterhand 不实现 STUN、NAT 打洞、DERP 或公网服务发现。模型、密钥、任务数据库仍归各节点；网络中继不改变 owner，也不承担任务调度。

## 信任与操作范围

- Tailscale 控制平面和可用中继是跨 NAT 通信的外部网络依赖。出站网络被封锁或设备离线时显示待送达/最后状态。
- Asterhand 独立验证邀请中的节点 TLS 证书、peer token 与授权项目；加入 Tailnet 本身不授予文件访问。
- 安装新 Tailscale、登录、系统防火墙配置和登录后自动启动均展示具体范围。复用已安装客户端，不静默升级全局网络软件。
- 不要求公网 IP 或路由器端口映射，不自动安装 WSL，不使用跳过 TLS 校验。
- 同局域网可使用 LAN 地址，但标准跨 NAT 验收使用 Tailscale 地址。

## 证据与验证

官方资料：[连接类型](https://tailscale.com/docs/reference/connection-types)、[Windows MSI](https://tailscale.com/docs/install/windows/msi)、[防火墙](https://tailscale.com/docs/reference/faq/firewall-ports)。安装引导固定 Tailscale 1.104.2 Windows amd64，校验值见 [依赖清单](../../../packaging/dependencies.json)。

本地无 Tailscale 的检测、脚本分支和包校验可自动测试；真实双 NAT、Tailscale 登录、DERP 回退必须在相应网络环境记录实测结果。编写了安装脚本不代表跨 NAT 已验收。
