//go:build windows

package launch

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(abs, `\\.\`) || strings.HasPrefix(abs, `\\?\`) || strings.Contains(strings.TrimPrefix(abs, filepath.VolumeName(abs)), ":") {
		return "", errors.New("路径不能使用设备命名空间或备用数据流")
	}
	p, err := windows.UTF16PtrFromString(abs)
	if err != nil {
		return "", err
	}
	h, err := windows.CreateFile(p, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			parent := filepath.Dir(abs)
			if parent == abs {
				return "", err
			}
			resolved, parentErr := canonicalPath(parent)
			return filepath.Join(resolved, filepath.Base(abs)), parentErr
		}
		return "", err
	}
	defer windows.CloseHandle(h)
	buffer := make([]uint16, 32768)
	n, err := windows.GetFinalPathNameByHandle(h, &buffer[0], uint32(len(buffer)), 0)
	if err != nil || n >= uint32(len(buffer)) {
		return "", fmt.Errorf("解析路径失败：%v", err)
	}
	resolved := windows.UTF16ToString(buffer[:n])
	if strings.HasPrefix(resolved, `\\?\UNC\`) {
		resolved = `\\` + strings.TrimPrefix(resolved, `\\?\UNC\`)
	} else {
		resolved = strings.TrimPrefix(resolved, `\\?\`)
	}
	return filepath.Clean(resolved), nil
}

func launchMutexName(home string) (string, error) {
	// This byte protocol is shared with install.ps1. Language-runtime Unicode
	// casing differs, so both sides use the same Windows filesystem mapping.
	source, err := windows.UTF16FromString(filepath.Clean(home))
	if err != nil {
		return "", err
	}
	locale := uint16(0) // LOCALE_NAME_INVARIANT is an empty UTF-16 string.
	const lcmapLowercase = 0x00000100
	mapString := windows.NewLazySystemDLL("kernel32.dll").NewProc("LCMapStringEx")
	count, _, callErr := mapString.Call(uintptr(unsafe.Pointer(&locale)), lcmapLowercase,
		uintptr(unsafe.Pointer(&source[0])), uintptr(len(source)-1), 0, 0, 0, 0, 0)
	if count == 0 {
		return "", fmt.Errorf("规范化启动锁路径失败：%w", callErr)
	}
	mapped := make([]uint16, int(count))
	count, _, callErr = mapString.Call(uintptr(unsafe.Pointer(&locale)), lcmapLowercase,
		uintptr(unsafe.Pointer(&source[0])), uintptr(len(source)-1), uintptr(unsafe.Pointer(&mapped[0])), uintptr(len(mapped)), 0, 0, 0)
	if count == 0 {
		return "", fmt.Errorf("规范化启动锁路径失败：%w", callErr)
	}
	key := windows.UTF16ToString(mapped[:count])
	return fmt.Sprintf(`Local\AsterhandLaunch-%x`, sha256.Sum256([]byte(key))), nil
}

func acquireLaunchLock(ctx context.Context, home string) (func(), error) {
	name, err := launchMutexName(home)
	if err != nil {
		return nil, err
	}
	// Windows mutex ownership belongs to the OS thread, not the goroutine.
	runtime.LockOSThread()
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		runtime.UnlockOSThread()
		return nil, err
	}
	h, err := windows.CreateMutex(nil, false, p)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		runtime.UnlockOSThread()
		return nil, err
	}
	for {
		status, waitErr := windows.WaitForSingleObject(h, 100)
		if status == windows.WAIT_OBJECT_0 || status == windows.WAIT_ABANDONED {
			return func() { windows.ReleaseMutex(h); windows.CloseHandle(h); runtime.UnlockOSThread() }, nil
		}
		if waitErr != nil || ctx.Err() != nil {
			windows.CloseHandle(h)
			runtime.UnlockOSThread()
			return nil, errors.New("另一个启动或安装操作尚未结束，请稍后重试")
		}
	}
}

func serviceBusy(home string) (bool, error) {
	p, err := windows.UTF16PtrFromString(filepath.Join(home, "service.lock"))
	if err != nil {
		return false, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return false, nil
	}
	if errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("无法核对节点锁，保留现有进程：%w", err)
	}
	windows.CloseHandle(h)
	return false, nil
}

func inspectProcess(pid int) (processRecord, error) {
	if pid <= 0 || uint64(pid) > uint64(^uint32(0)) {
		return processRecord{}, os.ErrProcessDone
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return processRecord{}, os.ErrProcessDone
	}
	if err != nil {
		return processRecord{}, err
	}
	defer windows.CloseHandle(h)
	if status, err := windows.WaitForSingleObject(h, 0); err != nil || status != uint32(windows.WAIT_TIMEOUT) {
		if err != nil {
			return processRecord{}, err
		}
		return processRecord{}, os.ErrProcessDone
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return processRecord{}, err
	}
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(h, 0, &buffer[0], &size); err != nil {
		return processRecord{}, err
	}
	return processRecord{PID: pid, Created: uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime), Executable: filepath.Clean(windows.UTF16ToString(buffer[:size]))}, nil
}

func configureChild(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}
