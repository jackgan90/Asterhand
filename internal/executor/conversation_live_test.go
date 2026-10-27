package executor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/conversation"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/prompt"
)

// Explicit opt-in: at most two short submitted turns, 45 seconds each. Read the
// credential from a node configured through its settings form, never test logs.
// All runtime state and encrypted test credentials live in a temporary node.
func TestRealConversationTwoTurns(t *testing.T) {
	if os.Getenv("ASTERHAND_CHAT_REAL_MODEL") != "1" {
		t.Skip("explicit real model and credential-home opt-in required")
	}
	manager := liveChatManager(t)
	runtime := NewConversationRuntime(manager)
	defer func() { runtime.Close() }()
	descriptor, err := runtime.Describe()
	if err != nil || !descriptor.CanSend {
		t.Fatal("test model is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	binding, err := runtime.Create(ctx, conversation.Binding{Runtime: descriptor.ID, Generation: descriptor.Binding, Key: model.ID()})
	if err != nil {
		t.Fatal(err)
	}
	marker := "ASTER-" + model.ID()[:8]
	for index, text := range []string{"记住这个测试口令：" + marker + "。只回复已记住。", "上一条口令是什么？只回复口令，不要解释。"} {
		turn := liveChatTurn(t, runtime, binding, text)
		if !turn.Accepted || !turn.Saved || turn.Run != "completed" {
			t.Fatal("real turn did not complete durably")
		}
		if turn.Prompt == nil || turn.Prompt.Mode != prompt.Append || turn.Prompt.State != prompt.Applied {
			t.Fatal("real model input did not confirm the product prompt")
		}
		if index == 1 {
			fragment, err := runtime.Message(ctx, binding, turn.AssistantMessage, 0)
			if err != nil || !strings.Contains(fragment.Text, marker) {
				t.Fatal("real follow-up did not retain context")
			}
		}
	}
	if err = runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if err = manager.SetCredential(manager.Get().LLM.CredentialRef, ""); err != nil {
		t.Fatal("could not remove temporary test credential")
	}
	runtime = NewConversationRuntime(manager)
	page, err := runtime.History(ctx, binding, 0, "")
	if err != nil || len(page.Messages) < 4 {
		t.Fatal("real native history was not recoverable without a key")
	}
}

func liveChatManager(t *testing.T) *config.Manager {
	t.Helper()
	home, node, entry := os.Getenv("ASTERHAND_CHAT_CREDENTIAL_HOME"), os.Getenv("ASTERHAND_CHAT_NODE"), os.Getenv("ASTERHAND_CHAT_ENTRY")
	if home == "" || node == "" || entry == "" {
		t.Fatal("explicit credential home and locked runtime paths required")
	}
	data, err := os.ReadFile(filepath.Join(home, "config.json"))
	var source config.Config
	if err != nil || json.Unmarshal(data, &source) != nil {
		t.Fatal("cannot read test credential selection")
	}
	reader := &config.Manager{Home: home}
	key, err := reader.Credential(source.LLM.CredentialRef)
	if err != nil || key == "" {
		t.Fatal("test credential is not available")
	}
	manager, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := manager.Get()
	c.LLM, c.AgentRuntime = source.LLM, "openclaw"
	provider, err := openClawProvider(c)
	if err != nil {
		t.Fatal("unsupported test provider or model")
	}
	setClawOptions(&c, openClawOptions{NodePath: node, EntryPath: entry, ProviderPaths: map[string]string{c.LLM.Provider: filepath.Join(filepath.Dir(filepath.Dir(entry)), filepath.FromSlash(provider.Package))}})
	if err = manager.Save(c); err != nil {
		t.Fatal(err)
	}
	if err = manager.SetCredential(c.LLM.CredentialRef, key); err != nil {
		t.Fatal("cannot protect temporary test credential")
	}
	return manager
}

func liveChatTurn(t *testing.T, runtime *ConversationRuntime, binding conversation.Binding, text string) conversation.Observation {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	submission := conversation.Submission{ID: model.ID(), Text: text, StartedAt: time.Now()}
	if _, err := runtime.Submit(ctx, binding, submission); err != nil {
		t.Fatal(err)
	}
	for ctx.Err() == nil {
		observation, err := runtime.Observe(ctx, binding, submission.ID, submission.StartedAt)
		if err == nil && observation.Stopped {
			return observation
		}
		time.Sleep(300 * time.Millisecond)
	}
	stop, stopCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer stopCancel()
	_ = runtime.Cancel(stop, binding, submission.ID)
	t.Fatal("real model turn exceeded the 45-second test budget")
	return conversation.Observation{}
}
