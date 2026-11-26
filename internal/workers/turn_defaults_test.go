package workers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/harnesses"
)

type observedTurnConfig struct {
	model      string
	effort     string
	sentModel  string
	sentEffort string
}

func setupHarnessForDefaultsTest(t *testing.T, harnessID string) (string, *harnesses.Service, string) {
	t.Helper()
	home := t.TempDir()
	wsDir := t.TempDir()
	hs, err := harnesses.Open(home, "")
	if err != nil {
		t.Fatal(err)
	}
	fakeExe := filepath.Join(home, harnessID+".cmd")
	if err := os.WriteFile(fakeExe, []byte("@echo off\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	enable := true
	if _, err := hs.UpdatePolicy(harnesses.UpdatePolicyInput{
		HarnessID:           harnessID,
		CollabWorkerEnabled: &enable,
		ManualPath:          &fakeExe,
	}); err != nil {
		t.Fatal(err)
	}
	return home, hs, wsDir
}

func TestDeepSeekCrossProcessIdleRecycleRestoresNativeDefaults(t *testing.T) {
	const (
		flashTuple = `["deepseek-official","deepseek-v4-flash"]`
		proTuple   = `["deepseek-official","deepseek-v4-pro"]`
	)

	var mu sync.Mutex
	// Persistent ACP server session state simulating real dsh 0.2.2-rc.5:
	// session/resume restores the last selected config rather than the original default.
	persistedModel := flashTuple
	persistedEffort := "high"
	spawns := 0
	resumes := 0
	var observed []observedTurnConfig

	makeAdapter := func() *DeepSeekAdapter {
		return &DeepSeekAdapter{
			spawn: func(exe string, args []string, workdir string, env []string) (workerProcess, error) {
				mu.Lock()
				spawns++
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
							writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"protocolVersion": 1}})
						case "session/new":
							mu.Lock()
							persistedModel = flashTuple
							persistedEffort = "high"
							opts := sampleACPConfigOptions()
							opts[0]["currentValue"] = persistedModel
							opts[1]["currentValue"] = persistedEffort
							mu.Unlock()
							writeJSONLine(p.outW, map[string]any{
								"jsonrpc": "2.0", "id": msg.ID,
								"result": map[string]any{"sessionId": "dsh-persist-1", "configOptions": opts},
							})
						case "session/resume":
							mu.Lock()
							resumes++
							opts := sampleACPConfigOptions()
							opts[0]["currentValue"] = persistedModel
							opts[1]["currentValue"] = persistedEffort
							mu.Unlock()
							writeJSONLine(p.outW, map[string]any{
								"jsonrpc": "2.0", "id": msg.ID,
								"result": map[string]any{"sessionId": "dsh-persist-1", "configOptions": opts},
							})
						case "session/set_config_option":
							cfgID, _ := msg.Params["configId"].(string)
							val, _ := msg.Params["value"].(string)
							mu.Lock()
							if cfgID == "model" {
								persistedModel = val
							} else if cfgID == "reasoning_effort" {
								persistedEffort = val
							}
							mu.Unlock()
							writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{}})
						case "session/prompt":
							mu.Lock()
							observed = append(observed, observedTurnConfig{model: persistedModel, effort: persistedEffort})
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
	}

	home, hs, wsDir := setupHarnessForDefaultsTest(t, harnesses.HarnessDeepSeek)
	svc, err := NewService(home, hs, makeAdapter())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	var clockMu sync.Mutex
	svc.now = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}

	w, err := svc.Create(context.Background(), CreateInput{
		OperationID:   "op_dsh_create",
		HarnessID:     harnesses.HarnessDeepSeek,
		WorkspaceRoot: wsDir,
		Alias:         "dsh-defaults",
	})
	if err != nil {
		t.Fatal(err)
	}

	sendAndWait := func(s *Service, opID, prompt, modelReq, effortReq string) {
		t.Helper()
		turn, err := s.Send(context.Background(), SendInput{
			OperationID:     opID,
			WorkerID:        w.ID,
			Prompt:          prompt,
			Model:           modelReq,
			ReasoningEffort: effortReq,
		})
		if err != nil {
			t.Fatalf("Send(%s): %v", opID, err)
		}
		got, _, _, err := s.Wait(context.Background(), turn.ID, w.WorkspaceID, 5*time.Second)
		if err != nil || got.State != TurnCompleted {
			t.Fatalf("Wait(%s): state=%s err=%v msg=%s", opID, got.State, err, got.ErrorMessage)
		}
	}

	// Turn 1: omit both -> native Flash/high; initial native defaults persisted onto Worker.
	sendAndWait(svc, "op_dsh_1", "turn 1", "", "")
	gotW, _, err := svc.Get(w.ID, w.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if gotW.NativeSessionID != "dsh-persist-1" || gotW.NativeDefaultModel != flashTuple || gotW.NativeDefaultReasoningEffort != "high" {
		t.Fatalf("expected initial native defaults persisted on worker, got %+v", gotW)
	}

	// Turn 2: temporary override -> Pro/low (now persisted in fake ACP session).
	sendAndWait(svc, "op_dsh_2", "turn 2", "deepseek-v4-pro", "low")

	// Simulate 10-minute idle recycle closing the resident DeepSeek process.
	clockMu.Lock()
	now = now.Add(11 * time.Minute)
	clockMu.Unlock()
	svc.reapIdleResidents()

	// Turn 3: omit both -> respawns via session/resume (which restores Pro/low),
	// MUST switch back to persisted original native defaults Flash/high!
	sendAndWait(svc, "op_dsh_3", "turn 3", "", "")

	// Turn 4: override to Pro/low again, then simulate full node restart (Close + reopen SQLite).
	sendAndWait(svc, "op_dsh_4", "turn 4", "deepseek-v4-pro", "low")
	if err := svc.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	svc2, err := NewService(home, hs, makeAdapter())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = svc2.Close(context.Background()) }()

	// Turn 5: omit both after full service restart -> MUST still restore Flash/high!
	sendAndWait(svc2, "op_dsh_5", "turn 5", "", "")

	mu.Lock()
	gotObs := append([]observedTurnConfig(nil), observed...)
	gotSpawns := spawns
	gotResumes := resumes
	mu.Unlock()

	if gotSpawns != 3 || gotResumes != 2 {
		t.Fatalf("expected 3 spawns and 2 session/resume calls across idle recycle + restart, got spawns=%d resumes=%d", gotSpawns, gotResumes)
	}
	want := []observedTurnConfig{
		{model: flashTuple, effort: "high"},
		{model: proTuple, effort: "low"},
		{model: flashTuple, effort: "high"},
		{model: proTuple, effort: "low"},
		{model: flashTuple, effort: "high"},
	}
	if !slices.Equal(gotObs, want) {
		t.Fatalf("DeepSeek cross-process turn configs mismatch:\ngot:  %+v\nwant: %+v", gotObs, want)
	}
}

