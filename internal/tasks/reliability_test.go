package tasks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/executor"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/store"
)

func TestEventBudgetPreservesEvidenceAndStopsNewWrites(t *testing.T) {
	e, j, token, root := toolsFixture(t)
	// Exercise the real log boundary, including monotonic sequence allocation.
	for j.Seq < store.EventLimit-1 {
		var err error
		j, err = e.Store.Change(j.ID, "progress", func(*model.Job) (any, error) { return nil, nil })
		if err != nil {
			t.Fatal(err)
		}
	}
	create := call("asterhand_create_text", "last-write", map[string]string{"path": "last.txt", "content": "written"})
	if _, err := e.Tool(context.Background(), token, create); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Tool(context.Background(), token, create); err != nil {
		t.Fatal("idempotent evidence replay", err)
	}
	if err := e.Store.Get("job", j.ID, &j); err != nil {
		t.Fatal(err)
	}
	if len(j.Artifacts) != 1 || j.Seq != store.EventLimit+1 {
		t.Fatalf("missing or duplicated evidence: %+v", j)
	}
	if b, err := os.ReadFile(filepath.Join(root, "last.txt")); err != nil || string(b) != "written" {
		t.Fatal("file", string(b), err)
	}
	_, err := e.Tool(context.Background(), token, call("asterhand_create_text", "too-late", map[string]string{"path": "extra.txt", "content": "no"}))
	if !errors.Is(err, store.ErrEventLimit) {
		t.Fatal("budget did not reject write", err)
	}
	if _, err := os.Stat(filepath.Join(root, "extra.txt")); !os.IsNotExist(err) {
		t.Fatal("extra side effect", err)
	}
	if e.active[token].ctx.Err() == nil {
		t.Fatal("runtime not stopped after exhausted budget")
	}
	if err := e.finish(j.ID, "failed", store.ErrEventLimit.Error(), ""); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.Get("job", j.ID, &j); err != nil {
		t.Fatal(err)
	}
	if j.State != "failed" || len(j.Artifacts) != 1 || j.Seq > store.EventLimit+store.ControlEventReserve {
		t.Fatal("unsafe finish", j)
	}
}

func TestFullBudgetRejectsDelegationBeforeSideEffect(t *testing.T) {
	e, j, token, _ := toolsFixture(t)
	j.Seq = store.EventLimit
	if err := e.Store.Put("job", j.ID, j); err != nil {
		t.Fatal(err)
	}
	e.active[token].mode = "route"
	called := false
	e.SubmitRemote = func(model.Job, Submission) (model.Outbox, error) { called = true; return model.Outbox{}, nil }
	_, err := e.Tool(context.Background(), token, call("asterhand_submit", "submit", Submission{Goal: "work"}))
	if !errors.Is(err, store.ErrEventLimit) || called {
		t.Fatal("delegation escaped budget", err, called)
	}
}

