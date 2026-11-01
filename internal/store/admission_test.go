package store

import (
	"errors"
	"testing"

	"github.com/jackgan90/Asterhand/internal/model"
)

func TestAdmissionRunsOnlyForNewUncancelledRequests(t *testing.T) {
	s := testStore(t)
	r := request()
	rejected := errors.New("configuration missing")
	checks := 0
	check := func() error { checks++; return rejected }
	if _, err := s.AcceptChecked("owner", "origin", r, check); !errors.Is(err, rejected) {
		t.Fatal("missing prerequisites accepted", err)
	}
	var jobs []model.Job
	if err := s.List("job", &jobs); err != nil || len(jobs) != 0 || checks != 1 {
		t.Fatal("rejection left a job", jobs, err, checks)
	}
	// A rejected first attempt has not consumed the request ID.
	j, err := s.AcceptChecked("owner", "origin", r, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.AcceptChecked("owner", "origin", r, check)
	if err != nil || replay.ID != j.ID || checks != 1 {
		t.Fatal("replay re-ran admission", replay, err, checks)
	}
	r.Goal += "changed"
	if _, err := s.AcceptChecked("owner", "origin", r, check); !errors.Is(err, ErrConflict) || checks != 1 {
		t.Fatal("changed replay bypassed conflict", err, checks)
	}
	r = request()
	r.RequestID = "cancelled-request"
	if err := s.Cancel("origin", r.RequestID); err != nil {
		t.Fatal(err)
	}
	cancelled, err := s.AcceptChecked("owner", "origin", r, check)
	if err != nil || cancelled.State != "cancelled" || cancelled.Attempt != "" || checks != 1 {
		t.Fatal("cancel tombstone did not bypass model prerequisites", cancelled, err, checks)
	}
	replay, err = s.AcceptChecked("owner", "origin", r, check)
	if err != nil || replay.ID != cancelled.ID || replay.State != "cancelled" || checks != 1 {
		t.Fatal("cancel replay changed", replay, err)
	}
}
