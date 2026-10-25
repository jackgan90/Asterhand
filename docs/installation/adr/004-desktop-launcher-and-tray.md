# ADR-004：固定桌面入口与节点内置托盘

- 状态：accepted

## 决策

提供固定安装根目录的 Windows GUI 启动器，读取 installation.json 定位当前版本和数据目录，以无控制台方式启动现有 CLI 节点。启动器只负责串行启动、健康确认、打开本机 Web 和启动错误提示，完成后退出。

托盘属于节点进程，直接调用进程内的停止入口；脚本、桌面与登录后启动共享入口，创建桌面快捷方式与托盘生命周期独立。现有 Web 停止入口复用相同收尾语义。

## 原因与后果

固定入口使快捷方式在版本目录更换后保持有效；保留控制台 CLI 可继续执行 version、status、doctor 和安装检查。托盘和服务同生命周期，避免常驻启动器或额外控制端口，也不增加 Windows Service 的用户会话边界。

Windows 适配层通过现有 x/sys/windows 调用系统 API，保持 CGO_ENABLED=0。图标由随源码保存的 PNG/ICO 构建输入生成并嵌入程序。支持 Explorer 重建、DPI 更新和会话结束；托盘是否展开由 Windows 用户设置决定。

忙时退出提示本机执行会停止并需要核对，远端已接收工作继续。关闭 Web 不退出节点；安装完成仍不自动启动，登录后启动仍为独立显式选项。

## 关联

[安装设计](../design.md) · [功能计划](../../development/features/windows-desktop-lifecycle/plan.md)
