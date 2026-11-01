//go:build windows

package config

import (
	"path/filepath"

	"github.com/jackgan90/Asterhand/internal/i18n"
	"golang.org/x/sys/windows"
)

func Lock(home string) (func(), error) {
	path, err := windows.UTF16PtrFromString(filepath.Join(home, "service.lock"))
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, i18n.Err("err.config.service_locked")
	}
	return func() { windows.CloseHandle(h) }, nil
}
