//go:build windows

package desktop

import (
	"strconv"
	"strings"
	"unsafe"

	"github.com/jackgan90/Asterhand/internal/i18n"
	"golang.org/x/sys/windows"
)

var (
	user32       = windows.NewLazySystemDLL("user32.dll")
	shell32      = windows.NewLazySystemDLL("shell32.dll")
	messageBox   = user32.NewProc("MessageBoxW")
	shellExecute = shell32.NewProc("ShellExecuteW")
)

func textPointer(s string) *uint16 {
	// Display text can contain model/user strings; NUL must not truncate them.
	p, _ := windows.UTF16PtrFromString(strings.ReplaceAll(s, "\x00", "�"))
	return p
}

// Open passes the URL/path directly to the Shell, without a command interpreter.
func Open(target string) error {
	if strings.ContainsRune(target, '\x00') {
		return i18n.Err("err.desktop.open_invalid_chars")
	}
	r, _, _ := shellExecute.Call(0, uintptr(unsafe.Pointer(textPointer("open"))), uintptr(unsafe.Pointer(textPointer(target))), 0, 0, 1)
	if r <= 32 {
		return i18n.ErrArgs("err.desktop.open_win_error", map[string]string{"code": strconv.FormatUint(uint64(r), 10)})
	}
	return nil
}

// Error shows a local diagnostic and optionally offers the log directory.
func Error(message, logDirectory string) {
	loc := i18n.Effective()
	flags := uintptr(0x10) // MB_ICONERROR
	if logDirectory != "" {
		message = i18n.T(loc, "desktop.dialog.log_prompt", map[string]string{"message": message, "dir": logDirectory})
		flags |= 4 // MB_YESNO
	}
	title := i18n.T(loc, "brand.dialog_title")
	r, _, _ := messageBox.Call(0, uintptr(unsafe.Pointer(textPointer(message))), uintptr(unsafe.Pointer(textPointer(title))), flags)
	if r == 6 && logDirectory != "" {
		_ = Open(logDirectory)
	}
}
