package quickinput

import (
	"io"
	"strings"
	"testing"

	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

func TestRetiredHostCannotOverwriteReplacementDraft(t *testing.T) {
	for _, retirement := range []string{"close", "failure"} {
		t.Run(retirement, func(t *testing.T) {
			commands, input := io.Pipe()
			output, events := io.Pipe()
			defer commands.Close()
			defer events.Close()
			p := &pipeHost{commands: input, events: output, exited: make(chan struct{})}
			defer p.Stop()
			draft := &Draft{}
			s := &remoteSurface{process: p, draft: draft, epoch: 1, initialized: true,
				out: make(chan hostipc.Message, 32), done: make(chan struct{}), session: "old-session-1234"}
			if retirement == "close" {
				p.Stop()
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				s.finish(hostFailure("host_disconnected"))
			}
			if _, err := draft.accept("replacement draft", 1); err != nil {
				t.Fatal(err)
			}
			// Model a frame read by the old loop before retirement, delivered after
			// its replacement has accepted text in the same draft epoch.
			err := s.receive(hostipc.Message{Kind: hostipc.Edit, Epoch: 1, Sequence: 2, Text: "late old draft"})
			text, revision := draft.Read()
			if err == nil || text != "replacement draft" || revision != 1 {
				t.Fatalf("retired frame changed draft: text=%q revision=%d error=%v", text, revision, err)
			}
		})
	}
}

func TestRelayCoalescesAndWaitsForParentSnapshotAcceptance(t *testing.T) {
	var sent []hostipc.Message
	r := newEditorRelay(1, 0, func(m hostipc.Message) error { sent = append(sent, m); return nil }, func(err error) { t.Fatal(err) })
	r.submit("a", 1, "", nil)
	r.submit("ab", 2, "", nil)
	done := false
	r.submit("abc", 3, "snapshot", func() { done = true })
	if len(sent) != 1 || done {
		t.Fatal("unbounded delivery or premature snapshot")
	}
	if err := r.accept(hostipc.Message{Sequence: 1, Epoch: 1, Revision: 1}); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 || sent[1].Text != "abc" || sent[1].Sequence != 3 || done {
		t.Fatal("latest full draft was not coalesced")
	}
	if err := r.accept(hostipc.Message{Sequence: 3, Epoch: 1, Revision: 2}); err != nil || !done {
		t.Fatal("snapshot did not wait for latest parent confirmation", err)
	}
	if r.accept(hostipc.Message{Sequence: 1, Epoch: 1, Revision: 1}) == nil {
		t.Fatal("stale ACK accepted")
	}
}

func TestParentDraftRejectsStaleEpochAndConflictingSequence(t *testing.T) {
	d := &Draft{}
	s := &remoteSurface{draft: d, epoch: 1, lastText: "", out: make(chan hostipc.Message, 32), done: make(chan struct{}), session: "test-session-1234"}
	m := hostipc.Message{Kind: hostipc.Edit, Epoch: 1, Sequence: 5, Text: "中文"}
	if err := s.acceptEdit(m); err != nil {
		t.Fatal(err)
	}
	if err := s.acceptEdit(m); err != nil {
		t.Fatal("idempotent resend", err)
	}
	text, rev := d.Read()
	if text != "中文" || rev != 1 {
		t.Fatal("canonical revision should advance once")
	}
	m.Text = "conflict"
	if s.acceptEdit(m) == nil {
		t.Fatal("same sequence with other text")
	}
	d.Clear()
	m.Sequence = 6
	if s.acceptEdit(m) == nil {
		t.Fatal("pre-clear epoch resurrected draft")
	}
	text, _ = d.Read()
	if text != "" {
		t.Fatal("clear lost")
	}
}

func TestMenuOnlyShowsActuallyUsableShortcut(t *testing.T) {
	s := Status{Desired: Defaults(), Supported: true, Registration: "unavailable", Surface: "failed"}
	if strings.Contains(s.MenuLabel(), "Ctrl") {
		t.Fatal("unregistered shortcut advertised")
	}
	s.Effective = &s.Desired.Binding
	s.Registration = "registered"
	s.Surface = "ready"
	if !strings.Contains(s.MenuLabel(), "Ctrl+Alt+J") {
		t.Fatal("effective shortcut omitted")
	}
	s.Registration = "recovering"
	if strings.Contains(s.MenuLabel(), "Ctrl") || !strings.Contains(s.MenuLabel(), "恢复") {
		t.Fatal("recovering advertised ready")
	}
}
