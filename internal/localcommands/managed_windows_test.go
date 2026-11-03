//go:build windows

package localcommands

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/model"
)

func TestWindowsManagedOutputAndDescendantStop(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "managed.cmd")
	body := "@echo off\r\necho managed-stdout\r\necho managed-stderr 1>&2\r\nstart \"\" /b ping.exe -n 30 127.0.0.1\r\nexit /b 0\r\n"
	if err := os.WriteFile(script, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	id := model.ID()
	backend := NewPlatformBackend()
	execution, err := backend.Start(ScriptSpec{Script: script}, id)
	if err != nil {
		t.Fatal(err)
	}
	defer execution.Close()
	out := make(chan string, 1)
	stderr := make(chan string, 1)
	go func() { b, _ := io.ReadAll(execution.Stdout()); out <- string(b) }()
	go func() { b, _ := io.ReadAll(execution.Stderr()); stderr <- string(b) }()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	_, err = execution.Wait(ctx)
	cancel()
	if err == nil {
		t.Fatalf("scope ended while descendant should be running; stdout=%q stderr=%q", <-out, <-stderr)
	}
	empty, err := backend.Reconcile(id)
	if err != nil || empty {
		t.Fatal("live scope reconciliation", empty, err)
	}
	if err = execution.ForceStop(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err = execution.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case text := <-out:
		if !strings.Contains(text, "managed-stdout") {
			t.Fatal(text)
		}
	case <-ctx.Done():
		t.Fatal("stdout did not drain")
	}
	select {
	case text := <-stderr:
		if !strings.Contains(text, "managed-stderr") {
			t.Fatal(text)
		}
	case <-ctx.Done():
		t.Fatal("stderr did not drain")
	}
	if empty, err = backend.Reconcile(id); err != nil || !empty {
		t.Fatal("empty scope reconciliation", empty, err)
	}
}
