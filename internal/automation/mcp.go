package automation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/launch"
	"github.com/jackgan90/Asterhand/internal/operations"
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

	var (
		principal       Principal
		initialInstance string
		cliPath         string
		status          principalFetchStatus
	)
	if *caller != "" {
		for attempt := 0; attempt < 3; attempt++ {
			principal, initialInstance, cliPath, status = fetchLivePrincipal(ctx, client, *caller)
			if status != principalTransientOffline || attempt == 2 {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
	} else {
		principal, err = client.LocalPrincipal()
		if err != nil {
			return err
		}
		_, initialInstance, cliPath, _ = fetchLivePrincipal(ctx, client, "")
	}
	if cliPath == "" {
		cliPath = resolveLocalCLIPath(*home)
	}
	defsSynced := false
	if _, liveDefs, ok := fetchLiveDefinitions(ctx, client); ok && len(liveDefs) > 0 {
		definitions = liveDefs
		defsSynced = true
	}

	host := NewMCPHostWithCLIPath(client, principal, definitions, cliPath)
	host.mu.Lock()
	if defsSynced {
		host.lastInstanceID = initialInstance
	} else {
		host.lastRev = ""
		host.lastPermKey = ""
	}
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
	lastPermKey    string
	lastInstanceID string
	cliPath        string
}

func NewMCPServer(client Caller, principal Principal, definitions []Definition) *mcp.Server {
	return NewMCPHost(client, principal, definitions).Server
}

func mcpInstructions(principal Principal, cliPath string) string {
	var parts []string
	if principal.AllowsToolset(ToolsetCore) {
		parts = append(parts, "Write tools auto-generate an operation_id when omitted and return it on both success and disconnection errors; pass an explicit operation_id from operations_new_id only when you need pre-generated cross-retry idempotency, and use operations_get to recover an unknown outcome.")
	}
	if principal.AllowsToolset(ToolsetCollab) {
		parts = append(parts, "For multi-harness collaboration: inspect available harnesses via harnesses_list, reuse an existing project worker via workers_list (or create one via workers_create), dispatch with workers_send (serialize workspace_write turns on the same physical worktree), wait with workers_wait (or workers_wait_any for multiple concurrent turns), inspect recent logs with workers_logs(tail=true), and read truncated output with workers_result(cursor=...).")
		if strings.TrimSpace(cliPath) != "" {
			callerFlag := ""
			if principal.CallerID != "" {
				callerFlag = " --caller " + principal.CallerID
			}
			parts = append(parts, fmt.Sprintf("For long-running turns (>30s) or MCP disconnect fallback, run in a background shell: %q wait <turn_id>%s (or %q automation <operation>%s --args '<json>').", cliPath, callerFlag, cliPath, callerFlag))
		}
	}
	if principal.AllowsToolset(ToolsetGeneral) {
		parts = append(parts, "Use capabilities to inspect enabled toolsets, node limits, and cli_path.")
	}
	return strings.Join(parts, " ")
}

func NewMCPHost(client Caller, principal Principal, definitions []Definition) *MCPHost {
	return NewMCPHostWithCLIPath(client, principal, definitions, "")
}

func NewMCPHostWithCLIPath(client Caller, principal Principal, definitions []Definition, cliPath string) *MCPHost {
	var opts *mcp.ServerOptions
	if inst := mcpInstructions(principal, cliPath); inst != "" {
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
		lastPermKey:   principalPermissionKey(principal),
		cliPath:       cliPath,
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
	h.lastRev = principal.PolicyRevision
	h.lastPermKey = principalPermissionKey(principal)
}

// SyncFromDaemon checks the daemon's current capabilities, instance_id, and
// policy_revision. When the daemon restarts/upgrades or caller authorization
// changes, it refreshes filtered tool definitions & schemas in-place and emits
// tools/list_changed with zero disconnect. Synced revision markers advance only
// after definitions are successfully fetched so transient failures retry cleanly.
func (h *MCPHost) SyncFromDaemon(ctx context.Context, callerID string) {
	if h.client == nil {
		return
	}
	p, instID, cliPath, status := fetchLivePrincipal(ctx, h.client, callerID)
	if status == principalTransientOffline {
		// Preserve already-registered tools during brief daemon restarts/upgrades.
		return
	}
	if status == principalAuthDenied {
		if callerID == "" {
			return
		}
		h.mu.Lock()
		h.syncLocked(p)
		h.lastPermKey = principalPermissionKey(p)
		h.mu.Unlock()
		return
	}

	permKey := principalPermissionKey(p)
	h.mu.Lock()
	if cliPath != "" {
		h.cliPath = cliPath
	}
	instanceChanged := instID != "" && instID != h.lastInstanceID
	policyChanged := p.PolicyRevision != "" && p.PolicyRevision != h.lastRev
	permChanged := permKey != h.lastPermKey
	h.mu.Unlock()

	if instanceChanged || policyChanged || permChanged {
		if _, canFetch := h.client.(DefinitionProvider); canFetch {
			fetchedInst, defs, fetched := fetchLiveDefinitions(ctx, h.client)
			if fetched && (len(defs) > 0 || !p.AllowsToolset(ToolsetCore)) {
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
				if fetchedInst == "" || instID == "" || fetchedInst == instID {
					if instID != "" {
						h.lastInstanceID = instID
					} else if fetchedInst != "" {
						h.lastInstanceID = fetchedInst
					}
					if p.PolicyRevision != "" {
						h.lastRev = p.PolicyRevision
					}
					h.lastPermKey = permKey
				} else {
					h.lastInstanceID = fetchedInst
				}
				h.syncLocked(p)
				h.mu.Unlock()
				return
			}
			// Enforce any immediate permission revocations fail-closed, while
			// keeping lastInstanceID/lastRev/lastPermKey unchanged so the next
			// poll retries fetching the updated definitions.
			h.mu.Lock()
			h.syncLocked(p)
			h.mu.Unlock()
			return
		}
	}

	h.mu.Lock()
	if instID != "" {
		h.lastInstanceID = instID
	}
	if p.PolicyRevision != "" {
		h.lastRev = p.PolicyRevision
	}
	h.lastPermKey = permKey
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
}

func principalPermissionKey(p Principal) string {
	var b strings.Builder
	roles := append([]string(nil), p.Roles...)
	slices.Sort(roles)
	b.WriteString("r:")
	b.WriteString(strings.Join(roles, ","))
	b.WriteString("|t:")
	if p.Toolsets == nil {
		b.WriteString("*")
	} else {
		enabled := make([]string, 0, len(p.Toolsets))
		for name, ok := range p.Toolsets {
			if ok {
				enabled = append(enabled, name)
			}
		}
		slices.Sort(enabled)
		b.WriteString(strings.Join(enabled, ","))
	}
	return b.String()
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
			"description": "Optional stable write identity. When omitted, Asterhand MCP auto-generates a valid operation_id and returns it in both success and disconnection error responses; pass an explicit value from operations_new_id only when you need pre-generated cross-retry idempotency.",
		}
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
			if rawID, present := fields["operation_id"]; present {
				if err := json.Unmarshal(rawID, &id); err != nil || id == "" {
					return mcpResult(d.Name, Response{Version: APIVersion, Error: Fail("invalid_operation_id", "operation_id must be a valid non-empty identifier when provided")}), nil
				}
				if err := operations.CheckName(id, d.Name); err != nil {
					return mcpResult(d.Name, Response{Version: APIVersion, Error: Fail("invalid_operation_id", "operation_id must match the required timestamped format when provided")}), nil
				}
				delete(fields, "operation_id")
			} else {
				id = operations.NewIDFor(d.Name, time.Now())
			}
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
		} else if id != "" && response.ID == "" {
			response.ID = id
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

func fetchLiveDefinitions(ctx context.Context, client Caller) (string, []Definition, bool) {
	dp, ok := client.(DefinitionProvider)
	if !ok {
		return "", nil, false
	}
	qCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	instID, defs, err := dp.FetchDefinitions(qCtx)
	if err != nil {
		return "", nil, false
	}
	return instID, defs, true
}

type principalFetchStatus int

const (
	principalOnline principalFetchStatus = iota
	principalAuthDenied
	principalTransientOffline
)

func isAuthDeniedCode(code string) bool {
	switch code {
	case "forbidden", "authorization_required", "toolset_disabled":
		return true
	default:
		return false
	}
}

func resolveLocalCLIPath(home string) string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return ""
	}
	if installPath := launch.FindInstallation(exe, home); installPath != "" {
		cliExe := filepath.Join(filepath.Dir(installPath), "asterhand-cli.exe")
		if info, sErr := os.Stat(cliExe); sErr == nil && !info.IsDir() {
			return cliExe
		}
	}
	return exe
}

