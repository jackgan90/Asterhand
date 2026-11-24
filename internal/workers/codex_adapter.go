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

type CodexAdapter struct{}

func NewCodexAdapter() *CodexAdapter { return &CodexAdapter{} }

func (a *CodexAdapter) HarnessID() string { return harnesses.HarnessCodex }

type codexSession struct {
	proc     *managedProc
	threadID string

	writeMu sync.Mutex
	nextID  int64

	mu       sync.Mutex
	pending  map[int64]chan rpcReply
	active   *codexActiveTurn
	closed   bool
	closeErr error
}

type rpcReply struct {
	result json.RawMessage
	err    error
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
	mp, err := spawnManagedProcess(spec.Executable, args, spec.WorkspaceRoot, sanitizedWorkerEnv())
	if err != nil {
		return ThreadHandle{}, err
	}
	sess := &codexSession{
		proc:    mp,
		pending: map[int64]chan rpcReply{},
	}
	go sess.readLoop()
	go func() {
		_, _ = io.Copy(io.Discard, mp.Stderr())
	}()

	initCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := sess.request(initCtx, "initialize", map[string]any{
		"clientInfo": map[string]any{"name": "asterhand", "version": "1.0"},
	}); err != nil {
		_ = mp.Kill()
		return ThreadHandle{}, fmt.Errorf("codex initialize failed: %w", err)
	}
	if err := sess.notify("initialized", nil); err != nil {
		_ = mp.Kill()
		return ThreadHandle{}, fmt.Errorf("codex initialized notify failed: %w", err)
	}

	sandbox, _, approvalPolicy, reviewer := codexTurnPolicies(spec.AccessMode, spec.ApprovalMode, spec.SessionAutoApprove, spec.WorkspaceRoot)

	var threadID string
	if spec.NativeSessionID != "" {
		params := map[string]any{
			"threadId":          spec.NativeSessionID,
			"cwd":               spec.WorkspaceRoot,
			"sandbox":           sandbox,
			"approvalPolicy":    approvalPolicy,
			"approvalsReviewer": reviewer,
		}
		if spec.Model != "" {
			params["model"] = spec.Model
		}
		raw, err := sess.request(initCtx, "thread/resume", params)
		if err != nil {
			_ = mp.Kill()
			return ThreadHandle{}, fmt.Errorf("native_session_unrecoverable: %w", err)
		}
		var res struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		if err := json.Unmarshal(raw, &res); err != nil || res.Thread.ID == "" {
			_ = mp.Kill()
			return ThreadHandle{}, errors.New("native_session_unrecoverable: invalid thread/resume response")
		}
		threadID = res.Thread.ID
	} else {
		params := map[string]any{
			"cwd":               spec.WorkspaceRoot,
			"sandbox":           sandbox,
			"approvalPolicy":    approvalPolicy,
			"approvalsReviewer": reviewer,
		}
		if spec.Model != "" {
			params["model"] = spec.Model
		}
		if spec.RoleHint != "" {
			params["developerInstructions"] = spec.RoleHint
		}
		raw, err := sess.request(initCtx, "thread/start", params)
		if err != nil {
			_ = mp.Kill()
			return ThreadHandle{}, fmt.Errorf("codex thread/start failed: %w", err)
		}
		var res struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		if err := json.Unmarshal(raw, &res); err != nil || res.Thread.ID == "" {
			_ = mp.Kill()
			return ThreadHandle{}, errors.New("codex thread/start returned empty thread id")
		}
		threadID = res.Thread.ID
	}
	sess.threadID = threadID
	return ThreadHandle{
		Key:             spec.WorkerID + ":" + threadID,
		NativeSessionID: threadID,
		ProcessRef:      sess,
	}, nil
}

