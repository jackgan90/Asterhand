package workers

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/harnesses"
)

type pipeFakeProc struct {
	inR, inW   *io.PipeReader
	inWriter   *io.PipeWriter
	outR       *io.PipeReader
	outW       *io.PipeWriter
	errR       *io.PipeReader
	errW       *io.PipeWriter
	done       chan struct{}
	closeOnce  sync.Once
}

func newPipeFakeProc() *pipeFakeProc {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	return &pipeFakeProc{
		inR:      inR,
		inWriter: inW,
		outR:     outR,
		outW:     outW,
		errR:     errR,
		errW:     errW,
		done:     make(chan struct{}),
	}
}

func (p *pipeFakeProc) Stdin() io.WriteCloser { return p.inWriter }
func (p *pipeFakeProc) Stdout() io.ReadCloser { return p.outR }
func (p *pipeFakeProc) Stderr() io.ReadCloser { return p.errR }
func (p *pipeFakeProc) Done() <-chan struct{} { return p.done }
func (p *pipeFakeProc) Kill() error {
	p.finish()
	return nil
}
func (p *pipeFakeProc) finish() {
	p.closeOnce.Do(func() {
		_ = p.inWriter.Close()
		_ = p.inR.Close()
		_ = p.outW.Close()
		_ = p.errW.Close()
		close(p.done)
	})
}

func writeJSONLine(w io.Writer, v any) {
	b, _ := json.Marshal(v)
	b = append(b, '\n')
	_, _ = w.Write(b)
}

func TestClaudeAdapterBuildArgsAndAntiRecursion(t *testing.T) {
	ro := buildClaudeArgs(ThreadOpenSpec{
		AccessMode: harnesses.AccessReadOnly,
		RoleHint:   "Be concise",
	}, "11111111-1111-4111-8111-111111111111", false, "claude-sonnet", "low")

	if !slices.Contains(ro, "--strict-mcp-config") {
		t.Fatalf("expected --strict-mcp-config in %v", ro)
	}
	if slices.Contains(ro, "--restricted") {
		t.Fatalf("must not pass --restricted (breaks user auth settings): %v", ro)
	}
	joined := strings.Join(ro, " ")
	for _, want := range []string{
		"--include-partial-messages",
		`--mcp-config {"mcpServers":{}}`,
		"--tools Read,Glob,Grep",
		"--permission-mode dontAsk",
		"--permission-prompts none",
		"--model claude-sonnet",
		"--effort low",
		"--append-system-prompt Be concise",
		"--session-id 11111111-1111-4111-8111-111111111111",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected %q in args: %s", want, joined)
		}
	}
	if strings.Contains(joined, "--allowed-tools") {
		t.Fatalf("read_only must not pass --allowed-tools: %s", joined)
	}

	ww := buildClaudeArgs(ThreadOpenSpec{
		AccessMode: harnesses.AccessWorkspaceWrite,
	}, "22222222-2222-4222-8222-222222222222", true, "", "")
	joinedWW := strings.Join(ww, " ")
	for _, want := range []string{
		"--include-partial-messages",
		"--tools Read,Glob,Grep,Edit,Write,Bash",
		"--allowed-tools",
		"Bash(node *)",
		"Bash(go test*)",
		"Bash(python *)",
		"Bash(python3 *)",
		"Bash(py *)",
		"--permission-mode acceptEdits",
		"--permission-prompts host",
		"--resume 22222222-2222-4222-8222-222222222222",
	} {
		if !strings.Contains(joinedWW, want) {
			t.Fatalf("expected %q in workspace_write args: %s", want, joinedWW)
		}
	}
}

