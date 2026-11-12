package tasks

import (
	"errors"
	"github.com/jackgan90/Asterhand/internal/store"
	"testing"
)

func TestDelegatedResolutionUsesHumanDisposition(t *testing.T) {
	e, j, _, _ := toolsFixture(t)
	if err := e.Recover(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ResolveAs(j.ID, "stale-attempt", "checked old task", "local"); !errors.Is(err, store.ErrConflict) {
		t.Fatal(err)
	}
	done, err := e.ResolveAs(j.ID, j.Attempt, "checked old task", "automation:local")
	if err != nil || done.State != "failed" || done.Execution != "unknown_manually_closed" {
		t.Fatal(done, err)
	}
}
