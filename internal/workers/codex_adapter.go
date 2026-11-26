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
)

type CodexAdapter struct {
	spawn spawnProcessFunc
}

func NewCodexAdapter() *CodexAdapter { return &CodexAdapter{spawn: defaultSpawnWorkerProcess} }

func (a *CodexAdapter) HarnessID() string { return harnesses.HarnessCodex }

type codexSession struct {
	proc     workerProcess
	openSpec ThreadOpenSpec
	threadID string

	writeMu sync.Mutex
	nextID  int64

	mu                    sync.Mutex
	nativeCaptured        bool
	nativeModel           string
	nativeEffort          string
	currentModel          string
	currentEffort         string
	collabMode            string
	collabDevInstructions *string
	pending               map[int64]chan rpcReply
	active                *codexActiveTurn
	closed                bool
	closeErr              error
}

type rpcReply struct {
	result     json.RawMessage
	err        error
	localClose bool
}

type codexActiveTurn struct {
	turnID           string
	items            []NativeItem
	deltas           map[string]*strings.Builder
	handledQuestions map[string]bool
	onEvent          func(AdapterEvent)
	onInteraction    InteractionHandler
	turnCtx          context.Context
	turnCancel       context.CancelFunc
	done             chan TurnOutcome
	doneOnce         sync.Once
	blockedCode      string
	blockedMsg       string
}

func (a *CodexAdapter) OpenThread(ctx context.Context, spec ThreadOpenSpec) (ThreadHandle, error) {
	if strings.TrimSpace(spec.Executable) == "" {
		return ThreadHandle{}, errors.New("codex executable not found")
	}
	args := []string{
		"app-server",
		"-c", `mcp_servers.asterhand={command='disabled',enabled=false}`,
	}
	spawnFn := a.spawn
	if spawnFn == nil {
		spawnFn = defaultSpawnWorkerProcess
	}
	mp, err := spawnFn(spec.Executable, args, spec.WorkspaceRoot, sanitizedWorkerEnv())
	if err != nil {
		return ThreadHandle{}, err
	}
	sess := &codexSession{
		proc:     mp,
		openSpec: spec,
		pending:  map[int64]chan rpcReply{},
	}
	go sess.readLoop()
	go func() {
		_, _ = io.Copy(io.Discard, mp.Stderr())
	}()

	initCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := sess.request(initCtx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "asterhand", "version": "1.0"},
		"capabilities": map[string]any{"experimentalApi": true},
	}); err != nil {
		_ = mp.Kill()
		return ThreadHandle{}, fmt.Errorf("codex initialize failed: %w", err)
	}
	if err := sess.notify("initialized", nil); err != nil {
		_ = mp.Kill()
		return ThreadHandle{}, fmt.Errorf("codex initialized notify failed: %w", err)
	}

	sandbox, _, approvalPolicy, reviewer := codexTurnPolicies(spec.AccessMode, spec.ApprovalMode, spec.SessionAutoApprove, spec.WorkspaceRoot)
	method, params, freshThread := buildCodexThreadOpenRequest(spec, sandbox, approvalPolicy, reviewer)
	raw, err := sess.request(initCtx, method, params)
	if err != nil {
		_ = mp.Kill()
		if !freshThread {
			return ThreadHandle{}, fmt.Errorf("native_session_unrecoverable: %w", err)
		}
		return ThreadHandle{}, fmt.Errorf("codex thread/start failed: %w", err)
	}
	opened, err := parseCodexThreadOpenResponse(raw, freshThread)
	if err != nil {
		_ = mp.Kill()
		return ThreadHandle{}, err
	}

	seededDefaults := spec.NativeDefaultsCaptured || strings.TrimSpace(spec.NativeDefaultModel) != "" || strings.TrimSpace(spec.NativeDefaultReasoningEffort) != ""
	sess.mu.Lock()
	sess.threadID = opened.threadID
	sess.currentModel = opened.model
	sess.currentEffort = opened.effort
	sess.collabMode = opened.collabMode
	sess.collabDevInstructions = opened.collabDevInstr
	if seededDefaults {
		sess.nativeCaptured = true
		sess.nativeModel = strings.TrimSpace(spec.NativeDefaultModel)
		sess.nativeEffort = strings.ToLower(strings.TrimSpace(spec.NativeDefaultReasoningEffort))
	} else if freshThread {
		sess.nativeCaptured = true
		sess.nativeModel = opened.model
		sess.nativeEffort = opened.effort
	}
	nativeCaptured := sess.nativeCaptured
	nativeModel := sess.nativeModel
	nativeEffort := sess.nativeEffort
	sess.mu.Unlock()
	return ThreadHandle{
		Key:                          spec.WorkerID + ":" + opened.threadID,
		NativeSessionID:              opened.threadID,
		NativeDefaultsCaptured:       nativeCaptured,
		NativeDefaultModel:           nativeModel,
		NativeDefaultReasoningEffort: nativeEffort,
		ProcessRef:                   sess,
	}, nil
}