func fetchLivePrincipal(ctx context.Context, client Caller, callerID string) (Principal, string, string, principalFetchStatus) {
	if reloader, ok := client.(interface{ ReloadCallerToken() bool }); ok {
		reloader.ReloadCallerToken()
	}
	qCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := client.Call(qCtx, "capabilities", "", json.RawMessage("{}"))
	if err != nil || resp.Error != nil {
		status := principalTransientOffline
		var apiErr *Error
		if err != nil && errors.As(err, &apiErr) && isAuthDeniedCode(apiErr.Code) {
			status = principalAuthDenied
		} else if resp.Error != nil && isAuthDeniedCode(resp.Error.Code) {
			status = principalAuthDenied
		}
		if callerID == "" {
			return localPrincipal(), "", "", status
		}
		return Principal{ID: "harness:" + callerID, CallerID: callerID, Roles: nil, Toolsets: map[string]bool{}}, "", "", status
	}
	b, _ := json.Marshal(resp.Data)
	var capData struct {
		InstanceID     string          `json:"instance_id"`
		PolicyRevision string          `json:"policy_revision"`
		CLIPath        string          `json:"cli_path"`
		Toolsets       map[string]bool `json:"toolsets"`
		Roles          []string        `json:"roles"`
	}
	_ = json.Unmarshal(b, &capData)
	if callerID == "" {
		return localPrincipal(), capData.InstanceID, capData.CLIPath, principalOnline
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
	}, capData.InstanceID, capData.CLIPath, principalOnline
}

func mcpResult(opName string, response Response) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		StructuredContent: response,
		Content:           []mcp.Content{&mcp.TextContent{Text: formatMCPText(opName, response)}},
		IsError:           response.Error != nil,
	}
}
