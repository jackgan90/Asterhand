package workers

import (
	"bufio"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/harnesses"
)

func sampleACPConfigOptions() []map[string]any {
	return []map[string]any{
		{
			"id":           "model",
			"category":     "model",
			"currentValue": `["deepseek-official","deepseek-v4-flash"]`,
			"options": []map[string]any{
				{
					"group": "deepseek-official",
					"name":  "DeepSeek",
					"options": []map[string]any{
						{"value": `["deepseek-official","deepseek-v4-flash"]`, "name": "deepseek-v4-flash"},
						{"value": `["deepseek-official","deepseek-v4-pro"]`, "name": "DeepSeek-V4-Pro"},
					},
				},
			},
		},
		{
			"id":           "reasoning_effort",
			"category":     "thought_level",
			"currentValue": "high",
			"options": []map[string]any{
				{"value": "off", "name": "Off"},
				{"value": "low", "name": "Low"},
				{"value": "high", "name": "High"},
				{"value": "max", "name": "Max"},
			},
		},
	}
}

func TestDeepSeekAdapterLifecycleModelResolutionAndMultiTurn(t *testing.T) {
	var mu sync.Mutex
	var gotArgs []string
	var gotEnv []string
	var setConfigs []map[string]any
	var prompts []string
	var closedSession bool
	var mcpServersEmpty bool

	adapter := &DeepSeekAdapter{
		spawn: func(exe string, args []string, workdir string, env []string) (workerProcess, error) {
			mu.Lock()
			gotArgs = append([]string(nil), args...)
			gotEnv = append([]string(nil), env...)
			mu.Unlock()
			p := newPipeFakeProc()
			go func() {
				defer p.finish()
				sc := bufio.NewScanner(p.inR)
				for sc.Scan() {
					var msg struct {
						ID     json.RawMessage `json:"id"`
						Method string          `json:"method"`
						Params map[string]any  `json:"params"`
					}
					if json.Unmarshal(sc.Bytes(), &msg) != nil {
						continue
					}
					switch msg.Method {
					case "initialize":
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "id": msg.ID,
							"result": map[string]any{"protocolVersion": 1},
						})
					case "session/new":
						if ms, ok := msg.Params["mcpServers"].([]any); ok && len(ms) == 0 {
							mu.Lock()
							mcpServersEmpty = true
							mu.Unlock()
						}
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "id": msg.ID,
							"result": map[string]any{
								"sessionId":     "dsh-sess-1",
								"configOptions": sampleACPConfigOptions(),
							},
						})
					case "session/set_config_option":
						mu.Lock()
						setConfigs = append(setConfigs, msg.Params)
						mu.Unlock()
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{},
						})
					case "session/prompt":
						arr, _ := msg.Params["prompt"].([]any)
						first, _ := arr[0].(map[string]any)
						txt, _ := first["text"].(string)
						mu.Lock()
						prompts = append(prompts, txt)
						n := len(prompts)
						mu.Unlock()
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "method": "session/update",
							"params": map[string]any{"update": map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": "thinking"}}},
						})
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "method": "session/update",
							"params": map[string]any{"update": map[string]any{"sessionUpdate": "tool_call", "toolCallId": "tc1", "title": "read", "status": "in_progress"}},
						})
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "method": "session/update",
							"params": map[string]any{"update": map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "tc1", "status": "completed"}},
						})
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "method": "session/update",
							"params": map[string]any{"update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "DSH_OK_"}}},
						})
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "method": "session/update",
							"params": map[string]any{"update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": string(rune('0' + n))}}},
						})
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "id": msg.ID,
							"result": map[string]any{"stopReason": "end_turn"},
						})
					case "session/close":
						mu.Lock()
						closedSession = true
						mu.Unlock()
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{},
						})
					}
				}
			}()
			return p, nil
		},
	}

	handle, err := adapter.OpenThread(context.Background(), ThreadOpenSpec{
		WorkerID:        "wkr_dsh",
		Executable:      "dsh.cmd",
		WorkspaceRoot:   t.TempDir(),
		AccessMode:      harnesses.AccessReadOnly,
		Model:           "deepseek-v4-pro",
		ReasoningEffort: "low",
		RoleHint:        "Act as a strict reviewer.",
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	if handle.NativeSessionID != "dsh-sess-1" {
		t.Fatalf("expected sessionId dsh-sess-1, got %q", handle.NativeSessionID)
	}

	// Verify spawn args, env, anti-recursion mcpServers:[], and config option resolution
	mu.Lock()
	if !slices.Equal(gotArgs, []string{"--profile", "acp"}) {
		t.Fatalf("unexpected args: %v", gotArgs)
	}
	if !slices.Contains(gotEnv, "DSH_PERMISSION_MODE=read-only") {
		t.Fatalf("expected DSH_PERMISSION_MODE=read-only in env")
	}
	if !mcpServersEmpty {
		t.Fatalf("expected mcpServers: [] in session/new")
	}
	if len(setConfigs) != 2 || setConfigs[0]["value"] != `["deepseek-official","deepseek-v4-pro"]` || setConfigs[1]["value"] != "low" {
		t.Fatalf("unexpected set_config_option calls: %+v", setConfigs)
	}
	mu.Unlock()

	// Turn 1 injects RoleHint
	var events []AdapterEvent
	_, done1, err := adapter.StartTurn(context.Background(), handle, TurnStartSpec{Prompt: "review file"}, func(ev AdapterEvent) {
		events = append(events, ev)
	})
	if err != nil {
		t.Fatalf("StartTurn 1: %v", err)
	}
	out1 := <-done1
	if out1.State != TurnCompleted {
		t.Fatalf("expected completed, got %s (%s)", out1.State, out1.ErrorMessage)
	}
	ans1, src1, avail1, _ := SelectFinalAnswer(out1.State, out1.Items)
	if !avail1 || src1 != AnswerSourceFinal || ans1 != "DSH_OK_1" {
		t.Fatalf("unexpected answer1: avail=%v src=%s ans=%q", avail1, src1, ans1)
	}
	if len(events) < 4 {
		t.Fatalf("expected structured events, got %+v", events)
	}

	// Turn 2 does NOT repeat RoleHint
	_, done2, err := adapter.StartTurn(context.Background(), handle, TurnStartSpec{Prompt: "follow up"}, nil)
	if err != nil {
		t.Fatalf("StartTurn 2: %v", err)
	}
	out2 := <-done2
	ans2, _, _, _ := SelectFinalAnswer(out2.State, out2.Items)
	if ans2 != "DSH_OK_2" {
		t.Fatalf("expected DSH_OK_2, got %q", ans2)
	}
	mu.Lock()
	if len(prompts) != 2 || !strings.Contains(prompts[0], "[Role Instructions]") || strings.Contains(prompts[1], "[Role Instructions]") {
		t.Fatalf("unexpected prompts: %+v", prompts)
	}
	mu.Unlock()

	if err := adapter.CloseThread(handle); err != nil {
		t.Fatalf("CloseThread: %v", err)
	}
	mu.Lock()
	if !closedSession {
		t.Fatalf("expected session/close RPC on CloseThread")
	}
	mu.Unlock()
}

func TestDeepSeekAdapterPermissionBlockedAndCancel(t *testing.T) {
	// Case 1: session/request_permission -> cancelled + session/cancel -> blocked_unsupported_approval
	var permReplied bool
	var cancelSent bool
	adapter := &DeepSeekAdapter{
		spawn: func(exe string, args []string, workdir string, env []string) (workerProcess, error) {
			p := newPipeFakeProc()
			go func() {
				defer p.finish()
				sc := bufio.NewScanner(p.inR)
				var promptID json.RawMessage
				for sc.Scan() {
					var msg struct {
						ID     json.RawMessage `json:"id"`
						Method string          `json:"method"`
						Result map[string]any  `json:"result"`
					}
					if json.Unmarshal(sc.Bytes(), &msg) != nil {
						continue
					}
					switch msg.Method {
					case "initialize":
						writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"protocolVersion": 1}})
					case "session/new":
						writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"sessionId": "dsh-blk"}})
					case "session/prompt":
						promptID = msg.ID
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "id": 99, "method": "session/request_permission",
							"params": map[string]any{"sessionId": "dsh-blk"},
						})
					case "session/cancel":
						cancelSent = true
						if len(promptID) > 0 {
							writeJSONLine(p.outW, map[string]any{
								"jsonrpc": "2.0", "id": promptID, "result": map[string]any{"stopReason": "cancelled"},
							})
						}
					default:
						if msg.Method == "" && msg.Result != nil {
							if oc, ok := msg.Result["outcome"].(map[string]any); ok && oc["outcome"] == "cancelled" {
								permReplied = true
							}
						}
					}
				}
			}()
			return p, nil
		},
	}

	h, err := adapter.OpenThread(context.Background(), ThreadOpenSpec{
		WorkerID: "wkr_blk", Executable: "dsh.cmd", WorkspaceRoot: t.TempDir(), AccessMode: harnesses.AccessReadOnly,
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	defer func() { _ = adapter.CloseThread(h) }()
	_, done, err := adapter.StartTurn(context.Background(), h, TurnStartSpec{Prompt: "write"}, nil)
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	out := <-done
	if out.State != TurnBlockedApproval || out.ErrorCode != TurnBlockedApproval {
		t.Fatalf("expected blocked_unsupported_approval, got %s / %s", out.State, out.ErrorCode)
	}
	if !permReplied || !cancelSent {
		t.Fatalf("expected permission cancel reply (%v) and session/cancel (%v)", permReplied, cancelSent)
	}

	// Case 2: InterruptTurn -> session/cancel -> TurnInterrupted without promoting partial text
	gotChunk := make(chan struct{}, 1)
	intAdapter := &DeepSeekAdapter{
		spawn: func(exe string, args []string, workdir string, env []string) (workerProcess, error) {
			p := newPipeFakeProc()
			go func() {
				defer p.finish()
				sc := bufio.NewScanner(p.inR)
				var promptID json.RawMessage
				for sc.Scan() {
					var msg struct {
						ID     json.RawMessage `json:"id"`
						Method string          `json:"method"`
					}
					if json.Unmarshal(sc.Bytes(), &msg) != nil {
						continue
					}
					switch msg.Method {
					case "initialize":
						writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"protocolVersion": 1}})
					case "session/new":
						writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"sessionId": "dsh-int"}})
					case "session/prompt":
						promptID = msg.ID
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "method": "session/update",
							"params": map[string]any{"update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "halfway"}}},
						})
					case "session/cancel":
						writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": promptID, "result": map[string]any{"stopReason": "cancelled"}})
					}
				}
			}()
			return p, nil
		},
	}
	h2, err := intAdapter.OpenThread(context.Background(), ThreadOpenSpec{
		WorkerID: "wkr_int", Executable: "dsh.cmd", WorkspaceRoot: t.TempDir(), AccessMode: harnesses.AccessReadOnly,
	})
	if err != nil {
		t.Fatalf("OpenThread int: %v", err)
	}
	defer func() { _ = intAdapter.CloseThread(h2) }()
	nid, done2, err := intAdapter.StartTurn(context.Background(), h2, TurnStartSpec{Prompt: "long"}, func(ev AdapterEvent) {
		if ev.Kind == "delta" {
			select {
			case gotChunk <- struct{}{}:
			default:
			}
		}
	})
	if err != nil {
		t.Fatalf("StartTurn int: %v", err)
	}
	select {
	case <-gotChunk:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for delta")
	}
	if err := intAdapter.InterruptTurn(context.Background(), h2, nid); err != nil {
		t.Fatalf("InterruptTurn: %v", err)
	}
	out2 := <-done2
	if out2.State != TurnInterrupted {
		t.Fatalf("expected interrupted, got %s", out2.State)
	}
	_, _, avail, partial := SelectFinalAnswer(out2.State, out2.Items)
	if avail || partial != "halfway" {
		t.Fatalf("expected partial_output without final answer, got avail=%v partial=%q", avail, partial)
	}
}

