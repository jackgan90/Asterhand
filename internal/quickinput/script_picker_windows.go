//go:build windows

package quickinput

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/localcommands"
	"golang.org/x/sys/windows"
)

// PickLocalCommandScript uses a disposable ordinary-user GUI process. Its stdin
// pipe also lets the helper detect abrupt parent death, without node secrets.
func PickLocalCommandScript(ctx context.Context) (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	return pickScriptProcess(ctx, executable, "pick-local-script", "--locale", i18n.Effective())
}

func pickScriptProcess(ctx context.Context, executable string, args ...string) (string, error) {
	if err := checkInteractiveSession(); err != nil {
		return "", err
	}
	token, err := restrictedHostToken()
	if err != nil {
		return "", err
	}
	if token != 0 {
		defer token.Close()
	}
	envToken := token
	if envToken == 0 {
		envToken = windows.GetCurrentProcessToken()
	}
	environment, err := envToken.Environ(false)
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.WaitDelay = time.Second
	cmd.Env, cmd.Dir = environment, filepath.Dir(executable)
	// HideWindow would hide the helper's first GUI dialog as well. Suppress
	// only its console; the explicitly requested file chooser must be visible.
	cmd.SysProcAttr = &syscall.SysProcAttr{Token: syscall.Token(token), CreationFlags: windows.CREATE_NO_WINDOW}
	owner, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	defer owner.Close()
	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", i18n.Wrap("err.quickinput.picker_open_failed", nil, err)
	}
	var result struct {
		Path string `json:"path"`
	}
	if len(output) > 16*1024 || json.Unmarshal(output, &result) != nil {
		return "", i18n.Err("err.quickinput.picker_invalid_result")
	}
	if result.Path == "" {
		return "", nil
	}
	return localcommands.ScriptPath(result.Path)
}

type scriptOpenFileName struct {
	Size                                     uint32
	Owner, Instance                          uintptr
	Filter, CustomFilter                     *uint16
	MaxCustomFilter, FilterIndex             uint32
	File                                     *uint16
	MaxFile                                  uint32
	FileTitle                                *uint16
	MaxFileTitle                             uint32
	InitialDirectory, Title                  *uint16
	Flags                                    uint32
	FileOffset, FileExtension                uint16
	DefaultExtension                         *uint16
	CustomData, Hook, TemplateName, Reserved uintptr
	ReservedWord, FlagsEx                    uint32
}

// RunScriptPicker is called only by the helper entry point before node startup.
func RunScriptPicker(locale string) (string, error) {
	if locale != "" {
		if !i18n.ValidLocale(locale) {
			return "", i18n.Err("err.cli.locale_invalid")
		}
		i18n.SetDefaultPreference(locale)
	}
	if err := verifyHostToken(windows.GetCurrentProcessToken()); err != nil {
		return "", err
	}
	if err := checkInteractiveDesktop(); err != nil {
		return "", err
	}
	uiDLL.NewProc("SetProcessDpiAwarenessContext").Call(^uintptr(3))
	return showScriptPicker(nil)
}

func showScriptPicker(ready func(uintptr)) (string, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	monitor := foregroundMonitor()
	buffer := make([]uint16, 32768)
	loc := i18n.Effective()
	filter := utf16.Encode([]rune(i18n.T(loc, "desktop.picker.filter_label") + "\x00*.bat;*.cmd;*.ps1\x00\x00"))
	title, _ := windows.UTF16PtrFromString(i18n.T(loc, "desktop.picker.title"))
	const fileMustExist, pathMustExist, noChangeDir = 0x1000, 0x800, 0x8
	const explorer, hideReadOnly, noRecent, enableHook = 0x80000, 0x4, 0x2000000, 0x20
	options := scriptOpenFileName{Size: uint32(unsafe.Sizeof(scriptOpenFileName{})), Filter: &filter[0], FilterIndex: 1,
		File: &buffer[0], MaxFile: uint32(len(buffer)), Title: title,
		Flags: fileMustExist | pathMustExist | noChangeDir | explorer | hideReadOnly | noRecent | enableHook}
	options.Hook = syscall.NewCallback(func(hwnd uintptr, message uint32, _ uintptr, value unsafe.Pointer) uintptr {
		if message == 0x4e && value != nil && (*struct {
			Window, ID uintptr
			Code       int32
		})(value).Code == -601 { // CDN_INITDONE
			parent, _, _ := uiDLL.NewProc("GetParent").Call(hwnd)
			positionScriptPicker(parent, monitor)
			if ready != nil {
				ready(parent)
			}
		}
		return 0
	})
	dialog := windows.NewLazySystemDLL("comdlg32.dll")
	ok, _, _ := dialog.NewProc("GetOpenFileNameW").Call(uintptr(unsafe.Pointer(&options)))
	runtime.KeepAlive(filter)
	if ok == 0 {
		code, _, _ := dialog.NewProc("CommDlgExtendedError").Call()
		if code != 0 {
			return "", i18n.ErrArgs("err.quickinput.picker_win_error", map[string]string{"code": strconv.FormatUint(uint64(code), 10)})
		}
		return "", nil
	}
	return localcommands.ScriptPath(windows.UTF16ToString(buffer))
}

func positionScriptPicker(hwnd, monitorID uintptr) {
	// Keep the explicitly requested chooser visible without stealing focus.
	const noSize, noMove, noActivate = 0x1, 0x2, 0x10
	monitor, ok := monitorDetails(monitorID)
	if !ok {
		uiDLL.NewProc("SetWindowPos").Call(hwnd, ^uintptr(0), 0, 0, 0, 0, noSize|noMove|noActivate)
		return
	}
	// Moving to another monitor can change the dialog's DPI and dimensions.
	// Re-read its actual size before the final alignment, while still hidden.
	for range 2 {
		var bounds nativeRect
		if ok, _, _ := uiDLL.NewProc("GetWindowRect").Call(hwnd, uintptr(unsafe.Pointer(&bounds))); ok == 0 {
			return
		}
		x := monitor.work.Left + max(0, (monitor.work.Right-monitor.work.Left-(bounds.Right-bounds.Left))/2)
		y := monitor.work.Top + max(0, (monitor.work.Bottom-monitor.work.Top-(bounds.Bottom-bounds.Top))/2)
		uiDLL.NewProc("SetWindowPos").Call(hwnd, ^uintptr(0), uintptr(x), uintptr(y), 0, 0, noSize|noActivate)
	}
}
