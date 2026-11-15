package workspace

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// Transport is a process-private file service, not the public automation API.
// Its bearer is given only to the owned adapter, never to model tool parameters.
// Control, wait and file I/O slots are separated so queued waiters or I/O floods
// never block cancellation, batch sealing or terminal completion.
type Transport struct {
	URL          string
	Token        string
	server       *http.Server
	service      *Service
	totalSlots   chan struct{}
	ioSlots      chan struct{}
	waitSlots    chan struct{}
	controlSlots chan struct{}
}

type bridgeRequest struct {
	Action           string     `json:"action"`
	ID               string     `json:"id"`
	Batch            string     `json:"batch"`
	Order            int        `json:"order"`
	Run              string     `json:"run"`
	Conversation     string     `json:"conversation"`
	Tool             string     `json:"tool"`
	Targets          []Target   `json:"targets"`
	NewBytes         int64      `json:"new_bytes"`
	InputFingerprint string     `json:"input_fingerprint"`
	Fingerprint      string     `json:"fingerprint"`
	Items            []SealItem `json:"items"`
	Path             string     `json:"path"`
	Data             []byte     `json:"data"`
	State            string     `json:"state"`
	Detail           string     `json:"detail"`
	Query            string     `json:"query"`
	Cursor           string     `json:"cursor"`
	Content          bool       `json:"content"`
	Progress         []Step     `json:"progress"`
	WaitMs           int        `json:"wait_ms"`
	Bootstrap        bool       `json:"bootstrap"`
}

func Serve(service *Service) (*Transport, error) {
	return ServeWithSlots(service, 12, 16, 8)
}

func ServeWithSlots(service *Service, ioCap, waitCap, controlCap int) (*Transport, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	t := &Transport{
		URL:          "http://" + listener.Addr().String(),
		Token:        rand.Text(),
		service:      service,
		totalSlots:   make(chan struct{}, ioCap+waitCap+controlCap),
		ioSlots:      make(chan struct{}, ioCap),
		waitSlots:    make(chan struct{}, waitCap),
		controlSlots: make(chan struct{}, controlCap),
	}
	t.server = &http.Server{Handler: t, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 4096}
	go func() { _ = t.server.Serve(listener) }()
	return t, nil
}

func (t *Transport) Close() error { return t.server.Close() }

func isControlAction(action string) bool {
	return action == "register" || action == "seal" || action == "cancel" || action == "end"
}

func (t *Transport) acquirePool(action string) (func(), bool) {
	var pool chan struct{}
	switch {
	case isControlAction(action):
		pool = t.controlSlots
	case action == "acquire":
		pool = t.waitSlots
	default:
		pool = t.ioSlots
	}
	select {
	case pool <- struct{}{}:
		return func() { <-pool }, true
	default:
		return nil, false
	}
}

func (t *Transport) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/" || r.Header.Get("Origin") != "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+t.Token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	select {
	case t.totalSlots <- struct{}{}:
		defer func() { <-t.totalSlots }()
	default:
		http.Error(w, "busy", http.StatusTooManyRequests)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 3<<20)
	defer r.Body.Close()
	var req bridgeRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&req) != nil || decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid_request", http.StatusBadRequest)
		return
	}
	releasePool, ok := t.acquirePool(req.Action)
	if !ok {
		http.Error(w, "busy", http.StatusTooManyRequests)
		return
	}
	defer releasePool()

	value, err := t.call(r.Context(), req)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	response := map[string]any{"value": value}
	if err != nil {
		code := Code(err)
		if os.IsNotExist(err) {
			code = "not_found"
		}
		response = map[string]any{"error": code}
	}
	_ = json.NewEncoder(w).Encode(response)
}

func (t *Transport) verifyTurn(req bridgeRequest) (Scope, error) {
	s := t.service
	s.mu.Lock()
	scope := s.scope
	turn := s.turn
	s.mu.Unlock()
	if turn == "" || req.Run != turn || !strings.EqualFold(req.Conversation, scope.Conversation) {
		return Scope{}, fail("invalid_turn")
	}
	return scope, nil
}

