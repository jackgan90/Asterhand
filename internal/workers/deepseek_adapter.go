package workers

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/jackgan90/Asterhand/internal/harnesses"
	"github.com/jackgan90/Asterhand/internal/model"
)

type DeepSeekAdapter struct {
	spawn spawnProcessFunc
}

func NewDeepSeekAdapter() *DeepSeekAdapter {
	return &DeepSeekAdapter{spawn: defaultSpawnWorkerProcess}
}

func (a *DeepSeekAdapter) HarnessID() string { return harnesses.HarnessDeepSeek }

type deepseekSession struct {
	adapter  *DeepSeekAdapter
	openSpec ThreadOpenSpec

	writeMu sync.Mutex
	nextID  int64

	mu            sync.Mutex
	proc          workerProcess
	stderr        *tailBuffer
	sessionID     string
	modelOptions  map[string]string // normalized key -> exact ACP option value
	effortOptions map[string]string // normalized key -> exact ACP option value
	currentModel  string
	currentEffort string
	roleHint      string
	roleInjected  bool
	pending       map[int64]chan rpcReply
	active        *deepseekActiveTurn
	closed        bool
	closeErr      error
}

type deepseekActiveTurn struct {
	turnID             string
	answerBuf          strings.Builder
	commentaryItems    []NativeItem
	commentarySeq      int
	needParagraphBreak bool
	toolTitles         map[string]string
	toolKinds          map[string]string
	toolRawInputs      map[string]json.RawMessage
	onEvent            func(AdapterEvent)
	onInteraction      InteractionHandler
	turnCtx            context.Context
	turnCancel         context.CancelFunc
	done               chan TurnOutcome
	doneOnce           sync.Once
	interrupted        bool
	blockedCode        string
	blockedMsg         string
}

func (t *deepseekActiveTurn) finish(out TurnOutcome) {
	t.doneOnce.Do(func() {
		if t.turnCancel != nil {
			t.turnCancel()
		}
		t.done <- out
		close(t.done)
	})
}

func (a *DeepSeekAdapter) OpenThread(ctx context.Context, spec ThreadOpenSpec) (ThreadHandle, error) {
	if strings.TrimSpace(spec.Executable) == "" {
		return ThreadHandle{}, errors.New("deepseek-harness executable not found")
	}
	sess := &deepseekSession{
		adapter:       a,
		openSpec:      spec,
		sessionID:     strings.TrimSpace(spec.NativeSessionID),
		modelOptions:  map[string]string{},
		effortOptions: map[string]string{},
		roleHint:      strings.TrimSpace(spec.RoleHint),
		roleInjected:  strings.TrimSpace(spec.NativeSessionID) != "",
		pending:       map[int64]chan rpcReply{},
	}
	if err := sess.spawnAndInit(ctx); err != nil {
		return ThreadHandle{}, err
	}
	return ThreadHandle{
		Key:             spec.WorkerID + ":" + sess.sessionID,
		NativeSessionID: sess.sessionID,
		ProcessRef:      sess,
	}, nil
}

