# Asterhand 图标资源

## 设计来源

图标由内置 imagegen 根据 Asterhand（星伴）的名称与个人跨设备助手定位生成，使用项目自己的设计提示；没有第三方品牌或现成图标输入。

- `application.png`：用户选定的星核托举原稿。蓝青色机械手/连接带托起暖金四芒星，两个节点形成互联关系，整体轮廓呼应 A。
- `tray.png`：以原稿为参考生成的小尺寸简化版本，保持 A 形、托举动作、星核和两个节点，减少金属反光和细纹，用宽色块与明确负空间提高托盘辨识度。

生成提示的核心约束：单个居中标识、透明背景、无文字和水印；应用图标使用克制金属质感，托盘图标强调蓝青/靛蓝/暖金色块和粗轮廓，不增加新的图形元素。

PNG 是图形源。ICO 是机械转换生成的二进制资源，不手工编辑。资源沿用项目 [MIT License](../../../LICENSE)。

## 生成与使用

在 Windows PowerShell 5.1 或 PowerShell 7 中从仓库根运行：

```powershell
.\scripts\build-icons.ps1
```

脚本使用 Windows 自带 System.Drawing 保留每像素透明度，并以高质量缩放生成 PNG 压缩帧：

| 输出 | 帧尺寸 | 来源 |
|---|---|---|
| application.ico | 16、20、24、32 | tray.png |
| application.ico | 48、64、96、128、256 | application.png |
| tray.ico | 16、20、24、32、40、48、64 | tray.png |

节点内嵌 tray.ico，按系统 DPI 选择图标帧。构建脚本通过 `scripts/set-executable-icon.ps1 -Executable <exe> -Icon <ico>` 将应用 ICO 写入节点和 GUI 启动器的 PE 资源。写入在同目录副本完成，逐帧读回验证后替换目标，保留其他资源；该步骤应在包摘要计算和签名前执行。

16—32 像素不依赖桌面原稿的高光和渐变。增加尺寸或替换图形源后，应运行 [图标资源测试](../../../scripts/test-icon-resources.ps1)，检查透明度、深浅背景预览、ICO 帧与实际 PE 资源。
