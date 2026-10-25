# 提供者无关的公共流程验证

| 行为 | 源码入口 | 验证重点 |
|---|---|---|
| 旧配置与嵌套选项迁移 | [运行时配置测试](../../../../internal/config/runtime_options_test.go) | 保留身份/凭据，快照不共享可变对象 |
| CLI 选项拒绝 | [启动参数测试](../../../../cmd/asterhand/runtime_options_test.go) | 无效选择在配置落盘前失败 |
| 受保护目录 | [目录测试](../../../../internal/executor/workspace_test.go)、[设置选择测试](../../../../internal/app/selection_test.go) | 适配器声明与别名保护有效 |
| 异名包目标 | [包契约](../../../../internal/environment/bundle_contract_test.go) | 使用描述分派，不依赖内置厂商名称 |
| 组件锁与准备 | [依赖守卫](../../../../scripts/test-dependency-tools.ps1) | 拒绝缺失/额外直接依赖与未知安装技术 |

命令见 [测试指南](../../../../tests/README.md) 与 [依赖维护](../../../../packaging/upgrade.md)。公共流程变化需要新装、重复安装和上一版本升级验证；真实模型测试与单元 fixture 分开运行。
