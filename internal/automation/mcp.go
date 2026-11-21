package automation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	initialInstance := ""
	if *caller != "" {
		principal, initialInstance, _ = fetchLivePrincipal(ctx, client, *caller)
	} else if _, inst, ok := fetchLivePrincipal(ctx, client, ""); ok {
		initialInstance = inst
	}
	if liveDefs, ok := fetchLiveDefinitions(ctx, client); ok && len(liveDefs) > 0 {
		definitions = liveDefs
	}

	host := NewMCPHost(client, principal, definitions)
	host.mu.Lock()
	host.lastInstanceID = initialInstance
	host.mu.Unlock()

	syncCtx, cancelSync := context.WithCancel(ctx)
	defer cancelSync()
	go host.watchDaemon(syncCtx, *caller, 2*time.Second)

	return host.Server.Run(ctx, &mcp.StdioTransport{})
}

type Caller interface {
	Call(context.Context, string, string, json.RawMessage) (Response, error)
}

// DefinitionProvider is implemented by Clients connected to a running Asterhand
// daemon so a long-lived MCP subprocess can hot-sync new tools and updated
// schemas whenever the daemon restarts or upgrades, with zero disconnect.
type DefinitionProvider interface {
	FetchDefinitions(ctx context.Context) (instanceID string, defs []Definition, err error)
}

// MCPHost wraps an MCP Server and supports live toolset & schema synchronization
// via AddTool / RemoveTools (which emit notifications/tools/list_changed in SDK v1.7.0).
type MCPHost struct {
	Server         *mcp.Server
	client         Caller
	mu             sync.Mutex
	definitions    map[string]Definition
	activeDigests  map[string]string
	lastRev        string
	lastInstanceID string
}

func NewMCPServer(client Caller, principal Principal, definitions []Definition) *mcp.Server {
	return NewMCPHost(client, principal, definitions).Server
}

func mcpInstructions(principal Principal) string {
	var parts []string
	if principal.AllowsToolset(ToolsetCore) {
		parts = append(parts, "Write tools require an operation_id from operations_new_id; use operations_get to recover a lost receipt.")
	}
	if principal.AllowsToolset(ToolsetCollab) {
		parts = append(parts, "For multi-harness collaboration: inspect available harnesses via harnesses_list, reuse an existing project worker via workers_list (or create one via workers_create), dispatch with workers_send, wait with workers_wait until finished, and read truncated output with workers_result.")
	}
	if principal.AllowsToolset(ToolsetGeneral) {
		parts = append(parts, "Use capabilities to inspect enabled toolsets and node limits.")
	}
	return strings.Join(parts, " ")
}

func NewMCPHost(client Caller, principal Principal, definitions []Definition) *MCPHost {
	var opts *mcp.ServerOptions
	if inst := mcpInstructions(principal); inst != "" {
		opts = &mcp.ServerOptions{Instructions: inst}
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "asterhand", Version: config.Version}, opts)
	byName := make(map[string]Definition, len(definitions))
	for _, d := range definitions {
		byName[d.Name] = d
	}
	host := &MCPHost{
		Server:        server,
		client:        client,
		definitions:   byName,
		activeDigests: map[string]string{},
		lastRev:       principal.PolicyRevision,
	}
	host.SyncPrincipal(principal)
	return host
}

// SyncPrincipal reconciles the registered MCP tools with the principal's current
// roles, toolsets, and active definitions. Adding, removing, or replacing changed
// tool schemas triggers tools/list_changed.
func (h *MCPHost) SyncPrincipal(principal Principal) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.syncLocked(principal)
}

// SyncFromDaemon checks the daemon's current capabilities and instance_id. When
// the daemon restarts/upgrades (instance_id changes), it refreshes tool
// definitions & schemas in-place and emits tools/list_changed with zero disconnect.
func (h *MCPHost) SyncFromDaemon(ctx context.Context, callerID string) {
	if h.client == nil {
		return
	}
	p, instID, ok := fetchLivePrincipal(ctx, h.client, callerID)
	if !ok && callerID == "" {
		// Unmanaged local MCP stays bound to local principal if node is temporarily stopped.
		return
	}

	h.mu.Lock()
	instanceChanged := ok && instID != "" && instID != h.lastInstanceID
	h.mu.Unlock()

	if instanceChanged {
		if defs, fetched := fetchLiveDefinitions(ctx, h.client); fetched && len(defs) > 0 {
			h.mu.Lock()
			updated := make(map[string]Definition, len(defs))
			for _, d := range defs {
				if prev, exists := h.definitions[d.Name]; exists && d.normalize == nil {
					// Keep local normalizer only when schema is identical; newer schemas validate on daemon.
					if definitionDigest(prev) == definitionDigest(d) {
						d.normalize = prev.normalize
					}
				}
				updated[d.Name] = d
			}
			h.definitions = updated
			h.lastInstanceID = instID
			h.syncLocked(p)
			h.mu.Unlock()
			return
		}
	}

	h.mu.Lock()
	if ok && instID != "" {
		h.lastInstanceID = instID
	}
	h.syncLocked(p)
	h.mu.Unlock()
}

