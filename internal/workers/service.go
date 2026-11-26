package workers

import (
	"context"
	"errors"
	"fmt"
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
	InputCallerGrace   time.Duration
	InputClaimedGrace  time.Duration
	InteractionTimeout time.Duration
}

func DefaultBudgets() Budgets {
	return Budgets{
		MaxActiveTurns:     4,
		MaxResidentProcs:   4,
		WallClockTimeout:   30 * time.Minute,
		StallTimeout:       10 * time.Minute,
		InterruptGrace:     10 * time.Second,
		IdleRecycleTimeout: 10 * time.Minute,
		InputCallerGrace:   15 * time.Second,
		InputClaimedGrace:  30 * time.Second,
		InteractionTimeout: 30 * time.Minute,
	}
}

const (
	MinWallClockSeconds = 30
	MaxWallClockSeconds = 3600
	MinDeferSeconds     = 30
	MaxDeferSeconds     = 300
	maxBufferedComments = 20
	maxCommentaryBytes  = 2048
	maxSteerBytes       = 16 << 10
)

type runningTurn struct {
	turnID          string
	workerID        string
	workspaceID     string
	workspaceRoot   string
	worktreeKey     string
	harnessID       string
	accessMode      string
	nativeTurnID    string
	handle          *ThreadHandle
	revoked         bool
	revokeReason    string
	cancelled       bool
	turnCtx         context.Context
	turnCancel      context.CancelFunc
	cancelCh        chan struct{}
	cancelOnce      sync.Once
	forceKillCtx    context.Context
	forceKillCancel context.CancelFunc
	forceKillCh     chan struct{}
	forceKillOnce   sync.Once
	forceKillErr    error
	stopErr         error
	persistErr      error
	wallClock       time.Duration
	execStarted     time.Time
	waitingSince    time.Time
	pausedTotal     time.Duration
	lastEventAt     time.Time
	streamEvents    int64
	streamBytes     int64
	commentarySeq   int64
	commentaries    []CommentaryEntry
	doneCh          chan struct{}
}

func (rt *runningTurn) signalCancel() {
	rt.cancelOnce.Do(func() {
		if rt.turnCancel != nil {
			rt.turnCancel()
		}
		close(rt.cancelCh)
	})
}

func (rt *runningTurn) signalForceKill() {
	rt.forceKillOnce.Do(func() {
		if rt.forceKillCancel != nil {
			rt.forceKillCancel()
		}
		if rt.forceKillCh != nil {
			close(rt.forceKillCh)
		}
	})
}

type worktreeLock struct {
	turnID        string
	workerID      string
	workspaceRoot string
	worktreeKey   string
	retained      bool
}

type residentProc struct {
	workerID   string
	harnessID  string
	handle     ThreadHandle
	lastUsedAt time.Time
}

type Service struct {
	home      string
	store     *Store
	harnesses *harnesses.Service
	adapters  map[string]Adapter
	budgets   Budgets
	now       func() time.Time

	mu                    sync.Mutex
	stopping              bool
	closed                bool
	activeTurns           map[string]*runningTurn // turnID -> runningTurn
	workerTurn            map[string]string       // workerID -> active turnID
	writeLocks            map[string]worktreeLock         // turnID -> held write lock
	pendingReconcile      map[string]pendingReconcileInfo // turnID -> unpersisted reconciliation
	residents             map[string]*residentProc
	nextResidentID        uint64
	waiters               map[string][]chan struct{} // turnID -> notification chans
	turnCommentaries      map[string][]CommentaryEntry
	turnCommentaryOrder   []string
	interactions          map[string]*pendingInteraction
	onInteractionsChanged func([]InteractionView)
	wg                    sync.WaitGroup
	reaperStop            chan struct{}
	reaperOnce            sync.Once
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
		home:             home,
		store:            st,
		harnesses:        hs,
		adapters:         am,
		budgets:          DefaultBudgets(),
		now:              time.Now,
		activeTurns:      map[string]*runningTurn{},
		workerTurn:       map[string]string{},
		writeLocks:       map[string]worktreeLock{},
		pendingReconcile: map[string]pendingReconcileInfo{},
		residents:        map[string]*residentProc{},
		waiters:          map[string][]chan struct{}{},
		turnCommentaries: map[string][]CommentaryEntry{},
		interactions:     map[string]*pendingInteraction{},
		reaperStop:       make(chan struct{}),
	}
	if err := s.markInterruptedOnStartup(); err != nil {
		_ = st.Close()
		return nil, err
	}
	if hs != nil {
		hs.OnRevokeWorker = s.RevokeWorkerHarness
		hs.OnRevokeWorkerWrite = s.RevokeWorkerWriteHarness
	}
	s.wg.Add(1)
	go s.idleReaperLoop()
	return s, nil
}

