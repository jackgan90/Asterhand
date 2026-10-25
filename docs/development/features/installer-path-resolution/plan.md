# Windows 安装路径解析

## 问题与范围

关联 INST-01/05/14。Windows PowerShell 当前目录与 .NET 进程工作目录可不同，直接用单参数 GetFullPath 解析相对路径可能在错误目录查找包或清单。

## 设计

安装器在入口使用 PowerShell SessionState 路径 API，把 PackagePath、ManifestPath、InstallDir 和 DataDir 解析为当前 FileSystem 位置下的绝对路径。允许尚未创建的目标目录，拒绝非 FileSystem provider；不修改全进程 CurrentDirectory。

默认 manifest 从已解析的包目录推导。程序和数据目录继续分离，重复安装保留已有数据。权威说明见 [安装需求](../../../installation/requirements.md)、[安装设计](../../../installation/design.md) 和 [使用指南](../../../installation/quickstart.md)。

## 验证方法

[安装路径测试](../../../../scripts/test-install-paths.ps1) 在系统 PowerShell 5.1 中故意分离两种工作目录，覆盖相对包/自动清单、相对显式清单、异目录绝对包、中文空格目标、重复安装数据保留和非文件 provider 拒绝。

此测试由 [完整安装检查](../../../../scripts/test-install.ps1) 调用，参数和使用示例见 [脚本指南](../../../../scripts/README.md)。可使用 BaselineInstaller 指定待比较的旧安装器，基线由执行者提供。

安装器与专项测试按路径解析和安装职责组织，未触发超限文件拆分。