func (a *CodexAdapter) StartTurn(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, fmt.Errorf("%w: %v", ErrPreStartFailed, err)
	}
	sess, ok := handle.ProcessRef.(*codexSession)
	if !ok || sess == nil {
		return "", nil, fmt.Errorf("%w: invalid codex handle", ErrPreStartFailed)
	}
	turnCtx, turnCancel := context.WithCancel(context.Background())
	at := &codexActiveTurn{
		deltas:        map[string]*strings.Builder{},
		onEvent:       onEvent,
		onInteraction: spec.OnInteraction,
		turnCtx:       turnCtx,
		turnCancel:    turnCancel,
		done:          make(chan TurnOutcome, 1),
	}
	sess.mu.Lock()
	if sess.closed {
		sess.mu.Unlock()
		turnCancel()
		return "", nil, fmt.Errorf("%w: codex session closed", ErrPreStartFailed)
	}
	sess.active = at
	plan := sess.planTurnConfigLocked(spec)
	sess.mu.Unlock()

	params := buildCodexTurnStartParams(handle.NativeSessionID, spec, plan)
	raw, err := sess.request(ctx, "turn/start", params)
	if err != nil {
		sess.clearActive(at)
		return "", nil, err
	}
	turnID, err := parseCodexTurnStartResponse(raw)
	if err != nil {
		sess.clearActive(at)
		return "", nil, err
	}
	sess.mu.Lock()
	at.turnID = turnID
	if plan.useCollabReset {
		sess.currentModel = plan.resetModel
		sess.currentEffort = ""
	} else {
		if plan.sendModel {
			sess.currentModel = plan.wantModel
		}
		if plan.sendEffort {
			sess.currentEffort = plan.wantEffort
		}
	}
	sess.mu.Unlock()
	return turnID, at.done, nil
}

func (s *codexSession) planTurnConfigLocked(spec TurnStartSpec) codexTurnConfigPlan {
	wantModel := firstNonEmpty(spec.Model, s.openSpec.Model, s.nativeModel)
	wantEffort := strings.ToLower(firstNonEmpty(spec.ReasoningEffort, s.openSpec.ReasoningEffort, s.nativeEffort))
	resetEffortToAuto := s.nativeCaptured && wantEffort == "" && s.currentEffort != ""
	resetModel := firstNonEmpty(wantModel, s.currentModel)
	useCollabReset := resetEffortToAuto && resetModel != ""
	var collabDevInstr any
	if s.collabDevInstructions != nil {
		collabDevInstr = *s.collabDevInstructions
	}
	return codexTurnConfigPlan{
		wantModel:      wantModel,
		wantEffort:     wantEffort,
		resetModel:     resetModel,
		collabMode:     firstNonEmpty(s.collabMode, "default"),
		collabDevInstr: collabDevInstr,
		useCollabReset: useCollabReset,
		sendModel:      !useCollabReset && wantModel != "" && wantModel != s.currentModel,
		sendEffort:     !useCollabReset && wantEffort != "" && wantEffort != s.currentEffort,
	}
}

func (a *CodexAdapter) InterruptTurn(ctx context.Context, handle ThreadHandle, nativeTurnID string) error {
	sess, ok := handle.ProcessRef.(*codexSession)
	if !ok || sess == nil || nativeTurnID == "" {
		return nil
	}
	iCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := sess.request(iCtx, "turn/interrupt", map[string]any{
		"threadId": handle.NativeSessionID,
		"turnId":   nativeTurnID,
	})
	return err
}

func (a *CodexAdapter) SteerTurn(ctx context.Context, handle ThreadHandle, nativeTurnID, text string) error {
	sess, ok := handle.ProcessRef.(*codexSession)
	if !ok || sess == nil || handle.NativeSessionID == "" || nativeTurnID == "" {
		return errors.New("codex turn is not active")
	}
	_, err := sess.request(ctx, "turn/steer", map[string]any{
		"threadId":       handle.NativeSessionID,
		"expectedTurnId": nativeTurnID,
		"input": []map[string]any{
			{"type": "text", "text": text},
		},
	})
	return err
}

