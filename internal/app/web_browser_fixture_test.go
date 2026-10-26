package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestWebBrowserFixture is an opt-in real HTTP/SQLite fixture for browser smoke
// checks. It uses no model, credentials, or existing node data. Its generated
// login link is private to this disposable node, so it is never logged.
func TestWebBrowserFixture(t *testing.T) {
	directory := os.Getenv("ASTERHAND_WEB_FIXTURE_DIR")
	if directory == "" {
		t.Skip("set ASTERHAND_WEB_FIXTURE_DIR for an explicitly selected browser smoke session")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	executor := &fakeExecutor{filename: "browser-answer.txt", ask: true}
	a := newNode(t, executor)
	b := newNode(t, &fakeExecutor{filename: "browser-remote.txt"})
	for index, node := range []*testNode{a, b} {
		cfg := node.cfg.Get()
		cfg.Name = fmt.Sprintf("Browser fixture %c", 'A'+index)
		cfg.WebListen, cfg.PeerListen = node.app.Address(), node.app.PeerAddress()
		if err := node.cfg.Save(cfg); err != nil {
			t.Fatal(err)
		}
	}
	invite, err := b.app.Peers.Invite()
	if err != nil {
		t.Fatal(err)
	}
	metadata := map[string]string{
		"url":    "http://" + a.app.Address() + "/#login=" + a.app.loginToken,
		"invite": invite, "workspace_a": a.root, "workspace_b": b.root,
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "session.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	commandFile := filepath.Join(directory, "command")
	for range ticker.C {
		command, err := os.ReadFile(commandFile)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(commandFile); err != nil {
			t.Fatal(err)
		}
		switch string(command) {
		case "stop":
			return
		case "offline":
			a.close()
		case "online":
			if a.closed {
				a.start(t, executor)
			}
		default:
			t.Fatalf("unknown fixture command %q", command)
		}
	}
}
