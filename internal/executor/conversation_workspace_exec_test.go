//go:build windows

package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/conversation"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/workspace"
	"golang.org/x/sys/windows"
)

type nativeExecMockController struct {
	mu     sync.Mutex
	steps  []nativeToolStep
	inputs []json.RawMessage
}

func newNativeExecMockServer() (*nativeExecMockController, *httptest.Server) {
	ctrl := &nativeExecMockController{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body json.RawMessage
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		ctrl.mu.Lock()
		index := len(ctrl.inputs)
		ctrl.inputs = append(ctrl.inputs, body)
		var step *nativeToolStep
		if index < len(ctrl.steps) {
			s := ctrl.steps[index]
			step = &s
		}
		ctrl.mu.Unlock()

		delta := map[string]any{"role": "assistant", "content": "Fixture completed"}
		finish := "stop"
		if step != nil {
			args, _ := json.Marshal(step.Params)
			delta = map[string]any{
				"role": "assistant",
				"tool_calls": []any{map[string]any{
					"index":    0,
					"id":       fmt.Sprintf("exec-fixture-%d", index),
					"type":     "function",
					"function": map[string]any{"name": step.Name, "arguments": string(args)},
				}},
			}
			finish = "tool_calls"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, choice := range []any{
			map[string]any{"index": 0, "delta": delta, "finish_reason": nil},
			map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish},
		} {
			data, _ := json.Marshal(map[string]any{"id": "fixture", "object": "chat.completion.chunk", "model": "chat", "choices": []any{choice}})
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	return ctrl, server
}

func (m *nativeExecMockController) reset(steps []nativeToolStep) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.steps = steps
	m.inputs = nil
}

func (m *nativeExecMockController) snapshotInputs() []json.RawMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]json.RawMessage, len(m.inputs))
	copy(out, m.inputs)
	return out
}

