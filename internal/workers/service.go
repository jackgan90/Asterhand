package workers

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/harnesses"
	"github.com/jackgan90/Asterhand/internal/model"
)

type Budgets struct {
	MaxActiveTurns     int
	MaxResidentProcs   int
	WallClockTimeout   time.Duration
	StallTimeout       time.Duration
	InterruptGrace     time.Duration
	IdleRecycleTimeout time.Duration
}

func DefaultBudgets() Budgets {
	return Budgets{
		MaxActiveTurns:     4,
		MaxResidentProcs:   4,
		WallClockTimeout:   15 * time.Minute,
		StallTimeout:       5 * time.Minute,
		InterruptGrace:     10 * time.Second,
		IdleRecycleTimeout: 10 * time.Minute,
	}
}

type runningTurn struct {
	turnID       string
	workerID     string
	workspaceID  string
	worktreeKey  string
	harnessID    string
	accessMode   string
	nativeTurnID string
	handle       *ThreadHandle
	revoked      bool
	revokeReason string
	cancelCh     chan struct{}
	cancelOnce   sync.Once
	lastEventAt  time.Time
	doneCh       chan struct{}
}

func (rt *runningTurn) signalCancel() {
	rt.cancelOnce.Do(func() { close(rt.cancelCh) })
}

type residentProc struct {
	workerID   string
	harnessID  string
	handle     ThreadHandle
	lastUsedAt time.Time
}

type Service struct {
	store     *Store
	harnesses *harnesses.Service
	adapters  map[string]Adapter
	budgets   Budgets
	now       func() time.Time

	mu          sync.Mutex
	stopping    bool
	closed      bool
	activeTurns map[string]*runningTurn // turnID -> runningTurn
	workerTurn  map[string]string       // workerID -> active turnID
	writeLocks  map[string]string       // worktreeKey -> active turnID
	residents   map[string]*residentProc
	waiters     map[string][]chan struct{} // turnID -> notification chans
	wg          sync.WaitGroup
	reaperStop  chan struct{}
	reaperOnce  sync.Once
}

func NewService(home string, hs *harnesses.Service, adapters ...Adapter) (*Service, error) {
	st, err := OpenStore(home)
	if err != nil {
		return nil, err
	}
	am := map[string]Adapter{}
	for _, a := range adapters {
		am[a.HarnessID()] = a
	}
	s := &Service{
		store:       st,
		harnesses:   hs,
		adapters:    am,
		budgets:     DefaultBudgets(),
		now:         time.Now,
		activeTurns: map[string]*runningTurn{},
		workerTurn:  map[string]string{},
		writeLocks:  map[string]string{},
		residents:   map[string]*residentProc{},
		waiters:     map[string][]chan struct{}{},
		reaperStop:  make(chan struct{}),
	}
	if err := s.markInterruptedOnStartup(); err != nil {
		_ = st.Close()
		return nil, err
	}
	if hs != nil {
		hs.OnRevokeWorker = s.RevokeWorkerHarness
	}
	s.wg.Add(1)
	go s.idleReaperLoop()
	return s, nil
}

func (s *Service) SetBudgets(b Budgets) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b.MaxActiveTurns > 0 {
		s.budgets.MaxActiveTurns = b.MaxActiveTurns
	}
	if b.MaxResidentProcs > 0 {
		s.budgets.MaxResidentProcs = b.MaxResidentProcs
	}
	if b.WallClockTimeout > 0 {
		s.budgets.WallClockTimeout = b.WallClockTimeout
	}
	if b.StallTimeout > 0 {
		s.budgets.StallTimeout = b.StallTimeout
	}
	if b.InterruptGrace > 0 {
		s.budgets.InterruptGrace = b.InterruptGrace
	}
	if b.IdleRecycleTimeout > 0 {
		s.budgets.IdleRecycleTimeout = b.IdleRecycleTimeout
	}
}

