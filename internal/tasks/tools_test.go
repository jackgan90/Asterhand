package tasks

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/store"
)

func toolsFixture(t *testing.T) (*Engine, model.Job, string, string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "授权 空间")
	os.Mkdir(root, 0700)
	c, err := config.Open(filepath.Join(base, "home"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := c.Get()
	cfg.Projects = []config.Project{{ID: "p", Name: "test", Root: root}}
	c.Save(cfg)
	s, err := store.Open(filepath.Join(base, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	e := New(s, c, nil)
	j, err := e.Accept("origin", model.Request{RequestID: model.ID(), Goal: "write", ProjectID: "p", Mode: "execute", StartBefore: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatal(err)
	}
	j, err = s.Change(j.ID, "running", func(j *model.Job) (any, error) { j.State = "running"; j.Attempt = "test-attempt"; return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	token := model.ID()
	e.active[token] = &active{job: j.ID, attempt: j.Attempt, ctx: ctx, cancel: cancel, mode: "execute"}
	return e, j, token, root
}
func call(tool, id string, args any) ToolCall {
	b, _ := json.Marshal(args)
	return ToolCall{Tool: tool, OperationID: id, Arguments: b}
}
func TestCreateExactUTF8AndDeduplicateTool(t *testing.T) {
	e, j, token, root := toolsFixture(t)
	c := call("asterhand_create_text", "op-one", map[string]string{"path": "hello-world.txt", "content": "hello world"})
	if _, err := e.Tool(context.Background(), token, c); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Tool(context.Background(), token, c); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "hello-world.txt"))
	if err != nil || string(b) != "hello world" {
		t.Fatalf("wrong bytes: %q %v", b, err)
	}
	e.Store.Get("job", j.ID, &j)
	if len(j.Artifacts) != 1 || j.Artifacts[0].Size != 11 || j.Artifacts[0].SHA256 != model.Digest(b) {
		t.Fatalf("invalid evidence: %+v", j.Artifacts)
	}
	c.Arguments = []byte(`{"path":"hello-world.txt","content":"changed"}`)
	if _, err = e.Tool(context.Background(), token, c); err == nil {
		t.Fatal("changed duplicate accepted")
	}
	if _, err = e.Tool(context.Background(), token, call("asterhand_create_text", "different-op", map[string]string{"path": "hello-world.txt", "content": "replace"})); err == nil {
		t.Fatal("overwrote existing file")
	}
	b, _ = os.ReadFile(filepath.Join(root, "hello-world.txt"))
	if string(b) != "hello world" {
		t.Fatal("original changed")
	}
}
func TestPathBoundaryAndRestrictedTools(t *testing.T) {
	e, _, token, root := toolsFixture(t)
	for _, path := range []string{"../outside.txt", "..\\outside.txt", "C:\\outside.txt", "/outside.txt", "file.txt:stream", "bad. ", ""} {
		if _, err := e.Tool(context.Background(), token, call("asterhand_create_text", model.ID(), map[string]string{"path": path, "content": "x"})); err == nil {
			t.Errorf("accepted path %q", path)
		}
	}
	if _, err := e.Tool(context.Background(), token, call("exec", "op", map[string]string{"command": "anything"})); err == nil {
		t.Fatal("arbitrary tool accepted")
	}
	if _, err := e.Tool(context.Background(), "wrong-token", call("asterhand_read_text", "op", map[string]string{"path": "x"})); err == nil {
		t.Fatal("forged token accepted")
	}
	outside := t.TempDir()
	link := filepath.Join(root, "escape")
	var err error
	if runtime.GOOS == "windows" {
		err = exec.Command("cmd", "/c", "mklink", "/J", link, outside).Run()
	} else {
		err = os.Symlink(outside, link)
	}
	if err != nil {
		t.Fatalf("cannot prepare link boundary test: %v", err)
	}
	if _, err = e.Tool(context.Background(), token, call("asterhand_create_text", "escape-op", map[string]string{"path": "escape/outside.txt", "content": "x"})); err == nil {
		t.Fatal("junction/symlink escaped root")
	}
	if _, err = os.Stat(filepath.Join(outside, "outside.txt")); !os.IsNotExist(err) {
		t.Fatal("outside file created")
	}
}
func TestQuestionRequiresBoundAnswerAndDoesNotWriteEarly(t *testing.T) {
	e, j, token, _ := toolsFixture(t)
	done := make(chan error, 1)
	go func() {
		_, err := e.Tool(context.Background(), token, call("asterhand_ask_user", "question-1", map[string]string{"question": "文件名？"}))
		done <- err
	}()
	deadline := time.After(3 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for j.Interaction == nil {
		select {
		case <-deadline:
			t.Fatal("question not persisted")
		case <-tick.C:
			e.Store.Get("job", j.ID, &j)
		}
	}
	i := j.Interaction
	if _, err := e.Answer(j.ID, i.ID, "wrong-attempt", "answer-123", "greeting.txt", "local"); err == nil {
		t.Fatal("accepted stale attempt")
	}
	if _, err := e.Answer(j.ID, i.ID, j.Attempt, "answer-123", "greeting.txt", "peer"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Answer(j.ID, i.ID, j.Attempt, "answer-123", "greeting.txt", "peer"); err != nil {
		t.Fatal("same answer retry rejected", err)
	}
	if _, err := e.Answer(j.ID, i.ID, j.Attempt, "answer-456", "other.txt", "peer"); err == nil {
		t.Fatal("second different answer accepted")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("answer not delivered")
	}
}
func TestRecoveryDoesNotRestartUnknownExecution(t *testing.T) {
	e, j, _, _ := toolsFixture(t)
	if err := e.Recover(); err != nil {
		t.Fatal(err)
	}
	e.Store.Get("job", j.ID, &j)
	if j.State != "reconciliation_required" || j.Attempt != "test-attempt" {
		t.Fatalf("unsafe recovery: %+v", j)
	}
	if _, err := e.Resolve(j.ID, "现场检查后关闭，未重新执行"); err != nil {
		t.Fatal(err)
	}
	e.Store.Get("job", j.ID, &j)
	if j.Execution != "unknown_manually_closed" || strings.Contains(j.State, "succeed") {
		t.Fatal("manual resolution forged success")
	}
}
