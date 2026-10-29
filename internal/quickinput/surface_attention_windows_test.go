//go:build windows

package quickinput

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

func TestCompletionAttentionQueuePressureDoesNotFailHost(t *testing.T) {
	s := &nativeSurface{commands: make(chan func(), 32), done: make(chan struct{})}
	for range cap(s.commands) {
		s.commands <- func() {}
	}
	for _, closed := range []bool{false, true} {
		if closed {
			close(s.done)
		}
		var busy atomic.Bool
		if err := serveHostCommand(context.Background(), s, nil, &busy, hostipc.Message{Kind: hostipc.ReplyCompleted}, nil, nil); err != nil {
			t.Fatalf("optional completion failed host command loop (closed=%v): %v", closed, err)
		}
		if len(s.commands) != cap(s.commands) {
			t.Fatal("completion replaced queued work")
		}
	}
}

// Showing without activation allows background/layout tests to run even when
// Windows has not granted this process foreground-activation permission.
func backgroundExperienceSurface(t *testing.T) *nativeSurface {
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
	surfaceSync(t, s, func() {
		s.positionForShow()
		s.showContent(true)
		uiDLL.NewProc("ShowWindow").Call(s.hwnd.Load(), 4) // SW_SHOWNOACTIVATE
	})
	return s
}

func TestNativeChatSendStability(t *testing.T) {
	if os.Getenv("ASTERHAND_CHAT_GATE") != "1" {
		t.Skip("explicit desktop gate required")
	}
	if os.Getenv("ASTERHAND_CHAT_GATE_CHILD") != "1" {
		runChatGateAtMedium(t)
		return
	}
	s := backgroundExperienceSurface(t)
	fixture, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "chat-send-stability.js"))
	if err != nil {
		t.Fatal(err)
	}
	evaluate(t, s, string(fixture))
	waitChatScript(t, s, `Boolean(window.__sendStability)`)
	result := evaluate(t, s, `window.__sendStability`)
	if !strings.Contains(result, `"ok":true`) {
		t.Fatal(result)
	}
	t.Log(result)
}

func TestNativeChatCompletionAttention(t *testing.T) {
	if os.Getenv("ASTERHAND_CHAT_GATE") != "1" {
		t.Skip("explicit desktop gate required")
	}
	if os.Getenv("ASTERHAND_CHAT_GATE_CHILD") != "1" {
		runChatGateAtMedium(t)
		return
	}
	s := backgroundExperienceSurface(t)
	var busy atomic.Bool
	deliver := func() {
		t.Helper()
		if err := serveHostCommand(context.Background(), s, nil, &busy, hostipc.Message{Kind: hostipc.ReplyCompleted}, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	var before uintptr
	surfaceSync(t, s, func() {
		before, _, _ = uiDLL.NewProc("GetForegroundWindow").Call()
		if before == s.hwnd.Load() {
			t.Error("fixture must be in background")
		}
	})
	deliver()
	surfaceSync(t, s, func() {
		foreground, _, _ := uiDLL.NewProc("GetForegroundWindow").Call()
		if !s.attention || foreground != before {
			t.Error("background completion did not request attention without activation")
		}
		// Activation message is tested here; a physical taskbar click remains a user check.
		s.windowEvent(s.hwnd.Load(), 0x0006, 1, 0)
		if s.attention {
			t.Error("activation retained attention")
		}
		uiDLL.NewProc("ShowWindow").Call(s.hwnd.Load(), 6)
	})
	deliver()
	surfaceSync(t, s, func() {
		iconic, _, _ := uiDLL.NewProc("IsIconic").Call(s.hwnd.Load())
		if !s.attention || iconic == 0 || s.contentVisible {
			t.Error("minimized completion resumed content/window or lost attention")
		}
		s.hide(false)
		if s.attention {
			t.Error("hide retained attention")
		}
	})
	deliver()
	surfaceSync(t, s, func() {
		if s.attention {
			t.Error("hidden window requested taskbar attention")
		}
	})
	t.Log("Real parent-command dispatch, background/minimized attention state, no activation/resume, activation-message and hide cleanup passed; taskbar pixels require visual acceptance.")
}
