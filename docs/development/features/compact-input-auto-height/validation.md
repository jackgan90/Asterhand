# 收起态快捷输入框自动高度验证

对应[功能计划与 CAH-01—08](plan.md)、[Web 需求](../../../web/requirements.md)、[Web 设计](../../../web/design.md)及[窗口设计](../../../installation/design.md)。

工程验证、独立复审及本机输入体验用户验收通过。验收范围为收起态单行起步、实际折行增高与删除回缩、十行滚动上限、展开态沿用原布局，以及换行扩高时滚动条和顶部残缺文字的闪现修复。默认采用恰好容纳当前文本的策略。

## 测试目的与入口

- [布局组件测试](../../../../tests/composer-layout.test.mjs)验证当前行数驱动的增长/回缩、0/1 额外行、十行上限、目标高度与实际视口分离、确认区、按帧合并、隐藏及销毁。几何 fixture 只验证事件与策略，真实折行由原生用例验证。
- [编辑器桥测试](../../../../tests/quick-input-bridge.test.mjs)验证首次 resize 在 initialized 之前，IME、快照、提交暂时清空、拒绝恢复和迟到 ACK 保持草稿修订边界。
- [窗口布局测试](../../../../internal/quickinput/window_layout_test.go)和[桥与原生专项](../../../../internal/quickinput/surface_composer_windows_test.go)验证 128—800 DIP 请求范围、代次拒绝、DPI、工作区与真实编辑区/窗口尺寸。
- `TestNativeCompactComposerHeight` 使用生产 HTML/CSS/编辑器与真实 WebView2，覆盖空白初始布局、真实单行折行边界、逐行增减、十行溢出、中英/emoji/URL、尾部换行、双主题、提交清空/恢复、确认区、展开态与最大字节草稿。
- `TestNativeCompactComposerLifecycle` 覆盖被动重测保留输入滚动位置、合成 IME 临时文本不提交、组合结束快照、隐藏恢复、额外一行策略及私有测量层清理。
- `TestNativeCompactComposerPlacement` 验证工作区底部增长时原生限幅，隐藏不写回受约束位置，删除缩回后恢复首选锚点。
- `TestNativeCompactComposerEditCaret` 使用浏览器原生编辑、撤销和重做，配合[独立全文光标几何夹具](../../../../tests/fixtures/compact-caret-regression.js)，验证英文整词跨行时短视口中部插字保持光标可见，以及原生长文编辑滚动；不依靠合成 input 事件判断浏览器编辑滚动。
- [滚动条原生回归](../../../../internal/quickinput/surface_composer_scrollbar_windows_test.go)与[逐帧夹具](../../../../tests/fixtures/compact-scrollbar-regression.js)在真实 WebView2 中延后窗口 resize，连续验证 1→10 行实际软换行的每个等待帧均无内外滚动条闪现；同时验证超过十行滚动、删除/清空移除滚动条，以及少于十行但宿主确认真实限高时仍可滚动。已限高的 4→5 行换行在新请求的等待帧中持续显示滚动条，确认同一限高不闪动，空间恢复后才移除。组件与桥测试另覆盖迟到/错误代次确认及确认早于浏览器 viewport 更新的顺序。
- [文本扩高原生回归](../../../../internal/quickinput/surface_composer_text_windows_test.go)记录同一实际软换行序列每帧 scrollTop，验证 1→10 行等待期间保持顶部，不闪现残缺上一行；刻意触发迟到自动滚动后仍恢复锚定，记录未完成扩高的真实截图；长文本末尾插入在扩高等待期间不提前滚动，十行视口就绪后继续跟随末尾。组件测试同时验证确认限高后释放锚定、隐藏移除 scroll 监听，以及外部视口缩小不误判为新的输入扩高。

文本专项还通过浏览器真实删除/重输排队“缩小→增高”，仅应用旧缩小并观察三个等待帧，要求新请求持续顶部锚定；释放新增长后跟随末尾。该用例和组件测试共同防止旧大视口暂时可容纳时提前消费等待状态。

## 自动化命令

工具版本使用[依赖清单](../../../../packaging/dependencies.json)。Go 与 Node 不在 PATH 时使用已准备的固定版本可执行程序路径，不要求重新下载依赖。

```powershell
$uiTests = Get-ChildItem -LiteralPath tests -Filter '*.test.mjs' -File
node --test $uiTests.FullName
go test ./...
go vet ./...
```

真实 WebView2 专项需要 Windows x64 交互桌面和合格的 Evergreen Runtime，运行器须能创建 Medium 非提权测试进程。用例只显示独立测试窗口，不尝试激活其他应用，不安装或连接日常节点。

```powershell
$evidenceDirectory = [IO.Path]::GetFullPath('./dist/audit/compact-input-auto-height')
$env:ASTERHAND_CHAT_GATE = '1'
$env:ASTERHAND_COMPOSER_CAPTURE = Join-Path $evidenceDirectory 'captures'
try {
    go test ./internal/quickinput -run '^TestNativeCompactComposer(Height|Lifecycle|Placement|EditCaret|Scrollbar|TextGrowth)$' -count=1 -timeout=180s -v
} finally {
    Remove-Item Env:ASTERHAND_CHAT_GATE, Env:ASTERHAND_COMPOSER_CAPTURE
}
```

截图由上述命令生成，包含空白、两行、双主题和溢出状态；可省略 capture 环境变量只验证行为。环境、实际命令、版本、passed/failed/skipped/blocked、日志和图片保留在执行者选择的忽略目录，长期文档不依赖既有本机产物。

既有真实聊天发送/收据和展开态公式回归沿用[聊天桌面验证](../chat-desktop-experience/validation.md)的 gate。真实前台激活需要用户手势；操作系统拒绝激活时记录环境边界，不能把这种执行记为通过。普通 Go 测试涉及本机回环连接、子进程与临时链接，运行环境需要允许这些操作。

## 实体体验与默认值

已验收的默认值为 `compactExtraRows = 0`，定义在[布局模块](../../../../internal/quickinput/assets/composer-layout.mjs)。后续试用额外一行时将此内部值调整为 `1` 后重建测试宿主；不增加持久设置。调整策略时应重新检查：

1. 空框一行；输入短句、连续长文、显式空行、粘贴和撤销/重做时，高度与当前文本匹配。
2. 从一行连续输入至十行，每次到达右侧触发软换行时，扩高等待期间均无滚动条闪现；连续删除至九行、一行和空框及时缩回。超过十行和工作区确实限高时仍能滚动，恢复足够空间后滚动条消失。
3. 拖动纵向滚动条、滚轮和键盘回看早先文本；状态刷新不拉回底部，继续输入时光标可见。
4. 实体中文 IME 的临时组合、取消、候选确认和发送快捷键；检查选区、光标和撤销历史。
5. 100%/125%/150%/200% 缩放及多屏、较窄/较矮工作区，窗口底部增长/回缩、隐藏恢复和最小化恢复。
6. 展开态仍使用原有编辑区，收起按当前草稿重算；发送继续自动展开，真实助手问答保持正常。

本机输入体验用户验收已完成，额外一行策略保留为后续可选调节。实体 IME、鼠标拖动滚动条、多屏及其他 DPI、真实模型问答仍按专项单独记录，不从本次验收推定整套矩阵已覆盖。合成 IME 事件不代替实体输入法，窗口与 DOM 检查不代替鼠标或多屏实测。既有隐藏 CPU 成本边界保持原记录，不因本功能关闭。
