package executor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/prompt"
)

func TestOptionalConversationPromptInstallation(t *testing.T) {
	root := t.TempDir()
	c := config.Config{}
	c.LLM.Model = "fixture/chat"
	plan, enabled := prepareConversationPrompt(c, root)
	if !enabled || plan.Application.Mode != prompt.Append || plan.Application.State != prompt.Pending {
		t.Fatal("policy preparation failed")
	}
	data, err := conversationConfig(c, openClawOptions{}, providerBridge{}, root, 12345, enabled)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err = json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	defaults := settings["agents"].(map[string]any)["defaults"].(map[string]any)
	if defaults["skipBootstrap"] != false || defaults["workspace"] != filepath.Join(root, "workspace") {
		t.Fatal("native persona initialization missing or workspace isolation changed")
	}
	entries := settings["plugins"].(map[string]any)["entries"].(map[string]any)
	if entries[chatPromptPlugin] == nil {
		t.Fatal("trusted prompt plugin missing")
	}
	tools := settings["tools"].(map[string]any)
	if tools["deny"].([]any)[0] != "*" || tools["toolSearch"] != false || tools["codeMode"].(map[string]any)["enabled"] != false {
		t.Fatal("prompt enabled executable tools")
	}
	blocked := t.TempDir()
	if err = os.WriteFile(filepath.Join(blocked, "prompt-plugin"), []byte("occupied path"), 0600); err != nil {
		t.Fatal(err)
	}
	_, enabled = prepareConversationPrompt(c, blocked)
	if enabled {
		t.Fatal("failed deployment reported success")
	}
	data, err = conversationConfig(c, openClawOptions{}, providerBridge{}, blocked, 12345, enabled)
	if err != nil {
		t.Fatal("optional failure blocked native configuration")
	}
	if err = json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	if settings["plugins"].(map[string]any)["entries"].(map[string]any)[chatPromptPlugin] != nil {
		t.Fatal("failed plugin remained enabled")
	}
}
