package tasks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/model"
)

func TestMaximumFileStillPersistsBoundedEvidence(t *testing.T) {
	e, j, token, root := toolsFixture(t)
	content := strings.Repeat("<", MaxText)
	if _, err := e.Tool(context.Background(), token, call("asterhand_create_text", "maximum-file", map[string]string{"path": "max.txt", "content": content})); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.Get("job", j.ID, &j); err != nil {
		t.Fatal(err)
	}
	if len(j.Artifacts) != 1 || j.Artifacts[0].Size != MaxText || len(j.Artifacts[0].Preview) > 8000 {
		t.Fatal("missing or oversized evidence")
	}
	b, err := os.ReadFile(filepath.Join(root, "max.txt"))
	if err != nil || string(b) != content {
		t.Fatal("file content truncated", err)
	}
}

func TestCancelDuringPreparedFileWaitHasNoEffect(t *testing.T) {
	e, j, token, root := toolsFixture(t)
	cfg := e.Config.Get()
	cfg.AcceptanceDelay = 60
	e.Config.Save(cfg)
	done := make(chan error, 1)
	go func() {
		_, err := e.Tool(context.Background(), token, call("asterhand_create_text", "cancel-file", map[string]string{"path": "cancelled.txt", "content": "hello world"}))
		done <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var ops []model.Operation
		e.Store.List("operation", &ops)
		if len(ops) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("prepared checkpoint missing")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := e.Cancel(j.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled tool succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not release callback")
	}
	if _, err := os.Stat(filepath.Join(root, "cancelled.txt")); !os.IsNotExist(err) {
		t.Fatal("cancelled file exists")
	}
}
