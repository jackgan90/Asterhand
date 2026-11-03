//go:build windows

package quickinput

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeLogToolbarRefresh(t *testing.T) {
	if os.Getenv("ASTERHAND_DESKTOP_TEST") != "1" {
		t.Skip("requires desktop validation")
	}
	if os.Getenv("ASTERHAND_CHAT_GATE_CHILD") != "1" {
		runChatGateAtMedium(t)
		return
	}
	s := backgroundExperienceSurface(t)
	fixture, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "log-toolbar-stability.js"))
	if err != nil {
		t.Fatal(err)
	}
	for _, theme := range []string{"star", "frost"} {
		for _, locale := range []string{"zh-CN", "en-US"} {
			if err := s.Update(theme); err != nil {
				t.Fatal(err)
			}
			if err := s.UpdateLocale(locale); err != nil {
				t.Fatal(err)
			}
			waitChatScript(t, s, `document.documentElement.lang==='`+locale+`' && document.documentElement.dataset.theme==='`+theme+`'`)
			evaluate(t, s, string(fixture))
			var result string
			for deadline := time.Now().Add(10 * time.Second); ; {
				result = evaluate(t, s, `window.__logToolbarStability`)
				if result != "null" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("toolbar polling fixture did not complete")
				}
				time.Sleep(30 * time.Millisecond)
			}
			if !strings.Contains(result, `"ok":true`) {
				t.Fatal(result)
			}
			t.Logf("WebView2 %s toolbar DOM mutations, opacity and focus: %s", s.version, result)
		}
	}
}