func (a *CodexAdapter) StartTurn(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
	sess, ok := handle.ProcessRef.(*codexSession)
	if !ok || sess == nil {
		return "", nil, errors.New("invalid codex handle")
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
		return "", nil, errors.New("codex session closed")
	}
	sess.active = at
	sess.mu.Unlock()

	_, sandboxPolicy, approvalPolicy, reviewer := codexTurnPolicies(spec.AccessMode, spec.ApprovalMode, spec.SessionAutoApprove, spec.WorkspaceRoot)
	params := map[string]any{
		"threadId":            handle.NativeSessionID,
		"cwd":                 spec.WorkspaceRoot,
		"approvalPolicy":      approvalPolicy,
		"approvalsReviewer":   reviewer,
		"sandboxPolicy":       sandboxPolicy,
		"clientUserMessageId": spec.ClientCorrelationID,
		"input": []map[string]any{
			{"type": "text", "text": spec.Prompt},
		},
	}
	if spec.Model != "" {
		params["model"] = spec.Model
	}
	if spec.ReasoningEffort != "" {
		params["effort"] = spec.ReasoningEffort
	}
	raw, err := sess.request(ctx, "turn/start", params)
	if err != nil {
		sess.clearActive(at)
		return "", nil, err
	}
	var res struct {
		Turn struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(raw, &res); err != nil || res.Turn.ID == "" {
		sess.clearActive(at)
		return "", nil, errors.New("codex turn/start returned empty turn id")
	}
	sess.mu.Lock()
	at.turnID = res.Turn.ID
	sess.mu.Unlock()
	return res.Turn.ID, at.done, nil
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
	var resp struct {
		Data []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Items  []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				ClientID string `json:"clientId"`
				Phase    string `json:"phase"`
				Text     string `json:"text"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	out := make([]NativeTurnRecord, 0, len(resp.Data))
	for _, t := range resp.Data {
		rec := NativeTurnRecord{
			NativeTurnID: t.ID,
			Status:       mapCodexTurnStatus(t.Status),
		}
		for _, it := range t.Items {
			rec.Items = append(rec.Items, NativeItem{
				ItemID:   it.ID,
				Type:     it.Type,
				ClientID: it.ClientID,
				Phase:    it.Phase,
				Text:     it.Text,
			})
		}
		out = append(out, rec)
	}
	return out, nil
}

func (s *codexSession) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	s.mu.Lock()
	if s.closed {
		err := s.closeErr
		if err == nil {
			err = errors.New("codex session closed")
		}
		s.mu.Unlock()
		return nil, err
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
		return nil, err
	}
	if err := s.writeLine(payload); err != nil {
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
	if !s.closed {
		s.closed = true
		if s.closeErr == nil {
			s.closeErr = errors.New("codex process closed")
		}
		for id, ch := range s.pending {
			ch <- rpcReply{err: s.closeErr}
			delete(s.pending, id)
		}
		if s.active != nil {
			s.finishActiveLocked(s.active, TurnOutcome{
				State:        TurnInterrupted,
				NativeTurnID: s.active.turnID,
				Items:        append([]NativeItem(nil), s.active.items...),
				ErrorCode:    "interrupted",
				ErrorMessage: "Codex process terminated before turn completion",
			})
		}
	}
	s.mu.Unlock()
	return s.proc.Kill()
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
	var env struct {
		ID     *json.RawMessage `json:"id"`
		Method string           `json:"method"`
		Params json.RawMessage  `json:"params"`
		Result json.RawMessage  `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
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

func (s *codexSession) handleNotification(method string, params json.RawMessage) {
	switch method {
	case "guardianWarning":
		var p struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(params, &p) == nil && strings.TrimSpace(p.Message) != "" {
			s.mu.Lock()
			at := s.active
			if at != nil && at.onEvent != nil {
				at.onEvent(AdapterEvent{Kind: "approval_auto", Summary: truncateSummary(p.Message, 240)})
			}
			s.mu.Unlock()
		}

	case "item/autoApprovalReview/started":
		s.mu.Lock()
		at := s.active
		if at != nil && at.onEvent != nil {
			at.onEvent(AdapterEvent{Kind: "approval_auto", Summary: "Codex Guardian reviewing approval request..."})
		}
		s.mu.Unlock()

	case "item/autoApprovalReview/completed":
		var p struct {
			Review struct {
				Status    string `json:"status"`
				RiskLevel string `json:"riskLevel"`
				Rationale string `json:"rationale"`
			} `json:"review"`
		}
		if json.Unmarshal(params, &p) == nil {
			msg := fmt.Sprintf("Codex Guardian review %s", firstNonEmptyStr(p.Review.Status, "completed"))
			if p.Review.RiskLevel != "" {
				msg += " (risk: " + p.Review.RiskLevel + ")"
			}
			if strings.TrimSpace(p.Review.Rationale) != "" {
				msg += ": " + truncateSummary(p.Review.Rationale, 160)
			}
			s.mu.Lock()
			at := s.active
			if at != nil && at.onEvent != nil {
				at.onEvent(AdapterEvent{Kind: "approval_auto", Summary: msg})
			}
			s.mu.Unlock()
		}

	case "item/agentMessage/delta":
		var p struct {
			TurnID string `json:"turnId"`
			ItemID string `json:"itemId"`
			Delta  string `json:"delta"`
		}
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
		isAsyncQuestion := p.Item.Type == "agentMessage" && len(p.Item.Questions) > 0
		phase := p.Item.Phase
		if isAsyncQuestion {
			phase = "commentary"
		}
		it := NativeItem{
			ItemID:   p.Item.ID,
			Type:     p.Item.Type,
			ClientID: p.Item.ClientID,
			Phase:    phase,
			Text:     p.Item.Text,
		}
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
		var p struct {
			Turn struct {
				ID     string `json:"id"`
				Status string `json:"status"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"turn"`
		}
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
