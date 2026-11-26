//go:build windows

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
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/operations"
)

func TestManagedCallerInstalledNodeStartReceiptRecoveryAndIsolation(t *testing.T) {
	installRoot := t.TempDir()
	home := t.TempDir()
	exePath := filepath.Join(installRoot, "versions", "0.19.1", "asterhand.exe")
	if err := os.MkdirAll(filepath.Dir(exePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exePath, []byte("stub"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "harness-callers"), 0700); err != nil {
		t.Fatal(err)
	}
	installPath := filepath.Join(installRoot, "installation.json")
	writeTestJSON(t, installPath, map[string]any{
		"schema":     1,
		"executable": exePath,
		"data":       home,
	})

	const nodeID = "node-installed-test"
	writeTestJSON(t, filepath.Join(home, "config.json"), map[string]any{"node_id": nodeID})
	if err := os.WriteFile(filepath.Join(home, "web-login.token"), []byte("test-web-login-token-0123456789ab"), 0600); err != nil {
		t.Fatal(err)
	}

	// Healthy node runtime endpoint so launch.StartOperation completes via real
	// installation lock + health check without spawning an external binary.
	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"node_id": nodeID, "pid": os.Getpid()})
	}))
	defer healthSrv.Close()
	writeTestJSON(t, filepath.Join(home, "runtime.json"), map[string]any{
		"address": healthSrv.Listener.Addr().String(),
		"pid":     os.Getpid(),
	})

	tokA := strings.Repeat("a", 48)
	tokB := strings.Repeat("b", 48)
	if err := os.WriteFile(filepath.Join(home, "harness-callers", "antigravity.token"), []byte(tokA), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "harness-callers", "claude-code.token"), []byte(tokB), 0600); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	generalEnabledA := true
	revokeOnNextCapA := false
	rolesOverrideA := []string{"read", "edit", "execute"}
	omitRolesA := false
	var capCalls atomic.Int32
	var remoteOpsGetCalls atomic.Int32

	daemonSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		auth := r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		switch req.Operation {
		case "capabilities":
			capCalls.Add(1)
			caller := ""
			gen := true
			roles := []string{"read", "edit", "execute"}
			omitRoles := false
			switch auth {
			case "Bearer " + tokA:
				caller = "antigravity"
				mu.Lock()
				gen = generalEnabledA
				if revokeOnNextCapA {
					revokeOnNextCapA = false
					generalEnabledA = false
				}
				roles = append([]string(nil), rolesOverrideA...)
				omitRoles = omitRolesA
				mu.Unlock()
			case "Bearer " + tokB:
				caller = "claude-code"
			default:
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(Response{Version: APIVersion, Node: nodeID, Instance: "inst_1", Error: Fail("forbidden", "bad token")})
				return
			}
			toolsets := map[string]bool{ToolsetCore: true, ToolsetCollab: true}
			if gen {
				toolsets[ToolsetGeneral] = true
			}
			capBody := map[string]any{
				"caller_id": caller,
				"toolsets":  toolsets,
			}
			if !omitRoles {
				capBody["roles"] = roles
			}
			_ = json.NewEncoder(w).Encode(Response{
				Version:  APIVersion,
				Node:     nodeID,
				Instance: "inst_1",
				Data:     capBody,
			})
		case "operations_get":
			remoteOpsGetCalls.Add(1)
			_ = json.NewEncoder(w).Encode(Response{
				Version:  APIVersion,
				Node:     nodeID,
				Instance: "inst_1",
				Error:    Fail("operation_not_found", "Not in daemon journal"),
			})
		default:
			_ = json.NewEncoder(w).Encode(Response{
				Version:  APIVersion,
				Node:     nodeID,
				Instance: "inst_1",
				Error:    Fail("forbidden", "unexpected op"),
			})
		}
	}))
	defer daemonSrv.Close()

	makeClient := func(callerID, tok string) *Client {
		baseTransport := daemonSrv.Client().Transport
		return &Client{
			home:     home,
			callerID: callerID,
			token:    tok,
			Profile: Profile{
				Version:      APIVersion,
				Node:         nodeID,
				Actor:        localActor,
				Installation: installPath,
			},
			http: &http.Client{
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					cloned := req.Clone(req.Context())
					cloned.URL.Scheme = "http"
					cloned.URL.Host = daemonSrv.Listener.Addr().String()
					return baseTransport.RoundTrip(cloned)
				}),
			},
		}
	}

	clientA := makeClient("antigravity", tokA)
	clientB := makeClient("claude-code", tokB)
	clientLocal := makeClient("", strings.Repeat("c", 64))

	// 1. Caller A runs installed-node node_start; discard first response to simulate lost reply.
	startID := operations.NewIDFor("node_start", time.Now())
	startResp, err := clientA.Call(context.Background(), "node_start", startID, json.RawMessage(`{}`))
	if err != nil || startResp.Error != nil || startResp.Receipt == nil || !startResp.Receipt.Terminal() {
		t.Fatalf("caller A installed node_start failed: resp=%+v err=%v", startResp, err)
	}
	if startResp.Receipt.Actor != "harness:antigravity" {
		t.Fatalf("expected launcher receipt actor harness:antigravity, got %q", startResp.Receipt.Actor)
	}

	// 2. Caller A recovers its own start receipt via operations_get without hitting daemon journal.
	args, _ := json.Marshal(map[string]string{"operation_id": startID})
	recA, err := clientA.Call(context.Background(), "operations_get", "", args)
	if err != nil || recA.Error != nil {
		t.Fatalf("caller A operations_get failed: resp=%+v err=%v", recA, err)
	}
	gotReceipt, ok := recA.Data.(operations.Receipt)
	if !ok || gotReceipt.ID != startID || gotReceipt.Actor != "harness:antigravity" || gotReceipt.State != "completed" || !gotReceipt.Accepted {
		t.Fatalf("unexpected recovered receipt for caller A: %#v", recA.Data)
	}

	// 3. Another managed caller (claude-code) MUST NOT claim caller A's start receipt.
	recB, err := clientB.Call(context.Background(), "operations_get", "", args)
	if err != nil {
		t.Fatalf("caller B unexpected transport error: %v", err)
	}
	if recB.Error == nil || recB.Error.Code != "operation_not_found" {
		t.Fatalf("caller B must not claim caller A's start receipt, got %+v", recB)
	}

	// 4. Local user actor MUST NOT claim managed caller A's start receipt.
	recLocal, err := clientLocal.Call(context.Background(), "operations_get", "", args)
	if err != nil {
		t.Fatalf("local client unexpected transport error: %v", err)
	}
	if recLocal.Error == nil || recLocal.Error.Code != "operation_not_found" {
		t.Fatalf("local user must not claim managed caller's start receipt, got %+v", recLocal)
	}

	// 5. Disabling general toolset for caller A rejects recovering even its own start receipt.
	mu.Lock()
	generalEnabledA = false
	mu.Unlock()
	recDenied, err := clientA.Call(context.Background(), "operations_get", "", args)
	if err != nil {
		t.Fatalf("unexpected transport error when general disabled: %v", err)
	}
	if recDenied.Error == nil || recDenied.Error.Code != "toolset_disabled" {
		t.Fatalf("expected toolset_disabled when general toolset disabled, got %+v", recDenied)
	}

	// 6. Ordinary (non-start:) operations_get bypasses launcher check with ZERO extra capabilities call.
	capBefore := capCalls.Load()
	remoteBefore := remoteOpsGetCalls.Load()
	ordinaryArgs, _ := json.Marshal(map[string]string{"operation_id": operations.NewID(time.Now())})
	_, err = clientA.Call(context.Background(), "operations_get", "", ordinaryArgs)
	if err != nil {
		t.Fatalf("ordinary operations_get transport err: %v", err)
	}
	if capCalls.Load() != capBefore {
		t.Fatalf("ordinary operations_get triggered %d extra capabilities call(s)", capCalls.Load()-capBefore)
	}
	if remoteOpsGetCalls.Load() != remoteBefore+1 {
		t.Fatalf("expected ordinary operations_get routed directly to daemon")
	}

	// 7. Post-read revocation re-check: if general is revoked AFTER pre-read
	// check and BEFORE returning the local receipt, operations_get MUST deny
	// and never leak the receipt.
	mu.Lock()
	generalEnabledA = true
	revokeOnNextCapA = true // 1st check succeeds, flips generalEnabledA=false for 2nd (post-read) check.
	mu.Unlock()
	recMidRevoke, err := clientA.Call(context.Background(), "operations_get", "", args)
	if err != nil {
		t.Fatalf("unexpected transport err on mid-read revocation: %v", err)
	}
	if recMidRevoke.Data != nil || recMidRevoke.Error == nil || recMidRevoke.Error.Code != "toolset_disabled" {
		t.Fatalf("expected mid-read revocation to block start receipt with toolset_disabled and nil Data, got %+v", recMidRevoke)
	}

	// 8. Missing/null roles in capabilities must fail with invalid_response (never skip role check);
	// explicit roles lacking required permission must fail with forbidden.
	mu.Lock()
	generalEnabledA = true
	omitRolesA = true
	mu.Unlock()
	recMissingRoles, err := clientA.Call(context.Background(), "operations_get", "", args)
	if err == nil || PublicError(err).Code != "invalid_response" || recMissingRoles.Data != nil {
		t.Fatalf("expected invalid_response when capabilities omits roles, got resp=%+v err=%v", recMissingRoles, err)
	}
	mu.Lock()
	omitRolesA = false
	rolesOverrideA = []string{"execute"} // lacks "read"
	mu.Unlock()
	recNoReadRole, err := clientA.Call(context.Background(), "operations_get", "", args)
	if err != nil || recNoReadRole.Data != nil || recNoReadRole.Error == nil || recNoReadRole.Error.Code != "forbidden" {
		t.Fatalf("expected forbidden when roles lacks read, got resp=%+v err=%v", recNoReadRole, err)
	}

	// 9. Disconnected daemon preserves node_unavailable (CLI exit 7) on BOTH
	// ordinary operations_get and start: operations_get, never returning receipt
	// or misclassifying transport failure as forbidden.
	mu.Lock()
	rolesOverrideA = []string{"read", "edit", "execute"}
	mu.Unlock()
	daemonSrv.Close()
	_, ordinaryOfflineErr := clientA.Call(context.Background(), "operations_get", "", ordinaryArgs)
	recStartOffline, startOfflineErr := clientA.Call(context.Background(), "operations_get", "", args)
	if ordinaryOfflineErr == nil || PublicError(ordinaryOfflineErr).Code != "node_unavailable" || ErrorExit(PublicError(ordinaryOfflineErr).Code) != 7 {
		t.Fatalf("expected ordinary offline operations_get -> node_unavailable (7), got %v", ordinaryOfflineErr)
	}
	if recStartOffline.Data != nil || startOfflineErr == nil || PublicError(startOfflineErr).Code != "node_unavailable" || ErrorExit(PublicError(startOfflineErr).Code) != 7 {
		t.Fatalf("expected start offline operations_get -> node_unavailable (7) with nil Data, got resp=%+v err=%v", recStartOffline, startOfflineErr)
	}
}

func writeTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestManagedCallerInstalledNodeStartPreDispatchDisconnectAllowsRetryAndPostDispatchReconciles(t *testing.T) {
	installRoot := t.TempDir()
	home := t.TempDir()
	exePath := filepath.Join(installRoot, "versions", "0.19.1", "asterhand.exe")
	if err := os.MkdirAll(filepath.Dir(exePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exePath, []byte("stub"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "harness-callers"), 0700); err != nil {
		t.Fatal(err)
	}
	installPath := filepath.Join(installRoot, "installation.json")
	writeTestJSON(t, installPath, map[string]any{"schema": 1, "executable": exePath, "data": home})

	const nodeID = "node-start-retry-test"
	writeTestJSON(t, filepath.Join(home, "config.json"), map[string]any{"node_id": nodeID})
	if err := os.WriteFile(filepath.Join(home, "web-login.token"), []byte("test-web-login-token-0123456789ab"), 0600); err != nil {
		t.Fatal(err)
	}
	tokA := strings.Repeat("d", 48)
	if err := os.WriteFile(filepath.Join(home, "harness-callers", "antigravity.token"), []byte(tokA), 0600); err != nil {
		t.Fatal(err)
	}

	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"node_id": nodeID, "pid": os.Getpid()})
	}))
	defer healthSrv.Close()
	runtimePath := filepath.Join(home, "runtime.json")
	writeTestJSON(t, runtimePath, map[string]any{"address": healthSrv.Listener.Addr().String(), "pid": os.Getpid()})

	var failAtCall atomic.Int32
	var revokeAtCall atomic.Int32
	var capCount atomic.Int32

	daemonSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Operation {
		case "capabilities":
			toolsets := map[string]bool{ToolsetCore: true, ToolsetGeneral: true}
			if target := revokeAtCall.Load(); target > 0 && capCount.Load() == target {
				delete(toolsets, ToolsetGeneral)
			}
			_ = json.NewEncoder(w).Encode(Response{
				Version:  APIVersion,
				Node:     nodeID,
				Instance: "inst_1",
				Data: map[string]any{
					"caller_id": "antigravity",
					"roles":     []string{"read", "edit", "execute"},
					"toolsets":  toolsets,
				},
			})
		case "operations_get":
			_ = json.NewEncoder(w).Encode(Response{
				Version:  APIVersion,
				Node:     nodeID,
				Instance: "inst_1",
				Error:    Fail("operation_not_found", "Not in daemon journal"),
			})
		}
	}))
	defer daemonSrv.Close()

	baseTransport := daemonSrv.Client().Transport
	clientA := &Client{
		home:     home,
		callerID: "antigravity",
		token:    tokA,
		Profile: Profile{
			Version:      APIVersion,
			Node:         nodeID,
			Actor:        localActor,
			Installation: installPath,
		},
		http: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				callNo := capCount.Add(1)
				if target := failAtCall.Load(); target > 0 && callNo == target {
					return nil, context.DeadlineExceeded
				}
				cloned := req.Clone(req.Context())
				cloned.URL.Scheme = "http"
				cloned.URL.Host = daemonSrv.Listener.Addr().String()
				return baseTransport.RoundTrip(cloned)
			}),
		},
	}

	// 1. Pre-dispatch disconnects at check #1, check #2, AND check #3 (right before launchLocked)
	// MUST all return node_unavailable with NO prepared receipt left on disk, and retrying the
	// exact same start ID after reconnect MUST complete cleanly.
	for _, checkPoint := range []int32{1, 2, 3} {
		startID := operations.NewIDFor("node_start", time.Now())
		capCount.Store(0)
		failAtCall.Store(checkPoint)
		resp, err := clientA.Call(context.Background(), "node_start", startID, json.RawMessage(`{}`))
		if resp.Receipt != nil {
			t.Fatalf("check #%d: expected no attached receipt on pre-dispatch disconnect, got %+v", checkPoint, resp)
		}
		if err == nil || PublicError(err).Code != "node_unavailable" || ErrorExit(PublicError(err).Code) != 7 {
			t.Fatalf("check #%d: expected node_unavailable (CLI 7), got resp=%+v err=%v", checkPoint, resp, err)
		}

		// After reconnect, no stray prepared receipt exists, and retrying with the SAME ID succeeds.
		failAtCall.Store(0)
		getArgs, _ := json.Marshal(map[string]string{"operation_id": startID})
		gotBeforeRetry, err := clientA.Call(context.Background(), "operations_get", "", getArgs)
		if err != nil || gotBeforeRetry.Error == nil || gotBeforeRetry.Error.Code != "operation_not_found" {
			t.Fatalf("check #%d: expected no stray prepared receipt on disk, got resp=%+v err=%v", checkPoint, gotBeforeRetry, err)
		}
		retryResp, err := clientA.Call(context.Background(), "node_start", startID, json.RawMessage(`{}`))
		if err != nil || retryResp.Error != nil || retryResp.Receipt == nil || retryResp.Receipt.State != "completed" {
			t.Fatalf("check #%d: expected same-ID retry to complete cleanly, got resp=%+v err=%v", checkPoint, retryResp, err)
		}
	}

	// 1b. Pre-dispatch revocation at check #3 (right before launchLocked) MUST also
	// roll back the prepared receipt, return toolset_disabled with no receipt, and
	// allow same-ID retry after permission is restored.
	revokedID := operations.NewIDFor("node_start", time.Now())
	capCount.Store(0)
	revokeAtCall.Store(3)
	revResp, err := clientA.Call(context.Background(), "node_start", revokedID, json.RawMessage(`{}`))
	if err != nil || revResp.Receipt != nil || revResp.Error == nil || revResp.Error.Code != "toolset_disabled" {
		t.Fatalf("check #3 revoke: expected toolset_disabled with nil receipt, got resp=%+v err=%v", revResp, err)
	}
	revokeAtCall.Store(0)
	revGetArgs, _ := json.Marshal(map[string]string{"operation_id": revokedID})
	revBeforeRetry, err := clientA.Call(context.Background(), "operations_get", "", revGetArgs)
	if err != nil || revBeforeRetry.Error == nil || revBeforeRetry.Error.Code != "operation_not_found" {
		t.Fatalf("check #3 revoke: expected no stray prepared receipt on disk, got resp=%+v err=%v", revBeforeRetry, err)
	}
	revRetry, err := clientA.Call(context.Background(), "node_start", revokedID, json.RawMessage(`{}`))
	if err != nil || revRetry.Error != nil || revRetry.Receipt == nil || revRetry.Receipt.State != "completed" {
		t.Fatalf("check #3 revoke: expected same-ID retry after permission restore to complete, got resp=%+v err=%v", revRetry, err)
	}

	// 2. Post-dispatch failure (after all pre-launch checks pass and launchLocked executes
	// against an offline non-runnable stub binary) MUST retain the prepared receipt and
	// report result_unknown, then reconcile to completed once the node becomes healthy.
	if err := os.Remove(runtimePath); err != nil {
		t.Fatal(err)
	}
	postID := operations.NewIDFor("node_start", time.Now())
	postResp, err := clientA.Call(context.Background(), "node_start", postID, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("post-dispatch launch failure unexpected transport err: %v", err)
	}
	if postResp.Error == nil || postResp.Error.Code != "result_unknown" || postResp.Receipt == nil || postResp.Receipt.State != "prepared" {
		t.Fatalf("expected post-dispatch failure -> result_unknown + prepared receipt, got %+v", postResp)
	}

	// Once the node runtime becomes healthy, retrying with the same ID reconciles to completed.
	writeTestJSON(t, runtimePath, map[string]any{"address": healthSrv.Listener.Addr().String(), "pid": os.Getpid()})
	reconciled, err := clientA.Call(context.Background(), "node_start", postID, json.RawMessage(`{}`))
	if err != nil || reconciled.Error != nil || reconciled.Receipt == nil || reconciled.Receipt.State != "completed" {
		t.Fatalf("expected post-dispatch same-ID retry to reconcile to completed, got resp=%+v err=%v", reconciled, err)
	}
}
