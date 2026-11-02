//go:build windows

package quickinput

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/model"
)

func TestLogUnconfirmedBrowserCleanupRetiresHost(t *testing.T) {
	r := testLogRuntime(t)
	entry := testLogEntry(model.ID())
	failure := errors.New("fixture: browser exit unconfirmed")
	entry.surface.closeError = failure
	close(entry.surface.done)
	go r.watch(entry)
	select {
	case delivery := <-r.peer.out:
		if delivery.message.Kind != "failure" || delivery.message.Code != "cleanup_unconfirmed" {
			t.Fatal(delivery.message)
		}
		delivery.written <- nil
	case <-time.After(time.Second):
		t.Fatal("parent was not notified of failed browser cleanup")
	}
	select {
	case err := <-r.peer.failures:
		if !errors.Is(err, failure) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("host was not retired")
	}
}

func testLogRuntime(t *testing.T) *logRuntime {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &logRuntime{ctx: ctx, peer: &hostPeer{ctx: ctx, out: make(chan hostDelivery, 32), failures: make(chan error, 1)},
		windows: make(map[string]*logWindowEntry), retiring: make(map[*logWindowEntry]bool), pending: make(map[uint64]logPendingOpen), events: make(chan logRuntimeEvent, 128), initTimeout: 10 * time.Millisecond}
}
func testLogEntry(id string) *logWindowEntry {
	return &logWindowEntry{surface: &nativeSurface{log: &logSurface{id: id, shown: make(chan error, 1)}, ready: make(chan error, 1), done: make(chan struct{}), commands: make(chan func(), 32)}}
}
func TestLogInitializationTimeoutDoesNotWaitForDisposal(t *testing.T) {
	r := testLogRuntime(t)
	entry := testLogEntry(model.ID())
	r.windows[entry.surface.log.id] = entry
	r.pending[1] = logPendingOpen{entry: entry}
	go r.watch(entry)
	select {
	case event := <-r.events:
		r.event(event)
	case <-time.After(time.Second):
		t.Fatal("deadline ignored")
	}
	if !entry.failed || !entry.surface.log.failed.Load() {
		t.Fatal("failed state not recorded")
	}
	select {
	case reply := <-r.peer.out:
		if reply.message.Code != "window_failed" {
			t.Fatal(reply)
		}
	case <-time.After(time.Second):
		t.Fatal("failure waited for browser cleanup")
	}
	if len(entry.surface.commands) != 1 {
		t.Fatal("error shell not requested")
	}
}
func TestLogRetiredEventsCannotDeleteReplacement(t *testing.T) {
	r := testLogRuntime(t)
	id := model.ID()
	old, next := testLogEntry(id), testLogEntry(id)
	r.windows[id] = next
	r.retiring[old] = true
	r.pending[7] = logPendingOpen{entry: next}
	for _, kind := range []string{"shown", "failed", "hidden", "activated", "closed"} {
		r.event(logRuntimeEvent{entry: old, kind: kind, request: 7})
	}
	if r.windows[id] != next || len(r.pending) != 1 || next.failed || len(r.retiring) != 0 {
		t.Fatal("old generation contaminated replacement")
	}
}

func TestLogFirstShowAcknowledgesBeforeBrowserDispatch(t *testing.T) {
	r := testLogRuntime(t)
	entry := testLogEntry(model.ID())
	entry.firstRequest = 7
	entry.surface.log.visible.Store(true)
	r.windows[entry.surface.log.id] = entry
	r.pending[7] = logPendingOpen{entry: entry}
	r.event(logRuntimeEvent{entry: entry, kind: "shown"})
	if len(r.pending) != 0 || len(entry.surface.commands) != 0 {
		t.Fatal("native show ACK waits for browser's UI loop")
	}
	select {
	case reply := <-r.peer.out:
		if reply.message.Code != "" {
			t.Fatal(reply)
		}
	default:
		t.Fatal("missing shown acknowledgement")
	}
}
func TestLogHiddenCacheEvictsLeastRecentlyUsed(t *testing.T) {
	r := testLogRuntime(t)
	var oldest *logWindowEntry
	for i := 0; i < 6; i++ {
		entry := testLogEntry(model.ID())
		entry.used = uint64(i)
		close(entry.surface.done)
		r.windows[entry.surface.log.id] = entry
		if i == 0 {
			oldest = entry
		}
		if i == 5 {
			entry.surface.log.visible.Store(true)
		}
	}
	r.trimHidden()
	if len(r.windows) != 5 || !r.retiring[oldest] {
		t.Fatal("incorrect hidden cache eviction")
	}
}