func TestDeepSeekAdapterResumeUnrecoverable(t *testing.T) {
	adapter := &DeepSeekAdapter{
		spawn: func(exe string, args []string, workdir string, env []string) (workerProcess, error) {
			p := newPipeFakeProc()
			go func() {
				defer p.finish()
				sc := bufio.NewScanner(p.inR)
				for sc.Scan() {
					var msg struct {
						ID     json.RawMessage `json:"id"`
						Method string          `json:"method"`
					}
					if json.Unmarshal(sc.Bytes(), &msg) != nil {
						continue
					}
					switch msg.Method {
					case "initialize":
						writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"protocolVersion": 1}})
					case "session/resume":
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "id": msg.ID,
							"error": map[string]any{"code": -32602, "message": "Invalid params: session is not resumable: bogus"},
						})
					}
				}
			}()
			return p, nil
		},
	}
	_, err := adapter.OpenThread(context.Background(), ThreadOpenSpec{
		WorkerID: "wkr_bad", Executable: "dsh.cmd", WorkspaceRoot: t.TempDir(),
		AccessMode: harnesses.AccessReadOnly, NativeSessionID: "bogus",
	})
	if err == nil || !strings.Contains(err.Error(), "native_session_unrecoverable") {
		t.Fatalf("expected native_session_unrecoverable, got %v", err)
	}
}

