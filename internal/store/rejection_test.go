package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jackgan90/Asterhand/internal/model"
)

func refusal(r model.Request) *model.TaskRejection {
	return &model.TaskRejection{Code: model.TaskConfigurationUnavailable, Message: "configuration missing", RequestID: r.RequestID, RequestDigest: model.Hash(r)}
}

func TestDefinitiveRejectionSurvivesRestoreRestartAndLateSubmission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	r := request()
	want := refusal(r)
	_, err = s.AcceptChecked("owner", "origin", r, func() error { return want })
	if !errors.Is(err, want) {
		t.Fatal("initial refusal not returned", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Configuration is now valid, but the old request identity stays refused.
	checks := 0
	_, err = s.AcceptChecked("owner", "origin", r, func() error { checks++; return nil })
	var actual *model.TaskRejection
	if !errors.As(err, &actual) || !reflect.DeepEqual(actual, want) || checks != 0 {
		t.Fatal("late copy could be admitted", err, checks)
	}
	if err := s.Cancel("origin", r.RequestID); err != nil {
		t.Fatal(err)
	}
	_, err = s.Accept("owner", "origin", r)
	if !errors.As(err, &actual) {
		t.Fatal("later cancellation replaced the final refusal", err)
	}
	r.Goal += " changed"
	if _, err := s.Accept("owner", "origin", r); !errors.Is(err, ErrConflict) {
		t.Fatal("refused identity accepted another digest", err)
	}
	var jobs []model.Job
	if err := s.List("job", &jobs); err != nil || len(jobs) != 0 {
		t.Fatal("refusal created a job", jobs, err)
	}
	if _, err := s.Accept("owner", "other-origin", r); err != nil {
		t.Fatal("refusal leaked across origin", err)
	}
	r.RequestID = "new-request-id"
	if _, err := s.Accept("owner", "origin", r); err != nil {
		t.Fatal("new user submission blocked", err)
	}
}

func TestFailedRejectionPersistenceDoesNotProveNonAcceptance(t *testing.T) {
	for _, boundary := range []string{"write", "commit"} {
		t.Run(boundary, func(t *testing.T) {
			s := testStore(t)
			setup := `CREATE TRIGGER reject_write BEFORE INSERT ON records WHEN NEW.kind='task-rejection' BEGIN SELECT RAISE(FAIL, 'synthetic write failure'); END;`
			if boundary == "commit" {
				setup = `PRAGMA foreign_keys=ON;
				CREATE TABLE parent(id INTEGER PRIMARY KEY);
				CREATE TABLE commit_guard(id INTEGER REFERENCES parent(id) DEFERRABLE INITIALLY DEFERRED);
				CREATE TRIGGER reject_write AFTER INSERT ON records WHEN NEW.kind='task-rejection' BEGIN INSERT INTO commit_guard VALUES(1); END;`
			}
			if _, err := s.db.Exec(setup); err != nil {
				t.Fatal(err)
			}
			r := request()
			_, err := s.AcceptChecked("owner", "origin", r, func() error { return refusal(r) })
			var rejected *model.TaskRejection
			if err == nil || errors.As(err, &rejected) {
				t.Fatal("failed storage claimed definitive rejection", err)
			}
			var receipts []model.TaskRejection
			if err := s.List("task-rejection", &receipts); err != nil || len(receipts) != 0 {
				t.Fatal("failed transaction left a refusal", receipts, err)
			}
		})
	}
}

func TestSchemaOneUpgradePreservesJobsAndProtectsRefusalRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.Accept("owner", "origin", request())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("PRAGMA user_version=1"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var schema int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&schema); err != nil || schema != 2 {
		t.Fatal("old binaries would ignore refusal records", schema, err)
	}
	var restored model.Job
	if err := s.Get("job", j.ID, &restored); err != nil || restored.Digest != j.Digest || restored.Seq != j.Seq {
		t.Fatal("upgrade changed original job", restored, err)
	}
	if events, err := s.Events(j.ID, 0); err != nil || len(events) != 1 {
		t.Fatal("upgrade changed original event", events, err)
	}
}

func TestNewerDatabaseRemainsUnmodified(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("PRAGMA user_version=3"); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(path); err == nil {
		s.Close()
		t.Fatal("newer database opened")
	}
	var schema int
	if err := db.QueryRow("PRAGMA user_version").Scan(&schema); err != nil || schema != 3 {
		t.Fatal("newer schema was downgraded", schema, err)
	}
}
