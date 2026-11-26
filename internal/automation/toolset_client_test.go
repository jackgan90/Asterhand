package automation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRestrictedCallerNodeStartRequiresGeneralToolset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Response{
			Version:  APIVersion,
			Node:     "node-test",
			Instance: "inst_1",
			Data: map[string]any{
				"caller_id": "antigravity",
				"roles":     []string{"read", "edit", "execute"},
				"toolsets":  map[string]bool{ToolsetCore: true, ToolsetCollab: true},
			},
		})
	}))
	defer srv.Close()

	baseTransport := srv.Client().Transport
	c := &Client{
		callerID: "antigravity",
		Profile:  Profile{Node: "node-test"},
		token:    strings.Repeat("a", 40),
		http: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				cloned := req.Clone(req.Context())
				cloned.URL.Scheme = "http"
				cloned.URL.Host = srv.Listener.Addr().String()
				return baseTransport.RoundTrip(cloned)
			}),
		},
	}

	resp, err := c.Call(context.Background(), "node_start", "op_start_1", json.RawMessage("{}"))
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != "toolset_disabled" || ErrorExit(resp.Error.Code) != 3 {
		t.Fatalf("expected node_start rejected with toolset_disabled (exit 3) for collab-only caller, got %+v", resp)
	}
}

func TestManagedCallerTokenReloadAndRevocation(t *testing.T) {
	home := t.TempDir()
	tokDir := filepath.Join(home, "harness-callers")
	if err := os.MkdirAll(tokDir, 0700); err != nil {
		t.Fatal(err)
	}
	tokPath := filepath.Join(tokDir, "antigravity.token")

	var mu sync.Mutex
	activeToken := strings.Repeat("a", 48)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		expected := "Bearer " + activeToken
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != expected {
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(Response{
				Version: APIVersion,
				Node:    "node-test",
				Error:   Fail("forbidden", "bad token"),
			})
			return
		}
		_ = json.NewEncoder(w).Encode(Response{
			Version:  APIVersion,
			Node:     "node-test",
			Instance: "inst_1",
			Data:     map[string]any{"caller_id": "antigravity", "toolsets": map[string]bool{ToolsetCollab: true}},
		})
	}))
	defer srv.Close()

	baseTransport := srv.Client().Transport
	c := &Client{
		home:     home,
		callerID: "antigravity",
		Profile:  Profile{Node: "node-test"},
		http: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				cloned := req.Clone(req.Context())
				cloned.URL.Scheme = "http"
				cloned.URL.Host = srv.Listener.Addr().String()
				return baseTransport.RoundTrip(cloned)
			}),
		},
	}

	// 1. Missing token file -> fails closed with forbidden.
	if resp, err := c.Call(context.Background(), "capabilities", "", json.RawMessage("{}")); err != nil || resp.Error == nil || resp.Error.Code != "forbidden" {
		t.Fatalf("expected forbidden when token file missing, got resp=%+v err=%v", resp, err)
	}

	// 2. Write token A -> automatically picked up on next call.
	if err := os.WriteFile(tokPath, []byte(activeToken+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if resp, err := c.Call(context.Background(), "capabilities", "", json.RawMessage("{}")); err != nil || resp.Error != nil {
		t.Fatalf("expected success after token A written, got resp=%+v err=%v", resp, err)
	}

	// 3. Rotate to token B -> automatically picked up without restarting client!
	tokB := strings.Repeat("b", 48)
	mu.Lock()
	activeToken = tokB
	mu.Unlock()
	if err := os.WriteFile(tokPath, []byte(tokB+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if resp, err := c.Call(context.Background(), "capabilities", "", json.RawMessage("{}")); err != nil || resp.Error != nil {
		t.Fatalf("expected success after token rotation to B, got resp=%+v err=%v", resp, err)
	}

	// 4. Delete token file (revoked) -> fails closed immediately!
	if err := os.Remove(tokPath); err != nil {
		t.Fatal(err)
	}
	if resp, err := c.Call(context.Background(), "capabilities", "", json.RawMessage("{}")); err != nil || resp.Error == nil || resp.Error.Code != "forbidden" {
		t.Fatalf("expected forbidden after token deleted, got resp=%+v err=%v", resp, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
