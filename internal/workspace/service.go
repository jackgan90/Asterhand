package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackgan90/Asterhand/internal/config"
)

type call struct {
	op       Operation
	held     map[string]*heldFile
	newBytes int64
	written  int64
	remove   map[string]bool
	oldBytes int64
	newFiles int
}

type Service struct {
	mu       sync.Mutex
	root     string
	journal  string
	id       string
	policy   string
	enabled  bool
	turn     string
	scope    Scope
	calls    int
	active   *call
	sequence int64
	records  map[string]Operation
	blocked  bool
}

func Open(root, journal, id string) (*Service, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	for _, p := range []string{root, journal, filepath.Join(journal, "staging")} {
		if err := os.MkdirAll(p, 0700); err != nil {
			return nil, err
		}
	}
	// The runtime owner guarantees the old process tree has exited before Open.
	// Retired files have durable before-images; staging is never a replay queue.
	orphans, err := os.ReadDir(filepath.Join(journal, "staging"))
	if err != nil {
		return nil, err
	}
	for _, item := range orphans {
		if item.Type().IsRegular() && (strings.HasSuffix(item.Name(), ".old") || strings.HasSuffix(item.Name(), ".new")) {
			if err := os.Remove(filepath.Join(journal, "staging", item.Name())); err != nil {
				return nil, err
			}
		}
	}
	s := &Service{root: root, journal: journal, id: id, records: map[string]Operation{}}
	entries, err := os.ReadDir(journal)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(journal, entry.Name()))
		var op Operation
		if err != nil || json.Unmarshal(data, &op) != nil || !validID(op.ID) {
			return nil, fail("journal_corrupt")
		}
		if op.State == "started" {
			op.State = "unknown"
		}
		if op.State == "unknown" {
			s.blocked = true
		}
		s.records[op.ID] = publicOperation(op)
		s.sequence = max(s.sequence, op.Sequence)
	}
	return s, nil
}

func validID(id string) bool {
	if len(id) == 0 || len(id) > 160 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func (s *Service) ID() string   { return s.id }
func (s *Service) Root() string { return s.root }

func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := Status{ID: s.id, Supported: platformSupported(), Authorized: s.enabled, Policy: s.policy,
		Ready: platformSupported() && s.enabled && !s.blocked, Busy: s.active != nil || s.turn != "", Enforcement: "restricted_service",
		Capabilities: []string{"files.read", "files.mutate", "files.discover", "memory.search", "progress", "artifacts.read", "versions.restore"},
		Limits:       map[string]int64{"file_bytes": MaxFileBytes, "batch_bytes": MaxBatchBytes, "batch_targets": MaxTargets, "workspace_bytes": MaxWorkspaceBytes, "workspace_files": MaxWorkspaceFiles, "turn_calls": MaxCalls, "page_bytes": PageBytes}}
	if !state.Supported {
		state.Reason = "unsupported_platform"
	} else if !state.Authorized {
		state.Reason = "not_authorized"
	} else if s.blocked {
		state.Reason = "reconciliation_required"
	}
	return state
}

func (s *Service) Configure(enabled bool, policy string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil || s.turn != "" {
		return fail("busy")
	}
	s.enabled, s.policy = enabled, policy
	return nil
}

func (s *Service) AcquireTurn(turn, policy string) error {
	return s.AcquireTurnScope(Scope{Turn: turn, Policy: policy})
}

func (s *Service) AcquireTurnScope(scope Scope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	turn := scope.Turn
	if !s.enabled || scope.Policy != s.policy || !platformSupported() {
		return fail("not_authorized")
	}
	if s.blocked {
		return fail("reconciliation_required")
	}
	if s.turn == turn {
		return nil
	}
	if !validID(turn) || s.turn != "" || s.active != nil {
		return fail("busy")
	}
	s.turn, s.scope, s.calls = turn, scope, 0
	return nil
}

// ReleaseTurn requires a trusted adapter's proof of native termination. An
// unfinished callback leaves a durable unknown operation and blocks mutation.
func (s *Service) ReleaseTurn(turn string, stopped bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turn != turn {
		return nil
	}
	if !stopped || s.active != nil {
		return fail("cleanup_pending")
	}
	s.turn = ""
	return nil
}

