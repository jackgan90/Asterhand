package automation

import (
	"encoding/json"
	"testing"

	"github.com/jackgan90/Asterhand/internal/drafts"
)

func TestSettingsDraftRequiresCompleteTargetSpecificSnapshot(t *testing.T) {
	node := `{"name":"draft","model":"model"}`
	desktop := `{"enabled":true,"binding":{"modifiers":["ctrl","alt"],"key":"KeyJ"},"theme_id":"star","locale":"auto"}`
	for _, settings := range []string{"node", "desktop"} {
		target := drafts.Target{Kind: "settings", Settings: settings}
		valid, other := node, desktop
		if settings == "desktop" {
			valid, other = desktop, node
		}
		if err := ValidateDraftContent("settings", target, json.RawMessage(valid)); err != nil {
			t.Fatal(err)
		}
		for _, invalid := range []string{`{}`, `{"name":"only"}`, `{"theme_id":"star"}`, `{"name":null,"model":"model"}`, other,
			`{"name":"n","model":"m","enabled":true,"binding":{"modifiers":[],"key":""},"theme_id":"star","locale":"auto"}`,
			`{"enabled":true,"binding":null,"theme_id":"star","locale":"auto"}`,
			`{"enabled":true,"binding":{"key":"KeyJ"},"theme_id":"star","locale":"auto"}`,
			`{"enabled":true,"binding":{"modifiers":null,"key":"KeyJ"},"theme_id":"star","locale":"auto"}`} {
			if err := ValidateDraftContent("settings", target, json.RawMessage(invalid)); err == nil {
				t.Fatalf("%s accepted %s", settings, invalid)
			}
		}
	}
}
