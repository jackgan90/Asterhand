//go:build windows

package desktop

import (
	"strings"
	"unsafe"
)

func (t *Tray) handleIconEvent(event uint32) {
	switch event {
	case wmDoubleClick, ninKeySelect:
		if !t.options.State().Stopping {
			t.openWeb()
		}
	case wmContextMenu:
		t.showMenu()
	}
}

func (t *Tray) openWeb() {
	if err := t.options.OpenWeb(); err != nil {
		Error(err.Error(), t.options.LogDirectory)
	}
}

func menuItem(menu, flags, id uintptr, text string) {
	appendMenu.Call(menu, flags, id, uintptr(unsafe.Pointer(textPointer(text))))
}

func (t *Tray) showMenu() {
	if t.menuActive {
		return
	}
	t.menuActive = true
	defer func() { t.menuActive = false }()
	menu, _, _ := createPopupMenu.Call()
	if menu == 0 {
		return
	}
	defer destroyMenu.Call(menu)
	state := t.options.State()
	menuItem(menu, 1, 0, "Asterhand · "+strings.ReplaceAll(state.Name, "&", "&&"))
	menuItem(menu, 1, 0, "状态："+state.text())
	menuItem(menu, 0x800, 0, "")
	flags := uintptr(0)
	if state.Stopping {
		flags = 1
	}
	menuItem(menu, flags, menuOpen, "打开 Asterhand")
	menuItem(menu, 0, menuLogs, "打开日志目录")
	menuItem(menu, 0x800, 0, "")
	menuItem(menu, flags, menuExit, "退出 Asterhand")
	var cursor point
	getCursorPosition.Call(uintptr(unsafe.Pointer(&cursor)))
	hwnd := t.window.Load()
	setForegroundWindow.Call(hwnd)
	command, _, _ := trackPopupMenu.Call(menu, 0x100|0x2, uintptr(cursor.X), uintptr(cursor.Y), 0, hwnd, 0)
	// The null message lets Windows dismiss the next menu normally (Shell rule).
	postMessage.Call(hwnd, 0, 0, 0)
	switch command {
	case menuOpen:
		t.openWeb()
	case menuLogs:
		if err := Open(t.options.LogDirectory); err != nil {
			Error(err.Error(), "")
		}
	case menuExit:
		t.requestExit()
	}
}

func (t *Tray) requestExit() {
	if !t.options.Stop(false) {
		message := "本机有任务或模型检查正在执行。退出后，本机执行会停止，在途任务需要在下次启动时核对；其他节点已接收的任务继续运行。\n\n确定退出 Asterhand？"
		result, _, _ := messageBox.Call(t.window.Load(), uintptr(unsafe.Pointer(textPointer(message))), uintptr(unsafe.Pointer(textPointer("退出 Asterhand"))), 0x24|0x100) // Yes/No, default No
		if result != 6 {
			return
		}
		t.options.Stop(true)
	}
	t.updateIcon()
}
