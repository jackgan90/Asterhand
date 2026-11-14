package localcommands

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func TestNotificationDefaultOffAndLegacyEdits(t *testing.T) {
	home := t.TempDir()
	r := Open(home)
	saved := saveCommand(t, r, testCommand(t, "build"))
	c := saved.Catalog.Commands[0]
	if !c.NotifyOnCompletion.Enabled() {
		t.Fatal("new command defaults off")
	}
	c.NotifyOnCompletion = NotificationOff
	c = saveCommand(t, r, c).Catalog.Commands[0]
	c.NotifyOnCompletion = NotificationDefault
	c.Description = "legacy edit"
	c = saveCommand(t, r, c).Catalog.Commands[0]
	if c.NotifyOnCompletion.Enabled() {
		t.Fatal("old editor reenabled notification")
	}
	reopened, err := Open(home).Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Commands[0].NotifyOnCompletion.Enabled() {
		t.Fatal("false lost on disk")
	}
	raw, err := os.ReadFile(r.path)
	if err != nil {
		t.Fatal(err)
	}
	old := bytes.ReplaceAll(raw, []byte(`"notify_on_completion": false,`), nil)
	if bytes.Equal(old, raw) {
		t.Fatal("fixture failed to remove switch", string(raw))
	}
	if err = os.WriteFile(r.path, old, 0600); err != nil {
		t.Fatal(err)
	}
	legacy, err := Open(home).Snapshot()
	if err != nil || !legacy.Commands[0].NotifyOnCompletion.Enabled() {
		t.Fatal(legacy, err)
	}
	unchanged, _ := os.ReadFile(r.path)
	if !bytes.Equal(old, unchanged) {
		t.Fatal("read rewrote catalog")
	}
}
func TestNotificationSettingRejectsNullAndWrongTypes(t *testing.T) {
	for _, value := range []string{`null`, `"false"`, `0`, `{}`} {
		var c Command
		if json.Unmarshal([]byte(`{"notify_on_completion":`+value+`}`), &c) == nil {
			t.Fatal(value)
		}
	}
}
