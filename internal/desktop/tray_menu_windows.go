//go:build windows

package desktop

import (
	"strings"
	"unsafe"

	"github.com/jackgan90/Asterhand/internal/i18n"
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
	loc := t.options.locale()
	state := t.options.State()
	menuItem(menu, 1, 0, i18n.T(loc, "desktop.tray.title_with_name", map[string]string{"name": strings.ReplaceAll(state.Name, "&", "&&")}))
	menuItem(menu, 1, 0, i18n.T(loc, "desktop.tray.status_prefix", map[string]string{"state": state.textFor(loc)}))
	menuItem(menu, 0x800, 0, "")
	flags := uintptr(0)
	if state.Stopping {
		flags = 1
	}
	menuItem(menu, flags, menuOpen, i18n.T(loc, "desktop.tray.open_web"))
	if t.options.Input != nil {
		input := t.options.Input.Status()
		inputFlags := flags
		if !input.Supported || !input.Desired.Enabled {
			inputFlags = 1
		}
		menuItem(menu, inputFlags, 4, input.MenuLabel())
		if input.Error != "" {
			menuItem(menu, 1, 0, i18n.T(loc, "desktop.tray.quickinput_needs_attention"))
		}
	}
	menuItem(menu, 0, menuLogs, i18n.T(loc, "desktop.tray.open_logs"))
	menuItem(menu, 0x800, 0, "")
	menuItem(menu, flags, menuExit, i18n.T(loc, "desktop.tray.exit"))
	var cursor point
	getCursorPosition.Call(uintptr(unsafe.Pointer(&cursor)))
	hwnd := t.window.Load()
	setForegroundWindow.Call(hwnd)
	command, _, _ := trackPopupMenu.Call(menu, 0x100|0x2, uintptr(cursor.X), uintptr(cursor.Y), 0, hwnd, 0)
	// The null message lets Windows dismiss the next menu normally (Shell rule).
	postMessage.Call(hwnd, 0, 0, 0)
	switch command {
	case 4:
		go func() {
			if err := t.options.Input.Open(); err != nil {
				Error(err.Error(), t.options.LogDirectory)
			}
		}()
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
		loc := t.options.locale()
		message := i18n.T(loc, "desktop.tray.exit_busy_message")
		title := i18n.T(loc, "desktop.tray.exit_busy_title")
		result, _, _ := messageBox.Call(t.window.Load(), uintptr(unsafe.Pointer(textPointer(message))), uintptr(unsafe.Pointer(textPointer(title))), 0x24|0x100) // Yes/No, default No
		if result != 6 {
			return
		}
		t.options.Stop(true)
	}
	t.updateIcon()
}
