//go:build windows

package quickinput

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The design gate uses the production trusted origin, editor and CSP in a real
// WebView2. The prototype is injected only by the test-only ExecuteScript path.
func TestChatSurfaceDesignGate(t *testing.T) {
	if os.Getenv("ASTERHAND_CHAT_GATE") != "1" {
		t.Skip("explicit chat design gate required")
	}
	if os.Getenv("ASTERHAND_CHAT_GATE_CHILD") != "1" {
		runChatGateAtMedium(t)
		return
	}
	if err := checkInteractiveDesktop(); err != nil {
		t.Fatal(err)
	}
	draft := &Draft{}
	s, err := startNativeSurface(t.TempDir(), nil, func(err error) { t.Errorf("surface: %v", err) }, draft)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	prototype, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "chat-panel-prototype.js"))
	if err != nil {
		t.Fatal(err)
	}
	if result := evaluate(t, s, string(prototype)); !strings.Contains(result, `"ok":true`) {
		t.Fatal("chat prototype", result)
	} else {
		t.Log(result)
	}
	if result := evaluate(t, s, `document.querySelector('#input').value='中文草稿';document.querySelector('#input').dispatchEvent(new Event('input'));true`); result != "true" {
		t.Fatal(result)
	}
	if err := s.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if text, _ := draft.Read(); text != "中文草稿" {
		t.Fatal("prototype replaced input editor")
	}
	if result := evaluate(t, s, `window.__gateNetwork='pending';fetch('https://example.invalid').then(()=>window.__gateNetwork='allowed').catch(()=>window.__gateNetwork='blocked');true`); result != "true" {
		t.Fatal(result)
	}
	if result := evaluate(t, s, `window.__gateNetwork`); result != `"blocked"` {
		t.Fatal("prototype changed CSP", result)
	}
	if result := evaluate(t, s, `[localStorage.length,sessionStorage.length]`); result != "[0,0]" {
		t.Fatal("prototype persisted body", result)
	}
}

func runChatGateAtMedium(t *testing.T) {
	t.Helper()
	token, err := restrictedHostToken()
	if err != nil {
		t.Fatal(err)
	}
	if token != 0 {
		defer token.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v", "-test.timeout=50s")
	cmd.Env = append(os.Environ(), "ASTERHAND_CHAT_GATE_CHILD=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, Token: syscall.Token(token)}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("medium UI gate: %v\n%s", err, output)
	}
	t.Log(string(output))
}
