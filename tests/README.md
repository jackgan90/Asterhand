# 测试目录

当前无测试实现或通过记录。

- `fixtures/`：脱敏协议样本、测试文件和隔离验收配置。
- `integration/`：真实进程、网络、SQLite 和故障注入。
- `e2e/`：Web 交互、安装和真实设备场景。

Go 单元测试随实现放在对应包。测试控制器、FakeExecutor 和命令在实施 feature 中加入，不能用始终成功的测试占位。

见 [测试设计](../docs/testing/design.md)、[人工验收](../docs/testing/acceptance.md) 与 [平台矩阵](../docs/testing/platforms.md)。