func TestClaudeAdapterStreamLifecycleAndModelRespawn(t *testing.T) {
	var mu sync.Mutex
	var spawnArgs [][]string
	turnIdx := 0

	adapter := &ClaudeAdapter{
		spawn: func(exe string, args []string, workdir string, env []string) (workerProcess, error) {
			mu.Lock()
			spawnArgs = append(spawnArgs, append([]string(nil), args...))
			mu.Unlock()
			p := newPipeFakeProc()
			go func() {
				defer p.finish()
				sc := bufio.NewScanner(p.inR)
				for sc.Scan() {
					var msg map[string]any
					if json.Unmarshal(sc.Bytes(), &msg) != nil {
						continue
					}
					switch msg["type"] {
					case "control_request":
						reqID, _ := msg["request_id"].(string)
						writeJSONLine(p.outW, map[string]any{
							"type": "control_response",
							"response": map[string]any{
								"subtype":    "success",
								"request_id": reqID,
								"response":   map[string]any{},
							},
						})
					case "user":
						mu.Lock()
						turnIdx++
						n := turnIdx
						mu.Unlock()
						writeJSONLine(p.outW, map[string]any{
							"type": "system", "subtype": "init", "model": "claude-test",
						})
						writeJSONLine(p.outW, map[string]any{
							"type": "assistant",
							"message": map[string]any{
								"id": "msg_1",
								"content": []map[string]any{
									{"type": "thinking"},
									{"type": "tool_use", "id": "tu_1", "name": "Read", "input": map[string]any{"file_path": "main.go"}},
									{"type": "text", "text": "Inspecting code"},
								},
							},
						})
						writeJSONLine(p.outW, map[string]any{
							"type": "user",
							"message": map[string]any{
								"role": "user",
								"content": []map[string]any{
									{"type": "tool_result", "tool_use_id": "tu_1", "content": "ok"},
								},
							},
						})
						writeJSONLine(p.outW, map[string]any{
							"type": "result", "subtype": "success", "is_error": false,
							"result": "FINAL_ANSWER_" + string(rune('0'+n)),
						})
					}
				}
			}()
			return p, nil
		},
	}

	handle, err := adapter.OpenThread(context.Background(), ThreadOpenSpec{
		WorkerID:      "wkr_1",
		Executable:    "claude.exe",
		WorkspaceRoot: t.TempDir(),
		AccessMode:    harnesses.AccessReadOnly,
		Model:         "claude-sonnet",
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	if handle.NativeSessionID == "" {
		t.Fatalf("expected pre-assigned UUID session ID")
	}
	defer func() { _ = adapter.CloseThread(handle) }()

	// Turn 1
	var events []AdapterEvent
	_, done1, err := adapter.StartTurn(context.Background(), handle, TurnStartSpec{Prompt: "first"}, func(ev AdapterEvent) {
		events = append(events, ev)
	})
	if err != nil {
		t.Fatalf("StartTurn 1: %v", err)
	}
	out1 := <-done1
	if out1.State != TurnCompleted {
		t.Fatalf("turn 1 state=%s err=%s", out1.State, out1.ErrorMessage)
	}
	ans1, src1, avail1, _ := SelectFinalAnswer(out1.State, out1.Items)
	if !avail1 || src1 != AnswerSourceFinal || ans1 != "FINAL_ANSWER_1" {
		t.Fatalf("unexpected answer1: avail=%v src=%s ans=%q", avail1, src1, ans1)
	}
	if len(events) < 4 {
		t.Fatalf("expected structured events, got %+v", events)
	}
	foundToolDone := false
	for _, ev := range events {
		if ev.Kind == "progress" && ev.Summary == "Tool completed; waiting for model..." {
			foundToolDone = true
			break
		}
	}
	if !foundToolDone {
		t.Fatalf("expected tool_result progress refresh event, got %+v", events)
	}

	// Turn 2 reuses resident process
	_, done2, err := adapter.StartTurn(context.Background(), handle, TurnStartSpec{Prompt: "second"}, nil)
	if err != nil {
		t.Fatalf("StartTurn 2: %v", err)
	}
	out2 := <-done2
	ans2, _, _, _ := SelectFinalAnswer(out2.State, out2.Items)
	if ans2 != "FINAL_ANSWER_2" {
		t.Fatalf("expected FINAL_ANSWER_2, got %q", ans2)
	}
	mu.Lock()
	spawnsAfter2 := len(spawnArgs)
	mu.Unlock()
	if spawnsAfter2 != 1 {
		t.Fatalf("expected 1 spawn across 2 same-model turns, got %d", spawnsAfter2)
	}

	// Turn 3 overrides model -> respawns with --resume <sid> --model claude-opus
	_, done3, err := adapter.StartTurn(context.Background(), handle, TurnStartSpec{Prompt: "third", Model: "claude-opus"}, nil)
	if err != nil {
		t.Fatalf("StartTurn 3: %v", err)
	}
	out3 := <-done3
	ans3, _, _, _ := SelectFinalAnswer(out3.State, out3.Items)
	if ans3 != "FINAL_ANSWER_3" {
		t.Fatalf("expected FINAL_ANSWER_3, got %q", ans3)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(spawnArgs) != 2 {
		t.Fatalf("expected respawn on model change, spawns=%d", len(spawnArgs))
	}
	joined2 := strings.Join(spawnArgs[1], " ")
	if !strings.Contains(joined2, "--resume "+handle.NativeSessionID) || !strings.Contains(joined2, "--model claude-opus") {
		t.Fatalf("expected --resume and --model claude-opus in respawn args: %s", joined2)
	}
}

func TestClaudeAdapterApprovalAndUserInputBlocking(t *testing.T) {
	cases := []struct {
		name     string
		toolName string
		want     string
	}{
		{"approval", "Bash", TurnBlockedApproval},
		{"user_input", "AskUserQuestion", TurnBlockedUserInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var denied bool
			adapter := &ClaudeAdapter{
				spawn: func(exe string, args []string, workdir string, env []string) (workerProcess, error) {
					p := newPipeFakeProc()
					go func() {
						defer p.finish()
						sc := bufio.NewScanner(p.inR)
						for sc.Scan() {
							var msg map[string]any
							if json.Unmarshal(sc.Bytes(), &msg) != nil {
								continue
							}
							switch msg["type"] {
							case "control_request":
								reqID, _ := msg["request_id"].(string)
								writeJSONLine(p.outW, map[string]any{
									"type": "control_response",
									"response": map[string]any{
										"subtype": "success", "request_id": reqID, "response": map[string]any{},
									},
								})
							case "user":
								writeJSONLine(p.outW, map[string]any{
									"type":       "control_request",
									"request_id": "perm_1",
									"request":    map[string]any{"subtype": "can_use_tool", "tool_name": tc.toolName},
								})
							case "control_response":
								resp, _ := msg["response"].(map[string]any)
								inner, _ := resp["response"].(map[string]any)
								if inner["behavior"] == "deny" {
									denied = true
								}
								writeJSONLine(p.outW, map[string]any{
									"type": "result", "subtype": "success", "is_error": false, "result": "aborted",
								})
							}
						}
					}()
					return p, nil
				},
			}
			handle, err := adapter.OpenThread(context.Background(), ThreadOpenSpec{
				WorkerID: "wkr_blk", Executable: "claude.exe", WorkspaceRoot: t.TempDir(), AccessMode: harnesses.AccessWorkspaceWrite,
			})
			if err != nil {
				t.Fatalf("OpenThread: %v", err)
			}
			defer func() { _ = adapter.CloseThread(handle) }()

			_, done, err := adapter.StartTurn(context.Background(), handle, TurnStartSpec{Prompt: "run"}, nil)
			if err != nil {
				t.Fatalf("StartTurn: %v", err)
			}
			out := <-done
			if out.State != tc.want || out.ErrorCode != tc.want {
				t.Fatalf("got state=%s code=%s, want %s", out.State, out.ErrorCode, tc.want)
			}
			if !denied {
				t.Fatalf("expected deny control_response to be sent")
			}
		})
	}
}

func TestClaudeAdapterInterruptAndMissingSession(t *testing.T) {
	// 1. Interrupt
	adapter := &ClaudeAdapter{
		spawn: func(exe string, args []string, workdir string, env []string) (workerProcess, error) {
			p := newPipeFakeProc()
			go func() {
				defer p.finish()
				sc := bufio.NewScanner(p.inR)
				for sc.Scan() {
					var msg map[string]any
					if json.Unmarshal(sc.Bytes(), &msg) != nil {
						continue
					}
					switch msg["type"] {
					case "control_request":
						reqID, _ := msg["request_id"].(string)
						req, _ := msg["request"].(map[string]any)
						writeJSONLine(p.outW, map[string]any{
							"type": "control_response",
							"response": map[string]any{"subtype": "success", "request_id": reqID, "response": map[string]any{}},
						})
						if req["subtype"] == "interrupt" {
							writeJSONLine(p.outW, map[string]any{
								"type": "result", "subtype": "success", "is_error": false, "result": "partial text",
							})
						}
					case "user":
						writeJSONLine(p.outW, map[string]any{
							"type": "assistant",
							"message": map[string]any{
								"id":      "msg_p",
								"content": []map[string]any{{"type": "text", "text": "partial before stop"}},
							},
						})
					}
				}
			}()
			return p, nil
		},
	}
	handle, err := adapter.OpenThread(context.Background(), ThreadOpenSpec{
		WorkerID: "wkr_int", Executable: "claude.exe", WorkspaceRoot: t.TempDir(), AccessMode: harnesses.AccessReadOnly,
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	defer func() { _ = adapter.CloseThread(handle) }()

	gotText := make(chan struct{}, 1)
	nid, done, err := adapter.StartTurn(context.Background(), handle, TurnStartSpec{Prompt: "essay"}, func(ev AdapterEvent) {
		if ev.Kind == "commentary" {
			select {
			case gotText <- struct{}{}:
			default:
			}
		}
	})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	select {
	case <-gotText:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for partial text")
	}
	if err := adapter.InterruptTurn(context.Background(), handle, nid); err != nil {
		t.Fatalf("InterruptTurn: %v", err)
	}
	out := <-done
	if out.State != TurnInterrupted {
		t.Fatalf("expected interrupted, got %s", out.State)
	}
	_, _, avail, partial := SelectFinalAnswer(out.State, out.Items)
	if avail || partial != "partial before stop" {
		t.Fatalf("expected partial_output without final answer, got avail=%v partial=%q", avail, partial)
	}

	// 2. Missing resume session
	missingAdapter := &ClaudeAdapter{
		spawn: func(exe string, args []string, workdir string, env []string) (workerProcess, error) {
			p := newPipeFakeProc()
			go func() {
				defer p.finish()
				sc := bufio.NewScanner(p.inR)
				for sc.Scan() {
					var msg map[string]any
					if json.Unmarshal(sc.Bytes(), &msg) != nil {
						continue
					}
					if msg["type"] == "control_request" {
						reqID, _ := msg["request_id"].(string)
						writeJSONLine(p.outW, map[string]any{
							"type": "control_response",
							"response": map[string]any{"subtype": "success", "request_id": reqID, "response": map[string]any{}},
						})
					} else if msg["type"] == "user" {
						_, _ = p.errW.Write([]byte("No conversation found with session ID: 00000000-0000-4000-8000-000000000099\n"))
						writeJSONLine(p.outW, map[string]any{
							"type": "result", "subtype": "error_during_execution", "is_error": true,
						})
						return
					}
				}
			}()
			return p, nil
		},
	}
	h2, err := missingAdapter.OpenThread(context.Background(), ThreadOpenSpec{
		WorkerID: "wkr_miss", Executable: "claude.exe", WorkspaceRoot: t.TempDir(),
		AccessMode: harnesses.AccessReadOnly, NativeSessionID: "00000000-0000-4000-8000-000000000099",
	})
	if err != nil {
		t.Fatalf("OpenThread missing: %v", err)
	}
	defer func() { _ = missingAdapter.CloseThread(h2) }()
	_, doneMiss, err := missingAdapter.StartTurn(context.Background(), h2, TurnStartSpec{Prompt: "hi"}, nil)
	if err != nil {
		t.Fatalf("StartTurn missing: %v", err)
	}
	outMiss := <-doneMiss
	if outMiss.State != TurnFailed || outMiss.ErrorCode != "native_session_unrecoverable" {
		t.Fatalf("expected native_session_unrecoverable, got state=%s code=%s msg=%s", outMiss.State, outMiss.ErrorCode, outMiss.ErrorMessage)
	}
}

func TestClaudeAdapterStreamEventHeartbeatAndResumeAfterInterrupt(t *testing.T) {
	var mu sync.Mutex
	var spawnArgs [][]string

	adapter := &ClaudeAdapter{
		spawn: func(exe string, args []string, workdir string, env []string) (workerProcess, error) {
			mu.Lock()
			spawnArgs = append(spawnArgs, append([]string(nil), args...))
			mu.Unlock()
			p := newPipeFakeProc()
			go func() {
				defer p.finish()
				sc := bufio.NewScanner(p.inR)
				for sc.Scan() {
					var msg map[string]any
					if json.Unmarshal(sc.Bytes(), &msg) != nil {
						continue
					}
					switch msg["type"] {
					case "control_request":
						reqID, _ := msg["request_id"].(string)
						req, _ := msg["request"].(map[string]any)
						writeJSONLine(p.outW, map[string]any{
							"type": "control_response",
							"response": map[string]any{"subtype": "success", "request_id": reqID, "response": map[string]any{}},
						})
						if req["subtype"] == "interrupt" {
							writeJSONLine(p.outW, map[string]any{
								"type": "result", "subtype": "success", "is_error": false, "result": "",
							})
						}
					case "user":
						if len(spawnArgs) == 1 {
							writeJSONLine(p.outW, map[string]any{
								"type":  "stream_event",
								"event": map[string]any{"type": "content_block_start", "content_block": map[string]any{"type": "tool_use", "name": "Write"}},
							})
							writeJSONLine(p.outW, map[string]any{
								"type":  "stream_event",
								"event": map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "input_json_delta", "partial_json": "{\"content\":\"abc\"}"}},
							})
						} else {
							writeJSONLine(p.outW, map[string]any{
								"type": "result", "subtype": "success", "is_error": false, "result": "RESUMED_OK",
							})
						}
					}
				}
			}()
			return p, nil
		},
	}

	h, err := adapter.OpenThread(context.Background(), ThreadOpenSpec{
		WorkerID: "wkr_hb", Executable: "claude.exe", WorkspaceRoot: t.TempDir(),
		AccessMode: harnesses.AccessWorkspaceWrite, ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	defer func() { _ = adapter.CloseThread(h) }()

	gotDelta := make(chan int, 2)
	gotProg := make(chan string, 2)
	nid, done1, err := adapter.StartTurn(context.Background(), h, TurnStartSpec{Prompt: "big write"}, func(ev AdapterEvent) {
		if ev.Kind == "delta" && ev.Bytes > 0 {
			select {
			case gotDelta <- ev.Bytes:
			default:
			}
		}
		if ev.Kind == "progress" && ev.Summary != "" {
			select {
			case gotProg <- ev.Summary:
			default:
			}
		}
	})
	if err != nil {
		t.Fatalf("StartTurn 1: %v", err)
	}
	select {
	case b := <-gotDelta:
		if b <= 0 {
			t.Fatalf("expected positive stream_event bytes, got %d", b)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for stream_event heartbeat")
	}
	select {
	case prog := <-gotProg:
		if prog != "Generating Write tool input..." {
			t.Fatalf("expected Write progress summary, got %q", prog)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for content_block_start progress")
	}
	if err := adapter.InterruptTurn(context.Background(), h, nid); err != nil {
		t.Fatalf("InterruptTurn: %v", err)
	}
	out1 := <-done1
	if out1.State != TurnInterrupted {
		t.Fatalf("expected interrupted turn 1, got %s", out1.State)
	}

	// Turn 2 changes effort after interrupted Turn 1 -> MUST respawn with --resume (NOT --session-id!).
	_, done2, err := adapter.StartTurn(context.Background(), h, TurnStartSpec{Prompt: "retry", ReasoningEffort: "low"}, nil)
	if err != nil {
		t.Fatalf("StartTurn 2: %v", err)
	}
	out2 := <-done2
	ans2, _, _, _ := SelectFinalAnswer(out2.State, out2.Items)
	if out2.State != TurnCompleted || ans2 != "RESUMED_OK" {
		t.Fatalf("expected RESUMED_OK, got state=%s ans=%q", out2.State, ans2)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(spawnArgs) != 2 {
		t.Fatalf("expected 2 spawns, got %d", len(spawnArgs))
	}
	joined2 := strings.Join(spawnArgs[1], " ")
	if !strings.Contains(joined2, "--resume "+h.NativeSessionID) || strings.Contains(joined2, "--session-id") {
		t.Fatalf("respawn after interrupted turn must use --resume (not --session-id): %s", joined2)
	}
}
