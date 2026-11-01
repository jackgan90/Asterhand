//go:build windows

package i18n

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const muiLanguageName = 0x8

var (
	kernel32                       = windows.NewLazySystemDLL("kernel32.dll")
	procGetUserPreferredUILangs    = kernel32.NewProc("GetUserPreferredUILanguages")
	procGetUserDefaultLocaleName   = kernel32.NewProc("GetUserDefaultLocaleName")
)

func detectSystemLanguages() ([]string, bool) {
	var numLangs uint32
	var bufLen uint32
	r1, _, _ := procGetUserPreferredUILangs.Call(
		muiLanguageName,
		uintptr(unsafe.Pointer(&numLangs)),
		0,
		uintptr(unsafe.Pointer(&bufLen)),
	)
	if r1 != 0 && bufLen > 0 {
		buf := make([]uint16, bufLen)
		r2, _, _ := procGetUserPreferredUILangs.Call(
			muiLanguageName,
			uintptr(unsafe.Pointer(&numLangs)),
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(unsafe.Pointer(&bufLen)),
		)
		if r2 != 0 {
			langs := splitMultiStringUTF16(buf)
			if len(langs) > 0 {
				return langs, true
			}
		}
	}
	buf := make([]uint16, 85)
	r, _, _ := procGetUserDefaultLocaleName.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if r > 0 {
		name := strings.TrimSpace(windows.UTF16ToString(buf))
		if name != "" {
			return []string{name}, true
		}
	}
	return nil, false
}

func splitMultiStringUTF16(buf []uint16) []string {
	var out []string
	start := 0
	for i, v := range buf {
		if v == 0 {
			if i > start {
				s := strings.TrimSpace(windows.UTF16ToString(buf[start:i]))
				if s != "" {
					out = append(out, s)
				}
			}
			start = i + 1
		}
	}
	return out
}
