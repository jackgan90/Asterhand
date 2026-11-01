//go:build windows

package desktop

import (
	"syscall"

	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/quickinput"
)

type hotkeyRequest struct {
	fn     func() (int, error)
	result chan hotkeyResult
}
type hotkeyResult struct {
	id  int
	err error
}
type trayRegistrar struct{ tray *Tray }

func (r *trayRegistrar) invoke(fn func() (int, error)) (int, error) {
	request := hotkeyRequest{fn, make(chan hotkeyResult, 1)}
	select {
	case r.tray.inputCommands <- request:
	default:
		return 0, i18n.Err("err.desktop.queue_full")
	}
	postMessage.Call(r.tray.window.Load(), wmInputCommand, 0, 0)
	select {
	case result := <-request.result:
		return result.id, result.err
	case <-r.tray.done:
		return 0, i18n.Err("err.desktop.tray_closed")
	}
}

func (r *trayRegistrar) Register(binding quickinput.Binding) (int, error) {
	modifiers, key, err := binding.WindowsKeys()
	if err != nil {
		return 0, err
	}
	return r.invoke(func() (int, error) {
		r.tray.nextHotkey++
		id := r.tray.nextHotkey
		if id > 0xbfff {
			return 0, i18n.Err("err.desktop.hotkey_ids_exhausted")
		}
		ok, _, callErr := user32.NewProc("RegisterHotKey").Call(r.tray.window.Load(), uintptr(id), uintptr(modifiers), uintptr(key))
		if ok == 0 {
			code := "unavailable"
			if callErr == syscall.Errno(1409) {
				code = "conflict"
			}
			args := map[string]string{"binding": binding.String(), "err": callErr.Error()}
			return 0, &quickinput.RegistrationError{
				Code:   code,
				Key:    "err.desktop.hotkey_register_failed",
				Args:   args,
				Detail: i18n.T(i18n.Effective(), "err.desktop.hotkey_register_failed", args),
			}
		}
		return id, nil
	})
}

func (r *trayRegistrar) Unregister(id int) error {
	_, err := r.invoke(func() (int, error) {
		ok, _, err := user32.NewProc("UnregisterHotKey").Call(r.tray.window.Load(), uintptr(id))
		if ok == 0 {
			return 0, err
		}
		return 0, nil
	})
	return err
}