func (s *Service) markInterruptedOnStartup() error {
	pending, err := s.store.ListNonTerminalTurns()
	if err != nil {
		return err
	}
	now := s.now().UTC()
	for _, t := range pending {
		if t.State == TurnReconcile {
			continue
		}
		t.State = TurnReconcile
		t.UpdatedAt = now
		if t.ErrorCode == "" {
			t.ErrorCode = "node_restarted"
			t.ErrorMessage = "Node restarted while turn was in progress; reconcile or manually resolve before sending new turns"
		}
		if err := s.store.SaveTurn(t); err != nil {
			return err
		}
		if w, err := s.store.GetWorker(t.WorkerID); err == nil && w.State != WorkerClosed {
			w.State = WorkerReconcile
			w.ActiveTurnID = t.ID
			w.UpdatedAt = now
			_ = s.store.SaveWorker(w)
		}
	}
	s.store.PruneExpiredDetails(now)
	return nil
}

// BeginStop integrates into App.RequestStop(confirmed). Active worker turns
// block unconfirmed stop; idle resident adapter processes do not.
func (s *Service) BeginStop(confirmed bool, next func() bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !confirmed && len(s.activeTurns) > 0 {
		return false
	}
	if next != nil && !next() {
		return false
	}
	s.stopping = true
	return true
}

func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.stopping = true
	turns := make([]*runningTurn, 0, len(s.activeTurns))
	for _, rt := range s.activeTurns {
		turns = append(turns, rt)
	}
	s.mu.Unlock()

	s.reaperOnce.Do(func() { close(s.reaperStop) })

	for _, rt := range turns {
		rt.signalCancel()
	}

	waitDone := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-ctx.Done():
		s.mu.Lock()
		for _, rt := range s.activeTurns {
			if rt.handle != nil {
				if ad := s.adapters[rt.harnessID]; ad != nil {
					_ = ad.ForceKill(*rt.handle)
				}
			}
		}
		s.mu.Unlock()
		select {
		case <-waitDone:
		case <-time.After(2 * time.Second):
		}
	}

	s.mu.Lock()
	residents := make([]*residentProc, 0, len(s.residents))
	for k, rp := range s.residents {
		residents = append(residents, rp)
		delete(s.residents, k)
	}
	s.closed = true
	s.mu.Unlock()

	for _, rp := range residents {
		if ad := s.adapters[rp.harnessID]; ad != nil {
			_ = ad.CloseThread(rp.handle)
		}
	}
	return s.store.Close()
}

type CreateInput struct {
	OperationID     string
	HarnessID       string
	WorkspaceRoot   string
	Alias           string
	AccessMode      string
	RoleHint        string
	Model           string
	ReasoningEffort string
}

func validateReasoningEffort(raw string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(raw))
	if v == "" {
		return "", nil
	}
	if len(v) > 32 {
		return "", automation.Fail("invalid_parameters", "reasoning_effort must be at most 32 characters")
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return "", automation.Fail("invalid_parameters", "reasoning_effort contains unsupported characters")
		}
	}
	return v, nil
}