func TestCodexAdapterPerTurnConfigOverridesDoNotLeakAcrossTurns(t *testing.T) {
	newStatefulCodexPeer := func() (func() *CodexAdapter, *sync.Mutex, *[]observedTurnConfig, *int, *int) {
		var mu sync.Mutex
		// Stateful Codex app-server thread store modeling real codex-cli 0.171.0:
		// turn/start model & effort overrides persist to subsequent turns and thread/resume.
		threadModel := "gpt-5.4"
		threadEffort := "high"
		spawns := 0
		resumes := 0
		turnSeq := 0
		var observed []observedTurnConfig

		makeAdapter := func() *CodexAdapter {
			return &CodexAdapter{
				spawn: func(exe string, args []string, workdir string, env []string) (workerProcess, error) {
					mu.Lock()
					spawns++
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
								writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{}})
							case "thread/start":
								mu.Lock()
								threadModel = "gpt-5.4"
								threadEffort = "high"
								if m, _ := msg.Params["model"].(string); m != "" {
									threadModel = m
								}
								curM, curE := threadModel, threadEffort
								mu.Unlock()
								writeJSONLine(p.outW, map[string]any{
									"jsonrpc": "2.0", "id": msg.ID,
									"result": map[string]any{
										"thread":          map[string]any{"id": "thr_codex_cfg"},
										"model":           curM,
										"reasoningEffort": curE,
									},
								})
							case "thread/resume":
								mu.Lock()
								resumes++
								if m, _ := msg.Params["model"].(string); m != "" {
									threadModel = m
								}
								curM, curE := threadModel, threadEffort
								mu.Unlock()
								writeJSONLine(p.outW, map[string]any{
									"jsonrpc": "2.0", "id": msg.ID,
									"result": map[string]any{
										"thread":          map[string]any{"id": "thr_codex_cfg"},
										"model":           curM,
										"reasoningEffort": curE,
									},
								})
							case "turn/start":
								sentM, _ := msg.Params["model"].(string)
								sentE, _ := msg.Params["effort"].(string)
								mu.Lock()
								turnSeq++
								tid := fmt.Sprintf("nt_%d", turnSeq)
								if sentM != "" {
									threadModel = sentM
								}
								if sentE != "" {
									threadEffort = sentE
								}
								observed = append(observed, observedTurnConfig{
									model:      threadModel,
									effort:     threadEffort,
									sentModel:  sentM,
									sentEffort: sentE,
								})
								mu.Unlock()
								writeJSONLine(p.outW, map[string]any{
									"jsonrpc": "2.0", "id": msg.ID,
									"result": map[string]any{"turn": map[string]any{"id": tid, "status": "inProgress"}},
								})
								writeJSONLine(p.outW, map[string]any{
									"jsonrpc": "2.0", "method": "item/completed",
									"params": map[string]any{
										"item": map[string]any{
											"id":    "item_" + tid,
											"type":  "agentMessage",
											"phase": "final_answer",
											"text":  "ok",
										},
									},
								})
								writeJSONLine(p.outW, map[string]any{
									"jsonrpc": "2.0", "method": "turn/completed",
									"params": map[string]any{"turn": map[string]any{"id": tid, "status": "completed"}},
								})
							}
						}
					}()
					return p, nil
				},
			}
		}
		return makeAdapter, &mu, &observed, &spawns, &resumes
	}

	runDirectTurn := func(t *testing.T, adapter *CodexAdapter, h ThreadHandle, spec TurnStartSpec) {
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

	t.Run("same_process_native_defaults_restored", func(t *testing.T) {
		makeAdapter, mu, observed, _, _ := newStatefulCodexPeer()
		adapter := makeAdapter()
		h, err := adapter.OpenThread(context.Background(), ThreadOpenSpec{
			WorkerID: "wkr_cdx_nat", Executable: "codex.exe", WorkspaceRoot: t.TempDir(), AccessMode: harnesses.AccessReadOnly,
		})
		if err != nil {
			t.Fatalf("OpenThread: %v", err)
		}
		defer func() { _ = adapter.CloseThread(h) }()

		if h.NativeDefaultModel != "gpt-5.4" || h.NativeDefaultReasoningEffort != "high" {
			t.Fatalf("expected handle native defaults gpt-5.4/high, got %q/%q", h.NativeDefaultModel, h.NativeDefaultReasoningEffort)
		}

		// Turn 1: omitted -> native gpt-5.4/high, zero redundant override fields sent.
		runDirectTurn(t, adapter, h, TurnStartSpec{Prompt: "turn 1"})
		// Turn 2: override both -> gpt-5.4-mini/low.
		runDirectTurn(t, adapter, h, TurnStartSpec{Prompt: "turn 2", Model: "gpt-5.4-mini", ReasoningEffort: "low"})
		// Turn 3: omitted -> MUST explicitly restore gpt-5.4/high.
		runDirectTurn(t, adapter, h, TurnStartSpec{Prompt: "turn 3"})
		// Turn 4: omitted again -> already at gpt-5.4/high, omit fields.
		runDirectTurn(t, adapter, h, TurnStartSpec{Prompt: "turn 4"})
		// Turn 5: effort-only override -> medium.
		runDirectTurn(t, adapter, h, TurnStartSpec{Prompt: "turn 5", ReasoningEffort: "medium"})
		// Turn 6: omitted -> explicitly restores effort only to high.
		runDirectTurn(t, adapter, h, TurnStartSpec{Prompt: "turn 6"})

		mu.Lock()
		got := append([]observedTurnConfig(nil), (*observed)...)
		mu.Unlock()
		want := []observedTurnConfig{
			{model: "gpt-5.4", effort: "high", sentModel: "", sentEffort: ""},
			{model: "gpt-5.4-mini", effort: "low", sentModel: "gpt-5.4-mini", sentEffort: "low"},
			{model: "gpt-5.4", effort: "high", sentModel: "gpt-5.4", sentEffort: "high"},
			{model: "gpt-5.4", effort: "high", sentModel: "", sentEffort: ""},
			{model: "gpt-5.4", effort: "medium", sentModel: "", sentEffort: "medium"},
			{model: "gpt-5.4", effort: "high", sentModel: "", sentEffort: "high"},
		}
		if !slices.Equal(got, want) {
			t.Fatalf("Codex same-process turn configs mismatch:\ngot:  %+v\nwant: %+v", got, want)
		}
	})

	t.Run("worker_explicit_defaults_restored_over_native", func(t *testing.T) {
		makeAdapter, mu, observed, _, _ := newStatefulCodexPeer()
		adapter := makeAdapter()
		h, err := adapter.OpenThread(context.Background(), ThreadOpenSpec{
			WorkerID: "wkr_cdx_def", Executable: "codex.exe", WorkspaceRoot: t.TempDir(), AccessMode: harnesses.AccessReadOnly,
			Model: "gpt-5.4-mini", ReasoningEffort: "medium",
		})
		if err != nil {
			t.Fatalf("OpenThread: %v", err)
		}
		defer func() { _ = adapter.CloseThread(h) }()

		runDirectTurn(t, adapter, h, TurnStartSpec{Prompt: "turn 1"})
		runDirectTurn(t, adapter, h, TurnStartSpec{Prompt: "turn 2", Model: "gpt-5.4", ReasoningEffort: "low"})
		runDirectTurn(t, adapter, h, TurnStartSpec{Prompt: "turn 3"})

		mu.Lock()
		got := append([]observedTurnConfig(nil), (*observed)...)
		mu.Unlock()
		want := []observedTurnConfig{
			{model: "gpt-5.4-mini", effort: "medium", sentModel: "", sentEffort: "medium"},
			{model: "gpt-5.4", effort: "low", sentModel: "gpt-5.4", sentEffort: "low"},
			{model: "gpt-5.4-mini", effort: "medium", sentModel: "gpt-5.4-mini", sentEffort: "medium"},
		}
		if !slices.Equal(got, want) {
			t.Fatalf("Codex worker-default turn configs mismatch:\ngot:  %+v\nwant: %+v", got, want)
		}
	})

	t.Run("cross_process_idle_recycle_and_restart_restores_native_defaults", func(t *testing.T) {
		makeAdapter, mu, observed, spawns, resumes := newStatefulCodexPeer()
		home, hs, wsDir := setupHarnessForDefaultsTest(t, harnesses.HarnessCodex)
		svc, err := NewService(home, hs, makeAdapter())
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		var clockMu sync.Mutex
		svc.now = func() time.Time {
			clockMu.Lock()
			defer clockMu.Unlock()
			return now
		}

		w, err := svc.Create(context.Background(), CreateInput{
			OperationID:   "op_cdx_create",
			HarnessID:     harnesses.HarnessCodex,
			WorkspaceRoot: wsDir,
			Alias:         "cdx-defaults",
		})
		if err != nil {
			t.Fatal(err)
		}

		sendAndWait := func(s *Service, opID, prompt, modelReq, effortReq string) {
			t.Helper()
			turn, err := s.Send(context.Background(), SendInput{
				OperationID:     opID,
				WorkerID:        w.ID,
				Prompt:          prompt,
				Model:           modelReq,
				ReasoningEffort: effortReq,
			})
			if err != nil {
				t.Fatalf("Send(%s): %v", opID, err)
			}
			got, _, _, err := s.Wait(context.Background(), turn.ID, w.WorkspaceID, 5*time.Second)
			if err != nil || got.State != TurnCompleted {
				t.Fatalf("Wait(%s): state=%s err=%v msg=%s", opID, got.State, err, got.ErrorMessage)
			}
		}

		// Turn 1: native gpt-5.4/high persisted onto Worker.
		sendAndWait(svc, "op_cdx_1", "turn 1", "", "")
		gotW, _, err := svc.Get(w.ID, w.WorkspaceID)
		if err != nil {
			t.Fatal(err)
		}
		if gotW.NativeDefaultModel != "gpt-5.4" || gotW.NativeDefaultReasoningEffort != "high" {
			t.Fatalf("expected Codex native defaults persisted on worker, got %+v", gotW)
		}

		// Turn 2: temporary override -> gpt-5.4-mini/low.
		sendAndWait(svc, "op_cdx_2", "turn 2", "gpt-5.4-mini", "low")

		// Idle recycle resident Codex process.
		clockMu.Lock()
		now = now.Add(11 * time.Minute)
		clockMu.Unlock()
		svc.reapIdleResidents()

		// Turn 3: omitted after thread/resume -> restores gpt-5.4/high!
		sendAndWait(svc, "op_cdx_3", "turn 3", "", "")

		// Turn 4: override again -> full service restart -> Turn 5 omitted restores gpt-5.4/high!
		sendAndWait(svc, "op_cdx_4", "turn 4", "gpt-5.4-mini", "low")
		if err := svc.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		svc2, err := NewService(home, hs, makeAdapter())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = svc2.Close(context.Background()) }()
		sendAndWait(svc2, "op_cdx_5", "turn 5", "", "")

		mu.Lock()
		got := append([]observedTurnConfig(nil), (*observed)...)
		gotSpawns := *spawns
		gotResumes := *resumes
		mu.Unlock()
		if gotSpawns != 3 || gotResumes != 2 {
			t.Fatalf("expected 3 spawns and 2 thread/resume calls, got spawns=%d resumes=%d", gotSpawns, gotResumes)
		}
		want := []observedTurnConfig{
			{model: "gpt-5.4", effort: "high", sentModel: "", sentEffort: ""},
			{model: "gpt-5.4-mini", effort: "low", sentModel: "gpt-5.4-mini", sentEffort: "low"},
			{model: "gpt-5.4", effort: "high", sentModel: "gpt-5.4", sentEffort: "high"},
			{model: "gpt-5.4-mini", effort: "low", sentModel: "gpt-5.4-mini", sentEffort: "low"},
			{model: "gpt-5.4", effort: "high", sentModel: "gpt-5.4", sentEffort: "high"},
		}
		if !slices.Equal(got, want) {
			t.Fatalf("Codex cross-process turn configs mismatch:\ngot:  %+v\nwant: %+v", got, want)
		}
	})
}