func (t *Transport) call(ctx context.Context, req bridgeRequest) (any, error) {
	s := t.service
	switch req.Action {
	case "register":
		scope, err := t.verifyTurn(req)
		if err != nil {
			return nil, err
		}
		fp := req.InputFingerprint
		if fp == "" {
			fp = req.Fingerprint
		}
		return s.Register(Register{
			ID: req.ID, Batch: req.Batch, Order: req.Order, Scope: scope, Tool: req.Tool,
			InputFingerprint: fp, Targets: req.Targets, Path: req.Path, NewBytes: req.NewBytes, Progress: req.Progress,
		})
	case "seal":
		scope, err := t.verifyTurn(req)
		if err != nil {
			return nil, err
		}
		return map[string]any{"sealed": true}, s.SealBatch(SealBatch{Batch: req.Batch, Scope: scope, Items: req.Items})
	case "acquire":
		if _, err := t.verifyTurn(req); err != nil {
			return nil, err
		}
		return s.acquireBoundedPoll(ctx, req.ID, req.Fingerprint, req.WaitMs)
	case "begin":
		scope, err := t.verifyTurn(req)
		if err != nil {
			return nil, err
		}
		return s.BeginContext(ctx, Begin{ID: req.ID, Scope: scope, Tool: req.Tool, Targets: req.Targets, Path: req.Path, NewBytes: req.NewBytes, Fingerprint: req.Fingerprint, Progress: req.Progress})
	case "cancel":
		return map[string]any{"cancelled": true}, s.CancelCall(req.ID, req.Detail)
	case "end":
		return s.End(req.ID, req.State, req.Detail)
	case "read":
		if req.ID != "" {
			return s.NativeRead(req.ID, req.Path)
		}
		if !req.Bootstrap {
			return nil, fail("invalid_call")
		}
		return s.readBootstrap(ctx, req.Path)
	case "stat":
		if req.ID == "" {
			if !req.Bootstrap {
				return nil, fail("invalid_call")
			}
			return s.statBootstrap(ctx, req.Path)
		}
		return s.StatWithCall(req.ID, req.Path)
	case "list":
		if req.ID == "" {
			if !req.Bootstrap {
				return nil, fail("invalid_call")
			}
			return s.ReadDirectoryContext(ctx, req.Path)
		}
		return s.ReadDirectoryWithCall(req.ID, req.Path)
	case "search":
		if req.ID == "" {
			return nil, fail("invalid_call")
		}
		return s.SearchWithCall(req.ID, req.Path, req.Query, req.Cursor, req.Content, true)
	case "write":
		return s.Write(req.ID, req.Path, req.Data, false)
	case "create":
		return s.Write(req.ID, req.Path, req.Data, true)
	case "remove":
		return nil, s.Remove(req.ID, req.Path)
	case "mkdir":
		return nil, s.Mkdir(req.ID, req.Path)
	default:
		return nil, fail("unsupported")
	}
}

func (s *Service) checkImmediatePollLocked(id, fingerprint string) (map[string]any, error) {
	c := s.callsByID[id]
	if c == nil {
		return nil, nil
	}
	if fingerprint != "" && c.op.Fingerprint != fingerprint {
		return nil, fail("fingerprint_mismatch")
	}
	return map[string]any{"granted": true, "operation": publicOperation(c.op)}, nil
}

func (s *Service) resolveMissingPollEntry(id, fingerprint string) (map[string]any, error) {
	s.mu.Lock()
	prior, hasPrior := s.records[id]
	s.mu.Unlock()
	if !hasPrior {
		return nil, fail("not_found")
	}
	if fingerprint != "" && prior.Fingerprint != fingerprint {
		return nil, fail("fingerprint_mismatch")
	}
	return map[string]any{"granted": false, "terminal": true, "operation": publicOperation(prior)}, nil
}

// acquireBoundedPoll waits up to waitMs (capped at 4s) for a registered call to
// receive its grant without cancelling the queued registration on poll timeout.
func (s *Service) acquireBoundedPoll(reqCtx context.Context, id, fingerprint string, waitMs int) (map[string]any, error) {
	s.mu.Lock()
	imm, immErr := s.checkImmediatePollLocked(id, fingerprint)
	s.mu.Unlock()
	if imm != nil || immErr != nil {
		return imm, immErr
	}

	timeout := 3500 * time.Millisecond
	if waitMs > 0 && waitMs < 4000 {
		timeout = time.Duration(waitMs) * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	for {
		s.sched.mu.Lock()
		e, ok := s.sched.entries[id]
		if !ok {
			s.sched.mu.Unlock()
			return s.resolveMissingPollEntry(id, fingerprint)
		}
		if e.sealed && fingerprint != "" && e.fingerprint != "" && e.fingerprint != fingerprint {
			s.sched.mu.Unlock()
			return nil, fail("fingerprint_mismatch")
		}
		if e.admissionClosed && !e.granted {
			reason := e.cancelReason
			s.sched.releaseEntryLocked(e)
			s.sched.mu.Unlock()
			s.mu.Lock()
			s.releaseRegisteredSlotLocked(id, reason)
			s.mu.Unlock()
			if reason == "" {
				reason = "cancelled"
			}
			return nil, fail(reason)
		}
		if !e.granted {
			okGrant, reason := s.sched.canGrantLocked(e)
			if okGrant {
				s.sched.grantLocked(e)
				s.sched.mu.Unlock()
				op, err := s.prepareGrantedCall(e)
				if err != nil {
					return nil, err
				}
				return map[string]any{"granted": true, "operation": op}, nil
			}
			e.phase, e.waitReason = PhaseQueued, reason
			waitCh := s.sched.notify
			s.sched.mu.Unlock()
			curOp := s.markPollQueued(id, reason)
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return map[string]any{"granted": false, "operation": curOp}, nil
			}
			timer := time.NewTimer(remaining)
			select {
			case <-waitCh:
				timer.Stop()
			case <-timer.C:
				return map[string]any{"granted": false, "operation": curOp}, nil
			case <-reqCtx.Done():
				timer.Stop()
				return nil, fail("cancelled")
			}
			continue
		}
		s.sched.mu.Unlock()
		op, err := s.prepareGrantedCall(e)
		if err != nil {
			return nil, err
		}
		return map[string]any{"granted": true, "operation": op}, nil
	}
}

func (s *Service) markPollQueued(id, reason string) Operation {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, exists := s.records[id]
	if !exists || op.State != "started" {
		return Operation{}
	}
	if op.Phase != PhaseQueued || op.WaitReason != reason {
		s.sequence++
		op.Sequence = s.sequence
		op.Phase = PhaseQueued
		op.WaitReason = reason
		s.records[id] = op
	}
	return publicOperation(op)
}