func (a *CodexAdapter) ForceKill(handle ThreadHandle) error {
	sess, ok := handle.ProcessRef.(*codexSession)
	if !ok || sess == nil {
		return nil
	}
	return sess.close()
}

func (a *CodexAdapter) CloseThread(handle ThreadHandle) error {
	return a.ForceKill(handle)
}

func (a *CodexAdapter) InspectThreadTurns(ctx context.Context, handle ThreadHandle) ([]NativeTurnRecord, error) {
	sess, ok := handle.ProcessRef.(*codexSession)
	if !ok || sess == nil {
		return nil, errors.New("invalid codex handle")
	}
	qCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	raw, err := sess.request(qCtx, "thread/turns/list", map[string]any{
		"threadId":  handle.NativeSessionID,
		"itemsView": "full",
		"limit":     20,
	})
	if err != nil {
		return nil, err
	}
	return parseCodexInspectTurnsResponse(raw)
}

func (s *codexSession) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	s.mu.Lock()
	if s.closed {
		err := s.closeErr
		if err == nil {
			err = errors.New("codex session closed")
		}
		s.mu.Unlock()
		return nil, fmt.Errorf("%w: %v", ErrPreStartFailed, err)
	}
	s.nextID++
	id := s.nextID
	ch := make(chan rpcReply, 1)
	s.pending[id] = ch
	s.mu.Unlock()

	payload, err := json.Marshal(map[string]any{
		"id":     id,
		"method": method,
		"params": params,
	})
	if err != nil {
		s.deletePending(id)
		return nil, fmt.Errorf("%w: %v", ErrPreStartFailed, err)
	}
	if err := s.writeLineCtx(ctx, payload); err != nil {
		s.deletePending(id)
		return nil, err
	}
	select {
	case <-ctx.Done():
		s.deletePending(id)
		return nil, ctx.Err()
	case rep := <-ch:
		return rep.result, rep.err
	}
}

func (s *codexSession) notify(method string, params any) error {
	obj := map[string]any{"method": method}
	if params != nil {
		obj["params"] = params
	}
	payload, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	return s.writeLine(payload)
}

func (s *codexSession) writeLine(b []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.proc.Stdin().Write(append(b, '\n'))
	return err
}

func (s *codexSession) writeLineCtx(ctx context.Context, b []byte) error {
	line := append(append([]byte(nil), b...), '\n')
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
		return fmt.Errorf("%w: codex session closed", ErrPreStartFailed)
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%w: %v", ErrPreStartFailed, err)
		}
	}
	_, err := proc.Stdin().Write(line)
	return err
}

func (s *codexSession) deletePending(id int64) {
	s.mu.Lock()
	delete(s.pending, id)
	s.mu.Unlock()
}

func (s *codexSession) clearActive(at *codexActiveTurn) {
	s.mu.Lock()
	if s.active == at {
		s.active = nil
	}
	s.mu.Unlock()
}

func (s *codexSession) close() error {
	s.mu.Lock()
	alreadyClosed := s.closed
	s.closed = true
	if s.closeErr == nil {
		s.closeErr = errors.New("codex process closed")
	}
	closeErr := s.closeErr
	pending := s.pending
	s.pending = map[int64]chan rpcReply{}
	active := s.active
	s.active = nil
	var activeTurnID string
	var activeItems []NativeItem
	if active != nil {
		activeTurnID = active.turnID
		activeItems = append([]NativeItem(nil), active.items...)
	}
	proc := s.proc
	s.mu.Unlock()

	if !alreadyClosed {
		for _, ch := range pending {
			ch <- rpcReply{err: closeErr, localClose: true}
		}
	}
	var killErr error
	if proc != nil {
		killErr = proc.Kill()
	}
	if active != nil {
		out := TurnOutcome{
			State:        TurnInterrupted,
			NativeTurnID: activeTurnID,
			Items:        activeItems,
			ErrorCode:    "interrupted",
			ErrorMessage: "Codex process terminated before turn completion",
			LocalClose:   true,
			StopErr:      killErr,
		}
		if killErr != nil {
			out.ErrorCode = "process_stop_unconfirmed"
			out.ErrorMessage = fmt.Sprintf("Codex process termination could not be confirmed: %v", killErr)
		}
		active.doneOnce.Do(func() {
			if active.turnCancel != nil {
				active.turnCancel()
			}
			active.done <- out
			close(active.done)
		})
	}
	return killErr
}

