//go:build windows

package desktop

import (
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"unicode/utf16"
	"unsafe"
)

//go:embed assets/tray.ico
var trayICO []byte

// iconFrame picks the closest larger raster for native downsampling. ICO data
// is embedded at build time, but bounds are still checked before passing to Win32.
func iconFrame(data []byte, size int) ([]byte, error) {
	if len(data) < 6 || binary.LittleEndian.Uint16(data[2:]) != 1 {
		return nil, errors.New("无效的托盘 ICO")
	}
	count := int(binary.LittleEndian.Uint16(data[4:]))
	if count == 0 || count > (len(data)-6)/16 {
		return nil, errors.New("托盘 ICO 目录不完整")
	}
	best, distance := -1, int(^uint(0)>>1)
	for i := 0; i < count; i++ {
		entry := data[6+i*16 : 6+(i+1)*16]
		width := int(entry[0])
		if width == 0 {
			width = 256
		}
		d := width - size
		if d < 0 {
			d = 1024 - d
		}
		if d < distance {
			best, distance = i, d
		}
	}
	entry := data[6+best*16 : 6+(best+1)*16]
	length := uint64(binary.LittleEndian.Uint32(entry[8:]))
	offset := uint64(binary.LittleEndian.Uint32(entry[12:]))
	if length == 0 || offset < uint64(6+count*16) || offset+length > uint64(len(data)) {
		return nil, errors.New("托盘 ICO 图像超出资源范围")
	}
	return data[offset : offset+length], nil
}

func trayIconSize() int {
	if getDPIForWindow.Find() == nil && getMetricsForDPI.Find() == nil {
		hwnd, _, _ := findWindow.Call(uintptr(unsafe.Pointer(textPointer("Shell_TrayWnd"))), 0)
		if hwnd != 0 {
			dpi, _, _ := getDPIForWindow.Call(hwnd)
			if dpi != 0 {
				size, _, _ := getMetricsForDPI.Call(49, dpi) // SM_CXSMICON
				if size >= 16 && size <= 256 {
					return int(size)
				}
			}
		}
	}
	size, _, _ := getSystemMetrics.Call(49)
	if size < 16 || size > 256 {
		return 16
	}
	return int(size)
}

func (t *Tray) reloadIcon() error {
	size := trayIconSize()
	if t.icon != 0 && t.iconSize == size {
		return nil
	}
	frame, err := iconFrame(trayICO, size)
	if err != nil {
		return err
	}
	icon, _, err := createIcon.Call(uintptr(unsafe.Pointer(&frame[0])), uintptr(len(frame)), 1, 0x00030000, uintptr(size), uintptr(size), 0)
	if icon == 0 {
		return fmt.Errorf("加载托盘图标失败: %w", err)
	}
	old := t.icon
	t.icon, t.iconSize = icon, size
	t.lastTip = ""
	if old != 0 {
		destroyIcon.Call(old)
	}
	return nil
}

func (t *Tray) refreshIcon() {
	if err := t.reloadIcon(); err != nil {
		log.Printf("刷新托盘图标失败: %v", err)
	}
	t.lastTip = ""
	t.updateIcon()
}

func (t *Tray) iconData() notifyIconData {
	d := notifyIconData{Window: t.window.Load(), ID: 1, Flags: 1 | 2 | 4 | 0x80, Callback: wmTray, Icon: t.icon}
	d.Size = uint32(unsafe.Sizeof(d))
	return d
}

func (t *Tray) updateIcon() {
	state := t.options.State()
	tip := "Asterhand · " + state.Name + " — " + state.text()
	if t.registered && tip == t.lastTip {
		return
	}
	d := t.iconData()
	units := utf16.Encode([]rune(tip))
	if len(units) > 127 {
		units = units[:127]
		if units[126] >= 0xd800 && units[126] <= 0xdbff {
			units = units[:126]
		}
	}
	copy(d.Tip[:], units)
	action := uintptr(nimModify)
	if !t.registered {
		action = nimAdd
	}
	ok, _, _ := notifyIcon.Call(action, uintptr(unsafe.Pointer(&d)))
	if ok == 0 {
		t.registered = false
		return
	}
	if !t.registered {
		d.Version = 4
		notifyIcon.Call(nimSetVersion, uintptr(unsafe.Pointer(&d)))
	}
	t.registered, t.lastTip = true, tip
}
