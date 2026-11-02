package hostipc

import (
	"strings"
	"testing"

	commandswire "github.com/jackgan90/Asterhand/internal/localcommands/wire"
)

func TestLogWindowAnchorIsRestrictedToLogActions(t *testing.T) {
	id := strings.Repeat("a", 32)
	open := Message{Version: Version, Session: id, Kind: OpenLog, Request: 1, WindowID: id, Anchor: &WindowAnchor{X: -1500, Y: 800}}
	if err := open.Validate(false); err != nil {
		t.Fatal(err)
	}
	call := Message{Version: Version, Session: id, Kind: CommandCall, Epoch: 1, CommandID: id, Command: &commandswire.Request{Action: "logs", ID: id}, Anchor: open.Anchor}
	if err := call.Validate(true); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"status", "stop", "settings", "list"} {
		m := call
		m.Command = &commandswire.Request{Action: action, ID: id}
		if action == "list" {
			m.Command.ID = ""
		}
		if m.Validate(true) == nil {
			t.Fatalf("anchor accepted for %s", action)
		}
	}
	open.Anchor = &WindowAnchor{X: 1 << 25}
	if open.Validate(false) == nil {
		t.Fatal("unbounded anchor")
	}
}

func TestLogProgressIsBoundedAndHostOnly(t *testing.T) {
	id := strings.Repeat("a", 32)
	m := Message{Version: Version, Session: id, Kind: LogProgress, LogEvent: &LogWindowEvent{ID: id, Stage: "initialization_failed", ElapsedMS: 15000, Code: "err.quickinput.init_timeout"}}
	if err := m.Validate(true); err != nil {
		t.Fatal(err)
	}
	if m.Validate(false) == nil {
		t.Fatal("parent diagnostic accepted")
	}
	for _, change := range []func(*LogWindowEvent){
		func(e *LogWindowEvent) { e.Code = "secret\ntext" }, func(e *LogWindowEvent) { e.Code = strings.Repeat("x", 97) },
		func(e *LogWindowEvent) { e.Stage = "arbitrary" }, func(e *LogWindowEvent) { e.ElapsedMS = -1 }, func(e *LogWindowEvent) { e.ID = "../outside" },
	} {
		e := *m.LogEvent
		change(&e)
		n := m
		n.LogEvent = &e
		if n.Validate(true) == nil {
			t.Fatalf("invalid diagnostic: %+v", e)
		}
	}
	m.Kind = State
	m.Request = 1
	if m.Validate(true) == nil {
		t.Fatal("diagnostic piggyback accepted")
	}
}
