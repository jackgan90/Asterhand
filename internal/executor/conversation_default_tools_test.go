package executor

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/workspace"
)

func TestDefaultToolPolicyReachesRuntimeConfiguration(t *testing.T) {
	manager, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{true, false} {
		c := manager.Get()
		if !enabled {
			c.LocalTools = config.LocalToolPolicy{Enabled: false, Revision: "user-disabled"}
			if err := manager.Save(c); err != nil {
				t.Fatal(err)
			}
			c = manager.Get()
		}
		root := t.TempDir()
		_, promptEnabled := prepareConversationPrompt(c, root)
		if !promptEnabled {
			t.Fatal("prompt setup failed")
		}
		access := &workspace.Transport{URL: "http://127.0.0.1:1234", Token: "fixture"}
		data, err := conversationConfig(c, openClawOptions{}, providerBridge{}, root, 12345, promptEnabled, access)
		if err != nil {
			t.Fatal(err)
		}
		var settings struct {
			Tools   struct{ Allow, Deny []string }
			Plugins struct {
				Entries map[string]struct {
					Config struct{ ToolsAllow []string }
				}
			}
		}
		if err := json.Unmarshal(data, &settings); err != nil {
			t.Fatal(err)
		}
		if enabled {
			if !reflect.DeepEqual(settings.Tools.Allow, nativeWorkspaceTools()) || !reflect.DeepEqual(settings.Plugins.Entries[chatPromptPlugin].Config.ToolsAllow, nativeWorkspaceTools()) {
				t.Fatal("default tools differ between runtime and prompt policy")
			}
		} else if len(settings.Tools.Allow) != 0 || !reflect.DeepEqual(settings.Tools.Deny, []string{"*"}) || len(settings.Plugins.Entries[chatPromptPlugin].Config.ToolsAllow) != 0 {
			t.Fatal("explicitly disabled tools remain exposed")
		}
	}
}