func TestCodexImplicitReasoningEffortResetAndPersistence(t *testing.T) {
	type collabCapture struct {
		usedCollab bool
		mode       string
		model      string
		effortNil  bool
		devInstr   *string
	}
	var mu sync.Mutex
	threadModel := "gpt-5.4"
	threadEffort := "" // implicit/auto (null in protocol)
	resumeMode := "default"
	var resumeDevInstr *string
	experimentalAPIRequested := false
	turnSeq := 0
	var observed []observedTurnConfig
	var collabSeen []collabCapture

	makeAdapter := func() *CodexAdapter {
		return &CodexAdapter{
			spawn: func(exe string, args []string, workdir string, env []string) (workerProcess, error) {
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
							if caps, ok := msg.Params["capabilities"].(map[string]any); ok {
								if exp, _ := caps["experimentalApi"].(bool); exp {
									mu.Lock()
									experimentalAPIRequested = true
									mu.Unlock()
								}
							}
							writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{}})
						case "thread/start":
							mu.Lock()
							threadModel = "gpt-5.4"
							threadEffort = ""
							curM := threadModel
							mu.Unlock()
							writeJSONLine(p.outW, map[string]any{
								"jsonrpc": "2.0", "id": msg.ID,
								"result": map[string]any{
									"thread":          map[string]any{"id": "thr_implicit"},
									"model":           curM,
									"reasoningEffort": nil,
								},
							})
						case "thread/resume":
							mu.Lock()
							curM, curE := threadModel, threadEffort
							curMode := resumeMode
							var devAny any
							if resumeDevInstr != nil {
								devAny = *resumeDevInstr
							}
							mu.Unlock()
							var effAny any
							if curE != "" {
								effAny = curE
							}
							writeJSONLine(p.outW, map[string]any{
								"jsonrpc": "2.0", "id": msg.ID,
								"result": map[string]any{
									"thread":          map[string]any{"id": "thr_implicit"},
									"model":           curM,
									"reasoningEffort": effAny,
									"collaborationMode": map[string]any{
										"mode": curMode,
										"settings": map[string]any{
											"model":                  curM,
											"reasoning_effort":       effAny,
											"developer_instructions": devAny,
										},
									},
								},
							})
						case "turn/start":
							mu.Lock()
							turnSeq++
							tid := fmt.Sprintf("nt_imp_%d", turnSeq)
							var capRec collabCapture
							if cm, ok := msg.Params["collaborationMode"].(map[string]any); ok {
								capRec.usedCollab = true
								capRec.mode, _ = cm["mode"].(string)
								if st, ok := cm["settings"].(map[string]any); ok {
									capRec.model, _ = st["model"].(string)
									if capRec.model != "" {
										threadModel = capRec.model
									}
									if effVal, exists := st["reasoning_effort"]; exists && effVal == nil {
										capRec.effortNil = true
										threadEffort = ""
									} else if effStr, ok := effVal.(string); ok {
										threadEffort = effStr
									}
									if di, ok := st["developer_instructions"].(string); ok {
										v := di
										capRec.devInstr = &v
									}
								}
							} else {
								// Mimic codex-cli 0.171.0: omitted or null effort preserves prior effort!
								if m, _ := msg.Params["model"].(string); m != "" {
									threadModel = m
								}
								if e, _ := msg.Params["effort"].(string); e != "" {
									threadEffort = e
								}
							}
							sentM, _ := msg.Params["model"].(string)
							sentE, _ := msg.Params["effort"].(string)
							observed = append(observed, observedTurnConfig{
								model:      threadModel,
								effort:     threadEffort,
								sentModel:  sentM,
								sentEffort: sentE,
							})
							collabSeen = append(collabSeen, capRec)
							mu.Unlock()
							writeJSONLine(p.outW, map[string]any{
								"jsonrpc": "2.0", "id": msg.ID,
								"result": map[string]any{"turn": map[string]any{"id": tid, "status": "inProgress"}},
							})
							writeJSONLine(p.outW, map[string]any{
								"jsonrpc": "2.0", "method": "item/completed",
								"params": map[string]any{"item": map[string]any{"id": "item_" + tid, "type": "agentMessage", "phase": "final_answer", "text": "ok"}},
							})
							writeJSONLine(p.outW, map[string]any{
								"jsonrpc": "2.0", "method": "turn/completed",
								"params": map[string]any{"turn": map[string]any{"id": tid, "status": "completed"}},
							})
						}
					}
				}()
				return p, nil
			},
		}
	}

	home, hs, wsDir := setupHarnessForDefaultsTest(t, harnesses.HarnessCodex)
	svc, err := NewService(home, hs, makeAdapter())
	if err != nil {
		t.Fatal(err)
	}
	w, err := svc.Create(context.Background(), CreateInput{
		OperationID:   "op_imp_create",
		HarnessID:     harnesses.HarnessCodex,
		WorkspaceRoot: wsDir,
		Alias:         "codex-implicit",
		RoleHint:      "Act as a strict reviewer.",
	})
	if err != nil {
		t.Fatal(err)
	}

	sendAndWait := func(s *Service, opID, prompt, modelReq, effortReq string) {
		t.Helper()
		turn, err := s.Send(context.Background(), SendInput{
			OperationID:     opID,
			WorkerID:        w.ID,
			Prompt:          prompt,
			Model:           modelReq,
			ReasoningEffort: effortReq,
		})
		if err != nil {
			t.Fatalf("Send(%s): %v", opID, err)
		}
		got, _, _, err := s.Wait(context.Background(), turn.ID, w.WorkspaceID, 5*time.Second)
		if err != nil || got.State != TurnCompleted {
			t.Fatalf("Wait(%s): state=%s err=%v msg=%s", opID, got.State, err, got.ErrorMessage)
		}
	}

	// Turn 1: implicit auto effort ("") captured and persisted with NativeDefaultsCaptured=true.
	sendAndWait(svc, "op_imp_1", "t1", "", "")
	gotW, _, err := svc.Get(w.ID, w.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if !gotW.NativeDefaultsCaptured || gotW.NativeDefaultModel != "gpt-5.4" || gotW.NativeDefaultReasoningEffort != "" {
		t.Fatalf("expected captured implicit defaults (true, gpt-5.4, \"\"), got %+v", gotW)
	}

	// Turn 2: override effort="low".
	sendAndWait(svc, "op_imp_2", "t2", "", "low")
	// Turn 3: omit -> MUST reset effort back to "" via collaborationMode and sync local cache.
	sendAndWait(svc, "op_imp_3", "t3", "", "")
	// Turn 4: override model="gpt-5.4-mini" AND effort="low" -> proves local cache was synced on Turn 3!
	sendAndWait(svc, "op_imp_4", "t4", "gpt-5.4-mini", "low")

	// Restart service; peer returns resumed mode="plan" + custom developer_instructions + effort="low".
	if err := svc.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	customInstr := "custom mode instructions"
	mu.Lock()
	resumeMode = "plan"
	resumeDevInstr = &customInstr
	mu.Unlock()

	svc2, err := NewService(home, hs, makeAdapter())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = svc2.Close(context.Background()) }()

	// Turn 5: omit after restart -> MUST reset to gpt-5.4/"" preserving plan mode + custom instructions,
	// and MUST NOT overwrite stored NativeDefaultReasoningEffort with "low".
	sendAndWait(svc2, "op_imp_5", "t5", "", "")

	gotW2, _, err := svc2.Get(w.ID, w.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if !gotW2.NativeDefaultsCaptured || gotW2.NativeDefaultModel != "gpt-5.4" || gotW2.NativeDefaultReasoningEffort != "" {
		t.Fatalf("restart must not backfill low into stored implicit defaults, got %+v", gotW2)
	}

	mu.Lock()
	expOK := experimentalAPIRequested
	gotObs := append([]observedTurnConfig(nil), observed...)
	gotCollab := append([]collabCapture(nil), collabSeen...)
	mu.Unlock()

	if !expOK {
		t.Fatal("expected initialize to request capabilities.experimentalApi=true")
	}
	wantObs := []observedTurnConfig{
		{model: "gpt-5.4", effort: "", sentModel: "", sentEffort: ""},
		{model: "gpt-5.4", effort: "low", sentModel: "", sentEffort: "low"},
		{model: "gpt-5.4", effort: "", sentModel: "", sentEffort: ""},
		{model: "gpt-5.4-mini", effort: "low", sentModel: "gpt-5.4-mini", sentEffort: "low"},
		{model: "gpt-5.4", effort: "", sentModel: "", sentEffort: ""},
	}
	if !slices.Equal(gotObs, wantObs) {
		t.Fatalf("Codex implicit effort turns mismatch:\ngot:  %+v\nwant: %+v", gotObs, wantObs)
	}
	if !gotCollab[2].usedCollab || gotCollab[2].mode != "default" || gotCollab[2].model != "gpt-5.4" || !gotCollab[2].effortNil || gotCollab[2].devInstr != nil {
		t.Fatalf("unexpected Turn 3 collaborationMode reset: %+v", gotCollab[2])
	}
	if !gotCollab[4].usedCollab || gotCollab[4].mode != "plan" || gotCollab[4].model != "gpt-5.4" || !gotCollab[4].effortNil || gotCollab[4].devInstr == nil || *gotCollab[4].devInstr != customInstr {
		t.Fatalf("unexpected Turn 5 collaborationMode reset (must preserve mode & instructions): %+v", gotCollab[4])
	}
}