func (s *Service) Begin(req Begin) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validID(req.ID) || req.Fingerprint == "" || len(req.Tool) > 100 {
		return Operation{}, fail("invalid_request")
	}
	if prior, ok := s.records[req.ID]; ok {
		if prior.Fingerprint != req.Fingerprint || prior.Scope != req.Scope {
			return Operation{}, fail("identity_conflict")
		}
		if prior.State == "started" {
			return publicOperation(prior), fail("operation_pending")
		}
		return publicOperation(prior), nil
	}
	if !s.enabled || req.Scope.Policy != s.policy || !platformSupported() {
		return Operation{}, fail("not_authorized")
	}
	if s.blocked {
		return Operation{}, fail("reconciliation_required")
	}
	if s.active != nil || (s.turn != "" && s.turn != req.Scope.Turn) || (req.Scope.Turn != "" && s.turn != req.Scope.Turn) {
		return Operation{}, fail("busy")
	}
	if s.turn != "" && s.calls >= MaxCalls {
		return Operation{}, fail("call_limit")
	}
	if len(req.Targets) > MaxTargets || req.NewBytes < 0 || req.NewBytes > MaxBatchBytes {
		return Operation{}, fail("batch_limit")
	}
	if len(req.Progress) > 20 {
		return Operation{}, fail("progress_limit")
	}
	for _, step := range req.Progress {
		if len(step.Text) > 200 || !utf8.ValidString(step.Text) || (step.Status != "pending" && step.Status != "in_progress" && step.Status != "completed") {
			return Operation{}, fail("progress_limit")
		}
	}
	if err := s.pruneLocked(); err != nil {
		return Operation{}, err
	}
	c := &call{held: map[string]*heldFile{}, remove: map[string]bool{}, newBytes: req.NewBytes,
		op: Operation{ID: req.ID, Scope: req.Scope, Tool: req.Tool, Fingerprint: req.Fingerprint, Sequence: s.sequence + 1, Started: time.Now().UTC(), State: "started", Changes: []Change{}, Progress: req.Progress}}
	if err := s.capture(c, req.Targets); err != nil {
		closeCall(c)
		return Operation{}, err
	}
	metadata, _ := json.Marshal(publicOperation(c.op))
	// Leave room for terminal timestamps, outcome codes and reconciliation text.
	if len(metadata) > MaxOperationBytes-3*1024 {
		closeCall(c)
		return Operation{}, fail("activity_limit")
	}
	if len(req.Targets) > 0 {
		snapshot, _ := json.Marshal(c.op)
		growth := max(0, c.newBytes-c.oldBytes)
		if req.Tool == "edit" || req.Tool == "apply_patch" {
			// Native algorithms determine the exact result. Their conservative
			// expansion bound must not block shrinking an already-full workspace.
			// Write checks the actual size before any filesystem change.
			growth = 0
		}
		if err := s.reserveLocked(growth, c.newFiles+len(c.op.ParentDirectories), int64(len(snapshot))); err != nil {
			closeCall(c)
			return Operation{}, err
		}
	}
	if err := s.persist(c.op); err != nil {
		closeCall(c)
		return Operation{}, err
	}
	s.sequence, s.active = c.op.Sequence, c
	s.records[c.op.ID] = publicOperation(c.op)
	s.calls++
	return publicOperation(c.op), nil
}

func (s *Service) capture(c *call, targets []Target) error {
	// Stable order also makes future per-file locking safe against deadlock.
	targets = append([]Target(nil), targets...)
	sort.Slice(targets, func(i, j int) bool { return pathKey(targets[i].Path) < pathKey(targets[j].Path) })
	var oldBytes int64
	parents := map[string]string{}
	writable := 0
	for _, target := range targets {
		name, err := relative(target.Path, false)
		if err != nil {
			return err
		}
		if _, exists := c.held[pathKey(name)]; exists {
			return fail("duplicate_target")
		}
		h, err := hold(s.root, name, true, true)
		if err != nil {
			return err
		}
		h.staging = filepath.Join(s.journal, "staging")
		c.held[pathKey(name)] = h
		c.remove[pathKey(name)] = target.Remove
		change := Change{Path: name, Before: MissingRevision, After: MissingRevision, State: "unchanged", Restorable: true}
		if h.file != nil {
			change.Content, err = h.bytes(MaxFileBytes)
			if err != nil {
				return err
			}
			if !utf8.Valid(change.Content) {
				return fail("not_utf8")
			}
			change.Before = revision(change.Content)
			change.After = change.Before
		}
		if target.Expected != "" && target.Expected != change.Before {
			return fail("revision_conflict")
		}
		oldBytes += int64(len(change.Content))
		if !target.Remove {
			writable++
			c.oldBytes += int64(len(change.Content))
			if h.file == nil {
				c.newFiles++
			}
			for _, parent := range h.missing {
				relative, _ := filepath.Rel(s.root, parent)
				parents[pathKey(relative)] = filepath.ToSlash(relative)
			}
		}
		c.op.Changes = append(c.op.Changes, change)
	}
	if writable == 0 {
		c.newBytes = 0
	} else if c.op.Tool == "apply_patch" {
		// A move writes the source bytes to another target before removal. Its
		// source contributes to the result bound, but is not replacement credit
		// for workspace capacity while both names can exist.
		c.newBytes += oldBytes
	} else if c.op.Tool == "edit" {
		c.newBytes += c.oldBytes
	}
	for _, parent := range parents {
		c.op.ParentDirectories = append(c.op.ParentDirectories, parent)
	}
	sort.Strings(c.op.ParentDirectories)
	if oldBytes+c.newBytes > MaxBatchBytes {
		return fail("batch_limit")
	}
	return nil
}

