//go:build windows

package config

import (
	"errors"
	"golang.org/x/sys/windows"
	"path/filepath"
)

func Lock(home string) (func(), error) {
	path, err := windows.UTF16PtrFromString(filepath.Join(home, "service.lock"))
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, errors.New("此节点数据目录已由另一个服务持有，或目录不可写")
	}
	return func() { windows.CloseHandle(h) }, nil
}
