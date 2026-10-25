package store

import (
	"github.com/jackgan90/Asterhand/internal/model"
	"testing"
)

func TestOperationAndArtifactCommitRollBackTogether(t *testing.T) {
	s := testStore(t)
	j, err := s.Accept("owner", "origin", request())
	if err != nil {
		t.Fatal(err)
	}
	j.Attempt = "attempt"
	if err = s.Put("job", j.ID, j); err != nil {
		t.Fatal(err)
	}
	op := model.Operation{ID: "op", JobID: j.ID, Attempt: j.Attempt, Digest: "digest", State: "prepared"}
	if err = s.Put("operation", op.ID, op); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`CREATE TRIGGER fail_evidence BEFORE INSERT ON events WHEN json_extract(NEW.body,'$.type')='artifact_verified' BEGIN SELECT RAISE(ABORT,'injected event failure'); END`); err != nil {
		t.Fatal(err)
	}
	op.State = "completed"
	op.Artifact = &model.Artifact{ID: "artifact", Operation: op.ID, Path: "file.txt", SHA256: "hash"}
	if err = s.CompleteOperation(op); err == nil {
		t.Fatal("injected failure absent")
	}
	var saved model.Operation
	if err = s.Get("operation", op.ID, &saved); err != nil {
		t.Fatal(err)
	}
	if err = s.Get("job", j.ID, &j); err != nil {
		t.Fatal(err)
	}
	if saved.State != "prepared" || len(j.Artifacts) != 0 || j.Seq != 1 {
		t.Fatal("partial commit", saved, j)
	}
	if _, err = s.db.Exec("DROP TRIGGER fail_evidence"); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteOperation(op); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteOperation(op); err != nil {
		t.Fatal(err)
	}
	if err = s.Get("job", j.ID, &j); err != nil {
		t.Fatal(err)
	}
	if len(j.Artifacts) != 1 || j.Seq != 2 {
		t.Fatal("duplicate evidence", j)
	}
}

func TestControlEventReserveRemainsBounded(t *testing.T) {
	s := testStore(t)
	j, err := s.Accept("owner", "origin", request())
	if err != nil {
		t.Fatal(err)
	}
	j.Seq = EventLimit + ControlEventReserve
	if err = s.Put("job", j.ID, j); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ChangeControl(j.ID, "finished", func(j *model.Job) (any, error) { j.State = "failed"; return nil, nil }); err != ErrEventLimit {
		t.Fatal("unbounded control events", err)
	}
	if err = s.Get("job", j.ID, &j); err != nil {
		t.Fatal(err)
	}
	if j.State != "queued" {
		t.Fatal("failed change leaked mutation", j)
	}
}
