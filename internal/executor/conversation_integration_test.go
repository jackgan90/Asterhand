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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/conversation"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/prompt"
)

// This opt-in test uses the locked native runtime and production private bridge.
// The HTTP provider is local; it needs no credential and incurs no model charge.
func TestNativeConversationBridge(t *testing.T) {
	if os.Getenv("ASTERHAND_CHAT_RUNTIME") != "1" {
		t.Skip("set ASTERHAND_CHAT_RUNTIME=1 with ASTERHAND_CHAT_NODE and ASTERHAND_CHAT_ENTRY")
	}
	node, entry := os.Getenv("ASTERHAND_CHAT_NODE"), os.Getenv("ASTERHAND_CHAT_ENTRY")
	if node == "" || entry == "" {
		t.Fatal("locked Node and OpenClaw entry paths are required")
	}
	root := t.TempDir()
	if evidence := os.Getenv("ASTERHAND_CHAT_EVIDENCE"); evidence != "" {
		var err error
		root, err = os.MkdirTemp(evidence, "native-policy-")
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("isolated fixture root: %s", root)
	}
	answer := strings.Repeat("中文🙂 code<>&\n", 1800)
	var calls, exposedTools, appliedPolicies, nativePersonas atomic.Int32
	var customPersona atomic.Bool
	policyConfig := config.Config{}
	policyConfig.LLM.Model = "fixture/chat"
	expectedPolicy := conversationPrompt(policyConfig).Text
	var contextPreserved atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Tools    []json.RawMessage `json:"tools"`
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		calls.Add(1)
		for _, message := range input.Messages {
			var content string
			_ = json.Unmarshal(message.Content, &content)
			if message.Role == "system" && strings.Contains(content, expectedPolicy) {
				appliedPolicies.Add(1)
			}
			if message.Role == "system" && strings.Contains(content, "You are a personal assistant running inside OpenClaw.") &&
				strings.Contains(content, "Be genuinely helpful, not performatively helpful.") && strings.Contains(content, "## Promised Work") {
				nativePersonas.Add(1)
			}
			if message.Role == "system" && strings.Contains(content, "ASTERHAND-CUSTOM-IDENTITY") && strings.Contains(content, "ASTERHAND-CUSTOM-SOUL") {
				customPersona.Store(true)
			}
		}
		if calls.Load() == 2 {
			var user, assistant bool
			for _, message := range input.Messages {
				var content string
				_ = json.Unmarshal(message.Content, &content)
				user = user || message.Role == "user" && strings.Contains(content, "fixture greeting")
				assistant = assistant || message.Role == "assistant" && strings.Contains(content, "中文")
			}
			contextPreserved.Store(user && assistant)
		}
		if len(input.Tools) != 0 {
			exposedTools.Add(1)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk, _ := json.Marshal(map[string]any{"id": "fixture", "object": "chat.completion.chunk", "model": "chat",
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": answer}, "finish_reason": nil}}})
		fmt.Fprintf(w, "data: %s\n\n", chunk)
		w.(http.Flusher).Flush()
		time.Sleep(250 * time.Millisecond)
		fmt.Fprint(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"model\":\"chat\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	process := startNativeChatFixture(t, root, node, entry, server.URL, true)
	defer func() { process.close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	binding := conversation.Binding{Runtime: "openclaw", Generation: strings.Repeat("a", 32), Key: model.ID()}
	if err := process.call(ctx, chatCommand{Action: "create", Binding: binding}, &binding); err != nil {
		t.Fatal(err)
	}
	request := model.ID()
	started := time.Now().UnixMilli()
	var receipt conversation.Receipt
	command := chatCommand{Action: "submit", Binding: binding, Run: request, Text: "fixture greeting"}
	if err := process.call(ctx, command, &receipt); err != nil {
		t.Fatal(err)
	}
	observation := waitNativeTurn(t, ctx, process, binding, request, started)
	if !observation.Accepted || !observation.Saved || observation.Run != "completed" || observation.AssistantMessage == "" {
		t.Fatalf("native receipt: %+v", observation)
	}
	if err := process.call(ctx, command, &receipt); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || exposedTools.Load() != 0 || appliedPolicies.Load() != 1 || observation.Prompt == nil || observation.Prompt.State != prompt.Applied || observation.Prompt.Mode != prompt.Append || nativePersonas.Load() != 1 {
		t.Fatalf("native policy/receipt: calls=%d tools=%d policy=%d observation=%+v", calls.Load(), exposedTools.Load(), appliedPolicies.Load(), observation.Prompt)
	}
	assertNativeMessage(t, ctx, process, binding, observation.AssistantMessage, answer)
	// Default native role files must be real workspace files, not product-owned copies.
	soulPath := filepath.Join(root, "workspace", "SOUL.md")
	soul, err := os.ReadFile(soulPath)
	if err != nil || !strings.Contains(string(soul), "Be genuinely helpful, not performatively helpful.") {
		t.Fatal("native SOUL initialization missing", err)
	}
	customSoul := string(soul) + "\nASTERHAND-CUSTOM-SOUL: retain this fixture persona.\n"
	customIdentity := "# IDENTITY.md\n- Name: ASTERHAND-CUSTOM-IDENTITY\n- Vibe: calm fixture persona\n"
	identityPath := filepath.Join(root, "workspace", "IDENTITY.md")
	if err := os.WriteFile(soulPath, []byte(customSoul), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(identityPath, []byte(customIdentity), 0600); err != nil {
		t.Fatal(err)
	}
	command.Run, command.Text = model.ID(), "follow up using the previous exchange"
	if err := process.call(ctx, command, &receipt); err != nil {
		t.Fatal(err)
	}
	second := waitNativeTurn(t, ctx, process, binding, command.Run, time.Now().UnixMilli())
	if !second.Saved || second.AssistantMessage == observation.AssistantMessage || !contextPreserved.Load() || calls.Load() != 2 || exposedTools.Load() != 0 || appliedPolicies.Load() != 2 || second.Prompt == nil || second.Prompt.State != prompt.Applied || second.Prompt.Mode != prompt.Append || nativePersonas.Load() != 2 || !customPersona.Load() {
		t.Fatal("native follow-up lost context, identity, or tool isolation")
	}
	if err := process.close(); err != nil {
		t.Fatal(err)
	}
	server.Close()
	process = startNativeChatFixture(t, root, node, entry, server.URL, false)
	assertNativeMessage(t, ctx, process, binding, observation.AssistantMessage, answer)
	if calls.Load() != 2 {
		t.Fatal("read-only recovery invoked provider")
	}
	for path, expected := range map[string]string{soulPath: customSoul, identityPath: customIdentity} {
		content, err := os.ReadFile(path)
		if err != nil || string(content) != expected {
			t.Fatal("restart replaced a custom persona file", err)
		}
	}
	t.Log("native system prompt and default SOUL preserved on both turns; custom SOUL/IDENTITY included on next turn and preserved across restart; zero tools")
}

// Exercise the production DeepSeek plugin/configuration and logical binding
// without a key or model call; the local HTTP fixture above cannot prove this.
func TestNativeConversationReadOnlyAdapter(t *testing.T) {
	if os.Getenv("ASTERHAND_CHAT_RUNTIME") != "1" {
		t.Skip("explicit locked native runtime required")
	}
	node, entry := os.Getenv("ASTERHAND_CHAT_NODE"), os.Getenv("ASTERHAND_CHAT_ENTRY")
	if node == "" || entry == "" {
		t.Fatal("locked paths are required")
	}
	home := t.TempDir()
	if evidence := os.Getenv("ASTERHAND_CHAT_EVIDENCE"); evidence != "" {
		var err error
		home, err = os.MkdirTemp(evidence, "native-readonly-")
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("isolated fixture home: %s", home)
	}
	manager, err := config.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	c := manager.Get()
	provider, err := openClawProvider(c)
	if err != nil {
		t.Fatal(err)
	}
	options := openClawOptions{NodePath: node, EntryPath: entry, ProviderPaths: map[string]string{c.LLM.Provider: filepath.Join(filepath.Dir(filepath.Dir(entry)), filepath.FromSlash(provider.Package))}}
	setClawOptions(&c, options)
	if err = manager.Save(c); err != nil {
		t.Fatal(err)
	}
	runtime := NewConversationRuntime(manager)
	defer func() { runtime.Close() }()
	descriptor, err := runtime.Describe()
	if err != nil || descriptor.CanSend {
		t.Fatalf("read-only descriptor: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	startup := time.Now()
	binding, err := runtime.Create(ctx, conversation.Binding{Runtime: descriptor.ID, Generation: descriptor.Binding, Key: model.ID()})
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("ASTERHAND_CHAT_COST") == "1" {
		t.Logf("native_create_ms=%d", time.Since(startup).Milliseconds())
		if marker := os.Getenv("ASTERHAND_CHAT_READY_FILE"); marker != "" {
			if err := os.WriteFile(marker, []byte("ready"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(5 * time.Second)
	}
	page, err := runtime.History(ctx, binding, 0, "")
	if err != nil || len(page.Messages) != 0 {
		t.Fatalf("read-only history: %v", err)
	}
	if err = runtime.Close(); err != nil {
		t.Fatal(err)
	}
	options.NodePath = filepath.Join(t.TempDir(), "new-version", "node.exe")
	options.EntryPath = filepath.Join(t.TempDir(), "new-version", "openclaw.mjs")
	setClawOptions(&c, options)
	if err = manager.Save(c); err != nil {
		t.Fatal(err)
	}
	after, err := runtime.Describe()
	if err != nil || after.Binding != descriptor.Binding {
		t.Fatal("installer version path orphaned history")
	}
}

func startNativeChatFixture(t *testing.T, root, node, entry, endpoint string, key bool, modes ...prompt.Mode) *chatProcess {
	t.Helper()
	for _, name := range []string{"state", "workspace", "bridge", "userprofile", "appdata", "localappdata", "temp"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	files, _ := chatBridgeFiles.ReadDir("chatbridge")
	for _, file := range files {
		data, _ := chatBridgeFiles.ReadFile("chatbridge/" + file.Name())
		if err := os.WriteFile(filepath.Join(root, "bridge", file.Name()), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	provider := map[string]any{"baseUrl": endpoint + "/v1", "api": "openai-completions",
		"models": []any{map[string]any{"id": "chat", "name": "Fixture", "contextWindow": 64000, "maxTokens": 8000}}}
	if key {
		provider["apiKey"] = "fixture"
	}
	c := config.Config{}
	c.LLM.Model = "fixture/chat"
	plan, enabled := prepareConversationPrompt(c, root)
	if !enabled {
		t.Fatal("fixture prompt preparation failed")
	}
	if len(modes) > 0 {
		plan.Application.Mode = modes[0]
		plan.Application = plan.Application.Normalize()
	}
	planData, _ := json.Marshal(plan)
	if err := os.WriteFile(filepath.Join(root, "prompt-policy.json"), planData, 0600); err != nil {
		t.Fatal(err)
	}
	enabled = plan.Application.Mode != prompt.Unsupported
	data, _ := conversationConfig(c, openClawOptions{}, providerBridge{}, root, port, false)
	var settings map[string]any
	json.Unmarshal(data, &settings)
	settings["plugins"] = map[string]any{"slots": map[string]any{"memory": "none"}, "allow": []string{}, "load": map[string]any{"paths": []string{}}, "entries": map[string]any{}}
	configureConversationPrompt(settings, root, enabled)
	settings["models"] = map[string]any{"providers": map[string]any{"fixture": provider}}
	data, _ = json.Marshal(settings)
	if err := os.WriteFile(filepath.Join(root, "openclaw.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	token := model.ID()
	cmd := exec.Command(node, filepath.Join(root, "bridge", "main.mjs"))
	cmd.Dir, cmd.Env = root, conversationEnvironment(root, node, "FIXTURE_KEY", "", token)
	process, err := launchChatProcess(cmd)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := process.call(ctx, chatCommand{Action: "bootstrap", Entry: entry, Port: port, Token: token, Prompt: &plan.Application, PromptInstance: plan.Instance}, nil); err != nil {
		process.close()
		t.Fatal(err)
	}
	return process
}

func waitNativeTurn(t *testing.T, ctx context.Context, process *chatProcess, binding conversation.Binding, request string, started int64) conversation.Observation {
	t.Helper()
	for attempt := 0; attempt < 40; attempt++ {
		var observation conversation.Observation
		if err := process.call(ctx, chatCommand{Action: "observe", Binding: binding, Run: request, Started: started}, &observation); err != nil {
			t.Fatal(err)
		}
		if observation.Stopped {
			return observation
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("native turn did not settle")
	return conversation.Observation{}
}

func assertNativeMessage(t *testing.T, ctx context.Context, process *chatProcess, binding conversation.Binding, id, expected string) {
	t.Helper()
	var page conversation.Page
	if err := process.call(ctx, chatCommand{Action: "history", Binding: binding}, &page); err != nil {
		t.Fatal(err)
	}
	serialized, _ := json.Marshal(page)
	if len(serialized) > conversation.PageBytes {
		t.Fatal("unbounded history page")
	}
	var recovered strings.Builder
	offset := 0
	for {
		var fragment conversation.Fragment
		if err := process.call(ctx, chatCommand{Action: "message", Binding: binding, Message: id, Offset: offset}, &fragment); err != nil {
			t.Fatal(err)
		}
		recovered.WriteString(fragment.Text)
		if !fragment.HasMore {
			break
		}
		if fragment.Next <= offset {
			t.Fatal("non-progressing fragment")
		}
		offset = fragment.Next
	}
	if recovered.String() != expected {
		t.Fatalf("full message changed: got %d bytes, expected %d", recovered.Len(), len(expected))
	}
}
