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
	turnID      string
	items       []NativeItem
	deltas      map[string]*strings.Builder
	onEvent     func(AdapterEvent)
	done        chan TurnOutcome
	doneOnce    sync.Once
	blockedCode string
	blockedMsg  string
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

	sandbox := "read-only"
	if spec.AccessMode == harnesses.AccessWorkspaceWrite {
		sandbox = "workspace-write"
	}

	var threadID string
	if spec.NativeSessionID != "" {
		params := map[string]any{
			"threadId":       spec.NativeSessionID,
			"cwd":            spec.WorkspaceRoot,
			"sandbox":        sandbox,
			"approvalPolicy": "never",
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
			"cwd":            spec.WorkspaceRoot,
			"sandbox":        sandbox,
			"approvalPolicy": "never",
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
	at := &codexActiveTurn{
		deltas:  map[string]*strings.Builder{},
		onEvent: onEvent,
		done:    make(chan TurnOutcome, 1),
	}
	sess.mu.Lock()
	if sess.closed {
		sess.mu.Unlock()
		return "", nil, errors.New("codex session closed")
	}
	sess.active = at
	sess.mu.Unlock()

	sandboxPolicy := map[string]any{"type": "readOnly", "networkAccess": false}
	if spec.AccessMode == harnesses.AccessWorkspaceWrite {
		sandboxPolicy = map[string]any{
			"type":          "workspaceWrite",
			"writableRoots": []string{spec.WorkspaceRoot},
		}
	}
	params := map[string]any{
		"threadId":            handle.NativeSessionID,
		"cwd":                 spec.WorkspaceRoot,
		"approvalPolicy":      "never",
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

	// Server -> Client request (approvals / user input): M1 bounded immediate rejection.
	if env.ID != nil && env.Method != "" {
		s.handleServerRequest(*env.ID, env.Method)
		return
	}

	// Server -> Client notification.
	if env.Method != "" {
		s.handleNotification(env.Method, env.Params)
	}
}

func (s *codexSession) handleServerRequest(rawID json.RawMessage, method string) {
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
				at.onEvent(AdapterEvent{Kind: "delta", ItemID: p.ItemID, Summary: "streaming"})
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
		it := NativeItem{
			ItemID:   p.Item.ID,
			Type:     p.Item.Type,
			ClientID: p.Item.ClientID,
			Phase:    p.Item.Phase,
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
	}
}

func (s *codexSession) finishActiveLocked(at *codexActiveTurn, outcome TurnOutcome) {
	if s.active == at {
		s.active = nil
	}
	at.doneOnce.Do(func() {
		at.done <- outcome
		close(at.done)
	})
}

func mapCodexTurnStatus(native string) string {
	switch native {
	case "completed":
		return TurnCompleted
	case "interrupted":
		return TurnInterrupted
	case "failed":
		return TurnFailed
	default:
		return TurnRunning
	}
}

func truncateSummary(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:clampUTF8Bytes([]byte(s), max)] + "..."
}

type codexItemNotification struct {
	TurnID string          `json:"turnId"`
	Item   codexThreadItem `json:"item"`
}

type codexThreadItem struct {
	ID             string `json:"id"`
	Type           string `json:"type"`
	ClientID       string `json:"clientId"`
	Phase          string `json:"phase"`
	Text           string `json:"text"`
	Command        string `json:"command"`
	Status         string `json:"status"`
	ExitCode       *int   `json:"exitCode"`
	DurationMs     *int64 `json:"durationMs"`
	Server         string `json:"server"`
	Tool           string `json:"tool"`
	Query          string `json:"query"`
	Path           string `json:"path"`
	Summary        []string `json:"summary"`
	CommandActions []struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Path    string `json:"path"`
		Query   string `json:"query"`
	} `json:"commandActions"`
	Changes []struct {
		Path string `json:"path"`
	} `json:"changes"`
}

func compactOneLine(s string, max int) string {
	fields := strings.Fields(s)
	return truncateSummary(strings.Join(fields, " "), max)
}

