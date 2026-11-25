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

type workerProcess interface {
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
	Done() <-chan struct{}
	Kill() error
}

type spawnProcessFunc func(executable string, args []string, workdir string, env []string) (workerProcess, error)

func defaultSpawnWorkerProcess(executable string, args []string, workdir string, env []string) (workerProcess, error) {
	return spawnManagedProcess(executable, args, workdir, env)
}

type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func newTailBuffer(maxBytes int) *tailBuffer {
	if maxBytes <= 0 {
		maxBytes = 4096
	}
	return &tailBuffer{max: maxBytes}
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.max {
		b.buf = append([]byte(nil), b.buf[len(b.buf)-b.max:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(b.buf))
}

type ClaudeAdapter struct {
	spawn spawnProcessFunc
}

func NewClaudeAdapter() *ClaudeAdapter {
	return &ClaudeAdapter{spawn: defaultSpawnWorkerProcess}
}

func (a *ClaudeAdapter) HarnessID() string { return harnesses.HarnessClaudeCode }

type claudeSession struct {
	adapter       *ClaudeAdapter
	openSpec      ThreadOpenSpec
	sessionID     string
	currentModel  string
	currentEffort string
	hasSentTurn   bool

	writeMu sync.Mutex
	nextReq int64

	mu       sync.Mutex
	proc     workerProcess
	stderr   *tailBuffer
	pending  map[string]chan claudeControlReply
	active   *claudeActiveTurn
	closed   bool
	closeErr error
}

type claudeControlReply struct {
	err error
}

type claudeActiveTurn struct {
	turnID        string
	items         []NativeItem
	onEvent       func(AdapterEvent)
	onInteraction InteractionHandler
	turnCtx       context.Context
	turnCancel    context.CancelFunc
	done          chan TurnOutcome
	doneOnce      sync.Once
	interrupted   bool
	blockedCode   string
	blockedMsg    string
}

func (t *claudeActiveTurn) finish(out TurnOutcome) {
	t.doneOnce.Do(func() {
		if t.turnCancel != nil {
			t.turnCancel()
		}
		t.done <- out
		close(t.done)
	})
}

func (a *ClaudeAdapter) OpenThread(ctx context.Context, spec ThreadOpenSpec) (ThreadHandle, error) {
	if strings.TrimSpace(spec.Executable) == "" {
		return ThreadHandle{}, errors.New("claude executable not found")
	}
	resume := strings.TrimSpace(spec.NativeSessionID) != ""
	sid := strings.TrimSpace(spec.NativeSessionID)
	if sid == "" {
		sid = newUUIDv4()
	}
	sess := &claudeSession{
		adapter:       a,
		openSpec:      spec,
		sessionID:     sid,
		currentModel:  strings.TrimSpace(spec.Model),
		currentEffort: strings.TrimSpace(spec.ReasoningEffort),
		hasSentTurn:   resume,
		pending:       map[string]chan claudeControlReply{},
	}
	if err := sess.spawnAndInit(ctx, resume, sess.currentModel, sess.currentEffort); err != nil {
		return ThreadHandle{}, err
	}
	return ThreadHandle{
		Key:             spec.WorkerID + ":" + sid,
		NativeSessionID: sid,
		ProcessRef:      sess,
	}, nil
}

func (s *claudeSession) spawnAndInit(ctx context.Context, resume bool, modelName, effort string) error {
	spawnFn := s.adapter.spawn
	if spawnFn == nil {
		spawnFn = defaultSpawnWorkerProcess
	}
	args := buildClaudeArgs(s.openSpec, s.sessionID, resume, modelName, effort)
	proc, err := spawnFn(s.openSpec.Executable, args, s.openSpec.WorkspaceRoot, sanitizedWorkerEnv())
	if err != nil {
		return err
	}
	stderrBuf := newTailBuffer(4096)
	s.mu.Lock()
	s.proc = proc
	s.stderr = stderrBuf
	s.pending = map[string]chan claudeControlReply{}
	s.closed = false
	s.closeErr = nil
	s.currentModel = strings.TrimSpace(modelName)
	s.currentEffort = strings.TrimSpace(effort)
	s.mu.Unlock()

	go func() {
		_, _ = io.Copy(stderrBuf, proc.Stderr())
	}()
	go s.readLoop(proc)

	initCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := s.sendControl(initCtx, map[string]any{"subtype": "initialize"}); err != nil {
		_ = proc.Kill()
		if isClaudeSessionMissing(err.Error(), stderrBuf.String()) {
			return fmt.Errorf("native_session_unrecoverable: %w", err)
		}
		return fmt.Errorf("claude initialize failed: %w", err)
	}
	return nil
}

func (s *claudeSession) ensureReady(ctx context.Context, wantModel, wantEffort string) error {
	wantModel = firstNonEmpty(wantModel, s.openSpec.Model)
	wantEffort = firstNonEmpty(wantEffort, s.openSpec.ReasoningEffort)

	s.mu.Lock()
	needRespawn := s.closed || s.proc == nil || wantModel != s.currentModel || wantEffort != s.currentEffort
	oldProc := s.proc
	resume := s.hasSentTurn || strings.TrimSpace(s.openSpec.NativeSessionID) != ""
	s.mu.Unlock()

	if !needRespawn {
		return nil
	}
	if oldProc != nil {
		_ = oldProc.Stdin().Close()
		select {
		case <-oldProc.Done():
		case <-time.After(1500 * time.Millisecond):
			_ = oldProc.Kill()
			select {
			case <-oldProc.Done():
			case <-time.After(2 * time.Second):
			}
		}
	}
	return s.spawnAndInit(ctx, resume, wantModel, wantEffort)
}

func (a *ClaudeAdapter) StartTurn(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, fmt.Errorf("%w: %v", ErrPreStartFailed, err)
	}
	sess, ok := handle.ProcessRef.(*claudeSession)
	if !ok || sess == nil {
		return "", nil, fmt.Errorf("%w: invalid claude handle", ErrPreStartFailed)
	}
	if err := sess.ensureReady(ctx, spec.Model, spec.ReasoningEffort); err != nil {
		return "", nil, fmt.Errorf("%w: %v", ErrPreStartFailed, err)
	}

	turnID := strings.TrimSpace(spec.ClientCorrelationID)
	if turnID == "" {
		turnID = "clturn_" + model.ID()[:12]
	}
	turnCtx, turnCancel := context.WithCancel(context.Background())
	active := &claudeActiveTurn{
		turnID:        turnID,
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
		return "", nil, fmt.Errorf("%w: claude session closed", ErrPreStartFailed)
	}
	sess.active = active
	sess.mu.Unlock()

	payload := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": spec.Prompt,
		},
	}
	if err := sess.writeJSONCtx(ctx, payload); err != nil {
		sess.mu.Lock()
		if sess.active == active {
			sess.active = nil
		}
		sess.mu.Unlock()
		turnCancel()
		return "", nil, fmt.Errorf("claude send turn failed: %w", err)
	}
	sess.mu.Lock()
	sess.hasSentTurn = true
	sess.mu.Unlock()
	return turnID, active.done, nil
}