func TestDeepSeekAdapterToolCallSummariesAndFailureReason(t *testing.T) {
	adapter := &DeepSeekAdapter{
		spawn: func(exe string, args []string, workdir string, env []string) (workerProcess, error) {
			p := newPipeFakeProc()
			go func() {
				defer p.finish()
				sc := bufio.NewScanner(p.inR)
				for sc.Scan() {
					var msg struct {
						ID     json.RawMessage `json:"id"`
						Method string          `json:"method"`
					}
					if json.Unmarshal(sc.Bytes(), &msg) != nil {
						continue
					}
					switch msg.Method {
					case "initialize":
						writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"protocolVersion": 1}})
					case "session/new":
						writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"sessionId": "dsh-logs"}})
					case "session/prompt":
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "method": "session/update",
							"params": map[string]any{"update": map[string]any{
								"sessionUpdate": "tool_call",
								"toolCallId":    "tc_read",
								"title":         "read",
								"status":        "in_progress",
								"rawInput":      map[string]any{"file_path": `C:\repo\alpha.txt`},
							}},
						})
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "method": "session/update",
							"params": map[string]any{"update": map[string]any{
								"sessionUpdate": "tool_call_update",
								"toolCallId":    "tc_read",
								"status":        "completed",
								"content": []map[string]any{
									{"type": "content", "content": map[string]any{"type": "text", "text": "SECRET_FILE_BODY"}},
								},
							}},
						})
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "method": "session/update",
							"params": map[string]any{"update": map[string]any{
								"sessionUpdate": "tool_call",
								"toolCallId":    "tc_write",
								"title":         "write",
								"status":        "in_progress",
								"rawInput":      map[string]any{"file_path": `C:\repo\forbidden.txt`, "content": "DO_NOT_LOG_THIS_BODY"},
							}},
						})
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "method": "session/update",
							"params": map[string]any{"update": map[string]any{
								"sessionUpdate": "tool_call_update",
								"toolCallId":    "tc_write",
								"status":        "failed",
								"content": []map[string]any{
									{"type": "content", "content": map[string]any{"type": "text", "text": "Error: [sandbox: file access denied under read-only mode]\nsecond line detail"}},
								},
							}},
						})
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "method": "session/update",
							"params": map[string]any{"update": map[string]any{
								"sessionUpdate": "tool_call",
								"toolCallId":    "tc_pwsh",
								"title":         "pwsh",
								"status":        "in_progress",
								"rawInput":      map[string]any{"command": `Set-Content -Path write_probe.txt -Value OK`},
							}},
						})
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "method": "session/update",
							"params": map[string]any{"update": map[string]any{
								"sessionUpdate": "tool_call_update",
								"toolCallId":    "tc_pwsh",
								"status":        "completed",
							}},
						})
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "method": "session/update",
							"params": map[string]any{"update": map[string]any{
								"sessionUpdate": "tool_call",
								"toolCallId":    "tc_pwsh_fail",
								"title":         "pwsh",
								"status":        "in_progress",
								"rawInput":      map[string]any{"command": `Set-Content -Path denied.txt -Value BAD`},
							}},
						})
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "method": "session/update",
							"params": map[string]any{"update": map[string]any{
								"sessionUpdate": "tool_call_update",
								"toolCallId":    "tc_pwsh_fail",
								"status":        "completed",
								"content": []map[string]any{
									{"type": "content", "content": map[string]any{"type": "text", "text": "Set-Content : Access denied\n[exit code: 1]"}},
								},
							}},
						})
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "method": "session/update",
							"params": map[string]any{"update": map[string]any{
								"sessionUpdate": "agent_message_chunk",
								"content":       map[string]any{"type": "text", "text": "DONE"},
							}},
						})
						writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"stopReason": "end_turn"}})
					case "session/close":
						writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{}})
					}
				}
			}()
			return p, nil
		},
	}

	h, err := adapter.OpenThread(context.Background(), ThreadOpenSpec{
		WorkerID: "wkr_logs", Executable: "dsh.cmd", WorkspaceRoot: t.TempDir(), AccessMode: harnesses.AccessReadOnly,
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	defer func() { _ = adapter.CloseThread(h) }()

	var summaries []string
	_, done, err := adapter.StartTurn(context.Background(), h, TurnStartSpec{Prompt: "test"}, func(ev AdapterEvent) {
		if ev.Summary != "" {
			summaries = append(summaries, ev.Kind+":"+ev.Summary)
		}
	})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	out := <-done
	if out.State != TurnCompleted {
		t.Fatalf("expected completed, got %s", out.State)
	}
	joined := strings.Join(summaries, "\n")
	for _, want := range []string{
		`tool:read C:\repo\alpha.txt`,
		`tool:read C:\repo\alpha.txt (completed)`,
		`edit:write C:\repo\forbidden.txt`,
		`edit:write C:\repo\forbidden.txt (failed: Error: [sandbox: file access denied under read-only mode])`,
		`command:pwsh: Set-Content -Path write_probe.txt -Value OK`,
		`command:pwsh: Set-Content -Path write_probe.txt -Value OK (completed)`,
		`command:pwsh: Set-Content -Path denied.txt -Value BAD`,
		`command:pwsh: Set-Content -Path denied.txt -Value BAD (exit 1)`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected %q in logs:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "DO_NOT_LOG_THIS_BODY") || strings.Contains(joined, "SECRET_FILE_BODY") {
		t.Fatalf("tool log summaries must not leak file body contents:\n%s", joined)
	}
}

func TestDeepSeekAdapterMultiStageCommentaryAndParagraphSeparation(t *testing.T) {
	adapter := &DeepSeekAdapter{
		spawn: func(exe string, args []string, workdir string, env []string) (workerProcess, error) {
			p := newPipeFakeProc()
			go func() {
				defer p.finish()
				sc := bufio.NewScanner(p.inR)
				for sc.Scan() {
					var msg struct {
						ID     json.RawMessage `json:"id"`
						Method string          `json:"method"`
					}
					if json.Unmarshal(sc.Bytes(), &msg) != nil {
						continue
					}
					switch msg.Method {
					case "initialize":
						writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"protocolVersion": 1}})
					case "session/new":
						writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"sessionId": "dsh-stage"}})
					case "session/prompt":
						// Stage 1 pre-tool commentary
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "method": "session/update",
							"params": map[string]any{"update": map[string]any{
								"sessionUpdate": "agent_message_chunk",
								"content":       map[string]any{"type": "text", "text": "Inspecting requested files."},
							}},
						})
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "method": "session/update",
							"params": map[string]any{"update": map[string]any{
								"sessionUpdate": "tool_call", "toolCallId": "tc1", "title": "glob", "status": "in_progress",
								"rawInput": map[string]any{"pattern": "*.txt"},
							}},
						})
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "method": "session/update",
							"params": map[string]any{"update": map[string]any{
								"sessionUpdate": "tool_call_update", "toolCallId": "tc1", "status": "completed",
							}},
						})
						// Stage 2 pre-tool commentary
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "method": "session/update",
							"params": map[string]any{"update": map[string]any{
								"sessionUpdate": "agent_message_chunk",
								"content":       map[string]any{"type": "text", "text": "Files discovered. Now reading both files."},
							}},
						})
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "method": "session/update",
							"params": map[string]any{"update": map[string]any{
								"sessionUpdate": "tool_call", "toolCallId": "tc2", "title": "read", "status": "in_progress",
								"rawInput": map[string]any{"file_path": "alpha.txt"},
							}},
						})
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "method": "session/update",
							"params": map[string]any{"update": map[string]any{
								"sessionUpdate": "tool_call_update", "toolCallId": "tc2", "status": "completed",
							}},
						})
						// Final answer split by an intermediate thought chunk (must separate with \n\n, not glue!)
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "method": "session/update",
							"params": map[string]any{"update": map[string]any{
								"sessionUpdate": "agent_message_chunk",
								"content":       map[string]any{"type": "text", "text": "Inspection complete."},
							}},
						})
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "method": "session/update",
							"params": map[string]any{"update": map[string]any{
								"sessionUpdate": "agent_thought_chunk",
								"content":       map[string]any{"type": "text", "text": "double check"},
							}},
						})
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "method": "session/update",
							"params": map[string]any{"update": map[string]any{
								"sessionUpdate": "agent_message_chunk",
								"content":       map[string]any{"type": "text", "text": "All checks passed."},
							}},
						})
						writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"stopReason": "end_turn"}})
					case "session/close":
						writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{}})
					}
				}
			}()
			return p, nil
		},
	}

	h, err := adapter.OpenThread(context.Background(), ThreadOpenSpec{
		WorkerID: "wkr_stage", Executable: "dsh.cmd", WorkspaceRoot: t.TempDir(), AccessMode: harnesses.AccessReadOnly,
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	defer func() { _ = adapter.CloseThread(h) }()

	var commentaryLogs []string
	_, done, err := adapter.StartTurn(context.Background(), h, TurnStartSpec{Prompt: "inspect"}, func(ev AdapterEvent) {
		if ev.Kind == "commentary" {
			commentaryLogs = append(commentaryLogs, ev.Summary)
		}
	})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	out := <-done
	if out.State != TurnCompleted {
		t.Fatalf("expected completed, got %s", out.State)
	}
	ans, src, avail, _ := SelectFinalAnswer(out.State, out.Items)
	wantAns := "Inspection complete.\n\nAll checks passed."
	if !avail || src != AnswerSourceFinal || ans != wantAns {
		t.Fatalf("expected clean separated final answer %q, got avail=%v src=%s ans=%q", wantAns, avail, src, ans)
	}
	if len(commentaryLogs) != 2 || commentaryLogs[0] != "Inspecting requested files." || commentaryLogs[1] != "Files discovered. Now reading both files." {
		t.Fatalf("expected pre-tool transitions logged as commentary, got %+v", commentaryLogs)
	}
}

func TestDeepSeekAdapterPerTurnConfigOverridesDoNotLeakAcrossTurns(t *testing.T) {
	const (
		flashTuple = `["deepseek-official","deepseek-v4-flash"]`
		proTuple   = `["deepseek-official","deepseek-v4-pro"]`
	)

	type turnObserved struct {
		model  string
		effort string
	}

	newPeer := func(resetEffortOnModelSwitch bool) (*DeepSeekAdapter, *sync.Mutex, *[]turnObserved, *[]map[string]any) {
		var mu sync.Mutex
		var observed []turnObserved
		var setCalls []map[string]any
		adapter := &DeepSeekAdapter{
			spawn: func(exe string, args []string, workdir string, env []string) (workerProcess, error) {
				p := newPipeFakeProc()
				go func() {
					defer p.finish()
					activeModel := flashTuple
					activeEffort := "high"
					sc := bufio.NewScanner(p.inR)
					for sc.Scan() {
						var msg struct {
							ID     json.RawMessage `json:"id"`
							Method string          `json:"method"`
							Params map[string]any  `json:"params"`
						}
						if json.Unmarshal(sc.Bytes(), &msg) != nil {
							continue
						}
						switch msg.Method {
						case "initialize":
							writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"protocolVersion": 1}})
						case "session/new":
							writeJSONLine(p.outW, map[string]any{
								"jsonrpc": "2.0", "id": msg.ID,
								"result": map[string]any{
									"sessionId":     "dsh-cfg-sess",
									"configOptions": sampleACPConfigOptions(),
								},
							})
						case "session/set_config_option":
							cfgID, _ := msg.Params["configId"].(string)
							val, _ := msg.Params["value"].(string)
							if cfgID == "model" {
								activeModel = val
								if resetEffortOnModelSwitch {
									activeEffort = "off"
								}
							} else if cfgID == "reasoning_effort" {
								activeEffort = val
							}
							mu.Lock()
							setCalls = append(setCalls, map[string]any{"configId": cfgID, "value": val})
							mu.Unlock()
							res := map[string]any{}
							if resetEffortOnModelSwitch {
								opts := sampleACPConfigOptions()
								opts[0]["currentValue"] = activeModel
								opts[1]["currentValue"] = activeEffort
								res["configOptions"] = opts
							}
							writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": res})
						case "session/prompt":
							mu.Lock()
							observed = append(observed, turnObserved{model: activeModel, effort: activeEffort})
							mu.Unlock()
							writeJSONLine(p.outW, map[string]any{
								"jsonrpc": "2.0", "method": "session/update",
								"params": map[string]any{"update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "ok"}}},
							})
							writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"stopReason": "end_turn"}})
						case "session/close":
							writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{}})
						}
					}
				}()
				return p, nil
			},
		}
		return adapter, &mu, &observed, &setCalls
	}

	runTurn := func(t *testing.T, adapter *DeepSeekAdapter, h ThreadHandle, spec TurnStartSpec) {
		t.Helper()
		_, done, err := adapter.StartTurn(context.Background(), h, spec, nil)
		if err != nil {
			t.Fatalf("StartTurn(%+v): %v", spec, err)
		}
		out := <-done
		if out.State != TurnCompleted {
			t.Fatalf("expected completed, got %s (%s)", out.State, out.ErrorMessage)
		}
	}

	t.Run("native_default_restored_after_turn_override", func(t *testing.T) {
		adapter, mu, observed, setCalls := newPeer(false)
		h, err := adapter.OpenThread(context.Background(), ThreadOpenSpec{
			WorkerID: "wkr_dsh_nat", Executable: "dsh.cmd", WorkspaceRoot: t.TempDir(), AccessMode: harnesses.AccessReadOnly,
		})
		if err != nil {
			t.Fatalf("OpenThread: %v", err)
		}
		defer func() { _ = adapter.CloseThread(h) }()

		// Turn 1: omit both -> native Flash/high, 0 set_config_option calls.
		runTurn(t, adapter, h, TurnStartSpec{Prompt: "turn 1"})
		mu.Lock()
		n1 := len(*setCalls)
		mu.Unlock()
		if n1 != 0 {
			t.Fatalf("expected 0 config frames for default turn 1, got %d", n1)
		}

		// Turn 2: override both -> Pro/low, 2 set_config_option calls.
		runTurn(t, adapter, h, TurnStartSpec{Prompt: "turn 2", Model: "deepseek-v4-pro", ReasoningEffort: "low"})
		mu.Lock()
		n2 := len(*setCalls)
		mu.Unlock()
		if n2 != 2 {
			t.Fatalf("expected 2 config frames after turn 2 override, got %d", n2)
		}

		// Turn 3: omit both -> MUST restore native Flash/high (+2 frames).
		runTurn(t, adapter, h, TurnStartSpec{Prompt: "turn 3"})
		mu.Lock()
		n3 := len(*setCalls)
		mu.Unlock()
		if n3 != 4 {
			t.Fatalf("expected +2 config frames to restore native defaults on turn 3, got total %d", n3)
		}

		// Turn 4: override effort only -> Flash/max (+1 frame only).
		runTurn(t, adapter, h, TurnStartSpec{Prompt: "turn 4", ReasoningEffort: "max"})
		mu.Lock()
		n4 := len(*setCalls)
		mu.Unlock()
		if n4 != 5 {
			t.Fatalf("expected +1 config frame for effort-only override on turn 4, got total %d", n4)
		}

		// Turn 5: omit both -> restores effort to high (+1 frame only).
		runTurn(t, adapter, h, TurnStartSpec{Prompt: "turn 5"})
		mu.Lock()
		n5 := len(*setCalls)
		mu.Unlock()
		if n5 != 6 {
			t.Fatalf("expected +1 config frame to restore effort on turn 5, got total %d", n5)
		}

		// Turn 6: omit both again -> already at Flash/high (0 extra frames).
		runTurn(t, adapter, h, TurnStartSpec{Prompt: "turn 6"})
		mu.Lock()
		n6 := len(*setCalls)
		got := append([]turnObserved(nil), (*observed)...)
		mu.Unlock()
		if n6 != 6 {
			t.Fatalf("expected 0 extra config frames when turn 6 stays at native defaults, got total %d", n6)
		}

		want := []turnObserved{
			{model: flashTuple, effort: "high"},
			{model: proTuple, effort: "low"},
			{model: flashTuple, effort: "high"},
			{model: flashTuple, effort: "max"},
			{model: flashTuple, effort: "high"},
			{model: flashTuple, effort: "high"},
		}
		if !slices.Equal(got, want) {
			t.Fatalf("observed per-turn configs mismatch:\ngot:  %+v\nwant: %+v", got, want)
		}
	})

	t.Run("worker_default_restored_over_native_default", func(t *testing.T) {
		adapter, mu, observed, setCalls := newPeer(true)
		h, err := adapter.OpenThread(context.Background(), ThreadOpenSpec{
			WorkerID: "wkr_dsh_def", Executable: "dsh.cmd", WorkspaceRoot: t.TempDir(), AccessMode: harnesses.AccessReadOnly,
			Model: "deepseek-v4-pro", ReasoningEffort: "low",
		})
		if err != nil {
			t.Fatalf("OpenThread: %v", err)
		}
		defer func() { _ = adapter.CloseThread(h) }()

		// Turn 1: omit both -> Worker default Pro/low.
		runTurn(t, adapter, h, TurnStartSpec{Prompt: "turn 1"})
		// Turn 2: temporary override to native model Flash (peer resets effort to "off" on model switch;
		// adapter MUST detect returned configOptions and re-apply Worker default effort "low"!).
		runTurn(t, adapter, h, TurnStartSpec{Prompt: "turn 2", Model: "deepseek-v4-flash"})
		// Turn 3: omit both -> MUST restore Worker default Pro/low (NOT native Flash/high).
		runTurn(t, adapter, h, TurnStartSpec{Prompt: "turn 3"})

		mu.Lock()
		got := append([]turnObserved(nil), (*observed)...)
		calls := len(*setCalls)
		mu.Unlock()
		want := []turnObserved{
			{model: proTuple, effort: "low"},
			{model: flashTuple, effort: "low"},
			{model: proTuple, effort: "low"},
		}
		if !slices.Equal(got, want) {
			t.Fatalf("observed per-turn configs mismatch:\ngot:  %+v\nwant: %+v (set_config_option calls=%d)", got, want, calls)
		}
	})
}
