//go:build windows

package quickinput

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"
)

func experienceSurface(t *testing.T) *nativeSurface {
	t.Helper()
	if err := checkInteractiveDesktop(); err != nil {
		t.Fatal(err)
	}
	s, err := startNativeSurface(t.TempDir(), nil, func(err error) { t.Errorf("surface: %v", err) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	s.Toggle()
	return s
}

func surfaceSync(t *testing.T, s *nativeSurface, fn func()) {
	t.Helper()
	done := make(chan struct{})
	if err := s.dispatch(context.Background(), func() { fn(); close(done) }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("native UI operation timed out")
	}
}

type experienceWindowState struct {
	visible, minimized, topmost bool
	bounds                      nativeRect
	style, extended, owner      uintptr
}

func experienceState(t *testing.T, s *nativeSurface) experienceWindowState {
	t.Helper()
	var result experienceWindowState
	surfaceSync(t, s, func() {
		hwnd := s.hwnd.Load()
		visible, _, _ := uiDLL.NewProc("IsWindowVisible").Call(hwnd)
		iconic, _, _ := uiDLL.NewProc("IsIconic").Call(hwnd)
		result.style, _, _ = uiDLL.NewProc("GetWindowLongPtrW").Call(hwnd, ^uintptr(15))
		result.extended, _, _ = uiDLL.NewProc("GetWindowLongPtrW").Call(hwnd, ^uintptr(19))
		result.owner, _, _ = uiDLL.NewProc("GetWindow").Call(hwnd, 4)
		result.visible, result.minimized, result.topmost = visible != 0, iconic != 0, result.extended&8 != 0
		uiDLL.NewProc("GetWindowRect").Call(hwnd, uintptr(unsafe.Pointer(&result.bounds)))
	})
	return result
}

func TestNativeChatDesktopWindow(t *testing.T) {
	if os.Getenv("ASTERHAND_CHAT_GATE") != "1" {
		t.Skip("explicit desktop gate required")
	}
	if os.Getenv("ASTERHAND_CHAT_GATE_CHILD") != "1" {
		runChatGateAtMedium(t)
		return
	}
	s := experienceSurface(t)
	waitChatScript(t, s, `document.querySelector('#input') && !document.querySelector('#input').disabled`)
	state := experienceState(t, s)
	if !state.visible || state.topmost || state.extended&0x40000 == 0 || state.extended&0x80 != 0 || state.owner != 0 {
		t.Fatalf("not an independent taskbar application window: %+v", state)
	}
	if state.style&0x20000 == 0 {
		t.Fatal("minimize style absent")
	}
	if result := evaluate(t, s, `getComputedStyle(document.querySelector('header')).getPropertyValue('app-region')==='drag' && [...document.querySelectorAll('header button')].every(b=>getComputedStyle(b).getPropertyValue('app-region')==='no-drag') && !document.querySelector('#chat-pin')`); result != "true" {
		t.Fatal("drag/control regions", result)
	}
	var enabled int32
	surfaceSync(t, s, func() {
		settings, err := s.core.object(3)
		if err != nil {
			t.Error(err)
			return
		}
		defer settings.release()
		regions, err := settings.query("{0528a73b-e92d-49f4-927a-e547dddaa37d}")
		if err != nil {
			t.Error(err)
			return
		}
		defer regions.release()
		if err := regions.call(37, uintptr(unsafe.Pointer(&enabled))); err != nil {
			t.Error(err)
		}
		if s.icons[0] == 0 || s.icons[1] == 0 {
			t.Error("window icon missing")
		}
		// Inject focus loss and the end of a native move without touching another app.
		s.windowEvent(s.hwnd.Load(), 0x0006, 0, 0)
		monitor, ok := monitorDetails(foregroundMonitor())
		if !ok {
			t.Error("monitor missing")
			return
		}
		point := windowPoint{monitor.work.Left + scaled(40, monitor.dpi), monitor.work.Top + scaled(40, monitor.dpi)}
		s.applyBounds(fitWindow(monitor.work, monitor.dpi, 240, &point))
		s.windowEvent(s.hwnd.Load(), 0x0232, 0, 0)
	})
	if enabled != 1 {
		t.Fatal("native dragging not enabled")
	}
	before := experienceState(t, s)
	if !before.visible {
		t.Fatal("focus loss hid the window")
	}
	evaluate(t, s, `document.querySelector('#chat-expand').click();true`)
	waitChatScript(t, s, `!document.querySelector('#chat-panel').hidden && innerHeight>400`)
	after := experienceState(t, s)
	if before.bounds.Left != after.bounds.Left || before.bounds.Top != after.bounds.Top {
		t.Fatalf("expansion lost dragged anchor: %+v -> %+v", before.bounds, after.bounds)
	}
	surfaceSync(t, s, func() { uiDLL.NewProc("ShowWindow").Call(s.hwnd.Load(), 6) })
	if state = experienceState(t, s); !state.visible || !state.minimized {
		t.Fatal("minimize removed taskbar window", state)
	}
	surfaceSync(t, s, func() { uiDLL.NewProc("ShowWindow").Call(s.hwnd.Load(), 9) })
	if state = experienceState(t, s); !state.visible || state.minimized {
		t.Fatal("restore failed", state)
	}
	s.Toggle()
	if experienceState(t, s).visible {
		t.Fatal("shortcut did not hide")
	}
	s.Toggle()
	if state = experienceState(t, s); !state.visible || state.bounds.Left != after.bounds.Left || state.bounds.Top != after.bounds.Top {
		t.Fatal("same-screen show lost placement", state)
	}
	surfaceSync(t, s, func() { uiDLL.NewProc("SendMessageW").Call(s.hwnd.Load(), 0x0010, 0, 0) })
	if experienceState(t, s).visible {
		t.Fatal("system close did not hide")
	}
	s.Toggle()
	evaluate(t, s, `document.querySelector('#input').dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));true`)
	for deadline := time.Now().Add(time.Second); experienceState(t, s).visible; {
		if time.Now().After(deadline) {
			t.Fatal("Escape did not hide")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Log("Native window styles, drag capability, placement, blur, minimize/restore and explicit hiding passed; physical taskbar/mouse checks remain separate.")
}

func TestNativeChatConstrainedPlacement(t *testing.T) {
	if os.Getenv("ASTERHAND_CHAT_GATE") != "1" {
		t.Skip("explicit desktop gate required")
	}
	if os.Getenv("ASTERHAND_CHAT_GATE_CHILD") != "1" {
		runChatGateAtMedium(t)
		return
	}
	s := experienceSurface(t)
	surfaceSync(t, s, func() {
		monitorID, _, _ := uiDLL.NewProc("MonitorFromWindow").Call(s.hwnd.Load(), 2)
		monitor, ok := monitorDetails(monitorID)
		if !ok {
			t.Error("monitor missing")
			return
		}
		s.height = 240
		point := windowPoint{monitor.work.Left + scaled(40, monitor.dpi), monitor.work.Bottom - scaled(260, monitor.dpi)}
		s.applyBounds(fitWindow(monitor.work, monitor.dpi, s.height, &point))
		s.windowEvent(s.hwnd.Load(), 0x0232, 0, 0)
		preferred := s.placements[monitor.name]
		var compact nativeRect
		uiDLL.NewProc("GetWindowRect").Call(s.hwnd.Load(), uintptr(unsafe.Pointer(&compact)))
		s.height = 760
		s.positionCurrent(nil)
		s.hide(false)
		if s.placements[monitor.name] != preferred {
			t.Error("hiding persisted a constrained resize position")
		}
		s.height = 240
		s.positionCurrent(nil)
		var restored nativeRect
		uiDLL.NewProc("GetWindowRect").Call(s.hwnd.Load(), uintptr(unsafe.Pointer(&restored)))
		if compact != restored {
			t.Errorf("collapse did not restore preferred anchor: %+v -> %+v", compact, restored)
		}
	})
}

func TestNativeChatMathAndStatus(t *testing.T) {
	if os.Getenv("ASTERHAND_CHAT_GATE") != "1" {
		t.Skip("explicit desktop gate required")
	}
	if os.Getenv("ASTERHAND_CHAT_GATE_CHILD") != "1" {
		runChatGateAtMedium(t)
		return
	}
	s := experienceSurface(t)
	evaluate(t, s, `document.querySelector('#chat-expand').click();window.__mathLoaded=false;window.__violations=[];document.addEventListener('securitypolicyviolation',e=>window.__violations.push(e.violatedDirective));true`)
	text := "行内公式 \\(a^2+b^2=c^2\\)，分数与积分：\n\\[\\int_0^1 \\frac{x^2}{1+x}dx\\]\n矩阵：\n$$\\begin{pmatrix}1&2\\\\3&4\\end{pmatrix}$$\n```latex\n\\[源代码保持原样\\]\n```"
	encoded, _ := json.Marshal(text)
	evaluate(t, s, `Promise.all([import('./chat-renderer.mjs'),import('./chat-status.mjs')]).then(([m,v])=>{
      window.__mathRender=m.messageRenderer(document.querySelector('#chat-messages'),{copy(text){window.__copied=text},expand(){}});
      window.__mathRender([{id:'math',role:'assistant',blocks:[{type:'text',text:`+string(encoded)+`}]}]);
      window.__statusView=v.replyStatusView(document.querySelector('#chat-status'));window.__statusView.visible(true);
      window.__statusView.turn({delivery:'accepted',run:'running',persistence:'saved'});
      document.fonts.ready.then(()=>window.__mathLoaded=true);
    });true`)
	waitChatScript(t, s, `window.__mathLoaded && document.querySelectorAll('.chat-math .katex').length===3`)
	if result := evaluate(t, s, `window.__violations.length===0 && document.querySelectorAll('.chat-math math').length===3 && [...document.fonts].some(f=>f.status==='loaded')`); result != "true" {
		t.Fatal("math/fonts/CSP", result, evaluate(t, s, `window.__violations`))
	}
	if result := evaluate(t, s, `document.querySelector('#input').placeholder==='' && !document.querySelector('#status').textContent && document.querySelector('footer').textContent.trim()==='Esc 隐藏'`); result != "true" {
		t.Fatal("unnecessary hints remain", result)
	}
	if result := evaluate(t, s, `document.querySelector('.chat-copy-source').click();window.__copied===`+string(encoded)); result != "true" {
		t.Fatal("math copy lost source", result)
	}
	if result := evaluate(t, s, `(()=>{const dots=document.querySelector('.reply-dots');if(matchMedia('(prefers-reduced-motion: reduce)').matches)return !dots.getAnimations().length;const a=dots.getAnimations()[0];if(!a)return false;a.pause();const clips=[100,500,900].map(time=>{a.currentTime=time;return getComputedStyle(dots).clipPath});a.play();return new Set(clips).size===3})()`); result != "true" {
		t.Fatal("one-to-three dot animation", result)
	}
	if directory := os.Getenv("ASTERHAND_CHAT_EXPERIENCE_CAPTURE"); directory != "" {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		for _, theme := range []string{"star", "frost"} {
			if err := s.Update(theme); err != nil {
				t.Fatal(err)
			}
			waitChatScript(t, s, `document.documentElement.dataset.theme==='`+theme+`'`)
			captureExperience(t, s, filepath.Join(directory, "chat-"+theme+".png"))
		}
	}
	evaluate(t, s, `window.__statusView.turn({delivery:'accepted',run:'completed',persistence:'saved'});true`)
	if result := evaluate(t, s, `document.querySelector('#chat-status').textContent==='' && !document.querySelector('.reply-dots').getAnimations().length`); result != "true" {
		t.Fatal("completion did not clear animation", result)
	}
	t.Log("Real WebView2 formula DOM, local fonts, strict CSP, source copy, themes and CSS animation passed.")
}
