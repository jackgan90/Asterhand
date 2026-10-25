//go:build windows

package quickinput

import (
	"github.com/wailsapp/go-webview2/pkg/combridge"
)

type suspendCallback interface {
	combridge.IUnknown
	Completed(uintptr, int32) uintptr
}
type suspendHandler struct{ fn func(uintptr, int32) uintptr }

func (h *suspendHandler) Completed(code uintptr, success int32) uintptr { return h.fn(code, success) }
func init() {
	combridge.RegisterVTable[combridge.IUnknown, suspendCallback]("{00f206a7-9d17-4605-91f6-4e8e4de192e3}", func(this, code uintptr, success int32) uintptr {
		return combridge.Resolve[suspendCallback](this).Completed(code, success)
	})
}

func (s *nativeSurface) resume() {
	s.wantsSuspended = false
	s.powerGeneration++
	if s.core == nil || s.closing {
		return
	}
	if extension, err := s.core.query("{A0D6DF20-3B92-416D-AA0C-437A9C727857}"); err == nil {
		_ = extension.call(69)
		extension.release()
	}
	if s.documentReady {
		_ = s.postJSON(map[string]string{"kind": "theme", "theme": s.theme})
	}
}

func (s *nativeSurface) suspend(complete func()) {
	s.wantsSuspended = true
	if s.core == nil || s.closing {
		if complete != nil {
			complete()
		}
		return
	}
	_ = s.controller.call(4, 0)
	extension, err := s.core.query("{A0D6DF20-3B92-416D-AA0C-437A9C727857}")
	if err != nil {
		if complete != nil {
			complete()
		}
		return
	}
	s.powerGeneration++
	generation := s.powerGeneration
	h := combridge.New[suspendCallback](&suspendHandler{fn: func(_ uintptr, _ int32) uintptr {
		defer extension.release()
		if generation != s.powerGeneration && !s.wantsSuspended && !s.closing {
			_ = extension.call(69)
		}
		if complete != nil {
			complete()
		}
		return 0
	}})
	defer h.Close()
	if err = extension.call(68, h.Ref()); err != nil {
		extension.release()
		if complete != nil {
			complete()
		}
	}
}
