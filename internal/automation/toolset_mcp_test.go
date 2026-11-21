package automation_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/operations"
	"github.com/jackgan90/Asterhand/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeMCPCaller struct {
	fn func(context.Context, string, string, json.RawMessage) (automation.Response, error)
}

func (f fakeMCPCaller) Call(ctx context.Context, op, id string, args json.RawMessage) (automation.Response, error) {
	return f.fn(ctx, op, id, args)
}

func TestToolsetFilteringAndDynamicSync(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := automation.NewRegistry("node", "inst", s)

	collabEnabled := true
	generalEnabled := true
	r.Authorize = func(token string) (automation.Principal, error) {
		if token != "caller-tok" {
			return automation.Principal{}, automation.Fail("forbidden", "bad token")
		}
		ts := map[string]bool{}
		if generalEnabled {
			ts[automation.ToolsetGeneral] = true
		}
		if collabEnabled {
			ts[automation.ToolsetCollab] = true
		}
		return automation.Principal{
			ID:       "harness:antigravity",
			CallerID: "antigravity",
			Roles:    []string{"read", "edit", "execute"},
			Scopes:   map[string][]string{"commands": {"*"}, "workers": {"*"}, "node": {"*"}},
			Toolsets: ts,
		}, nil
	}

	automation.Register(r, automation.Definition{Name: "operations.new_id", Role: "read", Toolset: automation.ToolsetCore}, nil, nil,
		func(ctx context.Context, c automation.Call, _ automation.Empty) (automation.Outcome, error) {
			return automation.Outcome{Data: map[string]string{"operation_id": operations.NewID(time.Now())}}, nil
		})
	automation.Register(r, automation.Definition{Name: "commands.list", Role: "read"}, nil, nil,
		func(ctx context.Context, c automation.Call, _ automation.Empty) (automation.Outcome, error) {
			return automation.Outcome{Data: map[string]any{"items": []any{}}}, nil
		})
	automation.Register(r, automation.Definition{Name: "workers.send", Role: "execute", Toolset: automation.ToolsetCollab, Write: true}, nil, nil,
		func(ctx context.Context, c automation.Call, _ automation.Empty) (automation.Outcome, error) {
			return automation.Outcome{Accepted: true, Data: map[string]any{"turn_id": "turn_1", "state": "running"}, Summary: map[string]any{"turn_id": "turn_1"}}, nil
		})

	// Both enabled -> core + general + collab visible.
	pBoth, _ := r.Authorize("caller-tok")
	if got := len(r.Definitions(pBoth)); got != 3 {
		t.Fatalf("expected 3 definitions, got %d", got)
	}

	// Turn off collab -> general still works, workers.send rejected with toolset_disabled.
	collabEnabled = false
	pGen, _ := r.Authorize("caller-tok")
	if got := len(r.Definitions(pGen)); got != 2 {
		t.Fatalf("expected 2 definitions (core+general), got %d", got)
	}
	resGen := r.Dispatch(context.Background(), "caller-tok", automation.Request{Version: "1", Node: "node", Operation: "commands.list", Arguments: json.RawMessage(`{}`)})
	if resGen.Error != nil {
		t.Fatalf("general op should still work: %+v", resGen.Error)
	}
	resCollab := r.Dispatch(context.Background(), "caller-tok", automation.Request{Version: "1", Node: "node", Operation: "workers.send", ID: operations.NewID(time.Now()), Arguments: json.RawMessage(`{}`)})
	if resCollab.Error == nil || resCollab.Error.Code != "toolset_disabled" {
		t.Fatalf("expected toolset_disabled, got %+v", resCollab.Error)
	}

	// Turn off both -> even core is disabled.
	generalEnabled = false
	pNone, _ := r.Authorize("caller-tok")
	if got := len(r.Definitions(pNone)); got != 0 {
		t.Fatalf("expected 0 definitions when both disabled, got %d", got)
	}
}