func (s *deepseekSession) spawnAndInit(ctx context.Context) error {
	spawnFn := s.adapter.spawn
	if spawnFn == nil {
		spawnFn = defaultSpawnWorkerProcess
	}
	permMode := "read-only"
	if s.openSpec.AccessMode == harnesses.AccessWorkspaceWrite {
		permMode = "workspace-write"
	}
	env := sanitizedWorkerEnvWith(map[string]string{
		"DSH_PERMISSION_MODE": permMode,
	})
	proc, err := spawnFn(s.openSpec.Executable, []string{"--profile", "acp"}, s.openSpec.WorkspaceRoot, env)
	if err != nil {
		return err
	}
	stderrBuf := newTailBuffer(4096)
	s.mu.Lock()
	s.proc = proc
	s.stderr = stderrBuf
	s.pending = map[int64]chan rpcReply{}
	s.closed = false
	s.closeErr = nil
	s.mu.Unlock()

	go func() {
		_, _ = io.Copy(stderrBuf, proc.Stderr())
	}()
	go s.readLoop(proc)

	initCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	if _, err := s.request(initCtx, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs":       map[string]any{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
		"clientInfo": map[string]any{"name": "asterhand", "version": "1.0"},
	}); err != nil {
		_ = proc.Kill()
		return fmt.Errorf("deepseek acp initialize failed: %w", err)
	}

	var rawSession json.RawMessage
	if s.sessionID != "" {
		rawSession, err = s.request(initCtx, "session/resume", map[string]any{
			"sessionId":  s.sessionID,
			"cwd":        s.openSpec.WorkspaceRoot,
			"mcpServers": []any{},
		})
		if err != nil {
			_ = proc.Kill()
			return fmt.Errorf("native_session_unrecoverable: %w", err)
		}
	} else {
		rawSession, err = s.request(initCtx, "session/new", map[string]any{
			"cwd":        s.openSpec.WorkspaceRoot,
			"mcpServers": []any{},
		})
		if err != nil {
			_ = proc.Kill()
			return fmt.Errorf("deepseek acp session/new failed: %w", err)
		}
	}
	var sessResp struct {
		SessionID     string            `json:"sessionId"`
		ConfigOptions []acpConfigOption `json:"configOptions"`
	}
	if err := json.Unmarshal(rawSession, &sessResp); err != nil {
		_ = proc.Kill()
		return fmt.Errorf("invalid deepseek session response: %w", err)
	}
	if s.sessionID == "" {
		if strings.TrimSpace(sessResp.SessionID) == "" {
			_ = proc.Kill()
			return errors.New("deepseek acp session/new returned empty sessionId")
		}
		s.sessionID = strings.TrimSpace(sessResp.SessionID)
	}
	s.indexConfigOptions(sessResp.ConfigOptions)

	if err := s.applyConfigOverrides(initCtx, s.openSpec.Model, s.openSpec.ReasoningEffort); err != nil {
		_ = proc.Kill()
		return err
	}
	return nil
}

func (a *DeepSeekAdapter) StartTurn(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
	sess, ok := handle.ProcessRef.(*deepseekSession)
	if !ok || sess == nil {
		return "", nil, fmt.Errorf("%w: invalid deepseek handle", ErrPreStartFailed)
	}
	sess.mu.Lock()
	needRespawn := sess.closed || sess.proc == nil
	sess.mu.Unlock()
	if needRespawn {
		if err := sess.spawnAndInit(ctx); err != nil {
			return "", nil, fmt.Errorf("%w: %v", ErrPreStartFailed, err)
		}
	}
	if err := sess.applyConfigOverrides(ctx, spec.Model, spec.ReasoningEffort); err != nil {
		return "", nil, fmt.Errorf("%w: %v", ErrPreStartFailed, err)
	}

	turnID := strings.TrimSpace(spec.ClientCorrelationID)
	if turnID == "" {
		turnID = "dshturn_" + model.ID()[:12]
	}
	turnCtx, turnCancel := context.WithCancel(context.Background())
	active := &deepseekActiveTurn{
		turnID:        turnID,
		toolTitles:    map[string]string{},
		toolKinds:     map[string]string{},
		toolRawInputs: map[string]json.RawMessage{},
		onEvent:       onEvent,
		onInteraction: spec.OnInteraction,
		turnCtx:       turnCtx,
		turnCancel:    turnCancel,
		done:          make(chan TurnOutcome, 1),
	}
	sess.mu.Lock()
	if sess.closed || sess.proc == nil {
		sess.mu.Unlock()
		turnCancel()
		return "", nil, errors.New("deepseek session closed")
	}
	sess.active = active
	promptText := spec.Prompt
	if sess.roleHint != "" && !sess.roleInjected {
		promptText = "[Role Instructions]\n" + sess.roleHint + "\n\n" + promptText
		sess.roleInjected = true
	}
	sid := sess.sessionID
	sess.mu.Unlock()

	go sess.runPrompt(sid, promptText, active)
	return turnID, active.done, nil
}

