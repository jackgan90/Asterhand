package automation_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/operations"
	"github.com/jackgan90/Asterhand/internal/store"
)

func TestDispatchReplayScopesAndCancellation(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := automation.NewRegistry("node", "instance", s)
	granted := true
	r.Authorize = func(token string) (automation.Principal, error) {
		if !granted || token != "token" {
			return automation.Principal{}, automation.Fail("forbidden", "revoked")
		}
		return automation.Principal{ID: "actor", Roles: []string{"execute"}, Scopes: map[string][]string{"commands": {"one"}}}, nil
	}
	type arguments struct {
		ID       string `json:"command_id"`
		Revision string `json:"revision"`
	}
	calls := 0
	automation.Register(r, automation.Definition{Name: "commands_run", Role: "execute", Write: true}, func(a arguments) string { return a.ID },
		func(p automation.Principal, a arguments) error { return p.Require("commands", a.ID) },
		func(ctx context.Context, c automation.Call, a arguments) (automation.Outcome, error) {
			calls++
			return automation.Outcome{Accepted: true, Summary: map[string]string{"run_id": "first"}}, nil
		})
	req := automation.Request{Version: "1", Node: "node", Operation: "commands_run", ID: operations.NewID(time.Now()), Arguments: json.RawMessage(`{"command_id":"one","revision":"before"}`)}
	first := r.Dispatch(context.Background(), "token", req)
	if first.Error != nil || !first.Receipt.Accepted {
		t.Fatal(first)
	}
	r.Now = func() time.Time { return time.Now().Add(24 * time.Hour) }
	second := r.Dispatch(context.Background(), "token", req)
	if second.Error != nil || calls != 1 || string(first.Receipt.Result) != string(second.Receipt.Result) {
		t.Fatal(calls, second)
	}
	granted = false
	if result := r.Dispatch(context.Background(), "token", req); result.Error.Code != "forbidden" || result.Receipt != nil {
		t.Fatal(result)
	}
	granted = true
	req.Arguments = json.RawMessage(`{"command_id":"other","revision":"before"}`)
	if result := r.Dispatch(context.Background(), "token", req); result.Error.Code != "forbidden" {
		t.Fatal(result)
	}
	r.Now = time.Now
	req.ID, req.Arguments = operations.NewID(time.Now()), json.RawMessage(`{"command_id":"one","revision":"before"}`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result := r.Dispatch(ctx, "token", req); result.Receipt.State != "rejected" || calls != 1 {
		t.Fatal(result, calls)
	}
}

func TestStrictSchemaRejectsUnspecifiedInput(t *testing.T) {
	type args struct {
		Name     string `json:"name"`
		Optional int    `json:"optional,omitempty"`
	}
	for _, input := range []string{`{}`, `{"name":null}`, `{"name":"one","secret":"x"}`, `{"name":"one","name":"two"}`, `{"name":"one"} {}`, `{"name":"\ud800"}`, `{"name":"\udc00"}`, "{\"name\":\"\xff\"}"} {
		if _, err := automation.Decode[args](json.RawMessage(input)); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	if value, err := automation.Decode[args](json.RawMessage(`{"name":"one"}`)); err != nil || value.Name != "one" {
		t.Fatal(value, err)
	}
}

// Publishes the observer's terminal receipt immediately before the writer's Finish.
type observingJournal struct {
	operations.Journal
	publish func(operations.Receipt)
}

func (s *observingJournal) Finish(receipt operations.Receipt, now time.Time) error {
	if s.publish != nil {
		s.publish(receipt)
		s.publish = nil
	}
	return s.Journal.Finish(receipt, now)
}

func TestOriginalResponseAndReplayUseTheObservedTerminalReceipt(t *testing.T) {
	for _, terminal := range []string{"completed", "rejected"} {
		for _, phase := range []string{"before_lookup", "during_finish"} {
			t.Run(terminal+"/"+phase, func(t *testing.T) {
				db, err := store.Open(filepath.Join(t.TempDir(), "operations.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				journal := &observingJournal{Journal: db}
				registry := automation.NewRegistry("node", "instance", journal)
				publish := func(receipt operations.Receipt) {
					receipt.State, receipt.Accepted = terminal, terminal == "completed"
					receipt.Code = ""
					if terminal == "rejected" {
						receipt.Code = "rejected"
					}
					if err := db.Finish(receipt, time.Now()); err != nil {
						t.Fatal(err)
					}
				}
				attempts := 0
				automation.Register(registry, automation.Definition{Name: "test.write", Role: "edit", Write: true}, nil, nil, func(_ context.Context, c automation.Call, _ automation.Empty) (automation.Outcome, error) {
					attempts++
					if phase == "before_lookup" {
						receipt, err := db.Lookup(c.Identity.Actor, c.Identity.Node, c.Identity.ID)
						if err != nil {
							t.Fatal(err)
						}
						publish(receipt)
					} else {
						journal.publish = publish
					}
					return automation.Outcome{Unknown: true}, automation.Fail("result_unknown", "Window disconnected")
				})
				req := automation.Request{Version: "1", Node: "node", Operation: "test.write", ID: operations.NewID(time.Now()), Arguments: json.RawMessage(`{}`)}
				for range 2 {
					reply := registry.DispatchHuman(context.Background(), req)
					if reply.Receipt == nil || reply.Receipt.State != terminal || attempts != 1 {
						t.Fatal(reply, attempts)
					}
					if terminal == "completed" && reply.Error != nil || terminal == "rejected" && (reply.Error == nil || reply.Error.Code != "rejected") {
						t.Fatal(reply)
					}
				}
			})
		}
	}
}
