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

	automation.Register(r, automation.Definition{Name: "operations_new_id", Role: "read", Toolset: automation.ToolsetCore}, nil, nil,
		func(ctx context.Context, c automation.Call, _ automation.Empty) (automation.Outcome, error) {
			return automation.Outcome{Data: map[string]string{"operation_id": operations.NewID(time.Now())}}, nil
		})
	automation.Register(r, automation.Definition{Name: "commands_list", Role: "read"}, nil, nil,
		func(ctx context.Context, c automation.Call, _ automation.Empty) (automation.Outcome, error) {
			return automation.Outcome{Data: map[string]any{"items": []any{}}}, nil
		})
	automation.Register(r, automation.Definition{Name: "workers_send", Role: "execute", Toolset: automation.ToolsetCollab, Write: true}, nil, nil,
		func(ctx context.Context, c automation.Call, _ automation.Empty) (automation.Outcome, error) {
			return automation.Outcome{Accepted: true, Data: map[string]any{"turn_id": "turn_1", "state": "running"}, Summary: map[string]any{"turn_id": "turn_1"}}, nil
		})

	// Both enabled -> core + general + collab visible.
	pBoth, _ := r.Authorize("caller-tok")
	if got := len(r.Definitions(pBoth)); got != 3 {
		t.Fatalf("expected 3 definitions, got %d", got)
	}

	// Turn off collab -> general still works, workers_send rejected with toolset_disabled.
	collabEnabled = false
	pGen, _ := r.Authorize("caller-tok")
	if got := len(r.Definitions(pGen)); got != 2 {
		t.Fatalf("expected 2 definitions (core+general), got %d", got)
	}
	resGen := r.Dispatch(context.Background(), "caller-tok", automation.Request{Version: "1", Node: "node", Operation: "commands_list", Arguments: json.RawMessage(`{}`)})
	if resGen.Error != nil {
		t.Fatalf("general op should still work: %+v", resGen.Error)
	}
	resCollab := r.Dispatch(context.Background(), "caller-tok", automation.Request{Version: "1", Node: "node", Operation: "workers_send", ID: operations.NewID(time.Now()), Arguments: json.RawMessage(`{}`)})
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
	automation.Register[newIDInput](r, automation.Definition{Name: "operations_new_id", Role: "read", Toolset: automation.ToolsetCore}, nil, nil, nil)
	automation.Register[automation.Empty](r, automation.Definition{Name: "operations_get", Role: "read", Toolset: automation.ToolsetCore}, nil, nil, nil)
	automation.Register[automation.Empty](r, automation.Definition{Name: "commands_list", Role: "read"}, nil, nil, nil)
	automation.Register[automation.Empty](r, automation.Definition{Name: "workers_create", Role: "execute", Toolset: automation.ToolsetCollab, Write: true}, nil, nil, nil)
	automation.Register[automation.Empty](r, automation.Definition{Name: "workers_wait", Role: "read", Toolset: automation.ToolsetCollab}, nil, nil, nil)
	defs = r.Definitions(automation.Principal{Roles: []string{"read", "edit", "execute"}})

	var seenNewIDOp string
	caller := fakeMCPCaller{fn: func(ctx context.Context, op, id string, args json.RawMessage) (automation.Response, error) {
		switch op {
		case "operations_new_id":
			var v newIDInput
			_ = json.Unmarshal(args, &v)
			seenNewIDOp = v.Operation
			return automation.Response{Version: "1", Data: map[string]any{"operation_id": "1700000000000_0123456789abcdef0123456789abcdef"}}, nil
		case "operations_get":
			return automation.Response{Version: "1", Data: operations.Receipt{State: "completed", Accepted: true, Result: json.RawMessage(`{"worker_id":"wkr_1","turn_id":"turn_42"}`)}}, nil
		case "workers_wait":
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

	// Verify operations_new_id translates underscore operation arg ("workers_create") -> canonical ("workers_create").
	resID, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "operations_new_id", Arguments: map[string]any{"operation": "workers_create"}})
	if err != nil || len(resID.Content) == 0 {
		t.Fatalf("new_id failed: %v", err)
	}
	if seenNewIDOp != "workers_create" {
		t.Fatalf("expected operations_new_id arg translated to workers_create, got %q", seenNewIDOp)
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

type upgradableFakeCaller struct {
	instanceID string
	defs       []automation.Definition
	calledNew  bool
}

func (u *upgradableFakeCaller) Call(ctx context.Context, op, id string, args json.RawMessage) (automation.Response, error) {
	if op == "capabilities" {
		return automation.Response{
			Version:  automation.APIVersion,
			Instance: u.instanceID,
			Data: map[string]any{
				"instance_id":     u.instanceID,
				"policy_revision": "rev1",
				"roles":           []string{"read", "edit", "execute"},
				"toolsets":        map[string]bool{automation.ToolsetCollab: true},
			},
		}, nil
	}
	if op == "harnesses_list" {
		u.calledNew = true
		return automation.Response{Version: automation.APIVersion, Instance: u.instanceID, Data: map[string]any{"items": []any{}}}, nil
	}
	return automation.Response{Version: automation.APIVersion, Instance: u.instanceID, Data: map[string]any{"ok": true}}, nil
}

func (u *upgradableFakeCaller) FetchDefinitions(ctx context.Context) (string, []automation.Definition, error) {
	return u.instanceID, append([]automation.Definition(nil), u.defs...), nil
}

func TestMCPHostLiveSchemaSyncOnDaemonUpgrade(t *testing.T) {
	v1Defs := []automation.Definition{
		{
			Name:        "workers_create",
			Description: "v1 description",
			Role:        "execute",
			Toolset:     automation.ToolsetCollab,
			Write:       true,
			Schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"access_mode": map[string]any{"type": "string", "description": "read_only only"}},
				"required":   []string{},
			},
		},
	}
	v2Defs := []automation.Definition{
		{
			Name:        "workers_create",
			Description: "v2 description",
			Role:        "execute",
			Toolset:     automation.ToolsetCollab,
			Write:       true,
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"access_mode": map[string]any{"type": "string", "enum": []string{"read_only", "workspace_write"}, "description": "read_only or workspace_write"},
				},
				"required": []string{},
			},
		},
		{
			Name:        "harnesses_list",
			Description: "List local harnesses",
			Role:        "read",
			Toolset:     automation.ToolsetCollab,
			Schema:      map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}},
		},
	}

	caller := &upgradableFakeCaller{instanceID: "inst_v1", defs: v1Defs}
	principal := automation.Principal{
		ID:       "harness:antigravity",
		CallerID: "antigravity",
		Roles:    []string{"read", "edit", "execute"},
		Toolsets: map[string]bool{automation.ToolsetCollab: true},
	}
	host := automation.NewMCPHost(caller, principal, v1Defs)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t1, t2 := mcp.NewInMemoryTransports()
	serverSession, err := host.Server.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()

	listChangedCh := make(chan struct{}, 4)
	client := mcp.NewClient(&mcp.Implementation{Name: "upgrade-client", Version: "1.0"}, &mcp.ClientOptions{
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

	// Initial v1 state: only workers_create (v1), no harnesses_list.
	host.SyncFromDaemon(ctx, "antigravity")
	listed1, err := clientSession.ListTools(ctx, nil)
	if err != nil || len(listed1.Tools) != 1 || listed1.Tools[0].Description != "v1 description" {
		t.Fatalf("unexpected v1 tools: %+v err=%v", listed1.Tools, err)
	}
	// Drain any initial notification.
	select {
	case <-listChangedCh:
	default:
	}

	// Simulate daemon restart/upgrade: new instance_id + updated schema + new tool!
	caller.instanceID = "inst_v2"
	caller.defs = v2Defs
	host.SyncFromDaemon(ctx, "antigravity")

	select {
	case <-listChangedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("expected notifications/tools/list_changed after daemon upgrade")
	}

	listed2, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*mcp.Tool{}
	for _, tool := range listed2.Tools {
		byName[tool.Name] = tool
	}
	if byName["workers_create"] == nil || byName["workers_create"].Description != "v2 description" {
		t.Fatalf("expected workers_create updated to v2 schema, got %+v", byName["workers_create"])
	}
	if byName["harnesses_list"] == nil {
		t.Fatalf("expected brand-new harnesses_list tool hot-synced without disconnect")
	}
	// Call the brand-new tool added by v2 daemon over the SAME existing MCP session!
	if _, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "harnesses_list", Arguments: map[string]any{}}); err != nil {
		t.Fatalf("failed invoking dynamically synced tool: %v", err)
	}
	if !caller.calledNew {
		t.Fatal("expected daemon to receive call for dynamically synced harnesses_list")
	}
}

