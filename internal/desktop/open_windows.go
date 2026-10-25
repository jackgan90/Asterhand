//go:build windows

package desktop

import (
	"errors"
	"fmt"
	"strings"
	"unsafe"

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
		return errors.New("打开目标包含无效字符")
	}
	r, _, _ := shellExecute.Call(0, uintptr(unsafe.Pointer(textPointer("open"))), uintptr(unsafe.Pointer(textPointer(target))), 0, 0, 1)
	if r <= 32 {
		return fmt.Errorf("无法打开目标，Windows 错误 %d", r)
	}
	return nil
}

// Error shows a local diagnostic and optionally offers the log directory.
func Error(message, logDirectory string) {
	flags := uintptr(0x10) // MB_ICONERROR
	if logDirectory != "" {
		message += "\n\n日志目录：" + logDirectory + "\n\n是否打开日志目录？"
		flags |= 4 // MB_YESNO
	}
	r, _, _ := messageBox.Call(0, uintptr(unsafe.Pointer(textPointer(message))), uintptr(unsafe.Pointer(textPointer("Asterhand · 星伴"))), flags)
	if r == 6 && logDirectory != "" {
		_ = Open(logDirectory)
	}
}
