package automation

import (
	"encoding/json"
	"testing"

	"github.com/jackgan90/Asterhand/internal/localcommands"
)

func TestNotificationFieldSchemaAndPresence(t *testing.T) {
	for _, schema := range []map[string]any{Schema[CommandInput](), Schema[CommandPatch](), Schema[CommandDraft](), Schema[localcommands.Command]()} {
		field := schema["properties"].(map[string]any)["notify_on_completion"].(map[string]any)
		if field["type"] != "boolean" {
			t.Fatal(field)
		}
	}
	for _, input := range []string{`{"command_id":"command1234567890","expected_revision":"revision1234567890"}`, `{"command_id":"command1234567890","expected_revision":"revision1234567890","notify_on_completion":false}`} {
		value, err := Decode[CommandPatch](json.RawMessage(input))
		if err != nil {
			t.Fatal(err)
		}
		if value.NotifyOnCompletion.Present() && value.NotifyOnCompletion.Enabled() {
			t.Fatal(value)
		}
	}
	for _, value := range []string{`null`, `0`, `"false"`} {
		if _, err := Decode[CommandPatch](json.RawMessage(`{"command_id":"command1234567890","expected_revision":"revision1234567890","notify_on_completion":` + value + `}`)); err == nil {
			t.Fatal(value)
		}
	}
}
