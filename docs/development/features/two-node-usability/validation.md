# 双节点交互与连接同步验证

## 自动化覆盖

| 行为 | 仓库测试入口 | 预期结果 |
|---|---|---|
| 名称/网络设置与就绪 | [就绪测试](../../../../internal/app/settings_readiness_test.go) | 元数据变化保持 probe，执行配置变化失效；监听变更提示重启 |
| 身份绑定的元数据更新 | [元数据测试](../../../../internal/app/peer_metadata_test.go) | 地址更新保持原信任，伪造、撤销与不可达地址拒绝 |
| 有界慢连接 | [延迟测试](../../../../internal/app/peer_latency_test.go) | 2.2 秒信息响应与 6 秒元数据回查成功，超出预算失败 |
| 浏览器模块 MIME | [静态资源测试](../../../../internal/app/web_assets_test.go) | 即使系统将 .mjs 关联为 text/plain，GET/HEAD 仍返回 JavaScript 类型及 nosniff |
| 排序、去重和发送状态 | [Web 状态测试](../../../../tests/web-state.test.mjs) | 新远端任务置顶，路由转执行保留卡片，禁用原因与忙碌状态准确 |

运行命令见 [测试指南](../../../../tests/README.md)。

## 真实浏览器与双设备

按 [安装与升级指南](../../../installation/quickstart.md#已有节点升级) 配置两个独立 Windows 节点。使用新文件名分别从 A 指定 B、从 B 指定 A 创建精确的 hello world；检查目标机器文件、页面结果及刷新后的卡片顺序。

仅修改名称应保持模型就绪，另一端在后续同步周期显示新名称；修改模型、凭据、运行时或授权目录则需重新验证。保存监听设置后按页面提示重启。检查浏览器模块响应类型和控制台错误。

完整离线、问答和重启规程见 [人工验收](../../../testing/acceptance.md)。自动化 fixture 和真机验收分别判断，平台支持见 [支持矩阵](../../../testing/platforms.md)。
