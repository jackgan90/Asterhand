//go:build windows

package quickinput

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeChatSendIntent(t *testing.T) {
	if os.Getenv("ASTERHAND_CHAT_GATE") != "1" {
		t.Skip("explicit desktop gate required")
	}
	if os.Getenv("ASTERHAND_CHAT_GATE_CHILD") != "1" {
		runChatGateAtMedium(t)
		return
	}
	s := backgroundExperienceSurface(t)
	fixture, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "chat-send-intent.js"))
	if err != nil {
		t.Fatal(err)
	}
	evaluate(t, s, string(fixture))
	waitChatScript(t, s, `Boolean(window.__sendIntentResult)`)
	result := evaluate(t, s, `window.__sendIntentResult`)
	if !strings.Contains(result, `"ok":true`) {
		t.Fatal(result)
	}
	for _, theme := range []string{"frost", "star"} {
		if err := s.Update(theme); err != nil {
			t.Fatal(err)
		}
		captureComposer(t, s, "send-intent-"+theme)
	}
	t.Log(result)
}