func (s *deepseekSession) runPrompt(sid, promptText string, active *deepseekActiveTurn) {
	raw, err := s.request(context.Background(), "session/prompt", map[string]any{
		"sessionId": sid,
		"prompt": []map[string]any{
			{"type": "text", "text": promptText},
		},
	})

	s.mu.Lock()
	if s.active == active {
		s.active = nil
	}
	commentary := append([]NativeItem(nil), active.commentaryItems...)
	answerText := strings.TrimSpace(active.answerBuf.String())
	blockedCode := active.blockedCode
	blockedMsg := active.blockedMsg
	interrupted := active.interrupted
	s.mu.Unlock()

	if blockedCode != "" {
		items := buildDeepSeekItems(commentary, active.turnID, answerText, "partial_answer")
		active.finish(TurnOutcome{
			State:        blockedCode,
			NativeTurnID: active.turnID,
			Items:        items,
			ErrorCode:    blockedCode,
			ErrorMessage: blockedMsg,
		})
		return
	}
	if err != nil {
		items := buildDeepSeekItems(commentary, active.turnID, answerText, "partial_answer")
		if interrupted {
			active.finish(TurnOutcome{
				State:        TurnInterrupted,
				NativeTurnID: active.turnID,
				Items:        items,
				ErrorCode:    "cancelled",
				ErrorMessage: "Interrupted",
			})
			return
		}
		active.finish(TurnOutcome{
			State:        TurnFailed,
			NativeTurnID: active.turnID,
			Items:        items,
			ErrorCode:    "deepseek_turn_failed",
			ErrorMessage: err.Error(),
		})
		return
	}

	var resp struct {
		StopReason string `json:"stopReason"`
	}
	_ = json.Unmarshal(raw, &resp)
	if resp.StopReason == "cancelled" || interrupted {
		items := buildDeepSeekItems(commentary, active.turnID, answerText, "partial_answer")
		active.finish(TurnOutcome{
			State:        TurnInterrupted,
			NativeTurnID: active.turnID,
			Items:        items,
			ErrorCode:    "cancelled",
			ErrorMessage: "Interrupted",
		})
		return
	}

	items := buildDeepSeekItems(commentary, active.turnID, answerText, "final_answer")
	if answerText != "" && active.onEvent != nil {
		item := items[len(items)-1]
		active.onEvent(AdapterEvent{
			Kind:    "item",
			ItemID:  item.ItemID,
			Phase:   "final_answer",
			Summary: truncateSummary(answerText, 160),
			Item:    &item,
		})
	}
	active.finish(TurnOutcome{
		State:        TurnCompleted,
		NativeTurnID: active.turnID,
		Items:        items,
	})
}

func (a *DeepSeekAdapter) InterruptTurn(ctx context.Context, handle ThreadHandle, nativeTurnID string) error {
	sess, ok := handle.ProcessRef.(*deepseekSession)
	if !ok || sess == nil {
		return nil
	}
	sess.mu.Lock()
	if sess.active != nil {
		sess.active.interrupted = true
	}
	sid := sess.sessionID
	sess.mu.Unlock()
	if sid == "" {
		return nil
	}
	return sess.notify("session/cancel", map[string]any{"sessionId": sid})
}

func (a *DeepSeekAdapter) ForceKill(handle ThreadHandle) error {
	sess, ok := handle.ProcessRef.(*deepseekSession)
	if !ok || sess == nil {
		return nil
	}
	sess.mu.Lock()
	proc := sess.proc
	sess.closed = true
	sess.mu.Unlock()
	if proc != nil {
		return proc.Kill()
	}
	return nil
}

func (a *DeepSeekAdapter) InspectThreadTurns(ctx context.Context, handle ThreadHandle) ([]NativeTurnRecord, error) {
	// ACP v1 does not provide a turn-history inspection RPC.
	return nil, nil
}

func (a *DeepSeekAdapter) CloseThread(handle ThreadHandle) error {
	sess, ok := handle.ProcessRef.(*deepseekSession)
	if !ok || sess == nil {
		return nil
	}
	sess.mu.Lock()
	proc := sess.proc
	sid := sess.sessionID
	closed := sess.closed
	sess.mu.Unlock()
	if proc == nil {
		return nil
	}
	if !closed && sid != "" {
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = sess.request(closeCtx, "session/close", map[string]any{"sessionId": sid})
		cancel()
	}
	sess.mu.Lock()
	sess.closed = true
	sess.mu.Unlock()
	_ = proc.Stdin().Close()
	select {
	case <-proc.Done():
		return nil
	case <-time.After(1500 * time.Millisecond):
		return proc.Kill()
	}
}

func (s *deepseekSession) writeJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	proc := s.proc
	s.mu.Unlock()
	if proc == nil {
		return errors.New("deepseek process is not running")
	}
	_, err = proc.Stdin().Write(b)
	return err
}

func (s *deepseekSession) notify(method string, params any) error {
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		msg["params"] = params
	}
	return s.writeJSON(msg)
}