func summarizeCodexItem(it codexThreadItem) (kind, summary string, emit bool) {
	switch it.Type {
	case "userMessage", "hookPrompt":
		return "", "", false
	case "agentMessage":
		k := "commentary"
		if it.Phase != "" {
			k = it.Phase
		}
		txt := compactOneLine(it.Text, 240)
		if txt == "" {
			return "", "", false
		}
		return k, txt, true
	case "commandExecution":
		desc := summarizeCommandActions(it)
		if desc == "" {
			desc = compactOneLine(it.Command, 180)
		}
		if desc == "" {
			desc = "commandExecution"
		}
		meta := []string{}
		if it.ExitCode != nil {
			meta = append(meta, fmt.Sprintf("exit %d", *it.ExitCode))
		} else if it.Status != "" && it.Status != "completed" {
			meta = append(meta, it.Status)
		}
		if it.DurationMs != nil && *it.DurationMs > 0 {
			meta = append(meta, fmt.Sprintf("%dms", *it.DurationMs))
		}
		if len(meta) > 0 {
			desc += " (" + strings.Join(meta, ", ") + ")"
		}
		return "command", desc, true
	case "fileChange":
		paths := make([]string, 0, len(it.Changes))
		for _, ch := range it.Changes {
			if p := strings.TrimSpace(ch.Path); p != "" {
				paths = append(paths, p)
			}
		}
		desc := "fileChange"
		if len(paths) > 0 {
			if len(paths) > 4 {
				desc = fmt.Sprintf("%s +%d", strings.Join(paths[:4], ", "), len(paths)-4)
			} else {
				desc = strings.Join(paths, ", ")
			}
		}
		if it.Status != "" && it.Status != "completed" {
			desc += " (" + it.Status + ")"
		}
		return "edit", desc, true
	case "mcpToolCall":
		name := strings.Trim(it.Server+"/"+it.Tool, "/")
		if name == "" {
			name = "mcpToolCall"
		}
		if it.Status != "" && it.Status != "completed" {
			name += " (" + it.Status + ")"
		}
		return "mcp", name, true
	case "webSearch":
		q := compactOneLine(it.Query, 180)
		if q == "" {
			q = "webSearch"
		}
		return "search", q, true
	case "reasoning":
		parts := []string{}
		for _, s := range it.Summary {
			if t := compactOneLine(s, 140); t != "" {
				parts = append(parts, t)
			}
		}
		if len(parts) == 0 {
			return "", "", false
		}
		return "reasoning", truncateSummary(strings.Join(parts, " / "), 240), true
	case "plan":
		txt := compactOneLine(it.Text, 240)
		if txt == "" {
			return "", "", false
		}
		return "plan", txt, true
	default:
		desc := it.Type
		if it.Path != "" {
			desc += ": " + it.Path
		}
		return "tool", desc, desc != ""
	}
}

func summarizeCommandActions(it codexThreadItem) string {
	if len(it.CommandActions) == 0 {
		return ""
	}
	parts := make([]string, 0, len(it.CommandActions))
	for _, act := range it.CommandActions {
		switch act.Type {
		case "read":
			if act.Path != "" {
				parts = append(parts, "read "+act.Path)
			}
		case "listFiles":
			if act.Path != "" {
				parts = append(parts, "list "+act.Path)
			} else {
				parts = append(parts, "list files")
			}
		case "search":
			s := "search"
			if act.Query != "" {
				s += " " + compactOneLine(act.Query, 60)
			}
			if act.Path != "" {
				s += " in " + act.Path
			}
			parts = append(parts, s)
		default:
			// Unknown action: let raw command fallback provide exact details.
			return ""
		}
	}
	if len(parts) == 0 {
		return ""
	}
	if len(parts) > 3 {
		return strings.Join(parts[:3], "; ") + fmt.Sprintf(" (+%d)", len(parts)-3)
	}
	return strings.Join(parts, "; ")
}
