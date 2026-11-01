package executor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/conversation"
	"github.com/jackgan90/Asterhand/internal/model"
)

// Uses the actual installed dependency paths, but only an isolated state directory
// and no model credentials. A short history caller must not abort cold startup.
func TestNativeConversationStartupSurvivesReadTimeout(t *testing.T) {
	if os.Getenv("ASTERHAND_CHAT_RUNTIME") != "1" {
		t.Skip("explicit locked native runtime required")
	}
	node, entry := os.Getenv("ASTERHAND_CHAT_NODE"), os.Getenv("ASTERHAND_CHAT_ENTRY")
	if node == "" || entry == "" {
		t.Fatal("locked paths required")
	}
	manager, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := manager.Get()
	provider, err := openClawProvider(c)
	if err != nil {
		t.Fatal(err)
	}
	setClawOptions(&c, openClawOptions{NodePath: node, EntryPath: entry, ProviderPaths: map[string]string{c.LLM.Provider: filepath.Join(filepath.Dir(filepath.Dir(entry)), filepath.FromSlash(provider.Package))}})
	if err = manager.Save(c); err != nil {
		t.Fatal(err)
	}
	r := NewConversationRuntime(manager)
	defer r.Close()
	var starts atomic.Int32
	r.start = func(ctx context.Context, c config.Config, o openClawOptions, p providerBridge, key, root string) (*chatProcess, error) {
		starts.Add(1)
		return startConversationProcess(ctx, c, o, p, key, root)
	}
	binding := conversation.Binding{Runtime: "openclaw", Generation: chatBindingID(), Key: model.ID()}
	short, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	_, err = r.History(short, binding, 0, "")
	cancel()
	if err == nil {
		t.Fatal("short cold-start caller unexpectedly completed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), conversationStartupBudget+30*time.Second)
	defer cancel()
	binding, err = r.Create(ctx, binding)
	if err != nil {
		t.Fatal(err)
	}
	if starts.Load() != 1 {
		t.Fatalf("cold startup restarted %d times", starts.Load())
	}
	page, err := r.History(ctx, binding, 0, "")
	if err != nil || len(page.Messages) != 0 {
		t.Fatal("initial history", err)
	}
	root := filepath.Join(manager.Home, "conversations", "runtime-"+binding.Generation)
	data, err := os.ReadFile(filepath.Join(root, "startup-diagnostic.json"))
	var report chatStartupReport
	if err != nil || json.Unmarshal(data, &report) != nil || report.Outcome != "ready" || len(report.Events) < 3 {
		t.Fatal("native startup diagnostic missing", err, string(data))
	}
	// Managed close kills the native tree. Reopening must inspect and recover its
	// existing database without changing the stable session binding or calling LLM.
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := NewConversationRuntime(manager)
	defer restarted.Close()
	page, err = restarted.History(ctx, binding, 0, "")
	if err != nil || len(page.Messages) != 0 {
		t.Fatal("restart history", err)
	}
	t.Logf("one shared cold startup, %d diagnostic stages; stable history after process restart", len(report.Events))
}