func closeCall(c *call) {
	for _, h := range c.held {
		h.close()
	}
}

func (s *Service) persist(op Operation) error {
	data, err := json.Marshal(op)
	if err != nil {
		return err
	}
	return config.AtomicFile(filepath.Join(s.journal, op.ID+".json"), data, 0600)
}

func (s *Service) Write(id, name string, data []byte, exclusive bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, h, err := s.mutable(id, name)
	if err != nil {
		return "", err
	}
	if exclusive && h.file != nil && !h.removed {
		return "exists", nil
	}
	if c.remove[pathKey(name)] {
		return "", fail("undeclared_write")
	}
	if len(data) > MaxFileBytes || !utf8.Valid(data) {
		return "", fail("file_limit")
	}
	if c.written+int64(len(data)) > c.newBytes {
		return "", fail("batch_limit")
	}
	if err := h.pinAvailableParents(); err != nil {
		return "", err
	}
	// A mixed batch may grow an early file before shrinking a later one.
	// Recheck that transient growth too, while allowing shrinking recovery.
	var oldSize int64
	newFiles := len(h.missing)
	if h.file != nil {
		info, statErr := h.file.Stat()
		if statErr != nil {
			return "", statErr
		}
		oldSize = info.Size()
	} else {
		newFiles++
	}
	if err := s.reserveWorkspaceLocked(max(0, int64(len(data))-oldSize), newFiles); err != nil {
		return "", err
	}
	if err = h.write(data); err != nil {
		return "", err
	}
	c.written += int64(len(data))
	return "created", nil
}

func (s *Service) Remove(id, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, h, err := s.mutable(id, name)
	if err != nil {
		return err
	}
	return h.remove()
}

func (s *Service) mutable(id, name string) (*call, *heldFile, error) {
	name, err := relative(name, false)
	if err != nil {
		return nil, nil, err
	}
	if s.active == nil || s.active.op.ID != id {
		return nil, nil, fail("invalid_call")
	}
	h := s.active.held[pathKey(name)]
	if h == nil {
		return nil, nil, fail("undeclared_target")
	}
	return s.active, h, nil
}

func (s *Service) End(id, state, detail string) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil || s.active.op.ID != id {
		if op, ok := s.records[id]; ok {
			return publicOperation(op), nil
		}
		return Operation{}, fail("not_found")
	}
	if state != "completed" && state != "failed" && state != "cancelled" && state != "unknown" {
		return Operation{}, fail("invalid_state")
	}
	c := s.active
	for i := range c.op.Changes {
		change := &c.op.Changes[i]
		h := c.held[pathKey(change.Path)]
		after, conflict, err := h.observe()
		change.After = after
		if err != nil {
			state = "unknown"
		}
		if change.After != change.Before {
			change.State = "changed"
		}
		if conflict {
			change.State = "conflict"
		}
	}
	c.op.State, c.op.Finished = state, time.Now().UTC()
	s.sequence++
	c.op.Sequence = s.sequence
	// Detail is a fixed code or bounded user reconciliation note, not tool text.
	if len(detail) > 2048 {
		detail = detail[:2048]
	}
	c.op.Detail = detail
	err := s.persist(c.op)
	if err != nil {
		c.op.State = "unknown"
	}
	s.records[id] = publicOperation(c.op)
	if c.op.State == "unknown" {
		s.blocked = true
	}
	closeCall(c)
	s.active = nil
	if err == nil {
		err = s.pruneLocked()
	}
	return publicOperation(c.op), err
}

// RuntimeStopped is called only after the owned process tree has exited.
func (s *Service) RuntimeStopped() error {
	s.mu.Lock()
	active := s.active
	s.mu.Unlock()
	var err error
	if active != nil {
		_, err = s.End(active.op.ID, "unknown", "runtime_stopped")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turn = ""
	return err
}
