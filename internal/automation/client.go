package automation

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Client struct {
	home     string
	callerID string
	Profile  Profile
	tokenMu  sync.RWMutex
	token    string
	http     *http.Client
}

func OpenClient(home string) (*Client, error) {
	abs, err := filepath.Abs(home)
	if err != nil {
		return nil, err
	}
	return openLocalClient(abs)
}

func OpenCallerClient(home, callerID string) (*Client, error) {
	abs, err := filepath.Abs(home)
	if err != nil {
		return nil, err
	}
	return openCallerClient(abs, callerID)
}

func (c *Client) CallerID() string { return c.callerID }
func (c *Client) LocalPrincipal() (Principal, error) { return localPrincipal(), nil }

func newTokenClient(profile Profile, secret string) (*Client, error) {
	tlsConfig, err := pinnedTLS(profile)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{MaxConnsPerHost: 16, MaxIdleConnsPerHost: 4, IdleConnTimeout: 30 * time.Second,
		DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			conn, err := dialLocal(ctx, profile.Pipe)
			if err != nil {
				return nil, err
			}
			secured := tls.Client(conn, tlsConfig)
			if err = secured.HandshakeContext(ctx); err != nil {
				conn.Close()
				return nil, err
			}
			return secured, nil
		}}
	return &Client{Profile: profile, token: secret, http: &http.Client{Transport: transport, Timeout: 40 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) Close() { c.http.CloseIdleConnections() }

func (c *Client) Call(ctx context.Context, operation, id string, arguments json.RawMessage) (Response, error) {
	if operation == "node_start" {
		if c.callerID != "" {
			if err := c.requireCallerToolset(ctx, ToolsetGeneral); err != nil {
				return Response{Version: APIVersion, Node: c.Profile.Node, ID: id, Error: PublicError(err)}, nil
			}
		}
		return c.startNode(ctx, id, arguments)
	}
	if operation == "operations_get" && c.home != "" && c.callerID == "" {
		if id != "" {
			return Response{}, Fail("invalid_parameters", "Read operations do not accept an operation identity")
		}
		if result, handled, err := c.startReceipt(arguments); handled {
			return result, err
		}
	}
	return c.remoteCall(ctx, operation, id, arguments)
}

func (c *Client) requireCallerToolset(ctx context.Context, toolset string) error {
	resp, err := c.remoteCall(ctx, "capabilities", "", json.RawMessage("{}"))
	if err != nil {
		return Fail("forbidden", "Managed caller cannot invoke local node_start without verified general toolset permission")
	}
	if resp.Error != nil {
		return resp.Error
	}
	b, _ := json.Marshal(resp.Data)
	var capData struct {
		Toolsets map[string]bool `json:"toolsets"`
	}
	if json.Unmarshal(b, &capData) != nil || !capData.Toolsets[toolset] {
		return Fail("toolset_disabled", "This toolset is disabled for the current caller")
	}
	return nil
}

func (c *Client) currentToken() string {
	c.tokenMu.RLock()
	defer c.tokenMu.RUnlock()
	return c.token
}

// ReloadCallerToken refreshes the managed caller bearer token from disk so a
// long-lived MCP subprocess recovers automatically after switch toggle or token
// rotation, and fails closed immediately when the token file is removed.
func (c *Client) ReloadCallerToken() bool {
	if c.callerID == "" || c.home == "" {
		return false
	}
	path := filepath.Join(c.home, "harness-callers", c.callerID+".token")
	next := ""
	if b, err := os.ReadFile(path); err == nil {
		s := strings.TrimSpace(string(b))
		if len(s) >= 32 && len(s) <= 256 {
			next = s
		}
	}
	c.tokenMu.Lock()
	changed := c.token != next
	c.token = next
	c.tokenMu.Unlock()
	return changed
}

func (c *Client) remoteCall(ctx context.Context, operation, id string, arguments json.RawMessage) (Response, error) {
	if c.callerID != "" && c.home != "" {
		c.ReloadCallerToken()
	}
	tok := c.currentToken()
	if c.callerID != "" && tok == "" {
		return Response{
			Version: APIVersion,
			Node:    c.Profile.Node,
			ID:      id,
			Error:   Fail("forbidden", "Managed caller credential is missing or revoked"),
		}, nil
	}
	request := Request{Version: APIVersion, Node: c.Profile.Node, Operation: operation, ID: id, Arguments: arguments}
	b, err := json.Marshal(request)
	if err != nil {
		return Response{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "https://asterhand.local/v1/call", bytes.NewReader(b))
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	response, err := c.http.Do(req)
	if err != nil {
		code := "node_unavailable"
		if id != "" {
			code = "result_unknown"
		}
		return Response{}, Fail(code, "The original node could not be reached; retain the same operation identity for reconciliation")
	}
	defer response.Body.Close()
	b, err = io.ReadAll(io.LimitReader(response.Body, MaxRequest+1))
	if err != nil || len(b) > MaxRequest {
		return Response{}, Fail("invalid_response", "The node response exceeded its budget")
	}
	var result Response
	if err = json.Unmarshal(b, &result); err != nil {
		return Response{}, Fail("invalid_response", "The node returned invalid JSON")
	}
	if response.StatusCode == http.StatusForbidden {
		return result, nil
	}
	if result.Version != APIVersion || result.Node != c.Profile.Node || result.ID != id || result.Instance == "" {
		return Response{}, Fail("invalid_response", "The node response did not match the request")
	}
	return result, nil
}

// FetchDefinitions pulls the running daemon's live tool definitions and schemas
// for the current authenticated principal so long-lived MCP processes hot-sync
// across node restarts and upgrades without dropping the stdio session.
func (c *Client) FetchDefinitions(ctx context.Context) (string, []Definition, error) {
	if c.callerID != "" && c.home != "" {
		c.ReloadCallerToken()
	}
	tok := c.currentToken()
	if c.callerID != "" && tok == "" {
		return "", nil, Fail("forbidden", "Managed caller credential is missing or revoked")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://asterhand.local/v1/definitions", nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", nil, Fail("node_unavailable", "The node could not be reached")
	}
	defer resp.Body.Close()
	const maxDefsBytes = 2 << 20
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxDefsBytes+1))
	if err != nil || len(b) > maxDefsBytes {
		return "", nil, Fail("invalid_response", "The node definitions response exceeded its budget")
	}
	if resp.StatusCode == http.StatusForbidden {
		return "", nil, Fail("forbidden", "Caller credential is not authorized")
	}
	if resp.StatusCode != http.StatusOK {
		return "", nil, Fail("invalid_response", "The node rejected the definitions request")
	}
	var payload struct {
		Version     string       `json:"api_version"`
		InstanceID  string       `json:"instance_id"`
		Definitions []Definition `json:"definitions"`
	}
	if err := json.Unmarshal(b, &payload); err != nil || payload.Version != APIVersion {
		return "", nil, Fail("invalid_response", "The node returned invalid definitions JSON")
	}
	return payload.InstanceID, payload.Definitions, nil
}
