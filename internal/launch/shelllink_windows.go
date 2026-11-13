//go:build windows

package launch

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	clsctxInprocServer = 1
	stgmRead           = 0
	swShowMinNoActive  = 7

	shcneCreate     = 0x00000002
	shcneDelete     = 0x00000004
	shcneUpdateItem = 0x00002000
	shcnfPathW      = 0x0005
	shcnfFlush      = 0x1000
)

var (
	ole32DLL             = windows.NewLazySystemDLL("ole32.dll")
	shell32DLL           = windows.NewLazySystemDLL("shell32.dll")
	procCoCreateInstance = ole32DLL.NewProc("CoCreateInstance")
	procSHChangeNotify   = shell32DLL.NewProc("SHChangeNotify")
)

type comObj struct {
	vtbl *[32]uintptr
}

//go:uintptrescapes
func (o *comObj) call(slot int, args ...uintptr) uintptr {
	params := make([]uintptr, 0, len(args)+1)
	params = append(params, uintptr(unsafe.Pointer(o)))
	params = append(params, args...)
	hr, _, _ := syscall.SyscallN(o.vtbl[slot], params...)
	return hr
}

func (o *comObj) release() {
	if o != nil {
		o.call(2)
	}
}

func (o *comObj) queryInterface(iidString string) (*comObj, error) {
	iid, err := windows.GUIDFromString(iidString)
	if err != nil {
		return nil, err
	}
	var out *comObj
	hr := o.call(0, uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&out)))
	if int32(hr) < 0 || out == nil {
		return nil, fmt.Errorf("QueryInterface(%s) HRESULT 0x%08x", iidString, uint32(hr))
	}
	return out, nil
}

func (o *comObj) setWide(slot int, value string) error {
	p, err := windows.UTF16PtrFromString(value)
	if err != nil {
		return err
	}
	hr := o.call(slot, uintptr(unsafe.Pointer(p)))
	runtime.KeepAlive(p)
	if int32(hr) < 0 {
		return fmt.Errorf("COM slot %d HRESULT 0x%08x", slot, uint32(hr))
	}
	return nil
}

func withShellLinkCOM(fn func(link, persist *comObj) error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hrErr := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED)
	if hrErr == nil || errors.Is(hrErr, syscall.Errno(windows.S_FALSE)) {
		defer windows.CoUninitialize()
	} else if !errors.Is(hrErr, syscall.Errno(windows.RPC_E_CHANGED_MODE)) {
		return hrErr
	}

	clsid, err := windows.GUIDFromString("{00021401-0000-0000-C000-000000000046}")
	if err != nil {
		return err
	}
	iidLink, err := windows.GUIDFromString("{000214F9-0000-0000-C000-000000000046}")
	if err != nil {
		return err
	}
	var link *comObj
	hr, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsid)),
		0,
		clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidLink)),
		uintptr(unsafe.Pointer(&link)),
	)
	if int32(hr) < 0 || link == nil {
		return fmt.Errorf("CoCreateInstance(ShellLink) HRESULT 0x%08x", uint32(hr))
	}
	defer link.release()

	persist, err := link.queryInterface("{0000010B-0000-0000-C000-000000000046}")
	if err != nil {
		return err
	}
	defer persist.release()

	return fn(link, persist)
}

func readShellLink(path string) (target, args string, err error) {
	err = withShellLinkCOM(func(link, persist *comObj) error {
		pathPtr, convErr := windows.UTF16PtrFromString(path)
		if convErr != nil {
			return convErr
		}
		hr := persist.call(5, uintptr(unsafe.Pointer(pathPtr)), stgmRead)
		runtime.KeepAlive(pathPtr)
		if int32(hr) < 0 {
			return fmt.Errorf("IPersistFile::Load HRESULT 0x%08x", uint32(hr))
		}
		targetBuf := make([]uint16, 4096)
		hr = link.call(3, uintptr(unsafe.Pointer(&targetBuf[0])), uintptr(len(targetBuf)), 0, 0)
		runtime.KeepAlive(targetBuf)
		if int32(hr) < 0 {
			return fmt.Errorf("IShellLinkW::GetPath HRESULT 0x%08x", uint32(hr))
		}
		if hr != 1 { // S_FALSE means no target path stored.
			target = windows.UTF16ToString(targetBuf)
		}
		argBuf := make([]uint16, 4096)
		hr = link.call(10, uintptr(unsafe.Pointer(&argBuf[0])), uintptr(len(argBuf)))
		runtime.KeepAlive(argBuf)
		if int32(hr) < 0 {
			return fmt.Errorf("IShellLinkW::GetArguments HRESULT 0x%08x", uint32(hr))
		}
		args = windows.UTF16ToString(argBuf)
		return nil
	})
	return target, args, err
}

func writeShellLink(path, installRoot, launcherPath string) error {
	return withShellLinkCOM(func(link, persist *comObj) error {
		if err := link.setWide(20, launcherPath); err != nil {
			return err
		}
		if err := link.setWide(11, ""); err != nil {
			return err
		}
		if err := link.setWide(9, installRoot); err != nil {
			return err
		}
		if err := link.setWide(7, shortcutDescription); err != nil {
			return err
		}
		if hr := link.call(15, swShowMinNoActive); int32(hr) < 0 {
			return fmt.Errorf("IShellLinkW::SetShowCmd HRESULT 0x%08x", uint32(hr))
		}
		iconPtr, err := windows.UTF16PtrFromString(launcherPath)
		if err != nil {
			return err
		}
		hr := link.call(17, uintptr(unsafe.Pointer(iconPtr)), 0)
		runtime.KeepAlive(iconPtr)
		if int32(hr) < 0 {
			return fmt.Errorf("IShellLinkW::SetIconLocation HRESULT 0x%08x", uint32(hr))
		}
		savePtr, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return err
		}
		hr = persist.call(6, uintptr(unsafe.Pointer(savePtr)), 1)
		runtime.KeepAlive(savePtr)
		if int32(hr) < 0 {
			return fmt.Errorf("IPersistFile::Save HRESULT 0x%08x", uint32(hr))
		}
		return nil
	})
}

func notifyShellPath(event uintptr, path string) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	_, _, _ = procSHChangeNotify.Call(event, shcnfPathW|shcnfFlush, uintptr(unsafe.Pointer(p)), 0)
	runtime.KeepAlive(p)
}