func (a *ClaudeAdapter) InterruptTurn(ctx context.Context, handle ThreadHandle, nativeTurnID string) error {
	sess, ok := handle.ProcessRef.(*claudeSession)
	if !ok || sess == nil {
		return nil
	}
	sess.mu.Lock()
	if sess.active != nil {
		sess.active.interrupted = true
	}
	sess.mu.Unlock()
	intCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return sess.sendControl(intCtx, map[string]any{"subtype": "interrupt"})
}

func (a *ClaudeAdapter) SteerTurn(_ context.Context, handle ThreadHandle, nativeTurnID, text string) error {
	sess, ok := handle.ProcessRef.(*claudeSession)
	if !ok || sess == nil {
		return errors.New("claude session is not active")
	}
	sess.mu.Lock()
	running := sess.proc != nil && sess.active != nil && (nativeTurnID == "" || sess.active.turnID == nativeTurnID)
	sess.mu.Unlock()
	if !running {
		return errors.New("claude turn is no longer running")
	}
	return sess.writeJSON(map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": text,
		},
	})
}

func (a *ClaudeAdapter) ForceKill(handle ThreadHandle) error {
	sess, ok := handle.ProcessRef.(*claudeSession)
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

func (a *ClaudeAdapter) InspectThreadTurns(ctx context.Context, handle ThreadHandle) ([]NativeTurnRecord, error) {
	// Claude Code stream-json does not expose a native turn-history RPC.
	return nil, nil
}

func (a *ClaudeAdapter) CloseThread(handle ThreadHandle) error {
	sess, ok := handle.ProcessRef.(*claudeSession)
	if !ok || sess == nil {
		return nil
	}
	sess.mu.Lock()
	proc := sess.proc
	sess.closed = true
	sess.mu.Unlock()
	if proc == nil {
		return nil
	}
	_ = proc.Stdin().Close()
	select {
	case <-proc.Done():
		return nil
	case <-time.After(1500 * time.Millisecond):
		return proc.Kill()
	}
}

func (s *claudeSession) writeJSON(v any) error {
	return s.writeJSONCtx(nil, v)
}

func (s *claudeSession) writeJSONCtx(ctx context.Context, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPreStartFailed, err)
	}
	b = append(b, '\n')
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	closed := s.closed
	proc := s.proc
	s.mu.Unlock()
	if closed || proc == nil {
		if ctx != nil {
			if cErr := ctx.Err(); cErr != nil {
				return fmt.Errorf("%w: %v", ErrPreStartFailed, cErr)
			}
		}
		return fmt.Errorf("%w: claude process is not running", ErrPreStartFailed)
	}
	// Final arbitration point after all locking and marshaling, immediately before Write.
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%w: %v", ErrPreStartFailed, err)
		}
	}
	_, err = proc.Stdin().Write(b)
	return err
}