func (s *deepseekSession) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	s.writeMu.Lock()
	s.nextID++
	id := s.nextID
	s.writeMu.Unlock()

	ch := make(chan rpcReply, 1)
	s.mu.Lock()
	if s.closed {
		err := s.closeErr
		if err == nil {
			err = errors.New("deepseek session closed")
		}
		s.mu.Unlock()
		return nil, err
	}
	s.pending[id] = ch
	s.mu.Unlock()

	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	if err := s.writeJSON(msg); err != nil {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, err
	}
	select {
	case rep := <-ch:
		return rep.result, rep.err
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (s *deepseekSession) readLoop(proc workerProcess) {
	scanner := bufio.NewScanner(proc.Stdout())
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		s.handleMessage(line)
	}
	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	s.mu.Lock()
	if s.proc != proc {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.closeErr = err
	stderrText := ""
	if s.stderr != nil {
		stderrText = s.stderr.String()
	}
	for id, ch := range s.pending {
		ch <- rpcReply{err: fmt.Errorf("deepseek acp exited: %v (%s)", err, stderrText)}
		delete(s.pending, id)
	}
	s.mu.Unlock()
}

func (s *deepseekSession) handleMessage(line string) {
	var msg struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		return
	}
	if msg.Method == "" && len(msg.ID) > 0 {
		var id int64
		if err := json.Unmarshal(msg.ID, &id); err != nil {
			return
		}
		s.mu.Lock()
		ch := s.pending[id]
		delete(s.pending, id)
		s.mu.Unlock()
		if ch != nil {
			if msg.Error != nil {
				ch <- rpcReply{err: fmt.Errorf("rpc error %d: %s", msg.Error.Code, msg.Error.Message)}
			} else {
				ch <- rpcReply{result: msg.Result}
			}
		}
		return
	}
	if msg.Method != "" && len(msg.ID) > 0 {
		s.handleServerRequest(msg.ID, msg.Method, msg.Params)
		return
	}
	if msg.Method == "session/update" {
		s.handleSessionUpdate(msg.Params)
	}
}

func (s *deepseekSession) handleServerRequest(rawID json.RawMessage, method string, params json.RawMessage) {
	var idVal any
	_ = json.Unmarshal(rawID, &idVal)
	if method != "session/request_permission" {
		_ = s.writeJSON(map[string]any{"jsonrpc": "2.0", "id": idVal, "error": map[string]any{"code": -32601, "message": "unsupported client request"}})
		return
	}
	var req struct {
		ToolCall struct {
			ToolCallID string `json:"toolCallId"`
			Title      string `json:"title"`
		} `json:"toolCall"`
	}
	_ = json.Unmarshal(params, &req)
	s.mu.Lock()
	active, sid := s.active, s.sessionID
	detail := req.ToolCall.Title
	var fallbackRaw json.RawMessage
	var fallbackKind string
	if active != nil && req.ToolCall.ToolCallID != "" {
		if detail == "" {
			detail = active.toolTitles[req.ToolCall.ToolCallID]
		}
		fallbackRaw = active.toolRawInputs[req.ToolCall.ToolCallID]
		fallbackKind = active.toolKinds[req.ToolCall.ToolCallID]
	}
	var handler InteractionHandler
	var turnCtx context.Context
	if active != nil {
		handler, turnCtx = active.onInteraction, active.turnCtx
	}
	s.mu.Unlock()
	if handler != nil && active != nil {
		ireq, allowOpts, rejectOpts := parseDeepSeekPermissionRequest(params, fallbackRaw, detail, fallbackKind)
		if turnCtx == nil {
			turnCtx = context.Background()
		}
		go resolveDeepSeekPermission(s, idVal, sid, active, handler, turnCtx, ireq, allowOpts, rejectOpts)
		return
	}
	rejectDeepSeekLegacyPermission(s, idVal, sid, active, detail)
}

