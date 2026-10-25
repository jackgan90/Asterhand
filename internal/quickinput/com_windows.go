//go:build windows

package quickinput

import (
	"fmt"
	"syscall"
	"unsafe"

	"github.com/wailsapp/go-webview2/pkg/combridge"
	"golang.org/x/sys/windows"
)

// Slots are the inherited Win32 ABI from Microsoft.Web.WebView2 1.0.2792.45
// WebView2.h, not the incomplete generated extension vtables in the binding.
type comRef struct{ table *[128]uintptr }

// uintptr arguments include native out-pointers. Force their Go allocation and
// lifetime across this wrapper; otherwise a stack growth before SyscallN can
// leave COM writing through an address on the old Go stack.
//
//go:uintptrescapes
func (p *comRef) call(slot int, args ...uintptr) error {
	if p == nil {
		return fmt.Errorf("WebView2 interface unavailable")
	}
	params := append([]uintptr{uintptr(unsafe.Pointer(p))}, args...)
	hr, _, _ := syscall.SyscallN(p.table[slot], params...)
	if int32(hr) < 0 {
		return fmt.Errorf("WebView2 HRESULT 0x%08x", uint32(hr))
	}
	return nil
}

func (p *comRef) release() {
	if p != nil {
		_ = p.call(2)
	}
}
func (p *comRef) retain() *comRef {
	if p != nil {
		_ = p.call(1)
	}
	return p
}
func (p *comRef) query(id string) (*comRef, error) {
	guid, err := windows.GUIDFromString(id)
	if err != nil {
		return nil, err
	}
	var result *comRef
	err = p.call(0, uintptr(unsafe.Pointer(&guid)), uintptr(unsafe.Pointer(&result)))
	return result, err
}

func (p *comRef) object(slot int) (*comRef, error) {
	var result *comRef
	err := p.call(slot, uintptr(unsafe.Pointer(&result)))
	if err == nil && result == nil {
		err = fmt.Errorf("WebView2 返回空接口，slot=%d", slot)
	}
	return result, err
}

func (p *comRef) text(slot int) (string, error) {
	return p.boundedText(slot, 4096)
}

func (p *comRef) boundedText(slot, limit int) (string, error) {
	var result *uint16
	if err := p.call(slot, uintptr(unsafe.Pointer(&result))); err != nil {
		return "", err
	}
	if result == nil {
		return "", nil
	}
	defer windows.CoTaskMemFree(unsafe.Pointer(result))
	units := make([]uint16, 0, min(limit, 4096))
	for i := 0; i <= limit; i++ {
		unit := *(*uint16)(unsafe.Add(unsafe.Pointer(result), uintptr(i)*2))
		if unit == 0 {
			text := windows.UTF16ToString(units)
			if len(text) > limit {
				return "", fmt.Errorf("WebView2 文本超过长度限制")
			}
			return text, nil
		}
		units = append(units, unit)
	}
	return "", fmt.Errorf("WebView2 文本超过长度限制")
}

func (p *comRef) putText(slot int, text string) error {
	value, err := windows.UTF16PtrFromString(text)
	if err != nil {
		return err
	}
	return p.call(slot, uintptr(unsafe.Pointer(value)))
}

type callbackFunc struct {
	fn func(uintptr, *comRef) uintptr
}

func (c *callbackFunc) Invoke(a uintptr, b *comRef) uintptr { return c.fn(a, b) }

type comCallback interface {
	combridge.IUnknown
	Invoke(uintptr, *comRef) uintptr
}
type controllerCallback interface{ comCallback }
type messageCallback interface{ comCallback }
type navigationCallback interface{ comCallback }
type resourceCallback interface{ comCallback }
type newWindowCallback interface{ comCallback }
type permissionCallback interface{ comCallback }
type processCallback interface{ comCallback }
type downloadCallback interface{ comCallback }
type browserExitCallback interface{ comCallback }

func registerCallback[T comCallback](id string) {
	combridge.RegisterVTable[combridge.IUnknown, T](id, func(this, a uintptr, b *comRef) uintptr {
		return combridge.Resolve[T](this).Invoke(a, b)
	})
}

func init() {
	registerCallback[controllerCallback]("{6c4819f3-c9b7-4260-8127-c9f5bde7f68c}")
	registerCallback[messageCallback]("{57213f19-00e6-49fa-8e07-898ea01ecbd2}")
	registerCallback[navigationCallback]("{9adbe429-f36d-432b-9ddc-f8881fbd76e3}")
	registerCallback[resourceCallback]("{ab00b74c-15f1-4646-80e8-e76341d25d71}")
	registerCallback[newWindowCallback]("{d4c185fe-c81c-4989-97af-2d3fa7ab5651}")
	registerCallback[permissionCallback]("{15e1c6a3-c72a-4df3-91d7-d097fbec6bfd}")
	registerCallback[processCallback]("{79e0aea4-990b-42d9-aa1d-0fcc2e5bc7f1}")
	registerCallback[downloadCallback]("{efedc989-c396-41ca-83f7-07f845a55724}")
	registerCallback[browserExitCallback]("{fa504257-a216-4911-a860-fe8825712861}")
}

func newCallback[T comCallback](fn func(uintptr, *comRef) uintptr) *combridge.ComObject[T] {
	return combridge.New[T](any(&callbackFunc{fn: fn}).(T))
}

type eventSubscription struct {
	object *comRef
	remove int
	token  int64
	close  func() error
}

func subscribe[T comCallback](p *comRef, add int, fn func(uintptr, *comRef) uintptr) (eventSubscription, error) {
	h := newCallback[T](fn)
	var token int64
	err := p.call(add, h.Ref(), uintptr(unsafe.Pointer(&token)))
	if err != nil {
		_ = h.Close()
		return eventSubscription{}, err
	}
	return eventSubscription{p, add + 1, token, h.Close}, nil
}

func (e eventSubscription) unsubscribe() {
	_ = e.object.call(e.remove, uintptr(e.token))
	_ = e.close()
}