func (s *codexSession) readLoop() {
	scanner := bufio.NewScanner(s.proc.Stdout())
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		s.handleMessage([]byte(line))
	}
	_ = s.close()
}

func (s *codexSession) handleMessage(raw []byte) {
	var env codexRPCEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return
	}
	// Response to client request.
	if env.ID != nil && env.Method == "" {
		var id int64
		if err := json.Unmarshal(*env.ID, &id); err != nil {
			return
		}
		s.mu.Lock()
		ch := s.pending[id]
		delete(s.pending, id)
		s.mu.Unlock()
		if ch != nil {
			if env.Error != nil {
				ch <- rpcReply{err: fmt.Errorf("codex rpc error %d: %s", env.Error.Code, env.Error.Message)}
			} else {
				ch <- rpcReply{result: env.Result}
			}
		}
		return
	}

	// Server -> Client request (approvals / user input).
	if env.ID != nil && env.Method != "" {
		s.handleServerRequest(*env.ID, env.Method, env.Params)
		return
	}

	// Server -> Client notification.
	if env.Method != "" {
		s.handleNotification(env.Method, env.Params)
	}
}

func (s *codexSession) handleServerRequest(rawID json.RawMessage, method string, params json.RawMessage) {
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
		req := parseCodexInteractionRequest(method, params)
		if turnCtx == nil {
			turnCtx = context.Background()
		}
		go func() {
			dec, err := handler(turnCtx, req)
			if err != nil {
				dec = InteractionDecision{Decision: DecisionCancelTurn, Message: err.Error()}
			}
			res, rpcErr, shouldInterrupt := buildCodexInteractionReply(method, params, req, dec)
			if rpcErr != nil {
				reply, _ := json.Marshal(map[string]any{"id": rawID, "error": rpcErr})
				_ = s.writeLine(reply)
			} else {
				reply, _ := json.Marshal(map[string]any{"id": rawID, "result": res})
				_ = s.writeLine(reply)
			}
			if shouldInterrupt {
				s.mu.Lock()
				tid := ""
				if s.active == active {
					if dec.Actor == "policy" {
						if req.Kind == InteractionInput {
							s.active.blockedCode = TurnBlockedUserInput
						} else {
							s.active.blockedCode = TurnBlockedApproval
						}
						s.active.blockedMsg = firstNonEmptyStr(dec.Message, "Blocked interactive request")
					}
					tid = s.active.turnID
				}
				s.mu.Unlock()
				if tid != "" {
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					_, _ = s.request(ctx, "turn/interrupt", map[string]any{"threadId": s.threadID, "turnId": tid})
				}
			}
		}()
		return
	}

	isApproval := strings.Contains(method, "Approval") || strings.Contains(method, "requestApproval")
	code := TurnBlockedUserInput
	msg := "Unsupported interactive user input requested by worker: " + method
	if isApproval {
		code = TurnBlockedApproval
		msg = "Unsupported interactive approval requested by worker: " + method
	}

	// Reply cleanly so codex app-server does not hang waiting for this JSON-RPC id.
	if method == "item/commandExecution/requestApproval" || method == "item/fileChange/requestApproval" || method == "execCommandApproval" || method == "applyPatchApproval" {
		reply, _ := json.Marshal(map[string]any{"id": rawID, "result": map[string]any{"decision": "cancel"}})
		_ = s.writeLine(reply)
	} else {
		reply, _ := json.Marshal(map[string]any{"id": rawID, "error": map[string]any{"code": -32001, "message": msg}})
		_ = s.writeLine(reply)
	}

	var tid string
	s.mu.Lock()
	if s.active != nil {
		s.active.blockedCode = code
		s.active.blockedMsg = msg
		tid = s.active.turnID
		if s.active.onEvent != nil {
			s.active.onEvent(AdapterEvent{Kind: "system", Summary: msg})
		}
	}
	s.mu.Unlock()

	if tid != "" {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, _ = s.request(ctx, "turn/interrupt", map[string]any{
				"threadId": s.threadID,
				"turnId":   tid,
			})
		}()
	}
}

func (s *codexSession) emitActiveEvent(ev AdapterEvent) {
	s.mu.Lock()
	at := s.active
	if at != nil && at.onEvent != nil {
		at.onEvent(ev)
	}
	s.mu.Unlock()
}

