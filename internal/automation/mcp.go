package automation

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func RunMCP(ctx context.Context, args []string, stderr io.Writer, definitions []Definition) error {
	f := flag.NewFlagSet("mcp", flag.ContinueOnError)
	f.SetOutput(stderr)
	home := f.String("home", config.DefaultHome(), "Existing node data directory")
	caller := f.String("caller", "", "Managed coding harness caller identifier")

	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return Fail("invalid_parameters", "Unexpected MCP positional arguments")
	}
	var (
		client *Client
		err    error
	)
	if *caller != "" {
		client, err = OpenCallerClient(*home, *caller)
	} else {
		client, err = OpenClient(*home)
	}
	if err != nil {
		return err
	}
	defer client.Close()

	principal, err := client.LocalPrincipal()
	if err != nil {
		return err
	}
	if *caller != "" {
		principal = fetchCallerPrincipal(ctx, client, *caller)
	}
	host := NewMCPHost(client, principal, definitions)
	if *caller != "" {
		syncCtx, cancelSync := context.WithCancel(ctx)
		defer cancelSync()
		go host.watchCallerPolicy(syncCtx, client, *caller, 2*time.Second)
	}
	return host.Server.Run(ctx, &mcp.StdioTransport{})
}

type Caller interface {
	Call(context.Context, string, string, json.RawMessage) (Response, error)
}

// MCPHost wraps an MCP Server and supports live toolset synchronization via
// AddTool / RemoveTools (which emit notifications/tools/list_changed in SDK v1.7.0).
type MCPHost struct {
	Server          *mcp.Server
	client          Caller
	definitions     map[string]Definition
	byToolName      map[string]string // exposed MCP tool name -> canonical operation name
	underscoreNames bool
	mu              sync.Mutex
	active          map[string]bool
	lastRev         string
}

func NewMCPServer(client Caller, principal Principal, definitions []Definition) *mcp.Server {
	return NewMCPHost(client, principal, definitions).Server
}

func mcpToolName(opName string, underscore bool) string {
	if underscore {
		return strings.ReplaceAll(opName, ".", "_")
	}
	return opName
}

func mcpInstructions(principal Principal, underscore bool) string {
	n := func(op string) string { return mcpToolName(op, underscore) }
	var parts []string
	if principal.AllowsToolset(ToolsetCore) {
		parts = append(parts, "Write tools require an operation_id from "+n("operations.new_id")+"; use "+n("operations.get")+" to recover a lost receipt.")
	}
	if principal.AllowsToolset(ToolsetCollab) {
		parts = append(parts, "For multi-harness collaboration: reuse an existing project worker via "+n("workers.list")+" (or create one via "+n("workers.create")+"), dispatch with "+n("workers.send")+", wait with "+n("workers.wait")+" until finished, and read truncated output with "+n("workers.result")+".")
	}
	if principal.AllowsToolset(ToolsetGeneral) {
		parts = append(parts, "Use capabilities to inspect enabled toolsets and node limits.")
	}
	return strings.Join(parts, " ")
}

func NewMCPHost(client Caller, principal Principal, definitions []Definition) *MCPHost {
	// Managed coding harnesses (Antigravity, Codex, Claude Code, etc.) rely on
	// LLM tool registries where dotted MCP tool names break parameter schema
	// generation (e.g. Antigravity writes "parameters": null for dotted names).
	// Map "." -> "_" at the MCP boundary for managed callers while preserving
	// canonical dotted names internally and for legacy direct MCP clients.
	underscore := principal.CallerID != ""
	var opts *mcp.ServerOptions
	if inst := mcpInstructions(principal, underscore); inst != "" {
		opts = &mcp.ServerOptions{Instructions: inst}
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "asterhand", Version: config.Version}, opts)
	byName := make(map[string]Definition, len(definitions))
	byToolName := make(map[string]string, len(definitions))
	for _, d := range definitions {
		byName[d.Name] = d
		byToolName[mcpToolName(d.Name, underscore)] = d.Name
	}
	host := &MCPHost{
		Server:          server,
		client:          client,
		definitions:     byName,
		byToolName:      byToolName,
		underscoreNames: underscore,
		active:          map[string]bool{},
		lastRev:         principal.PolicyRevision,
	}
	host.SyncPrincipal(principal)
	return host
}

// SyncPrincipal reconciles the registered MCP tools with the principal's current
// roles and toolsets. Adding or removing tools triggers tools/list_changed.
func (h *MCPHost) SyncPrincipal(principal Principal) {
	h.mu.Lock()
	defer h.mu.Unlock()
	desired := map[string]bool{}
	for name, d := range h.definitions {
		if principal.Role(d.Role) && principal.AllowsToolset(d.EffectiveToolset()) {
			desired[name] = true
		}
	}
	toRemove := []string{}
	for name := range h.active {
		if !desired[name] {
			toRemove = append(toRemove, mcpToolName(name, h.underscoreNames))
			delete(h.active, name)
		}
	}
	if len(toRemove) > 0 {
		h.Server.RemoveTools(toRemove...)
	}
	for name := range desired {
		if h.active[name] {
			continue
		}
		h.addDefinition(h.definitions[name])
		h.active[name] = true
	}
	h.lastRev = principal.PolicyRevision
}