func (s *deepseekSession) handleSessionUpdate(raw json.RawMessage) {
	var env struct {
		Update struct {
			SessionUpdate string          `json:"sessionUpdate"`
			ToolCallID    string          `json:"toolCallId"`
			Title         string          `json:"title"`
			Kind          string          `json:"kind"`
			Status        string          `json:"status"`
			RawInput      json.RawMessage `json:"rawInput"`
			RawOutput     json.RawMessage `json:"rawOutput"`
			Content       json.RawMessage `json:"content"`
		} `json:"update"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return
	}
	u := env.Update
	s.mu.Lock()
	active := s.active
	if active == nil {
		s.mu.Unlock()
		return
	}
	switch u.SessionUpdate {
	case "agent_message_chunk":
		chunkBytes := 0
		if txt := extractACPChunkText(u.Content); txt != "" {
			chunkBytes = len(txt)
			if active.needParagraphBreak {
				if active.answerBuf.Len() > 0 && !strings.HasSuffix(active.answerBuf.String(), "\n") && !strings.HasPrefix(txt, "\n") {
					active.answerBuf.WriteString("\n\n")
				}
				active.needParagraphBreak = false
			}
			active.answerBuf.WriteString(txt)
		}
		onEvent := active.onEvent
		s.mu.Unlock()
		if onEvent != nil {
			onEvent(AdapterEvent{Kind: "delta", Bytes: chunkBytes})
		}
		return
	case "agent_thought_chunk":
		if active.answerBuf.Len() > 0 {
			active.needParagraphBreak = true
		}
		onEvent := active.onEvent
		s.mu.Unlock()
		if onEvent != nil {
			onEvent(AdapterEvent{Kind: "progress", Summary: "Thinking...", Bytes: len(raw)})
		}
		return
	case "tool_call":
		var flushed *NativeItem
		if pre := strings.TrimSpace(active.answerBuf.String()); pre != "" {
			active.commentarySeq++
			it := NativeItem{
				ItemID: fmt.Sprintf("%s_c%d", active.turnID, active.commentarySeq),
				Type:   "agentMessage",
				Phase:  "commentary",
				Text:   pre,
			}
			active.commentaryItems = append(active.commentaryItems, it)
			flushed = &it
		}
		active.answerBuf.Reset()
		active.needParagraphBreak = false

		baseTitle := firstNonEmpty(u.Title, u.Kind, "tool")
		kind := classifyDeepSeekToolKind(baseTitle)
		label := summarizeDeepSeekToolCall(baseTitle, u.RawInput)
		if u.ToolCallID != "" {
			active.toolTitles[u.ToolCallID] = label
			active.toolKinds[u.ToolCallID] = kind
			if len(u.RawInput) > 0 {
				active.toolRawInputs[u.ToolCallID] = append(json.RawMessage(nil), u.RawInput...)
			}
		}
		onEvent := active.onEvent
		s.mu.Unlock()
		if onEvent != nil {
			if flushed != nil {
				onEvent(AdapterEvent{
					Kind:    "commentary",
					ItemID:  flushed.ItemID,
					Phase:   "commentary",
					Summary: truncateSummary(flushed.Text, 160),
					Item:    flushed,
				})
			}
			onEvent(AdapterEvent{Kind: kind, ItemID: u.ToolCallID, Summary: truncateSummary(label, 160)})
		}
		return
	case "tool_call_update":
		prev := active.toolTitles[u.ToolCallID]
		kind := active.toolKinds[u.ToolCallID]
		baseTitle := firstNonEmpty(u.Title, u.Kind, "tool")
		if kind == "" {
			kind = classifyDeepSeekToolKind(baseTitle)
			if u.ToolCallID != "" {
				active.toolKinds[u.ToolCallID] = kind
			}
		}
		label := prev
		if label == "" || len(u.RawInput) > 0 {
			label = summarizeDeepSeekToolCall(firstNonEmpty(u.Title, prev, u.Kind, "tool"), u.RawInput)
			if u.ToolCallID != "" && label != "" {
				active.toolTitles[u.ToolCallID] = label
			}
		}
		onEvent := active.onEvent
		s.mu.Unlock()
		if onEvent != nil && (u.Status == "completed" || u.Status == "failed") {
			summary := label + " (" + u.Status + ")"
			if u.Status == "failed" {
				if reason := extractACPToolFailure(u.Content, u.RawOutput); reason != "" {
					summary = label + " (failed: " + reason + ")"
				}
			} else if kind == "command" {
				if code := extractACPNonZeroExitCode(u.Content); code != "" {
					summary = label + " (exit " + code + ")"
				}
			}
			onEvent(AdapterEvent{
				Kind:    kind,
				ItemID:  u.ToolCallID,
				Summary: truncateSummary(summary, 200),
			})
		}
		return
	case "usage_update":
		onEvent := active.onEvent
		s.mu.Unlock()
		if onEvent != nil {
			onEvent(AdapterEvent{Kind: "progress"})
		}
		return
	default:
		s.mu.Unlock()
	}
}
