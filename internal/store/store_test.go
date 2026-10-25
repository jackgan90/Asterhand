package store

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/model"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func request() model.Request {
	return model.Request{RequestID: "request-123", ProjectID: "p", Goal: "create hello", Mode: "execute", StartBefore: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)}
}
func TestConcurrentAcceptanceIsIdempotent(t *testing.T) {
	s := testStore(t)
	r := request()
	var wg sync.WaitGroup
	ids := make(chan string, 20)
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); j, err := s.Accept("owner", "origin", r); ids <- j.ID; errs <- err }()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	id := ""
	for next := range ids {
		if id != "" && id != next {
			t.Fatal("duplicate jobs")
		}
		id = next
	}
	events, err := s.Events(id, 0)
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%v err=%v", events, err)
	}
	r.Goal = "different"
	if _, err = s.Accept("owner", "origin", r); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	other, err := s.Accept("owner", "other-origin", r)
	if err != nil || other.ID == id {
		t.Fatal("origin scope not isolated")
	}
}
func TestMutationAndEventRollBackTogether(t *testing.T) {
	s := testStore(t)
	j, err := s.Accept("owner", "origin", request())
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Change(j.ID, "bad", func(j *model.Job) (any, error) { j.State = "succeeded"; return nil, errors.New("injected failure") })
	if err == nil {
		t.Fatal("missing failure")
	}
	var actual model.Job
	if err = s.Get("job", j.ID, &actual); err != nil {
		t.Fatal(err)
	}
	if actual.State != "queued" || actual.Seq != 1 {
		t.Fatalf("partial mutation: %+v", actual)
	}
	events, _ := s.Events(j.ID, 0)
	if len(events) != 1 {
		t.Fatal("event escaped transaction")
	}
}
func TestCancelBeforeSubmitSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Cancel("origin", "request-123"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	j, err := s.Accept("owner", "origin", request())
	if err != nil || j.State != "cancelled" || j.Attempt != "" {
		t.Fatalf("late request started: %+v %v", j, err)
	}
}
func TestClosedDatabaseNeverAcknowledgesAcceptance(t *testing.T) {
	s := testStore(t)
	s.Close()
	if _, err := s.Accept("owner", "origin", request()); err == nil {
		t.Fatal("acknowledged failed persistence")
	}
}
func TestEventCursorPagesWithoutGaps(t *testing.T) {
	s := testStore(t)
	j, _ := s.Accept("owner", "origin", request())
	for i := 0; i < 240; i++ {
		if _, err := s.Change(j.ID, "progress", func(j *model.Job) (any, error) { return "tick", nil }); err != nil {
			t.Fatal(err)
		}
	}
	var after int64
	total := 0
	for {
		events, err := s.Events(j.ID, after)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) == 0 {
			break
		}
		for _, e := range events {
			if e.Seq != after+1 {
				t.Fatal("cursor gap")
			}
			after = e.Seq
			total++
		}
	}
	if total != 241 {
		t.Fatalf("events=%d", total)
	}
}