func TestFullBudgetCancellationIsIdempotent(t *testing.T) {
	e, j, _, _ := toolsFixture(t)
	j.Seq = store.EventLimit
	if err := e.Store.Put("job", j.ID, j); err != nil {
		t.Fatal(err)
	}
	for range 40 {
		var err error
		j, err = e.Cancel(j.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if j.State != "cancel_requested" || j.Seq != store.EventLimit+1 {
		t.Fatal("repeat cancellation consumed reserve", j)
	}
	// Also exercises cancellation committing after execute selected a success state.
	if err := e.finish(j.ID, "completed_unverified", "", "done"); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.Get("job", j.ID, &j); err != nil {
		t.Fatal(err)
	}
	if j.State != "cancelled" || j.Seq != store.EventLimit+2 {
		t.Fatal("finish lost cancellation", j)
	}
}

func TestLegacyFullBudgetRecoveryRepairsEvidenceAndAllowsResolution(t *testing.T) {
	e, j, _, root := toolsFixture(t)
	j.Seq = store.EventLimit
	if err := e.Store.Put("job", j.ID, j); err != nil {
		t.Fatal(err)
	}
	// Legacy completed-operation/job-artifact split commit left this durable record.
	op := model.Operation{ID: "legacy-op", JobID: j.ID, Attempt: j.Attempt, Digest: "digest", State: "completed",
		Artifact: &model.Artifact{ID: "artifact", Operation: "legacy-op", Path: filepath.Join(root, "last.txt"), Size: 7, SHA256: model.Digest([]byte("written"))}}
	if err := e.Store.Put("operation", op.ID, op); err != nil {
		t.Fatal(err)
	}
	if err := e.Recover(); err != nil {
		t.Fatal(err)
	}
	if err := e.Recover(); err != nil {
		t.Fatal("repeat recovery", err)
	}
	if err := e.Store.Get("job", j.ID, &j); err != nil {
		t.Fatal(err)
	}
	if j.State != "reconciliation_required" || len(j.Artifacts) != 1 || j.Seq != store.EventLimit+2 {
		t.Fatal("recovery lost evidence", j)
	}
	if _, err := e.Resolve(j.ID, "checked the persisted file evidence"); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.Get("job", j.ID, &j); err != nil {
		t.Fatal(err)
	}
	if j.State != "failed" || j.Execution != "unknown_manually_closed" {
		t.Fatal("unsafe resolution", j)
	}
}

func TestQuestionCannotOverwriteCancellation(t *testing.T) {
	for _, phase := range []string{"request", "deliver", "request-before-context-cancel", "deliver-before-context-cancel"} {
		t.Run(phase, func(t *testing.T) {
			e, j, token, _ := toolsFixture(t)
			if phase == "deliver" || phase == "deliver-before-context-cancel" {
				j.State = "waiting_input"
				j.Interaction = &model.Interaction{ID: "question", Attempt: j.Attempt, Kind: "question", State: "saved", Answer: "answer"}
				if err := e.Store.Put("job", j.ID, j); err != nil {
					t.Fatal(err)
				}
			}
			// Freeze Tool's earlier snapshot, then let cancellation commit first.
			var cancelled model.Job
			var err error
			if phase == "request-before-context-cancel" || phase == "deliver-before-context-cancel" {
				// Cancel's durable transaction has committed, but its active-table
				// lookup has not yet cancelled the context.
				cancelled, err = e.Store.ChangeControl(j.ID, "cancel_requested", func(j *model.Job) (any, error) {
					j.State = "cancel_requested"
					return nil, nil
				})
			} else {
				cancelled, err = e.Cancel(j.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if phase == "request" || phase == "request-before-context-cancel" {
				_, err = e.ask(e.active[token], j, call("asterhand_ask_user", "question", map[string]string{"question": "Continue?"}))
			} else {
				_, err = e.deliverAnswer(e.active[token], "question")
			}
			if err == nil {
				t.Fatal("accepted stale question update")
			}
			if err = e.Store.Get("job", j.ID, &j); err != nil {
				t.Fatal(err)
			}
			if j.State != "cancel_requested" || j.Seq != cancelled.Seq {
				t.Fatal("cancellation overwritten", j)
			}
		})
	}
}

func TestAnswerAtBudgetBoundaryAndReplayPreservesReserve(t *testing.T) {
	e, j, token, _ := toolsFixture(t)
	j.Seq = store.EventLimit
	j.State = "waiting_input"
	j.Interaction = &model.Interaction{ID: "question", Attempt: j.Attempt, Kind: "question", State: "pending", Expires: time.Now().Add(time.Minute).Format(time.RFC3339Nano)}
	if err := e.Store.Put("job", j.ID, j); err != nil {
		t.Fatal(err)
	}
	for range 40 {
		if _, err := e.Answer(j.ID, "question", j.Attempt, "answer-id", "answer", "local"); err != nil {
			t.Fatal(err)
		}
	}
	for range 40 {
		if _, err := e.deliverAnswer(e.active[token], "question"); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Store.Get("job", j.ID, &j); err != nil {
		t.Fatal(err)
	}
	if j.Seq != store.EventLimit+2 || j.State != "running" {
		t.Fatal("replay consumed reserve", j)
	}
	if _, err := e.deliverAnswer(e.active[token], "different-question"); err == nil {
		t.Fatal("accepted stale question")
	}
}

type failingPersistenceExecutor struct {
	store *store.Store
	calls int
}

func (f *failingPersistenceExecutor) Execute(context.Context, executor.Run) (executor.Result, error) {
	f.calls++
	f.store.Close()
	return executor.Result{OK: true, Status: "ok"}, nil
}

func TestPersistenceFailureStopsScheduling(t *testing.T) {
	e, j, _, _ := toolsFixture(t)
	j.State = "queued"
	if err := e.Store.Put("job", j.ID, j); err != nil {
		t.Fatal(err)
	}
	x := &failingPersistenceExecutor{store: e.Store}
	e.Executor = x
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := e.Run(ctx); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("persistence failure not propagated", err)
	}
	if x.calls != 1 {
		t.Fatal("scheduler continued", x.calls)
	}
	if err := e.finish(j.ID, "failed", "failure", ""); err == nil {
		t.Fatal("finish swallowed persistence error")
	}
}