func (s *codexSession) handleNotification(method string, params json.RawMessage) {
	switch method {
	case "guardianWarning":
		if summary, ok := parseCodexGuardianWarning(params); ok {
			s.emitActiveEvent(AdapterEvent{Kind: "approval_auto", Summary: summary})
		}

	case "item/autoApprovalReview/started":
		s.emitActiveEvent(AdapterEvent{Kind: "approval_auto", Summary: "Codex Guardian reviewing approval request..."})

	case "item/autoApprovalReview/completed":
		if summary, ok := parseCodexAutoApprovalReview(params); ok {
			s.emitActiveEvent(AdapterEvent{Kind: "approval_auto", Summary: summary})
		}

	case "item/agentMessage/delta":
		var p codexAgentDeltaNotification
		if json.Unmarshal(params, &p) != nil {
			return
		}
		s.mu.Lock()
		at := s.active
		if at != nil && (at.turnID == "" || at.turnID == p.TurnID) {
			if at.deltas[p.ItemID] == nil {
				at.deltas[p.ItemID] = &strings.Builder{}
			}
			at.deltas[p.ItemID].WriteString(p.Delta)
			if at.onEvent != nil {
				at.onEvent(AdapterEvent{Kind: "delta", ItemID: p.ItemID, Summary: "streaming", Bytes: len(p.Delta)})
			}
		}
		s.mu.Unlock()

	case "item/started":
		var p codexItemNotification
		if json.Unmarshal(params, &p) != nil {
			return
		}
		if _, summary, ok := summarizeCodexItem(p.Item); ok && summary != "" {
			s.mu.Lock()
			at := s.active
			if at != nil && (at.turnID == "" || at.turnID == p.TurnID) && at.onEvent != nil {
				at.onEvent(AdapterEvent{Kind: "progress", ItemID: p.Item.ID, Phase: p.Item.Phase, Summary: summary})
			}
			s.mu.Unlock()
		}

	case "item/completed":
		var p codexItemNotification
		if json.Unmarshal(params, &p) != nil {
			return
		}
		it, isAsyncQuestion := buildCodexCompletedNativeItem(p.Item)
		s.mu.Lock()
		at := s.active
		if at != nil && (at.turnID == "" || at.turnID == p.TurnID) {
			at.items = append(at.items, it)
			if at.onEvent != nil {
				if kind, summary, ok := summarizeCodexItem(p.Item); ok {
					at.onEvent(AdapterEvent{Kind: kind, ItemID: it.ItemID, Phase: it.Phase, Summary: summary, Item: &it})
				}
			}
		}
		s.mu.Unlock()
		if isAsyncQuestion && at != nil {
			handleCodexAsyncQuestions(s, at, p.Item)
		}

	case "turn/completed":
		var p codexTurnCompletedNotification
		if json.Unmarshal(params, &p) != nil {
			return
		}
		s.mu.Lock()
		at := s.active
		if at != nil && (at.turnID == "" || at.turnID == p.Turn.ID) {
			state := mapCodexTurnStatus(p.Turn.Status)
			errCode, errMsg := "", ""
			if at.blockedCode != "" {
				state = at.blockedCode
				errCode = at.blockedCode
				errMsg = at.blockedMsg
			} else if p.Turn.Error != nil && p.Turn.Error.Message != "" {
				errCode = "native_turn_error"
				errMsg = p.Turn.Error.Message
			}
			s.finishActiveLocked(at, TurnOutcome{
				State:        state,
				NativeTurnID: p.Turn.ID,
				Items:        append([]NativeItem(nil), at.items...),
				ErrorCode:    errCode,
				ErrorMessage: errMsg,
			})
		}
		s.mu.Unlock()

	default:
		if strings.HasSuffix(method, "Delta") || strings.HasSuffix(method, "/delta") {
			s.mu.Lock()
			at := s.active
			var onEvent func(AdapterEvent)
			if at != nil {
				onEvent = at.onEvent
			}
			s.mu.Unlock()
			if onEvent != nil {
				onEvent(AdapterEvent{Kind: "delta", Bytes: len(params)})
			}
		}
	}
}

func (s *codexSession) finishActiveLocked(at *codexActiveTurn, outcome TurnOutcome) {
	if s.active == at {
		s.active = nil
	}
	at.doneOnce.Do(func() {
		if at.turnCancel != nil {
			at.turnCancel()
		}
		at.done <- outcome
		close(at.done)
	})
}