func TestMCPHostTransientOfflinePreservesToolsAndStableJSONDigest(t *testing.T) {
	defs := []automation.Definition{
		{
			Name:        "workers_send",
			Description: "Send turn",
			Role:        "execute",
			Toolset:     automation.ToolsetCollab,
			Write:       true,
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"worker_id": map[string]any{"type": "string"},
					"prompt":    map[string]any{"type": "string"},
				},
				"required": []string{"worker_id", "prompt"},
			},
			OutputSchema: map[string]any{"type": "object", "properties": map[string]any{"data": map[string]any{"type": "object"}}},
		},
		{
			Name:        "workers_wait",
			Description: "Wait turn",
			Role:        "read",
			Toolset:     automation.ToolsetCollab,
			Schema:      map[string]any{"type": "object", "properties": map[string]any{"turn_id": map[string]any{"type": "string"}}, "required": []string{"turn_id"}},
			OutputSchema: map[string]any{"type": "object", "properties": map[string]any{"data": map[string]any{"type": "object"}}},
		},
	}
	// Simulate /v1/definitions JSON round-trip ([]string -> []any) to prove zero spurious list_changed on same-schema restart.
	rawDefs, _ := json.Marshal(defs)
	var roundTripped []automation.Definition
	if err := json.Unmarshal(rawDefs, &roundTripped); err != nil {
		t.Fatal(err)
	}

	mode := "online"
	instanceID := "inst_1"
	caller := &struct {
		fakeMCPCaller
		defs []automation.Definition
	}{defs: roundTripped}
	var currentDefs = &caller.defs
	_ = currentDefs

	dynCaller := &transientAndAuthCaller{
		getState: func() (string, string, []automation.Definition) {
			return mode, instanceID, roundTripped
		},
	}

	pCollab := automation.Principal{
		ID:       "harness:claude-code",
		CallerID: "claude-code",
		Roles:    []string{"read", "edit", "execute"},
		Toolsets: map[string]bool{automation.ToolsetCollab: true},
	}
	host := automation.NewMCPHostWithCLIPath(dynCaller, pCollab, defs, `C:\Apps\Asterhand\asterhand-cli.exe`)
	host.SyncFromDaemon(context.Background(), "claude-code")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t1, t2 := mcp.NewInMemoryTransports()
	serverSession, err := host.Server.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()

	listChangedCh := make(chan struct{}, 8)
	client := mcp.NewClient(&mcp.Implementation{Name: "resilience-client", Version: "1.0"}, &mcp.ClientOptions{
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

	if initRes := clientSession.InitializeResult(); initRes == nil || !strings.Contains(initRes.Instructions, "asterhand-cli.exe") {
		t.Fatalf("expected MCP instructions to advertise cli_path, got %+v", initRes)
	}

	// 1. Transient daemon restart (node_unavailable) MUST preserve all tools and emit zero list_changed!
	mode = "offline"
	host.SyncFromDaemon(ctx, "claude-code")
	select {
	case <-listChangedCh:
		t.Fatal("transient node_unavailable must not emit tools/list_changed or drop tools")
	case <-time.After(60 * time.Millisecond):
	}
	listedOffline, err := clientSession.ListTools(ctx, nil)
	if err != nil || len(listedOffline.Tools) != 2 {
		t.Fatalf("expected 2 tools preserved during transient disconnect, got %d (err=%v)", len(listedOffline.Tools), err)
	}

	// 2. Daemon recovers with a new instance_id and JSON-roundtripped identical schemas -> zero spurious list_changed!
	mode = "online"
	instanceID = "inst_2"
	host.SyncFromDaemon(ctx, "claude-code")
	select {
	case <-listChangedCh:
		t.Fatal("same-schema restart after JSON round-trip must not emit spurious tools/list_changed")
	case <-time.After(60 * time.Millisecond):
	}

	// 3. Explicit credential revocation (forbidden) MUST immediately remove all tools and emit list_changed!
	mode = "forbidden"
	host.SyncFromDaemon(ctx, "claude-code")
	select {
	case <-listChangedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("expected notifications/tools/list_changed on explicit forbidden")
	}
	listedRevoked, err := clientSession.ListTools(ctx, nil)
	if err != nil || len(listedRevoked.Tools) != 0 {
		t.Fatalf("expected 0 tools after explicit auth denial, got %d (err=%v)", len(listedRevoked.Tools), err)
	}
}

type transientAndAuthCaller struct {
	getState func() (mode string, instanceID string, defs []automation.Definition)
}

func (t *transientAndAuthCaller) Call(ctx context.Context, op, id string, args json.RawMessage) (automation.Response, error) {
	mode, inst, _ := t.getState()
	if mode == "offline" {
		return automation.Response{}, automation.Fail("node_unavailable", "Daemon restarting")
	}
	if mode == "forbidden" {
		return automation.Response{Version: automation.APIVersion, Error: automation.Fail("forbidden", "Credential revoked")}, nil
	}
	return automation.Response{
		Version:  automation.APIVersion,
		Instance: inst,
		Data: map[string]any{
			"instance_id":     inst,
			"policy_revision": "rev1",
			"roles":           []string{"read", "edit", "execute"},
			"toolsets":        map[string]bool{automation.ToolsetCollab: true},
		},
	}, nil
}

func (t *transientAndAuthCaller) FetchDefinitions(ctx context.Context) (string, []automation.Definition, error) {
	mode, inst, defs := t.getState()
	if mode == "offline" {
		return "", nil, automation.Fail("node_unavailable", "Daemon restarting")
	}
	return inst, defs, nil
}

func TestMCPWriteAutoGeneratesOptionalOperationIDAndTextFormatting(t *testing.T) {
	r := automation.NewRegistry("node", "inst", nil)
	type sendArgs struct {
		Prompt string `json:"prompt"`
	}
	automation.Register[sendArgs](r, automation.Definition{Name: "workers_send", Role: "execute", Toolset: automation.ToolsetCollab, Write: true}, nil, nil, nil)
	automation.Register[automation.Empty](r, automation.Definition{Name: "node_start", Role: "execute", Toolset: automation.ToolsetGeneral, Write: true}, nil, nil, nil)
	automation.Register[automation.Empty](r, automation.Definition{Name: "workers_wait", Role: "read", Toolset: automation.ToolsetCollab}, nil, nil, nil)
	defs := r.Definitions(automation.Principal{Roles: []string{"read", "edit", "execute"}})

	var seenIDs []string
	failTransport := false
	bigAnswer := strings.Repeat("A", 50<<10)
	caller := fakeMCPCaller{fn: func(ctx context.Context, op, id string, args json.RawMessage) (automation.Response, error) {
		seenIDs = append(seenIDs, id)
		if failTransport {
			return automation.Response{}, automation.Fail("result_unknown", "Lost pipe after write")
		}
		if op == "workers_wait" {
			return automation.Response{
				Version: automation.APIVersion,
				Data: map[string]any{
					"worker_id":    "wkr_1",
					"turn_id":      "turn_big",
					"state":        "completed",
					"truncated":    true,
					"result_bytes": 60000,
					"next_cursor":  "24576",
					"answer":       bigAnswer,
					"next_step":    "Turn completed with truncated preview. Call workers_result(turn_id=\"turn_big\", cursor=\"24576\") to read the rest.",
					"after_vcs":    map[string]any{"changed_files": []string{"a.go"}},
				},
			}, nil
		}
		return automation.Response{
			Version: automation.APIVersion,
			ID:      id,
			Data:    map[string]any{"turn_id": "turn_1", "state": "running"},
			Receipt: &operations.Receipt{State: "completed", Accepted: true},
		}, nil
	}}

	pAll := automation.Principal{
		ID:       "harness:claude-code",
		CallerID: "claude-code",
		Roles:    []string{"read", "edit", "execute"},
		Toolsets: map[string]bool{automation.ToolsetGeneral: true, automation.ToolsetCollab: true},
	}
	host := automation.NewMCPHost(caller, pAll, defs)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t1, t2 := mcp.NewInMemoryTransports()
	serverSession, err := host.Server.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "opid-client", Version: "1.0"}, nil).Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	// 1. Write tool schema exposes operation_id without marking it required.
	listed, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		if tool.Name == "workers_send" {
			b, _ := json.Marshal(tool.InputSchema)
			var s map[string]any
			_ = json.Unmarshal(b, &s)
			for _, reqField := range s["required"].([]any) {
				if reqField == "operation_id" {
					t.Fatalf("operation_id must be optional on MCP write tools, schema=%s", string(b))
				}
			}
		}
	}

	// 2. Omitting operation_id auto-generates valid timestamped ID and echoes it back.
	resSend, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "workers_send", Arguments: map[string]any{"prompt": "hello"}})
	if err != nil || resSend.IsError || len(seenIDs) != 1 || operations.CheckName(seenIDs[0], "workers_send") != nil {
		t.Fatalf("expected auto-generated valid operation_id, seen=%v err=%v res=%+v", seenIDs, err, resSend)
	}
	sendText := resSend.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(sendText, seenIDs[0]) {
		t.Fatalf("expected auto-generated operation_id %q in text %q", seenIDs[0], sendText)
	}

	// 3. node_start without operation_id generates start:-prefixed ID.
	resStart, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "node_start", Arguments: map[string]any{}})
	if err != nil || resStart.IsError || len(seenIDs) != 2 || !strings.HasPrefix(seenIDs[1], "start:") {
		t.Fatalf("expected start:-prefixed operation_id for node_start, seen=%v err=%v res=%+v", seenIDs, err, resStart)
	}

	// 4. Transport error still echoes auto-generated operation_id for reconciliation.
	failTransport = true
	resErr, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "workers_send", Arguments: map[string]any{"prompt": "lost"}})
	if err != nil || !resErr.IsError || len(seenIDs) != 3 {
		t.Fatalf("expected IsError with generated ID recorded, seen=%v err=%v res=%+v", seenIDs, err, resErr)
	}
	errText := resErr.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(errText, seenIDs[2]) {
		t.Fatalf("expected generated operation_id %q in disconnect error text %q", seenIDs[2], errText)
	}
	failTransport = false

	// 5. Explicit malformed / empty / null operation_id is rejected cleanly.
	for _, bad := range []any{"", nil, "not_a_timestamp_id"} {
		badRes, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "workers_send", Arguments: map[string]any{"prompt": "x", "operation_id": bad}})
		if err != nil || !badRes.IsError || !strings.Contains(badRes.Content[0].(*mcp.TextContent).Text, "invalid_operation_id") {
			t.Fatalf("expected invalid_operation_id for %#v, got err=%v res=%+v", bad, err, badRes)
		}
	}

	// 6. Over-budget workers_wait text projection preserves turn_id, next_cursor, and actionable cursor next_step.
	resWait, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "workers_wait", Arguments: map[string]any{}})
	if err != nil || resWait.IsError {
		t.Fatalf("workers_wait over-budget failed: %v %+v", err, resWait)
	}
	waitText := resWait.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(waitText, `"text_truncated":true`) || !strings.Contains(waitText, `"turn_id":"turn_big"`) || !strings.Contains(waitText, `"next_cursor":"24576"`) || !strings.Contains(waitText, `cursor=\"24576\"`) {
		t.Fatalf("over-budget text projection lost essential cursor metadata: %s", waitText)
	}
}
