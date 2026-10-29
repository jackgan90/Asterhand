//go:build windows

package quickinput

// Release/development host builds both embed application.ico as resource group
// 1. Unresourced `go test` binaries fall back to the system application icon.
func (s *nativeSurface) setWindowIcons() {
	dpi, _, _ := uiDLL.NewProc("GetDpiForWindow").Call(s.hwnd.Load())
	if dpi == 0 {
		dpi = 96
	}
	for index, metric := range []uintptr{11, 49} { // SM_CXICON, SM_CXSMICON
		size, _, _ := uiDLL.NewProc("GetSystemMetricsForDpi").Call(metric, dpi)
		icon, _, _ := uiDLL.NewProc("LoadImageW").Call(s.instance, 1, 1, size, size, 0)
		if icon == 0 {
			shared, _, _ := uiDLL.NewProc("LoadIconW").Call(0, 32512)
			icon, _, _ = uiDLL.NewProc("CopyIcon").Call(shared)
		}
		if icon != 0 {
			kind := uintptr(1 - index) // ICON_BIG, ICON_SMALL
			uiDLL.NewProc("SendMessageW").Call(s.hwnd.Load(), 0x0080, kind, icon)
			if old := s.icons[index]; old != 0 {
				uiDLL.NewProc("DestroyIcon").Call(old)
			}
			s.icons[index] = icon
		}
	}
}

func (s *nativeSurface) releaseWindowIcons() {
	for index, icon := range s.icons {
		if icon != 0 {
			uiDLL.NewProc("DestroyIcon").Call(icon)
			s.icons[index] = 0
		}
	}
}