func assertOpenClawTokenPlaceholder(t *testing.T, configPath string) {
	t.Helper()
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Plugins struct {
			Entries map[string]struct {
				Config struct {
					Endpoint string `json:"endpoint"`
					Token    string `json:"token"`
				} `json:"config"`
			} `json:"entries"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	entry, ok := parsed.Plugins.Entries[workspacePlugin]
	if !ok {
		t.Fatalf("missing %s entry in openclaw.json", workspacePlugin)
	}
	if entry.Config.Token != "${ASTERHAND_WORKSPACE_TOKEN}" {
		t.Fatalf("openclaw.json workspace token = %q, want %q", entry.Config.Token, "${ASTERHAND_WORKSPACE_TOKEN}")
	}
	if !strings.HasPrefix(entry.Config.Endpoint, "http://127.0.0.1:") {
		t.Fatalf("unexpected workspace endpoint = %q", entry.Config.Endpoint)
	}
}

func waitForGrandchildPIDHandle(t *testing.T, pidFile string) windows.Handle {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(pidFile)
		if err == nil {
			pid, convErr := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 32)
			if convErr == nil && pid > 0 {
				h, openErr := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
				if openErr != nil {
					t.Fatalf("OpenProcess(%d): %v", pid, openErr)
				}
				return h
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("grandchild pid file %s did not appear within deadline", pidFile)
	return 0
}

func TestNativeWorkspaceExecAndStop(t *testing.T) {
	if os.Getenv("ASTERHAND_CHAT_RUNTIME") != "1" {
		t.Skip("requires explicit pinned Windows native runtime")
	}
	node, entry := os.Getenv("ASTERHAND_CHAT_NODE"), os.Getenv("ASTERHAND_CHAT_ENTRY")
	if node == "" || entry == "" {
		t.Fatal("locked runtime paths required")
	}
	root := t.TempDir()
	ctrl, server := newNativeExecMockServer()
	defer server.Close()

	process, files := startNativeWorkspaceFixture(t, root, node, entry, server.URL)
	defer process.close()
	assertOpenClawTokenPlaceholder(t, filepath.Join(root, "openclaw.json"))

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	binding := conversation.Binding{Runtime: "openclaw", Generation: strings.Repeat("b", 32), Key: model.ID()}
	if err := process.call(ctx, chatCommand{Action: "create", Binding: binding}, &binding); err != nil {
		t.Fatal(err)
	}

	verifyExecTurnRoundTrip(t, ctx, process, files, binding, ctrl, root)
	verifyMidRunStopKillsGrandchild(t, ctx, process, files, binding, ctrl)
	verifyFollowUpTurnAfterStop(t, ctx, process, files, binding, ctrl)

	if err := process.close(); err != nil {
		t.Fatal(err)
	}
	if err := files.RuntimeStopped(); err != nil {
		t.Fatal(err)
	}
}

func verifyExecTurnRoundTrip(
	t *testing.T,
	ctx context.Context,
	process *chatProcess,
	files *workspace.Service,
	binding conversation.Binding,
	ctrl *nativeExecMockController,
	root string,
) {
	t.Helper()
	checkScript := `import fs from 'node:fs';
const leakedKeys = Object.keys(process.env).filter(k =>
  /^(ASTERHAND_|OPENCLAW_|DEEPSEEK_)/.test(k) || process.env[k] === 'FIXTURE_KEY'
);
let outsideWriteBlocked = false;
try {
  fs.writeFileSync('../outside-probe.txt', 'nope');
} catch (err) {
  outsideWriteBlocked = Boolean(err && err.code);
}
fs.writeFileSync('result.json', JSON.stringify({leakedKeys, outsideWriteBlocked, ok: true}));
console.log('done');
`
	ctrl.reset([]nativeToolStep{
		{"write_file", map[string]any{"path": "scripts/check.mjs", "content": checkScript}},
		{"exec_command", map[string]any{"command": "node scripts/check.mjs"}},
		{"read_file", map[string]any{"path": "result.json"}},
	})
	run := model.ID()
	if err := files.AcquireTurnScope(workspace.Scope{Turn: run, Conversation: binding.Key, Policy: "fixture-policy"}); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UnixMilli()
	if err := process.call(ctx, chatCommand{Action: "submit", Binding: binding, Run: run, Text: "Write, execute, and read check"}, nil); err != nil {
		t.Fatal(err)
	}
	_ = waitNativeTurn(t, ctx, process, binding, run, started)
	if err := files.ReleaseTurn(run, true); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(files.Root(), "result.json"))
	if err != nil {
		t.Fatalf("read result.json: %v", err)
	}
	var out struct {
		LeakedKeys          []string `json:"leakedKeys"`
		OutsideWriteBlocked bool     `json:"outsideWriteBlocked"`
		OK                  bool     `json:"ok"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || len(out.LeakedKeys) != 0 || !out.OutsideWriteBlocked {
		t.Fatalf("unexpected sandbox check output: %+v", out)
	}
	if _, err := os.Stat(filepath.Join(root, "outside-probe.txt")); !os.IsNotExist(err) {
		t.Fatalf("outside-probe.txt should not exist, err=%v", err)
	}
	inputs := ctrl.snapshotInputs()
	if len(inputs) != 4 || !strings.Contains(string(inputs[3]), "outsideWriteBlocked") {
		t.Fatalf("expected 4 model turns ending with result.json readback, got %d", len(inputs))
	}
}

func verifyMidRunStopKillsGrandchild(
	t *testing.T,
	ctx context.Context,
	process *chatProcess,
	files *workspace.Service,
	binding conversation.Binding,
	ctrl *nativeExecMockController,
) {
	t.Helper()
	spawnScript := `import {spawn} from 'node:child_process';
import fs from 'node:fs';
const child = spawn(process.execPath, ['-e', 'setInterval(()=>{},1000)'], {stdio: 'ignore'});
fs.writeFileSync('grandchild.tmp', String(child.pid));
fs.renameSync('grandchild.tmp', 'grandchild.pid');
setInterval(() => {}, 1000);
`
	ctrl.reset([]nativeToolStep{
		{"write_file", map[string]any{"path": "scripts/spawn.mjs", "content": spawnScript}},
		{"exec_command", map[string]any{"command": "node scripts/spawn.mjs", "yieldMs": 25000, "timeoutSec": 60}},
	})
	run := model.ID()
	if err := files.AcquireTurnScope(workspace.Scope{Turn: run, Conversation: binding.Key, Policy: "fixture-policy"}); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UnixMilli()
	if err := process.call(ctx, chatCommand{Action: "submit", Binding: binding, Run: run, Text: "Start long grandchild process"}, nil); err != nil {
		t.Fatal(err)
	}
	h := waitForGrandchildPIDHandle(t, filepath.Join(files.Root(), "grandchild.pid"))
	defer windows.CloseHandle(h)

	if w, err := windows.WaitForSingleObject(h, 0); w != uint32(windows.WAIT_TIMEOUT) || err != nil {
		t.Fatalf("expected grandchild process alive before Stop, wait=%d err=%v", w, err)
	}
	files.CancelTurn(run)
	if err := process.call(ctx, chatCommand{Action: "cancel", Binding: binding, Run: run}, nil); err != nil {
		t.Fatal(err)
	}
	_ = waitNativeTurn(t, ctx, process, binding, run, started)
	if err := files.ReleaseTurn(run, true); err != nil {
		t.Fatalf("ReleaseTurn after Stop: %v", err)
	}
	if w, err := windows.WaitForSingleObject(h, 3000); w != windows.WAIT_OBJECT_0 || err != nil {
		t.Fatalf("expected grandchild process terminated within 3s after Stop, wait=%d err=%v", w, err)
	}
	if st := files.Status(); !st.Ready || st.Busy || st.Reason != "" {
		t.Fatalf("expected clean ready workspace status after Stop, got %+v", st)
	}
}

func verifyFollowUpTurnAfterStop(
	t *testing.T,
	ctx context.Context,
	process *chatProcess,
	files *workspace.Service,
	binding conversation.Binding,
	ctrl *nativeExecMockController,
) {
	t.Helper()
	recoverScript := "import fs from 'node:fs';\nfs.writeFileSync('after-stop.txt', 'recovered');\n"
	ctrl.reset([]nativeToolStep{
		{"write_file", map[string]any{"path": "scripts/recover.mjs", "content": recoverScript}},
		{"exec_command", map[string]any{"command": "node scripts/recover.mjs"}},
	})
	run := model.ID()
	if err := files.AcquireTurnScope(workspace.Scope{Turn: run, Conversation: binding.Key, Policy: "fixture-policy"}); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UnixMilli()
	if err := process.call(ctx, chatCommand{Action: "submit", Binding: binding, Run: run, Text: "Verify recovery turn"}, nil); err != nil {
		t.Fatal(err)
	}
	_ = waitNativeTurn(t, ctx, process, binding, run, started)
	if err := files.ReleaseTurn(run, true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(files.Root(), "after-stop.txt"))
	if err != nil || string(data) != "recovered" {
		t.Fatalf("after-stop.txt = %q, err=%v", data, err)
	}
}