func (h *MCPHost) normalizeOperationArg(opName string, fields map[string]json.RawMessage) {
	if !h.underscoreNames {
		return
	}
	if opName == "operations.new_id" {
		if raw, ok := fields["operation"]; ok {
			var v string
			if json.Unmarshal(raw, &v) == nil {
				if canon, found := h.byToolName[v]; found && canon != v {
					if encoded, err := json.Marshal(canon); err == nil {
						fields["operation"] = encoded
					}
				}
			}
		}
	}
	if opName == "batches.submit" || opName == "read.many" {
		if raw, ok := fields["items"]; ok {
			var items []map[string]json.RawMessage
			if json.Unmarshal(raw, &items) == nil {
				changed := false
				for _, item := range items {
					var v string
					if json.Unmarshal(item["operation"], &v) == nil {
						if canon, found := h.byToolName[v]; found && canon != v {
							if encoded, err := json.Marshal(canon); err == nil {
								item["operation"] = encoded
								changed = true
							}
						}
					}
				}
				if changed {
					if encoded, err := json.Marshal(items); err == nil {
						fields["items"] = encoded
					}
				}
			}
		}
	}
}

func (h *MCPHost) addDefinition(d Definition) {
	schema := maps.Clone(d.Schema)
	props := maps.Clone(schema["properties"].(map[string]any))
	required := append([]string{}, schema["required"].([]string)...)
	if d.Write {
		pattern := "^[0-9]{13}_[a-f0-9]{32}$"
		if d.Name == "node.start" {
			pattern = "^start:[0-9]{13}_[a-f0-9]{32}$"
		}
		newIDTool := mcpToolName("operations.new_id", h.underscoreNames)
		props["operation_id"] = map[string]any{"type": "string", "pattern": pattern, "description": "Create once with " + newIDTool + ", or offline CLI new-id. Reuse unchanged after disconnection; a new identity is a new side effect."}
		required = append(required, "operation_id")
	}
	schema["properties"], schema["required"] = props, required
	exposedName := mcpToolName(d.Name, h.underscoreNames)
	h.Server.AddTool(&mcp.Tool{Name: exposedName, Description: d.Description, InputSchema: schema, OutputSchema: d.OutputSchema, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: !d.Write}},
		func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			input := request.Params.Arguments
			if len(input) == 0 {
				input = json.RawMessage("{}")
			}
			if err := checkJSON(input); err != nil {
				return mcpResult(d.Name, h.underscoreNames, Response{Version: APIVersion, Error: PublicError(err)}), nil
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(input, &fields); err != nil {
				return mcpResult(d.Name, h.underscoreNames, Response{Version: APIVersion, Error: Fail("invalid_parameters", "Expected an argument object")}), nil
			}
			id := ""
			if d.Write {
				if err := json.Unmarshal(fields["operation_id"], &id); err != nil || id == "" {
					return mcpResult(d.Name, h.underscoreNames, Response{Version: APIVersion, Error: Fail("invalid_operation_id", "A stable operation_id is required")}), nil
				}
				delete(fields, "operation_id")
			}
			h.normalizeOperationArg(d.Name, fields)
			arguments, _ := json.Marshal(fields)
			if _, err := d.normalize(arguments); err != nil {
				return mcpResult(d.Name, h.underscoreNames, Response{Version: APIVersion, ID: id, Error: PublicError(err)}), nil
			}
			response, err := h.client.Call(ctx, d.Name, id, arguments)
			if err != nil {
				response = Response{Version: APIVersion, ID: id, Error: PublicError(err)}
			}
			return mcpResult(d.Name, h.underscoreNames, response), nil
		})
}

func (h *MCPHost) watchCallerPolicy(ctx context.Context, client Caller, callerID string, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p := fetchCallerPrincipal(ctx, client, callerID)
			h.SyncPrincipal(p)
		}
	}
}

func fetchCallerPrincipal(ctx context.Context, client Caller, callerID string) Principal {
	if reloader, ok := client.(interface{ ReloadCallerToken() bool }); ok {
		reloader.ReloadCallerToken()
	}
	qCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := client.Call(qCtx, "capabilities", "", json.RawMessage("{}"))
	if err != nil || resp.Error != nil {
		// Credential revoked or node unavailable: expose zero tools.
		return Principal{ID: "harness:" + callerID, CallerID: callerID, Roles: nil, Toolsets: map[string]bool{}}
	}
	b, _ := json.Marshal(resp.Data)
	var capData struct {
		PolicyRevision string          `json:"policy_revision"`
		Toolsets       map[string]bool `json:"toolsets"`
		Roles          []string        `json:"roles"`
	}
	_ = json.Unmarshal(b, &capData)
	roles := capData.Roles
	if len(roles) == 0 && (capData.Toolsets[ToolsetGeneral] || capData.Toolsets[ToolsetCollab]) {
		roles = []string{"read", "edit", "execute"}
	}
	toolsets := capData.Toolsets
	if toolsets == nil {
		toolsets = map[string]bool{}
	}
	return Principal{
		ID:             "harness:" + callerID,
		CallerID:       callerID,
		PolicyRevision: capData.PolicyRevision,
		Roles:          roles,
		Toolsets:       toolsets,
	}
}

func mcpResult(opName string, underscore bool, response Response) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		StructuredContent: response,
		Content:           []mcp.Content{&mcp.TextContent{Text: formatMCPText(opName, underscore, response)}},
		IsError:           response.Error != nil,
	}
}
