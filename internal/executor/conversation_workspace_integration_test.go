package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/conversation"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/workspace"
)

type nativeToolStep struct {
	Name   string
	Params any
}

func TestNativeWorkspaceTools(t *testing.T) {
	if os.Getenv("ASTERHAND_CHAT_RUNTIME") != "1" {
		t.Skip("requires explicit pinned Windows native runtime")
	}
	node, entry := os.Getenv("ASTERHAND_CHAT_NODE"), os.Getenv("ASTERHAND_CHAT_ENTRY")
	if node == "" || entry == "" {
		t.Fatal("locked runtime paths required")
	}
	root := t.TempDir()
	if evidence := os.Getenv("ASTERHAND_CHAT_EVIDENCE"); evidence != "" {
		var err error
		root, err = os.MkdirTemp(evidence, "native-tools-")
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("fixture: %s", root)
	}
	prefix := strings.Repeat("padding line\n", 300)
	steps := []nativeToolStep{
		{"write_file", map[string]any{"path": "target.txt", "content": prefix + "old\n"}},
		{"read_file", map[string]any{"path": "target.txt"}},
		{"edit_file", map[string]any{"path": "target.txt", "edits": []any{map[string]any{"oldText": "old", "newText": "new"}}}},
		{"apply_file_patch", map[string]any{"input": "*** Begin Patch\n*** Update File: target.txt\n*** Move to: moved.txt\n@@\n-new\n+moved\n*** Add File: second.txt\n+second\n*** End Patch"}},
		{"search_files", map[string]any{"query": "moved", "content": true}},
		{"write_file", map[string]any{"path": "memory/facts.md", "content": "# Facts\nAsterhand fixture favorite instrument is a celesta.\n"}},
		{"memory_search", map[string]any{"query": "celesta"}},
		{"memory_get", map[string]any{"path": "memory/facts.md"}},
		{"progress_card", map[string]any{"plan": []any{map[string]any{"step": "Prepare files", "status": "completed"}}}},
		{"apply_file_patch", map[string]any{"input": "*** Begin Patch\n*** Delete File: second.txt\n*** End Patch"}},
	}
	var mu sync.Mutex
	var schemas [][]string
	var inputs []json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body json.RawMessage
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		var parsed struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		_ = json.Unmarshal(body, &parsed)
		names := []string{}
		for _, tool := range parsed.Tools {
			names = append(names, tool.Function.Name)
		}
		mu.Lock()
		index := len(inputs)
		inputs = append(inputs, body)
		schemas = append(schemas, names)
		mu.Unlock()
		delta := map[string]any{"role": "assistant", "content": "Fixture completed"}
		finish := "stop"
		if index < len(steps) {
			step := steps[index]
			args, _ := json.Marshal(step.Params)
			delta = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("fixture-%d", index), "type": "function", "function": map[string]any{"name": step.Name, "arguments": string(args)}}}}
			finish = "tool_calls"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, choice := range []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}, map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}} {
			data, _ := json.Marshal(map[string]any{"id": "fixture", "object": "chat.completion.chunk", "model": "chat", "choices": []any{choice}})
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	process, files := startNativeWorkspaceFixture(t, root, node, entry, server.URL)
	defer process.close()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	binding := conversation.Binding{Runtime: "openclaw", Generation: strings.Repeat("a", 32), Key: model.ID()}
	if err := process.call(ctx, chatCommand{Action: "create", Binding: binding}, &binding); err != nil {
		t.Fatal(err)
	}
	run := model.ID()
	scope := workspace.Scope{Turn: run, Conversation: binding.Key, Policy: "fixture-policy"}
	if err := files.AcquireTurnScope(scope); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UnixMilli()
	if err := process.call(ctx, chatCommand{Action: "submit", Binding: binding, Run: run, Text: "Run the local file fixture"}, nil); err != nil {
		t.Fatal(err)
	}
	observation := waitNativeTurn(t, ctx, process, binding, run, started)
	mu.Lock()
	report, _ := json.MarshalIndent(map[string]any{"schemas": schemas, "inputs": inputs, "observation": observation}, "", "  ")
	mu.Unlock()
	if err := os.WriteFile(filepath.Join(root, "tool-report.json"), report, 0600); err != nil {
		t.Fatal(err)
	}
	if len(inputs) != len(steps)+1 {
		t.Fatalf("model calls %d; report %s", len(inputs), filepath.Join(root, "tool-report.json"))
	}
	for _, names := range schemas {
		for _, name := range nativeWorkspaceTools() {
			if !slices.Contains(names, name) {
				t.Errorf("missing tool %s: %v", name, names)
			}
		}
		for _, name := range names {
			if !slices.Contains(nativeWorkspaceTools(), name) {
				t.Errorf("unexpected tool %s", name)
			}
		}
	}
	data, err := os.ReadFile(filepath.Join(files.Root(), "moved.txt"))
	if err != nil || string(data) != prefix+"moved\n" {
		t.Fatalf("native move: %q %v", data, err)
	}
	if _, err = os.Stat(filepath.Join(files.Root(), "second.txt")); !os.IsNotExist(err) {
		t.Fatalf("native delete: %v", err)
	}
	ops, _ := files.Operations(run, 0)
	if len(ops) != len(steps) {
		t.Fatalf("activities %d, expected %d", len(ops), len(steps))
	}
	for _, op := range ops {
		if op.State != "completed" {
			t.Errorf("failed native operation: %+v", op)
		}
	}
	var finalInput struct {
		Messages []struct {
			Role, Content string
			Call          string `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(inputs[len(inputs)-1], &finalInput); err != nil {
		t.Fatal(err)
	}
	foundMemory := false
	for _, message := range finalInput.Messages {
		if message.Call == "fixture6" && strings.Contains(message.Content, "memory/facts.md") && strings.Contains(message.Content, "celesta") {
			foundMemory = true
		}
	}
	if !foundMemory {
		t.Fatal("native keyword search did not retrieve the saved memory")
	}
	if err := files.ReleaseTurn(run, true); err != nil {
		t.Fatal(err)
	}
	if err := process.close(); err != nil {
		t.Fatal(err)
	}
	if err := files.RuntimeStopped(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "outside.txt"), []byte("outside-secret-never-to-model"), 0600); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	steps = []nativeToolStep{
		{"memory_search", map[string]any{"query": "celesta"}},
		{"read_file", map[string]any{"path": "../outside.txt"}},
		{"write_file", map[string]any{"path": "../outside.txt", "content": "overwritten"}},
		{"write_file", map[string]any{"path": "scripts/example.ps1", "content": "Write-Output 'script text only'"}},
		{"exec", map[string]any{"command": "echo forbidden > executed.txt"}},
	}
	inputs = nil
	schemas = nil
	mu.Unlock()
	restarted, reopened := startNativeWorkspaceFixture(t, root, node, entry, server.URL)
	defer restarted.close()
	assertNativeMessage(t, ctx, restarted, binding, observation.AssistantMessage, "Fixture completed")
	second := conversation.Binding{Runtime: "openclaw", Generation: binding.Generation, Key: model.ID()}
	if err := restarted.call(ctx, chatCommand{Action: "create", Binding: second}, &second); err != nil {
		t.Fatal(err)
	}
	run = model.ID()
	if err := reopened.AcquireTurnScope(workspace.Scope{Turn: run, Conversation: second.Key, Policy: "fixture-policy"}); err != nil {
		t.Fatal(err)
	}
	started = time.Now().UnixMilli()
	if err := restarted.call(ctx, chatCommand{Action: "submit", Binding: second, Run: run, Text: "Recall saved facts and check workspace boundaries"}, nil); err != nil {
		t.Fatal(err)
	}
	observation = waitNativeTurn(t, ctx, restarted, second, run, started)
	mu.Lock()
	report, _ = json.MarshalIndent(map[string]any{"schemas": schemas, "inputs": inputs, "observation": observation}, "", "  ")
	mu.Unlock()
	if err := os.WriteFile(filepath.Join(root, "restart-report.json"), report, 0600); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(report), "outside-secret-never-to-model") {
		t.Fatal("outside contents reached model")
	}
	if len(inputs) != len(steps)+1 {
		t.Fatalf("restart calls: %d", len(inputs))
	}
	assertWorkspaceMemoryResult(t, inputs[len(inputs)-1], "fixture0")
	data, err = os.ReadFile(filepath.Join(root, "outside.txt"))
	if err != nil || string(data) != "outside-secret-never-to-model" {
		t.Fatal("outside file modified", err)
	}
	if _, err = os.Stat(filepath.Join(reopened.Root(), "executed.txt")); !os.IsNotExist(err) {
		t.Fatal("script executed", err)
	}
	ops, _ = reopened.Operations(run, 0)
	failed := 0
	for _, op := range ops {
		if op.State == "failed" {
			failed++
		}
	}
	if failed != 2 {
		t.Fatalf("expected two rejected path activities: %+v", ops)
	}
	if err := reopened.ReleaseTurn(run, true); err != nil {
		t.Fatal(err)
	}
}

func assertWorkspaceMemoryResult(t *testing.T, input json.RawMessage, callID string) {
	t.Helper()
	var request struct {
		Messages []struct {
			Content string
			Call    string `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(input, &request); err != nil {
		t.Fatal(err)
	}
	for _, message := range request.Messages {
		if message.Call == callID && strings.Contains(message.Content, "memory/facts.md") && strings.Contains(message.Content, "celesta") {
			return
		}
	}
	t.Fatal("saved memory was not retrieved in a new session after restart")
}

func startNativeWorkspaceFixture(t *testing.T, root, node, entry, endpoint string) (*chatProcess, *workspace.Service) {
	t.Helper()
	for _, name := range []string{"state", "workspace", "bridge", "userprofile", "appdata", "localappdata", "temp"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	files, err := workspace.Open(filepath.Join(root, "workspace"), filepath.Join(root, "versions"), "fixture-workspace")
	if err != nil {
		t.Fatal(err)
	}
	if err = files.Configure(true, "fixture-policy"); err != nil {
		t.Fatal(err)
	}
	if err = seedNativeWorkspace(files, entry, "fixture-policy"); err != nil {
		t.Fatal(err)
	}
	access, err := workspace.Serve(files)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { access.Close(); files.RuntimeStopped() })
	entries, _ := chatBridgeFiles.ReadDir("chatbridge")
	for _, file := range entries {
		data, _ := chatBridgeFiles.ReadFile("chatbridge/" + file.Name())
		if err = os.WriteFile(filepath.Join(root, "bridge", file.Name()), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err = prepareWorkspacePlugin(root); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	c := config.Config{LocalTools: config.LocalToolPolicy{Enabled: true, Revision: "fixture-policy"}}
	c.LLM.Model = "fixture/chat"
	plan, enabled := prepareConversationPrompt(c, root)
	if !enabled {
		t.Fatal("prompt preparation failed")
	}
	base := c
	base.LocalTools.Enabled = false
	data, err := conversationConfig(base, openClawOptions{}, providerBridge{}, root, port, false)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	json.Unmarshal(data, &settings)
	settings["plugins"] = map[string]any{"slots": map[string]any{"memory": "none"}, "allow": []string{}, "load": map[string]any{"paths": []string{}}, "entries": map[string]any{}}
	configureConversationPrompt(settings, root, true)
	if err = configureWorkspace(settings, c, root, access); err != nil {
		t.Fatal(err)
	}
	settings["models"] = map[string]any{"providers": map[string]any{"fixture": map[string]any{"baseUrl": endpoint + "/v1", "api": "openai-completions", "apiKey": "fixture", "models": []any{map[string]any{"id": "chat", "name": "Fixture", "contextWindow": 64000, "maxTokens": 8000}}}}}
	data, _ = json.Marshal(settings)
	if err = os.WriteFile(filepath.Join(root, "openclaw.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	token := model.ID()
	cmd := exec.Command(node, filepath.Join(root, "bridge", "main.mjs"))
	cmd.Dir, cmd.Env = root, conversationEnvironment(root, node, "FIXTURE_KEY", "", token)
	// Isolated fixture diagnostics contain only synthetic inputs/credentials.
	diagnostic, err := os.Create(filepath.Join(root, "gateway.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { diagnostic.Close() })
	cmd.Stderr = diagnostic
	process, err := launchChatProcess(cmd)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	if err = process.call(ctx, chatCommand{Action: "bootstrap", Entry: entry, Port: port, Token: token, Prompt: &plan.Application, PromptInstance: plan.Instance, StartupTimeoutMS: 140000}, nil); err != nil {
		process.close()
		t.Fatal(err)
	}
	return process, files
}
