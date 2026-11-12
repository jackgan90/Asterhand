//go:build windows

package quickinput

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/editors"
	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

// Uses the packaged child, its real IPC and WebView2. The window stays hidden.
func TestPackagedNativeLiveEditor(t *testing.T) {
	dir := os.Getenv("ASTERHAND_NATIVE_TEST_PACKAGE")
	if dir == "" {
		t.Skip("set ASTERHAND_NATIVE_TEST_PACKAGE to the application build directory")
	}
	manifest, err := os.ReadFile(filepath.Join(dir, "input-host.json"))
	if err != nil {
		t.Fatal(err)
	}
	var bundle struct {
		Source string `json:"source_sha256"`
	}
	if err = json.Unmarshal(manifest, &bundle); err != nil || bundle.Source == "" {
		t.Fatal("invalid host manifest", err)
	}
	old := config.SourceDigest
	config.SourceDigest = bundle.Source
	t.Cleanup(func() { config.SourceDigest = old })

	draft := &Draft{text: "unfinished native text", revision: 1}
	host, err := startRemoteSurface(t.TempDir(), "star", draft, func() (hostProcess, error) { return launchWindowsHostFrom(dir) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	broker := editors.New()
	defer broker.Close()
	registered := make(chan struct{})
	host.serveEditors(func(ctx context.Context, page string, send func(hostipc.EditorRequest) editors.Reply) {
		commands := broker.Commands(ctx, "native", page)
		close(registered)
		for command := range commands {
			reply := send(hostipc.EditorRequest{Node: "native-test-node", Command: command})
			_ = broker.Respond("native", editors.Poll{Page: page, Reply: &reply})
		}
	})
	<-registered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	call := func(command editors.Command, result any) {
		t.Helper()
		data, err := broker.Call(ctx, host.session, command, nil)
		if err != nil {
			t.Fatal(command.Action, err)
		}
		if err = json.Unmarshal(data, result); err != nil {
			t.Fatal(err)
		}
	}
	var rows []struct {
		ID   string `json:"draft_id"`
		Kind string `json:"kind"`
	}
	call(editors.Command{Action: "list"}, &rows)
	if len(rows) != 1 || rows[0].Kind != "chat" || rows[0].ID == "" {
		t.Fatal("missing open chat editor", rows)
	}
	var state struct {
		Revision string
		Content  struct{ Text string }
	}
	id := rows[0].ID
	call(editors.Command{Action: "get", Editor: id}, &state)
	if state.Content.Text != "unfinished native text" || state.Revision == "" {
		t.Fatal("unexpected initial draft", state)
	}
	var ack struct{ Revision, State string }
	call(editors.Command{Action: "update", Editor: id, Revision: state.Revision, Content: json.RawMessage(`{"text":"continued by agent","mode":"message"}`)}, &ack)
	if ack.State != "editable" || ack.Revision == state.Revision {
		t.Fatal("missing update acknowledgement", ack)
	}
	call(editors.Command{Action: "get", Editor: id}, &state)
	if state.Content.Text != "continued by agent" || ack.Revision != state.Revision {
		t.Fatal("update lost in suspended WebView", state)
	}
	if err = host.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	if text, _ := draft.Read(); text != "continued by agent" {
		t.Fatal("parent draft did not receive the edit")
	}
}