func (s *Service) Create(ctx context.Context, in CreateInput) (Worker, error) {
	if in.AccessMode == "" {
		in.AccessMode = harnesses.AccessReadOnly
	}
	caps, ok := harnesses.StaticCapabilities(in.HarnessID)
	if !ok || !caps.SupportsWorker {
		return Worker{}, automation.Fail("unsupported_worker", "Harness is not supported as a managed worker")
	}
	if _, ok := s.adapters[in.HarnessID]; !ok {
		return Worker{}, automation.Fail("unsupported_worker", "No runtime adapter is registered for this harness yet")
	}
	enabled, exe := s.harnesses.IsWorkerEnabled(in.HarnessID)
	if !enabled {
		return Worker{}, automation.Fail("forbidden", "Target harness is not enabled as a collaboration worker")
	}
	if exe == "" && in.HarnessID == harnesses.HarnessCodex {
		return Worker{}, automation.Fail("unavailable", "Target harness executable was not found")
	}
	protLevel := caps.ReadOnlyLevel
	if in.AccessMode == harnesses.AccessWorkspaceWrite {
		protLevel = caps.WorkspaceWriteLevel
	}
	if protLevel == harnesses.ProtectionUnsupported {
		return Worker{}, automation.Fail("unsupported_access_mode", "Requested access_mode is not supported for this harness")
	}
	ws, canonRoot, err := s.harnesses.ResolveWorkspace(in.WorkspaceRoot, in.AccessMode)
	if err != nil {
		return Worker{}, err
	}
	alias := strings.TrimSpace(in.Alias)
	if len(alias) > 64 {
		return Worker{}, automation.Fail("invalid_parameters", "Alias must be at most 64 characters")
	}
	effort, err := validateReasoningEffort(in.ReasoningEffort)
	if err != nil {
		return Worker{}, err
	}
	wtKey, _ := harnesses.WorktreeKey(canonRoot)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping || s.closed {
		return Worker{}, automation.Fail("unavailable", "Node is stopping")
	}
	if in.OperationID != "" {
		if existing, err := s.store.WorkerByOperationID(in.OperationID); err == nil {
			return existing, nil
		}
	}
	if alias != "" {
		if _, err := s.store.FindActiveWorkerByAlias(ws.ID, alias); err == nil {
			return Worker{}, automation.Fail("conflict", "An active worker with this alias already exists in this workspace")
		}
	}
	now := s.now().UTC()
	w := Worker{
		ID:              "wkr_" + model.ID()[:16],
		WorkspaceID:     ws.ID,
		WorkspaceRoot:   canonRoot,
		WorktreeKey:     wtKey,
		Alias:           alias,
		HarnessID:       in.HarnessID,
		AccessMode:      in.AccessMode,
		ProtectionLevel: protLevel,
		RoleHint:        strings.TrimSpace(in.RoleHint),
		Model:           strings.TrimSpace(in.Model),
		ReasoningEffort: effort,
		State:           WorkerIdle,
		SessionEpoch:    1,
		OperationID:     in.OperationID,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.store.SaveWorker(w); err != nil {
		return Worker{}, err
	}
	return w, nil
}

func (s *Service) List(workspaceRoot, workspaceID string, includeClosed bool, limit int) ([]Worker, error) {
	wsID := workspaceID
	if wsID == "" && strings.TrimSpace(workspaceRoot) != "" {
		ws, _, err := s.harnesses.ResolveWorkspace(workspaceRoot, harnesses.AccessReadOnly)
		if err != nil {
			return nil, err
		}
		wsID = ws.ID
	}
	return s.store.ListWorkers(wsID, includeClosed, limit)
}

func (s *Service) Get(workerID, expectedWorkspaceID string) (Worker, []Turn, error) {
	w, err := s.store.GetWorker(workerID)
	if errors.Is(err, ErrNotFound) {
		return Worker{}, nil, automation.Fail("not_found", "Worker not found")
	}
	if err != nil {
		return Worker{}, nil, err
	}
	if expectedWorkspaceID != "" && w.WorkspaceID != expectedWorkspaceID {
		return Worker{}, nil, automation.Fail("workspace_mismatch", "Worker does not belong to the expected workspace")
	}
	turns, err := s.store.ListTurnsByWorker(w.ID, 10)
	return w, turns, err
}

func (s *Service) GetTurn(turnID, expectedWorkspaceID string) (Turn, error) {
	t, err := s.store.GetTurn(turnID)
	if errors.Is(err, ErrNotFound) {
		return Turn{}, automation.Fail("not_found", "Turn not found")
	}
	if err != nil {
		return Turn{}, err
	}
	if expectedWorkspaceID != "" && t.WorkspaceID != expectedWorkspaceID {
		return Turn{}, automation.Fail("workspace_mismatch", "Turn does not belong to the expected workspace")
	}
	return t, nil
}

type SendInput struct {
	OperationID         string
	CallerID            string
	WorkerID            string
	ExpectedWorkspaceID string
	Prompt              string
	Model               string
	ReasoningEffort     string
	ArtifactPaths       []string
	GitRef              string
	DiffBase            string
	OutputContract      string
}

func (s *Service) validateSendLocked(in SendInput) (Worker, string, []string, error) {
	w, err := s.store.GetWorker(in.WorkerID)
	if errors.Is(err, ErrNotFound) {
		return Worker{}, "", nil, automation.Fail("not_found", "Worker not found")
	}
	if err != nil {
		return Worker{}, "", nil, err
	}
	if in.ExpectedWorkspaceID != "" && w.WorkspaceID != in.ExpectedWorkspaceID {
		return Worker{}, "", nil, automation.Fail("workspace_mismatch", "Worker does not belong to the expected workspace")
	}
	if w.State == WorkerClosed {
		return Worker{}, "", nil, automation.Fail("conflict", "Worker is closed")
	}
	if w.State == WorkerReconcile {
		return Worker{}, "", nil, automation.Fail("reconciliation_required", "Worker has an unresolved turn requiring reconciliation")
	}
	if _, busy := s.workerTurn[w.ID]; busy {
		return Worker{}, "", nil, automation.Fail("worker_busy", "Worker already has an active turn")
	}
	if len(s.activeTurns) >= s.budgets.MaxActiveTurns {
		return Worker{}, "", nil, automation.Fail("worker_capacity", "Node active worker turn capacity reached")
	}
	enabled, exe := s.harnesses.IsWorkerEnabled(w.HarnessID)
	if !enabled {
		return Worker{}, "", nil, automation.Fail("forbidden", "Target harness worker authorization is disabled")
	}
	ws, _, err := s.harnesses.ResolveWorkspace(w.WorkspaceRoot, w.AccessMode)
	if err != nil {
		return Worker{}, "", nil, err
	}
	if ws.ID != w.WorkspaceID {
		return Worker{}, "", nil, automation.Fail("forbidden", "Worker workspace authorization changed")
	}
	resolvedArtifacts := make([]string, 0, len(in.ArtifactPaths))
	for _, ap := range in.ArtifactPaths {
		canonAP, err := harnesses.CheckArtifactPath(ws, ap)
		if err != nil {
			return Worker{}, "", nil, err
		}
		resolvedArtifacts = append(resolvedArtifacts, canonAP)
	}
	if w.AccessMode == harnesses.AccessWorkspaceWrite {
		if holder, locked := s.writeLocks[w.WorktreeKey]; locked && holder != "" {
			return Worker{}, "", nil, automation.Fail("worker_busy", "Workspace worktree already has an active write turn")
		}
	}
	return w, exe, resolvedArtifacts, nil
}

func (s *Service) Send(ctx context.Context, in SendInput) (Turn, error) {
	if strings.TrimSpace(in.Prompt) == "" {
		return Turn{}, automation.Fail("invalid_parameters", "Prompt is required")
	}
	if len(in.Prompt) > 128<<10 {
		return Turn{}, automation.Fail("invalid_parameters", "Prompt exceeds 128 KiB limit")
	}
	turnEffort, err := validateReasoningEffort(in.ReasoningEffort)
	if err != nil {
		return Turn{}, err
	}

	s.mu.Lock()
	if s.stopping || s.closed {
		s.mu.Unlock()
		return Turn{}, automation.Fail("unavailable", "Node is stopping")
	}
	if in.OperationID != "" {
		if existing, err := s.store.TurnByOperationID(in.OperationID); err == nil {
			s.mu.Unlock()
			return existing, nil
		}
	}
	w, exe, resolvedArtifacts, err := s.validateSendLocked(in)
	if err != nil {
		s.mu.Unlock()
		return Turn{}, err
	}

	effectiveModel := strings.TrimSpace(in.Model)
	if effectiveModel == "" {
		effectiveModel = w.Model
	}
	if turnEffort == "" {
		turnEffort = w.ReasoningEffort
	}

	now := s.now().UTC()
	turnID := "turn_" + model.ID()[:16]
	t := Turn{
		ID:                  turnID,
		WorkerID:            w.ID,
		WorkspaceID:         w.WorkspaceID,
		HarnessID:           w.HarnessID,
		SessionEpoch:        w.SessionEpoch,
		OperationID:         in.OperationID,
		CallerID:            in.CallerID,
		ClientCorrelationID: "corr_" + turnID,
		NativeSessionID:     w.NativeSessionID,
		AccessMode:          w.AccessMode,
		Model:               effectiveModel,
		ReasoningEffort:     turnEffort,
		State:               TurnPrepared,
		GitRef:              strings.TrimSpace(in.GitRef),
		DiffBase:            strings.TrimSpace(in.DiffBase),
		StartedAt:           now,
		UpdatedAt:           now,
	}
	if err := s.store.SaveTurn(t); err != nil {
		s.mu.Unlock()
		return Turn{}, err
	}
	w.State = WorkerRunning
	w.ActiveTurnID = t.ID
	w.UpdatedAt = now
	if err := s.store.SaveWorker(w); err != nil {
		s.mu.Unlock()
		return Turn{}, err
	}

	rt := &runningTurn{
		turnID:      t.ID,
		workerID:    w.ID,
		workspaceID: w.WorkspaceID,
		worktreeKey: w.WorktreeKey,
		harnessID:   w.HarnessID,
		accessMode:  w.AccessMode,
		cancelCh:    make(chan struct{}),
		lastEventAt: now,
		doneCh:      make(chan struct{}),
	}
	s.activeTurns[t.ID] = rt
	s.workerTurn[w.ID] = t.ID
	if w.AccessMode == harnesses.AccessWorkspaceWrite {
		s.writeLocks[w.WorktreeKey] = t.ID
	}
	s.wg.Add(1)
	s.mu.Unlock()

	prompt := composePrompt(in.Prompt, resolvedArtifacts, t.GitRef, t.DiffBase, in.OutputContract)
	go s.runTurn(rt, w, t, exe, prompt)
	return t, nil
}

func (s *Service) Cancel(ctx context.Context, turnID, expectedWorkspaceID string) (Turn, error) {
	t, err := s.GetTurn(turnID, expectedWorkspaceID)
	if err != nil {
		return Turn{}, err
	}
	if IsTurnTerminal(t.State) {
		return t, nil
	}
	s.mu.Lock()
	rt := s.activeTurns[turnID]
	s.mu.Unlock()
	if rt != nil {
		rt.signalCancel()
		select {
		case <-rt.doneCh:
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
	}
	return s.GetTurn(turnID, expectedWorkspaceID)
}

func (s *Service) CloseWorker(ctx context.Context, workerID, expectedWorkspaceID string) (Worker, error) {
	w, _, err := s.Get(workerID, expectedWorkspaceID)
	if err != nil {
		return Worker{}, err
	}
	if w.State == WorkerClosed {
		return w, nil
	}
	s.mu.Lock()
	if _, busy := s.workerTurn[w.ID]; busy {
		s.mu.Unlock()
		return Worker{}, automation.Fail("worker_busy", "Cancel active turn before closing worker")
	}
	rp := s.residents[w.ID]
	delete(s.residents, w.ID)
	s.mu.Unlock()

	if rp != nil {
		if ad := s.adapters[rp.harnessID]; ad != nil {
			_ = ad.CloseThread(rp.handle)
		}
	}
	now := s.now().UTC()
	w.State = WorkerClosed
	w.ActiveTurnID = ""
	w.UpdatedAt = now
	w.ClosedAt = &now
	if err := s.store.SaveWorker(w); err != nil {
		return Worker{}, err
	}
	return w, nil
}
