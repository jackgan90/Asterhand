package app

import (
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/store"
	"testing"
)

func TestNodeRestartsWithLegacyFullEventJob(t *testing.T) {
	f := &fakeExecutor{}
	n := newNode(t, f)
	n.close()
	s, err := store.Open(n.cfg.Home + "/asterhand.db")
	if err != nil {
		t.Fatal(err)
	}
	j := model.Job{ID: "legacy-full", Attempt: "attempt", State: "running", Seq: store.EventLimit}
	if err = s.Put("job", j.ID, j); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	n.start(t, f)
	if err = n.app.Store.Get("job", j.ID, &j); err != nil {
		t.Fatal(err)
	}
	if j.State != "reconciliation_required" || f.calls.Load() != 0 {
		t.Fatal("unsafe startup recovery", j)
	}
	if _, err = n.app.Engine.Resolve(j.ID, "现场核对后关闭旧任务"); err != nil {
		t.Fatal(err)
	}
}