func TestPlainTextOnlyMCPClientWorkflowAndListChanged(t *testing.T) {
	defs := []automation.Definition{}
	r := automation.NewRegistry("node", "inst", nil)
	type newIDInput struct {
		Operation string `json:"operation,omitempty"`
	}
	automation.Register[newIDInput](r, automation.Definition{Name: "operations.new_id", Role: "read", Toolset: automation.ToolsetCore}, nil, nil, nil)
	automation.Register[automation.Empty](r, automation.Definition{Name: "operations.get", Role: "read", Toolset: automation.ToolsetCore}, nil, nil, nil)
	automation.Register[automation.Empty](r, automation.Definition{Name: "commands.list", Role: "read"}, nil, nil, nil)
	automation.Register[automation.Empty](r, automation.Definition{Name: "workers.create", Role: "execute", Toolset: automation.ToolsetCollab, Write: true}, nil, nil, nil)
	automation.Register[automation.Empty](r, automation.Definition{Name: "workers.wait", Role: "read", Toolset: automation.ToolsetCollab}, nil, nil, nil)
	defs = r.Definitions(automation.Principal{Roles: []string{"read", "edit", "execute"}})

	var seenNewIDOp string
	caller := fakeMCPCaller{fn: func(ctx context.Context, op, id string, args json.RawMessage) (automation.Response, error) {
		switch op {
		case "operations.new_id":
			var v newIDInput
			_ = json.Unmarshal(args, &v)
			seenNewIDOp = v.Operation
			return automation.Response{Version: "1", Data: map[string]any{"operation_id": "1700000000000_0123456789abcdef0123456789abcdef"}}, nil
		case "operations.get":
			return automation.Response{Version: "1", Data: operations.Receipt{State: "completed", Accepted: true, Result: json.RawMessage(`{"worker_id":"wkr_1","turn_id":"turn_42"}`)}}, nil
		case "workers.wait":
			return automation.Response{Version: "1", Data: map[string]any{"worker_id": "wkr_1", "turn_id": "turn_42", "state": "completed", "answer_available": true, "answer": "Finding #1: missing nil check in parser.go"}}, nil
		default:
			return automation.Response{Version: "1", Data: map[string]any{"ok": true}}, nil
		}
	}}

	pCollab := automation.Principal{ID: "harness:antigravity", CallerID: "antigravity", Roles: []string{"read", "edit", "execute"}, Toolsets: map[string]bool{automation.ToolsetCollab: true}}
	host := automation.NewMCPHost(caller, pCollab, defs)

	t1, t2 := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverSession, err := host.Server.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()

	listChangedCh := make(chan struct{}, 4)
	client := mcp.NewClient(&mcp.Implementation{Name: "plain-text-client", Version: "1.0"}, &mcp.ClientOptions{
		ToolListChangedHandler: func(ctx context.Context, req *mcp.ToolListChangedRequest) {
			select {
			case listChangedCh <- struct{}{}:
			default:
			}
		},
	})
	clientSession, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	if initRes := clientSession.InitializeResult(); initRes == nil || !strings.Contains(initRes.Instructions, "workers_wait") || strings.Contains(initRes.Instructions, "Use capabilities") {
		t.Fatalf("unexpected collab-only MCP instructions: %+v", initRes)
	}

	// Verify managed caller exposes underscore MCP tool names (no dots) and only core + collab tools.
	listed, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range listed.Tools {
		if strings.Contains(tool.Name, ".") {
			t.Fatalf("managed caller tool name must not contain '.': %s", tool.Name)
		}
		names[tool.Name] = true
	}
	if !names["operations_new_id"] || !names["operations_get"] || !names["workers_wait"] || names["commands_list"] {
		t.Fatalf("unexpected collab-only tool list: %+v", names)
	}

	// Verify operations_new_id translates underscore operation arg ("workers_create") -> canonical ("workers.create").
	resID, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "operations_new_id", Arguments: map[string]any{"operation": "workers_create"}})
	if err != nil || len(resID.Content) == 0 {
		t.Fatalf("new_id failed: %v", err)
	}
	if seenNewIDOp != "workers.create" {
		t.Fatalf("expected operations_new_id arg translated to workers.create, got %q", seenNewIDOp)
	}
	idText := resID.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(idText, "1700000000000_0123456789abcdef0123456789abcdef") {
		t.Fatalf("operations_new_id text missing operation_id: %q", idText)
	}

	resRec, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "operations_get", Arguments: map[string]any{}})
	if err != nil || len(resRec.Content) == 0 {
		t.Fatalf("operations_get failed: %v", err)
	}
	recText := resRec.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(recText, "turn_42") || !strings.Contains(recText, "wkr_1") {
		t.Fatalf("operations_get text missing worker/turn identity: %q", recText)
	}

	resWait, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "workers_wait", Arguments: map[string]any{}})
	if err != nil || len(resWait.Content) == 0 {
		t.Fatalf("workers_wait failed: %v", err)
	}
	waitText := resWait.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(waitText, "Finding #1: missing nil check in parser.go") {
		t.Fatalf("workers_wait text missing review answer: %q", waitText)
	}

	// Hot-switch principal from collab-only to general-only -> triggers tools/list_changed.
	pGen := automation.Principal{ID: "harness:antigravity", CallerID: "antigravity", Roles: []string{"read", "edit", "execute"}, Toolsets: map[string]bool{automation.ToolsetGeneral: true}}
	host.SyncPrincipal(pGen)
	select {
	case <-listChangedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("expected notifications/tools/list_changed on SyncPrincipal")
	}
	listed2, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names2 := map[string]bool{}
	for _, tool := range listed2.Tools {
		names2[tool.Name] = true
	}
	if !names2["commands_list"] || names2["workers_wait"] {
		t.Fatalf("unexpected general-only tool list after hot switch: %+v", names2)
	}
}