func (s *Service) Budgets() Budgets {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.budgets
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
	if b.InputCallerGrace > 0 {
		s.budgets.InputCallerGrace = b.InputCallerGrace
	}
	if b.InteractionTimeout > 0 {
		s.budgets.InteractionTimeout = b.InteractionTimeout
	}
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
	exe, err := s.harnesses.IsWorkerAccessEnabled(in.HarnessID, in.AccessMode)
	if err != nil {
		return Worker{}, err
	}
	if exe == "" && caps.WorkerManaged {
		return Worker{}, automation.Fail("unavailable", "Target harness executable was not found")
	}
	protLevel := caps.ReadOnlyLevel
	if in.AccessMode == harnesses.AccessWorkspaceWrite {
		protLevel = caps.WorkspaceWriteLevel
	}
	if protLevel == harnesses.ProtectionUnsupported {
		return Worker{}, automation.Fail("unsupported_access_mode", "Requested access_mode is not supported for this harness")
	}
	reqApprovalMode, effApprovalMode, err := NormalizeApprovalMode(in.AccessMode, in.ApprovalMode, caps)
	if err != nil {
		return Worker{}, err
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
	opKey := dedupKey(in.OperationKey, in.OperationID)
	if opKey != "" {
		if existing, err := s.store.WorkerByOperationID(opKey); err == nil {
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
		ID:                    "wkr_" + model.ID()[:16],
		WorkspaceID:           ws.ID,
		WorkspaceRoot:         canonRoot,
		WorktreeKey:           wtKey,
		Alias:                 alias,
		HarnessID:             in.HarnessID,
		AccessMode:            in.AccessMode,
		ApprovalMode:          reqApprovalMode,
		EffectiveApprovalMode: effApprovalMode,
		ProtectionLevel:       protLevel,
		RoleHint:              strings.TrimSpace(in.RoleHint),
		Model:                 strings.TrimSpace(in.Model),
		ReasoningEffort:       effort,
		State:                 WorkerIdle,
		SessionEpoch:          1,
		OperationID:           in.OperationID,
		OperationKey:          opKey,
		ActorID:               in.ActorID,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	if err := s.store.SaveWorker(w); err != nil {
		return Worker{}, err
	}
	if w.HarnessID == harnesses.HarnessDeepSeek {
		w.Warnings = checkDeepSeekWorkspaceACL(w.WorkspaceRoot)
	}
	return w, nil
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
	if w.State == WorkerReconcile || s.hasPendingOrRetainedForWorkerLocked(w.ID) {
		return Worker{}, "", nil, automation.Fail("reconciliation_required", "Worker has an unresolved turn requiring reconciliation")
	}
	if _, busy := s.workerTurn[w.ID]; busy {
		return Worker{}, "", nil, automation.Fail("worker_busy", "Worker already has an active turn")
	}
	if len(s.activeTurns) >= s.budgets.MaxActiveTurns {
		return Worker{}, "", nil, automation.Fail("worker_capacity", "Node active worker turn capacity reached")
	}
	exe, err := s.harnesses.IsWorkerAccessEnabled(w.HarnessID, w.AccessMode)
	if err != nil {
		return Worker{}, "", nil, err
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
		curKey, _ := harnesses.WorktreeKey(w.WorkspaceRoot)
		if curKey != "" {
			w.WorktreeKey = curKey
		}
		for _, lk := range s.writeLocks {
			holderKey, _ := harnesses.WorktreeKey(lk.workspaceRoot)
			conflict := (curKey != "" && (curKey == lk.worktreeKey || curKey == holderKey)) ||
				harnesses.WithinRoot(lk.workspaceRoot, w.WorkspaceRoot) ||
				harnesses.WithinRoot(w.WorkspaceRoot, lk.workspaceRoot)
			if !conflict {
				continue
			}
			var msg string
			if lk.retained {
				msg = "Workspace worktree write lock is retained by turn " + lk.turnID + " (worker " + lk.workerID + ") awaiting stop verification/reconciliation; resolve it before starting another workspace_write turn"
			} else if lk.workerID != "" {
				msg = "Workspace worktree already has an active write turn (" + lk.turnID + " on worker " + lk.workerID + "); serialize workspace_write turns on the same worktree"
			} else {
				msg = "Workspace worktree already has an active write turn (" + lk.turnID + "); serialize workspace_write turns on the same worktree"
			}
			return Worker{}, "", nil, automation.Fail("worktree_busy", msg)
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
	if in.WallClockSeconds != 0 && (in.WallClockSeconds < MinWallClockSeconds || in.WallClockSeconds > MaxWallClockSeconds) {
		return Turn{}, automation.Fail("invalid_parameters", "wall_clock_seconds must be between 30 and 3600")
	}

	s.mu.Lock()
	if s.stopping || s.closed {
		s.mu.Unlock()
		return Turn{}, automation.Fail("unavailable", "Node is stopping")
	}
	opKey := dedupKey(in.OperationKey, in.OperationID)
	if opKey != "" {
		if existing, err := s.store.TurnByOperationID(opKey); err == nil {
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
		OperationKey:        opKey,
		ActorID:             in.ActorID,
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
	if w.AccessMode == harnesses.AccessWorkspaceWrite {
		t.WriteLockRoot = w.WorkspaceRoot
		t.WriteLockKey = w.WorktreeKey
	}
	w.State = WorkerRunning
	w.ActiveTurnID = t.ID
	w.UpdatedAt = now
	if err := s.store.SaveTurnAndWorker(t, &w); err != nil {
		s.mu.Unlock()
		return Turn{}, err
	}

	turnCtx, turnCancel := context.WithCancel(context.Background())
	forceKillCtx, forceKillCancel := context.WithCancel(context.Background())
	rt := &runningTurn{
		turnID:          t.ID,
		workerID:        w.ID,
		workspaceID:     w.WorkspaceID,
		workspaceRoot:   w.WorkspaceRoot,
		worktreeKey:     w.WorktreeKey,
		harnessID:       w.HarnessID,
		accessMode:      w.AccessMode,
		turnCtx:         turnCtx,
		turnCancel:      turnCancel,
		cancelCh:        make(chan struct{}),
		forceKillCtx:    forceKillCtx,
		forceKillCancel: forceKillCancel,
		forceKillCh:     make(chan struct{}),
		wallClock:       time.Duration(in.WallClockSeconds) * time.Second,
		lastEventAt:     now,
		doneCh:          make(chan struct{}),
	}
	s.activeTurns[t.ID] = rt
	s.workerTurn[w.ID] = t.ID
	if w.AccessMode == harnesses.AccessWorkspaceWrite {
		s.writeLocks[t.ID] = worktreeLock{
			turnID:        t.ID,
			workerID:      w.ID,
			workspaceRoot: w.WorkspaceRoot,
			worktreeKey:   w.WorktreeKey,
		}
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
	if rt != nil {
		rt.cancelled = true
	}
	s.mu.Unlock()
	if rt != nil {
		rt.signalCancel()
		select {
		case <-rt.doneCh:
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
		s.mu.Lock()
		pErr := rt.persistErr
		s.mu.Unlock()
		if pErr != nil {
			return Turn{}, pErr
		}
	} else if flushed, hadPending, fErr := s.flushPendingReconcile(turnID); hadPending {
		if fErr != nil {
			return Turn{}, fErr
		}
		return flushed, nil
	}
	return s.GetTurn(turnID, expectedWorkspaceID)
}

// SteerTurn injects mid-turn guidance into an actively running turn when the
// underlying harness adapter implements SteerAdapter (e.g. Codex turn/steer,
// Claude Code stream-json stdin). Never silently cancels or restarts turns on
// harnesses without native steering support.
func (s *Service) SteerTurn(ctx context.Context, in SteerInput) (Turn, error) {
	turnID := strings.TrimSpace(in.TurnID)
	if turnID == "" {
		return Turn{}, automation.Fail("invalid_parameters", "turn_id is required")
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return Turn{}, automation.Fail("invalid_parameters", "text is required")
	}
	if len(text) > maxSteerBytes {
		return Turn{}, automation.Fail("invalid_parameters", fmt.Sprintf("text exceeds %d bytes limit", maxSteerBytes))
	}

	t, err := s.GetTurn(turnID, in.WorkspaceID)
	if err != nil {
		return Turn{}, err
	}
	switch t.State {
	case TurnWaitingApproval:
		return Turn{}, automation.Fail("turn_not_running", "Turn is waiting for human permission approval; wait for approval to finish before steering")
	case TurnWaitingInput:
		return Turn{}, automation.Fail("turn_not_running", "Turn is waiting for input; use workers_reply instead of workers_steer")
	case TurnRunning:
	default:
		return Turn{}, automation.Fail("turn_not_running", fmt.Sprintf("Turn is in %s state; wait or use workers_send to start a new turn instead", t.State))
	}

	s.mu.Lock()
	rt, ok := s.activeTurns[turnID]
	var adapter Adapter
	var handle ThreadHandle
	var nativeTurnID string
	if ok && rt != nil && rt.handle != nil {
		adapter = s.adapters[rt.harnessID]
		handle = *rt.handle
		nativeTurnID = rt.nativeTurnID
	}
	s.mu.Unlock()

	if !ok || rt == nil || adapter == nil || nativeTurnID == "" {
		return Turn{}, automation.Fail("turn_not_running", "Turn is not actively executing yet; wait briefly and retry")
	}
	steerer, ok := adapter.(SteerAdapter)
	if !ok {
		return Turn{}, automation.Fail("unsupported_steer", fmt.Sprintf("Harness %s does not support mid-turn steering; wait for this turn to finish (or cancel it) and call workers_send", t.HarnessID))
	}

	steerCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := steerer.SteerTurn(steerCtx, handle, nativeTurnID, text); err != nil {
		return Turn{}, automation.Fail("steer_failed", fmt.Sprintf("Failed to steer running turn: %v", err))
	}

	now := s.now().UTC()
	s.mu.Lock()
	if cur, exists := s.activeTurns[turnID]; exists {
		cur.lastEventAt = now
	}
	s.mu.Unlock()

	_, _ = s.store.AppendLog(turnID, LogEntry{
		Timestamp: now,
		Kind:      "steer",
		Summary:   truncateSummary("Steer: "+text, 500),
	})
	if latest, gErr := s.store.GetTurn(turnID); gErr == nil && latest.State == TurnRunning {
		latest.Progress = "Steer delivered: " + truncateSummary(text, 120)
		latest.UpdatedAt = now
		_ = s.store.SaveTurn(latest)
		t = latest
	}
	s.populateTurnActivity(&t)
	s.notifyWaiters(turnID)
	return t, nil
}

func (s *Service) CloseWorker(ctx context.Context, workerID, expectedWorkspaceID string) (Worker, error) {
	now := s.now().UTC()
	var detached *residentProc
	s.mu.Lock()
	w, err := s.store.GetWorker(workerID)
	if errors.Is(err, ErrNotFound) {
		s.mu.Unlock()
		return Worker{}, automation.Fail("not_found", "Worker not found")
	}
	if err != nil {
		s.mu.Unlock()
		return Worker{}, err
	}
	if expectedWorkspaceID != "" && w.WorkspaceID != expectedWorkspaceID {
		s.mu.Unlock()
		return Worker{}, automation.Fail("workspace_mismatch", "Worker does not belong to the expected workspace")
	}
	if w.State == WorkerClosed {
		s.mu.Unlock()
		return w, nil
	}
	if w.State == WorkerReconcile || s.hasPendingOrRetainedForWorkerLocked(w.ID) {
		s.mu.Unlock()
		return Worker{}, automation.Fail("reconciliation_required", "Resolve pending reconciliation turn before closing worker")
	}
	if _, busy := s.workerTurn[w.ID]; busy {
		s.mu.Unlock()
		return Worker{}, automation.Fail("worker_busy", "Cancel active turn before closing worker")
	}
	w.State = WorkerClosed
	w.ActiveTurnID = ""
	w.UpdatedAt = now
	w.ClosedAt = &now
	if err := s.store.SaveWorker(w); err != nil {
		s.mu.Unlock()
		return Worker{}, err
	}
	if rp, ok := s.residents[w.ID]; ok {
		detached = rp
		delete(s.residents, w.ID)
	}
	s.mu.Unlock()

	s.closeDetachedResident(detached)
	return w, nil
}