func (h *MCPHost) syncLocked(principal Principal) {
	desired := map[string]string{}
	for name, d := range h.definitions {
		if principal.Role(d.Role) && principal.AllowsToolset(d.EffectiveToolset()) {
			desired[name] = definitionDigest(d)
		}
	}
	toRemove := []string{}
	for name, prevDigest := range h.activeDigests {
		nextDigest, keep := desired[name]
		if !keep || nextDigest != prevDigest {
			toRemove = append(toRemove, name)
			delete(h.activeDigests, name)
		}
	}
	if len(toRemove) > 0 {
		h.Server.RemoveTools(toRemove...)
	}
	for name, digest := range desired {
		if h.activeDigests[name] == digest {
			continue
		}
		h.addDefinition(h.definitions[name])
		h.activeDigests[name] = digest
	}
	h.lastRev = principal.PolicyRevision
}

func definitionDigest(d Definition) string {
	b, _ := json.Marshal(struct {
		Name         string         `json:"n"`
		Description  string         `json:"d"`
		Write        bool           `json:"w"`
		Schema       map[string]any `json:"s"`
		OutputSchema map[string]any `json:"o"`
	}{
		Name:         d.Name,
		Description:  d.Description,
		Write:        d.Write,
		Schema:       d.Schema,
		OutputSchema: d.OutputSchema,
	})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:12])
}

func (h *MCPHost) addDefinition(d Definition) {
	schema := map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}, "additionalProperties": false}
	if d.Schema != nil {
		schema = maps.Clone(d.Schema)
	}
	props := map[string]any{}
	if rawProps, ok := schema["properties"].(map[string]any); ok {
		props = maps.Clone(rawProps)
	}
	required := []string{}
	switch rawReq := schema["required"].(type) {
	case []string:
		required = append(required, rawReq...)
	case []any:
		for _, item := range rawReq {
			if s, ok := item.(string); ok {
				required = append(required, s)
			}
		}
	}
	if d.Write {
		pattern := "^[0-9]{13}_[a-f0-9]{32}$"
		if d.Name == "node_start" {
			pattern = "^start:[0-9]{13}_[a-f0-9]{32}$"
		}
		props["operation_id"] = map[string]any{
			"type":        "string",
			"pattern":     pattern,
			"description": "Create once with operations_new_id, or offline CLI new-id. Reuse unchanged after disconnection; a new identity is a new side effect.",
		}
		required = append(required, "operation_id")
	}
	schema["properties"], schema["required"] = props, required

	normalize := d.normalize
	h.Server.AddTool(&mcp.Tool{
		Name:         d.Name,
		Description:  d.Description,
		InputSchema:  schema,
		OutputSchema: d.OutputSchema,
		Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: !d.Write},
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		input := request.Params.Arguments
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		if err := checkJSON(input); err != nil {
			return mcpResult(d.Name, Response{Version: APIVersion, Error: PublicError(err)}), nil
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(input, &fields); err != nil {
			return mcpResult(d.Name, Response{Version: APIVersion, Error: Fail("invalid_parameters", "Expected an argument object")}), nil
		}
		id := ""
		if d.Write {
			if err := json.Unmarshal(fields["operation_id"], &id); err != nil || id == "" {
				return mcpResult(d.Name, Response{Version: APIVersion, Error: Fail("invalid_operation_id", "A stable operation_id is required")}), nil
			}
			delete(fields, "operation_id")
		}
		arguments, _ := json.Marshal(fields)
		if normalize != nil {
			if _, err := normalize(arguments); err != nil {
				return mcpResult(d.Name, Response{Version: APIVersion, ID: id, Error: PublicError(err)}), nil
			}
		}
		response, err := h.client.Call(ctx, d.Name, id, arguments)
		if err != nil {
			response = Response{Version: APIVersion, ID: id, Error: PublicError(err)}
		}
		return mcpResult(d.Name, response), nil
	})
}

func (h *MCPHost) watchDaemon(ctx context.Context, callerID string, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.SyncFromDaemon(ctx, callerID)
		}
	}
}

func fetchLiveDefinitions(ctx context.Context, client Caller) ([]Definition, bool) {
	dp, ok := client.(DefinitionProvider)
	if !ok {
		return nil, false
	}
	qCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, defs, err := dp.FetchDefinitions(qCtx)
	if err != nil {
		return nil, false
	}
	return defs, true
}

func fetchLivePrincipal(ctx context.Context, client Caller, callerID string) (Principal, string, bool) {
	if reloader, ok := client.(interface{ ReloadCallerToken() bool }); ok {
		reloader.ReloadCallerToken()
	}
	qCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := client.Call(qCtx, "capabilities", "", json.RawMessage("{}"))
	if err != nil || resp.Error != nil {
		if callerID == "" {
			return localPrincipal(), "", false
		}
		// Credential revoked or node unavailable: expose zero tools for managed callers.
		return Principal{ID: "harness:" + callerID, CallerID: callerID, Roles: nil, Toolsets: map[string]bool{}}, "", false
	}
	b, _ := json.Marshal(resp.Data)
	var capData struct {
		InstanceID     string          `json:"instance_id"`
		PolicyRevision string          `json:"policy_revision"`
		Toolsets       map[string]bool `json:"toolsets"`
		Roles          []string        `json:"roles"`
	}
	_ = json.Unmarshal(b, &capData)
	if callerID == "" {
		return localPrincipal(), capData.InstanceID, true
	}
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
	}, capData.InstanceID, true
}

func mcpResult(opName string, response Response) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		StructuredContent: response,
		Content:           []mcp.Content{&mcp.TextContent{Text: formatMCPText(opName, response)}},
		IsError:           response.Error != nil,
	}
}