func (s *claudeSession) sendControl(ctx context.Context, req map[string]any) error {
	s.writeMu.Lock()
	s.nextReq++
	reqID := fmt.Sprintf("ctrl_%d", s.nextReq)
	s.writeMu.Unlock()

	ch := make(chan claudeControlReply, 1)
	s.mu.Lock()
	if s.closed {
		err := s.closeErr
		if err == nil {
			err = errors.New("claude session closed")
		}
		s.mu.Unlock()
		return err
	}
	s.pending[reqID] = ch
	s.mu.Unlock()

	msg := map[string]any{
		"type":       "control_request",
		"request_id": reqID,
		"request":    req,
	}
	if err := s.writeJSON(msg); err != nil {
		s.mu.Lock()
		delete(s.pending, reqID)
		s.mu.Unlock()
		return err
	}

	select {
	case reply := <-ch:
		return reply.err
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pending, reqID)
		s.mu.Unlock()
		return ctx.Err()
	}
}

func (s *claudeSession) readLoop(proc workerProcess) {
	scanner := bufio.NewScanner(proc.Stdout())
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		s.handleLine(line)
	}
	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	s.onProcessExit(proc, err)
}

func (s *claudeSession) onProcessExit(proc workerProcess, readErr error) {
	s.mu.Lock()
	if s.proc != proc {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.closeErr = readErr
	stderrText := ""
	if s.stderr != nil {
		stderrText = s.stderr.String()
	}
	for id, ch := range s.pending {
		ch <- claudeControlReply{err: fmt.Errorf("claude exited: %v (%s)", readErr, stderrText)}
		delete(s.pending, id)
	}
	active := s.active
	s.active = nil
	s.mu.Unlock()

	if active != nil {
		state := TurnFailed
		code := "claude_process_exited"
		msg := firstNonEmpty(stderrText, readErr.Error())
		if active.blockedCode != "" {
			state, code, msg = active.blockedCode, active.blockedCode, active.blockedMsg
		} else if active.interrupted {
			state, code, msg = TurnInterrupted, "cancelled", "Interrupted"
		} else if isClaudeSessionMissing(msg, stderrText) {
			code = "native_session_unrecoverable"
		}
		active.finish(TurnOutcome{
			State:        state,
			NativeTurnID: active.turnID,
			Items:        active.items,
			ErrorCode:    code,
			ErrorMessage: msg,
			LocalClose:   true,
		})
	}
}

func (s *claudeSession) handleLine(line string) {
	var env struct {
		Type           string          `json:"type"`
		Subtype        string          `json:"subtype"`
		RequestID      string          `json:"request_id"`
		SessionID      string          `json:"session_id"`
		Model          string          `json:"model"`
		PermissionMode string          `json:"permissionMode"`
		IsError        bool            `json:"is_error"`
		Result         string          `json:"result"`
		Errors         []string        `json:"errors"`
		Request        json.RawMessage `json:"request"`
		Response       json.RawMessage `json:"response"`
		Message        json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal([]byte(line), &env); err != nil {
		return
	}
	switch env.Type {
	case "control_response":
		s.handleControlResponse(env.Response)
	case "control_request":
		s.handleControlRequest(env.RequestID, env.Request)
	case "system":
		if env.Subtype == "init" {
			summary := "Claude session initialized"
			if env.Model != "" {
				summary = "Claude session initialized (" + env.Model + ")"
			}
			s.emit(AdapterEvent{Kind: "system", Summary: summary})
			if mapped := mapClaudePermissionMode(env.PermissionMode); mapped != "" {
				s.emit(AdapterEvent{Kind: "approval_mode", Summary: mapped})
			}
		}
	case "stream_event":
		s.emit(AdapterEvent{Kind: "delta", Bytes: len(line)})
		if prog := summarizeClaudeStreamStart(line); prog != "" {
			s.emit(AdapterEvent{Kind: "progress", Summary: prog})
		}
	case "assistant":
		s.handleAssistant(env.Message)
	case "user":
		if claudeMessageHasToolResult(env.Message) {
			s.emit(AdapterEvent{Kind: "progress", Summary: "Tool completed; waiting for model..."})
		}
	case "result":
		s.handleResult(env.Subtype, env.IsError, env.Result, env.Errors)
	}
}

func (s *claudeSession) handleControlResponse(raw json.RawMessage) {
	var resp struct {
		Subtype   string `json:"subtype"`
		RequestID string `json:"request_id"`
		Error     string `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil || resp.RequestID == "" {
		return
	}
	var err error
	if resp.Subtype == "error" || resp.Error != "" {
		err = errors.New(firstNonEmpty(resp.Error, "control request failed"))
	}
	s.mu.Lock()
	ch := s.pending[resp.RequestID]
	delete(s.pending, resp.RequestID)
	s.mu.Unlock()
	if ch != nil {
		ch <- claudeControlReply{err: err}
	}
}

func mapClaudePermissionMode(raw string) string {
	switch strings.TrimSpace(raw) {
	case "auto":
		return ApprovalModeAuto
	case "acceptEdits", "default", "manual":
		return ApprovalModeAskHuman
	case "dontAsk":
		return ApprovalModeDenyEscalation
	default:
		return ""
	}
}

func (s *claudeSession) handleControlRequest(reqID string, raw json.RawMessage) {
	var req struct {
		Subtype  string          `json:"subtype"`
		ToolName string          `json:"tool_name"`
		Input    json.RawMessage `json:"input"`
	}
	_ = json.Unmarshal(raw, &req)
	if req.Subtype != "can_use_tool" {
		_ = s.writeJSON(map[string]any{
			"type": "control_response",
			"response": map[string]any{
				"subtype":    "error",
				"request_id": reqID,
				"error":      "unsupported control request",
			},
		})
		return
	}

	s.mu.Lock()
	active := s.active
	var handler InteractionHandler
	var turnCtx context.Context
	if active != nil {
		handler = active.onInteraction
		turnCtx = active.turnCtx
	}
	s.mu.Unlock()

	if handler != nil && active != nil {
		ireq := parseClaudeInteractionRequest(req.ToolName, req.Input)
		if turnCtx == nil {
			turnCtx = context.Background()
		}
		go func() {
			dec, err := handler(turnCtx, ireq)
			if err != nil {
				dec = InteractionDecision{Decision: DecisionCancelTurn, Message: err.Error()}
			}
			respPayload, shouldInterrupt := buildClaudeInteractionReply(req.ToolName, req.Input, ireq, dec)
			if shouldInterrupt {
				s.mu.Lock()
				if s.active == active {
					if dec.Actor == "policy" {
						if ireq.Kind == InteractionInput {
							s.active.blockedCode = TurnBlockedUserInput
						} else {
							s.active.blockedCode = TurnBlockedApproval
						}
						s.active.blockedMsg = firstNonEmptyStr(dec.Message, "Blocked interactive request")
					} else {
						s.active.interrupted = true
					}
				}
				s.mu.Unlock()
			}
			_ = s.writeJSON(map[string]any{
				"type": "control_response",
				"response": map[string]any{
					"subtype":    "success",
					"request_id": reqID,
					"response":   respPayload,
				},
			})
			if shouldInterrupt {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_ = s.sendControl(ctx, map[string]any{"subtype": "interrupt"})
			}
		}()
		return
	}

	blockedCode := TurnBlockedApproval
	_, toolDesc := summarizeClaudeToolUse(req.ToolName, req.Input)
	blockedMsg := fmt.Sprintf("Claude Code requested interactive approval for %s, which is not supported in this milestone", firstNonEmpty(toolDesc, req.ToolName, "tool"))
	if strings.EqualFold(req.ToolName, "AskUserQuestion") {
		blockedCode = TurnBlockedUserInput
		blockedMsg = "Claude Code requested interactive user input, which is not supported in this milestone"
	}
	s.mu.Lock()
	if s.active != nil && s.active.blockedCode == "" {
		s.active.blockedCode = blockedCode
		s.active.blockedMsg = blockedMsg
	}
	s.mu.Unlock()
	s.emit(AdapterEvent{Kind: "system", Summary: blockedMsg})

	_ = s.writeJSON(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": reqID,
			"response": map[string]any{
				"behavior":  "deny",
				"message":   blockedMsg,
				"interrupt": true,
			},
		},
	})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.sendControl(ctx, map[string]any{"subtype": "interrupt"})
	}()
}

func (s *claudeSession) handleAssistant(raw json.RawMessage) {
	var msg struct {
		ID      string `json:"id"`
		Content []struct {
			Type  string          `json:"type"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Text  string          `json:"text"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return
	}
	for _, b := range msg.Content {
		switch b.Type {
		case "thinking":
			s.emit(AdapterEvent{Kind: "progress", ItemID: msg.ID, Summary: "Thinking..."})
		case "tool_use":
			kind, summary := summarizeClaudeToolUse(b.Name, b.Input)
			s.emit(AdapterEvent{Kind: kind, ItemID: b.ID, Summary: summary})
		case "text":
			txt := strings.TrimSpace(b.Text)
			if txt == "" {
				continue
			}
			item := NativeItem{
				ItemID: firstNonEmpty(msg.ID, "clmsg_"+model.ID()[:8]),
				Type:   "agentMessage",
				Phase:  "partial_answer",
				Text:   txt,
			}
			s.mu.Lock()
			if s.active != nil {
				s.active.items = append(s.active.items, item)
			}
			s.mu.Unlock()
			s.emit(AdapterEvent{Kind: "commentary", ItemID: item.ItemID, Summary: truncateSummary(txt, 160), Item: &item})
		}
	}
}

func (s *claudeSession) handleResult(subtype string, isError bool, resultText string, errList []string) {
	s.mu.Lock()
	active := s.active
	s.active = nil
	stderrText := ""
	if s.stderr != nil {
		stderrText = s.stderr.String()
	}
	s.mu.Unlock()
	if active == nil {
		return
	}

	if active.blockedCode != "" {
		active.finish(TurnOutcome{
			State:        active.blockedCode,
			NativeTurnID: active.turnID,
			Items:        active.items,
			ErrorCode:    active.blockedCode,
			ErrorMessage: active.blockedMsg,
		})
		return
	}
	if active.interrupted {
		active.finish(TurnOutcome{
			State:        TurnInterrupted,
			NativeTurnID: active.turnID,
			Items:        active.items,
			ErrorCode:    "cancelled",
			ErrorMessage: "Interrupted",
		})
		return
	}
	if isError {
		errMsg := firstNonEmpty(strings.TrimSpace(resultText), strings.Join(errList, "; "), stderrText, subtype, "Claude turn failed")
		code := "claude_turn_failed"
		if isClaudeSessionMissing(errMsg, stderrText) {
			code = "native_session_unrecoverable"
		}
		active.finish(TurnOutcome{
			State:        TurnFailed,
			NativeTurnID: active.turnID,
			Items:        active.items,
			ErrorCode:    code,
			ErrorMessage: errMsg,
		})
		return
	}
	if txt := strings.TrimSpace(resultText); txt != "" {
		finalItem := NativeItem{
			ItemID: active.turnID + "_final",
			Type:   "agentMessage",
			Phase:  "final_answer",
			Text:   txt,
		}
		active.items = append(active.items, finalItem)
		if active.onEvent != nil {
			active.onEvent(AdapterEvent{Kind: "item", ItemID: finalItem.ItemID, Phase: "final_answer", Summary: truncateSummary(txt, 160), Item: &finalItem})
		}
	}
	active.finish(TurnOutcome{
		State:        TurnCompleted,
		NativeTurnID: active.turnID,
		Items:        active.items,
	})
}

func (s *claudeSession) emit(ev AdapterEvent) {
	s.mu.Lock()
	active := s.active
	s.mu.Unlock()
	if active != nil && active.onEvent != nil {
		active.onEvent(ev)
	}
}