func TestLegacyUncapturedWorkerResumeDoesNotPoisonStoredDefaults(t *testing.T) {
	const proTuple = `["deepseek-official","deepseek-v4-pro"]`

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
						opts := sampleACPConfigOptions()
						opts[0]["currentValue"] = proTuple
						opts[1]["currentValue"] = "low"
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "id": msg.ID,
							"result": map[string]any{"sessionId": "dsh-legacy-sess", "configOptions": opts},
						})
					case "session/prompt":
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

	home, hs, wsDir := setupHarnessForDefaultsTest(t, harnesses.HarnessDeepSeek)
	svc, err := NewService(home, hs, adapter)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = svc.Close(context.Background()) }()

	w, err := svc.Create(context.Background(), CreateInput{
		OperationID:   "op_leg_create",
		HarnessID:     harnesses.HarnessDeepSeek,
		WorkspaceRoot: wsDir,
		Alias:         "legacy-dsh",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate pre-upgrade Worker record: has NativeSessionID, no NativeDefaultsCaptured.
	w.NativeSessionID = "dsh-legacy-sess"
	if err := svc.store.SaveWorker(w); err != nil {
		t.Fatal(err)
	}

	turn, err := svc.Send(context.Background(), SendInput{
		OperationID: "op_leg_1",
		WorkerID:    w.ID,
		Prompt:      "continue legacy",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _, _, err := svc.Wait(context.Background(), turn.ID, w.WorkspaceID, 5*time.Second)
	if err != nil || got.State != TurnCompleted {
		t.Fatalf("legacy resume wait failed: state=%s err=%v msg=%s", got.State, err, got.ErrorMessage)
	}

	afterW, _, err := svc.Get(w.ID, w.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if afterW.NativeDefaultsCaptured || afterW.NativeDefaultModel != "" || afterW.NativeDefaultReasoningEffort != "" {
		t.Fatalf("resuming uncaptured legacy worker must not persist resumed override as native defaults, got %+v", afterW)
	}
}
