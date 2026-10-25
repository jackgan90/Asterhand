//go:build windows

package quickinput

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

//go:embed assets/*
var inputAssets embed.FS

const surfaceOrigin = "https://quick-input.asterhand.invalid"
const surfaceURL = surfaceOrigin + "/quick-input/index.html"
const inputCSP = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'"

func (s *nativeSurface) configureContent() error {
	settings, err := s.core.object(3)
	if err != nil {
		return err
	}
	defer settings.release()
	for _, slot := range []int{8, 10, 12, 14, 16, 18, 20} {
		if err = settings.call(slot, 0); err != nil {
			return err
		}
	}
	s4, err := settings.query("{cb56846c-4168-4d53-b04f-03b6d6796ff2}")
	if err != nil {
		return err
	}
	defer s4.release()
	for _, slot := range []int{24, 26, 28} {
		if err = s4.call(slot, 0); err != nil {
			return err
		}
	}
	if err = s.events(); err != nil {
		return err
	}
	pattern, _ := windows.UTF16PtrFromString("*")
	if err = s.core.call(57, uintptr(unsafe.Pointer(pattern)), 0); err != nil {
		return err
	}
	return s.core.putText(5, surfaceURL)
}

func (s *nativeSurface) events() error {
	add := func(event eventSubscription, err error) error {
		if err == nil {
			s.subscriptions = append(s.subscriptions, event)
		}
		return err
	}
	if err := add(subscribe[messageCallback](s.core, 34, s.receiveMessage)); err != nil {
		return err
	}
	if err := add(subscribe[resourceCallback](s.core, 55, s.resourceRequested)); err != nil {
		return err
	}
	if err := add(subscribe[navigationCallback](s.core, 7, func(_ uintptr, args *comRef) uintptr {
		uri, err := args.text(3)
		if err != nil || uri != surfaceURL || s.documentReady {
			_ = args.call(8, 1)
		}
		return 0
	})); err != nil {
		return err
	}
	if err := add(subscribe[navigationCallback](s.core, 17, func(_ uintptr, args *comRef) uintptr {
		_ = args.call(8, 1)
		return 0
	})); err != nil {
		return err
	}
	if err := add(subscribe[newWindowCallback](s.core, 44, func(_ uintptr, args *comRef) uintptr {
		_ = args.call(6, 1)
		return 0
	})); err != nil {
		return err
	}
	if err := add(subscribe[permissionCallback](s.core, 23, func(_ uintptr, args *comRef) uintptr {
		_ = args.call(7, 2)
		return 0
	})); err != nil {
		return err
	}
	if err := add(subscribe[processCallback](s.core, 25, func(_ uintptr, _ *comRef) uintptr {
		if !s.closing && s.onFailure != nil {
			go s.onFailure(fmt.Errorf("WebView2 渲染进程中断，请重试快捷输入"))
		}
		return 0
	})); err != nil {
		return err
	}
	extension, err := s.core.query("{20d02d59-6df2-42dc-bd06-f98a694b1302}")
	if err != nil {
		return err
	}
	defer extension.release()
	event, err := subscribe[downloadCallback](extension, 75, func(_ uintptr, args *comRef) uintptr {
		_ = args.call(5, 1)
		return 0
	})
	if err != nil {
		return err
	}
	// The subscription owns an interface reference until it is removed.
	event.object.retain()
	closeHandler := event.close
	event.close = func() error { defer event.object.release(); return closeHandler() }
	s.subscriptions = append(s.subscriptions, event)
	return nil
}

func (s *nativeSurface) receiveMessage(_ uintptr, args *comRef) uintptr {
	source, err := args.text(3)
	if err != nil || source != surfaceURL || s.closing {
		return 0
	}
	current, err := s.core.text(4)
	if err != nil || current != surfaceURL {
		return 0
	}
	message, err := args.boundedText(4, 128*1024)
	if err == nil && len(message) <= 128*1024 {
		s.bridgeMessage(message)
		if s.onMessage != nil {
			s.onMessage(message)
		}
	}
	return 0
}

func (s *nativeSurface) resourceRequested(_ uintptr, args *comRef) uintptr {
	request, err := args.object(3)
	if err != nil {
		return 0
	}
	defer request.release()
	uri, _ := request.text(3)
	method, _ := request.text(5)
	file := strings.TrimPrefix(uri, surfaceOrigin+"/quick-input/")
	body, mime, status := []byte("Blocked"), "text/plain", 403
	if method == "GET" && strings.HasPrefix(uri, surfaceOrigin+"/quick-input/") {
		allowed := map[string]string{"index.html": "text/html; charset=utf-8", "input.css": "text/css; charset=utf-8", "input.js": "application/javascript; charset=utf-8", "input-model.mjs": "application/javascript; charset=utf-8"}
		if contentType, ok := allowed[file]; ok {
			if data, readErr := inputAssets.ReadFile("assets/" + file); readErr == nil {
				body, mime, status = data, contentType, 200
			}
		}
	}
	streamPointer, _, _ := windows.NewLazySystemDLL("shlwapi.dll").NewProc("SHCreateMemStream").Call(uintptr(unsafe.Pointer(&body[0])), uintptr(len(body)))
	if streamPointer == 0 {
		return 0
	}
	// SHCreateMemStream returns an IStream allocated by Windows, never Go heap
	// memory. Copy its pointer bits into a typed native reference immediately.
	var stream *comRef
	*(*uintptr)(unsafe.Pointer(&stream)) = streamPointer
	defer stream.release()
	reason, _ := windows.UTF16PtrFromString("Response")
	headers, _ := windows.UTF16PtrFromString("Content-Type: " + mime + "\r\nCache-Control: no-store\r\nX-Content-Type-Options: nosniff\r\nContent-Security-Policy: " + inputCSP)
	var response *comRef
	err = s.environment.call(4, streamPointer, uintptr(status), uintptr(unsafe.Pointer(reason)), uintptr(unsafe.Pointer(headers)), uintptr(unsafe.Pointer(&response)))
	if err == nil {
		_ = args.call(5, uintptr(unsafe.Pointer(response)))
		response.release()
	}
	return 0
}

func (s *nativeSurface) postJSON(value any) error {
	current, err := s.core.text(4)
	if err != nil || current != surfaceURL {
		return fmt.Errorf("输入界面来源不正确")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.core.putText(32, string(data))
}
