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

func (s *Service) markInterruptedOnStartup() error {
	pending, err := s.store.ListNonTerminalTurns()
	if err != nil {
		return err
	}
	now := s.now().UTC()
	for _, t := range pending {
		if t.State == TurnReconcile {
			if err := s.restoreRetainedWriteLockOnStartup(t, now); err != nil {
				return err
			}
			continue
		}
		t.State = TurnReconcile
		t.PendingInteraction = nil
		t.UpdatedAt = now
		if t.ErrorCode == "" {
			t.ErrorCode = "node_restarted"
			t.ErrorMessage = "Node restarted while turn was in progress; reconcile or manually resolve before sending new turns"
		}
		if t.AccessMode == harnesses.AccessWorkspaceWrite {
			t.WriteLockRetained = true
			if t.WriteLockReason == "" {
				t.WriteLockReason = t.ErrorCode
			}
		}
		var workerToSave *Worker
		if w, err := s.store.GetWorker(t.WorkerID); err == nil && w.State != WorkerClosed {
			w.State = WorkerReconcile
			w.ActiveTurnID = t.ID
			w.UpdatedAt = now
			workerToSave = &w
		}
		if err := s.store.SaveTurnAndWorker(t, workerToSave); err != nil {
			return err
		}
		if err := s.restoreRetainedWriteLockOnStartup(t, now); err != nil {
			return err
		}
	}
	s.store.PruneExpiredDetails(now)
	return nil
}

func (s *Service) restoreRetainedWriteLockOnStartup(t Turn, now time.Time) error {
	if t.AccessMode != harnesses.AccessWorkspaceWrite {
		return nil
	}
	if !t.WriteLockRetained && t.ErrorCode != "process_stop_unconfirmed" {
		return nil
	}
	root := strings.TrimSpace(t.WriteLockRoot)
	key := strings.TrimSpace(t.WriteLockKey)
	if root == "" || key == "" {
		if w, err := s.store.GetWorker(t.WorkerID); err == nil {
			if root == "" {
				root = w.WorkspaceRoot
			}
			if key == "" {
				key = w.WorktreeKey
			}
		}
	}
	// Preserve the dispatch-time WriteLockKey as an immutable fact of this turn;
	// only backfill from live git topology when legacy records have no key.
	if key == "" && root != "" {
		if liveKey, _ := harnesses.WorktreeKey(root); liveKey != "" {
			key = liveKey
		}
	}
	if root == "" && key == "" {
		return nil
	}
	s.mu.Lock()
	s.writeLocks[t.ID] = worktreeLock{
		turnID:        t.ID,
		workerID:      t.WorkerID,
		workspaceRoot: root,
		worktreeKey:   key,
		retained:      true,
	}
	s.mu.Unlock()
	if !t.WriteLockRetained || t.WriteLockRoot != root || t.WriteLockKey != key || t.WriteLockReason == "" {
		t.WriteLockRetained = true
		t.WriteLockRoot = root
		t.WriteLockKey = key
		if t.WriteLockReason == "" {
			t.WriteLockReason = t.ErrorCode
		}
		t.UpdatedAt = now
		if err := s.store.SaveTurn(t); err != nil {
			return err
		}
	}
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
		rt.cancelled = true
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
	drained := false
	var closeTimeoutErr error
	select {
	case <-waitDone:
		drained = true
	case <-ctx.Done():
		closeTimeoutErr = ctx.Err()
		for _, rt := range turns {
			rt.signalForceKill()
		}
		// Prefer letting the turn runner arbitrate ForceKill and persist
		// reconciliation_required / retained write locks; fall back to direct
		// ForceKill if an adapter ignores InterruptTurn cancellation.
		select {
		case <-waitDone:
			drained = true
		case <-time.After(300 * time.Millisecond):
			type killTarget struct {
				rt     *runningTurn
				ad     Adapter
				handle ThreadHandle
			}
			var targets []killTarget
			s.mu.Lock()
			for _, rt := range s.activeTurns {
				if rt.handle != nil {
					if ad := s.adapters[rt.harnessID]; ad != nil {
						targets = append(targets, killTarget{rt: rt, ad: ad, handle: *rt.handle})
					}
				}
			}
			s.mu.Unlock()
			for _, kt := range targets {
				if kErr := kt.ad.ForceKill(kt.handle); kErr != nil {
					s.mu.Lock()
					if kt.rt.forceKillErr == nil {
						kt.rt.forceKillErr = kErr
					}
					s.mu.Unlock()
				}
			}
			select {
			case <-waitDone:
				drained = true
			case <-time.After(2 * time.Second):
			}
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

	var errs []error
	s.mu.Lock()
	for _, rt := range turns {
		if rt.stopErr != nil {
			errs = append(errs, rt.stopErr)
		} else if rt.forceKillErr != nil {
			errs = append(errs, rt.forceKillErr)
		}
		if rt.persistErr != nil {
			errs = append(errs, rt.persistErr)
		}
	}
	s.mu.Unlock()

	if !drained {
		// Never close the database while background goroutines may still touch it.
		go func() {
			<-waitDone
			_ = s.store.Close()
		}()
		if closeTimeoutErr == nil {
			closeTimeoutErr = context.DeadlineExceeded
		}
		errs = append(errs, fmt.Errorf("workers service close timed out waiting for active goroutines: %w", closeTimeoutErr))
		return errors.Join(errs...)
	}
	if storeErr := s.store.Close(); storeErr != nil {
		errs = append(errs, storeErr)
	}
	return errors.Join(errs...)
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
	s.populateTurnActivity(&t)
	if t.State == TurnWaitingApproval || t.State == TurnWaitingInput {
		t.PendingInteraction = s.pendingForTurn(t.ID)
	}
	return t, nil
}

func (s *Service) populateTurnActivity(t *Turn) {
	now := s.now().UTC()
	if t.FinishedAt != nil {
		if sec := int(t.FinishedAt.Sub(t.StartedAt).Seconds()); sec >= 0 {
			t.ElapsedSeconds = sec
		}
		return
	}
	if !t.StartedAt.IsZero() {
		if sec := int(now.Sub(t.StartedAt).Seconds()); sec >= 0 {
			t.ElapsedSeconds = sec
		}
	}
	s.mu.Lock()
	if rt, ok := s.activeTurns[t.ID]; ok {
		if !rt.lastEventAt.IsZero() {
			if idle := int(now.Sub(rt.lastEventAt).Seconds()); idle >= 0 {
				t.IdleSeconds = idle
			}
		}
		t.StreamEvents = rt.streamEvents
		t.StreamBytes = rt.streamBytes
		t.LatestCommentarySeq = rt.commentarySeq
	} else if hist := s.turnCommentaries[t.ID]; len(hist) > 0 {
		t.LatestCommentarySeq = hist[len(hist)-1].Seq
	}
	s.mu.Unlock()
}

func (s *Service) recordCommentary(rt *runningTurn, text string, now time.Time) int64 {
	txt := truncateSummary(strings.TrimSpace(text), maxCommentaryBytes)
	if txt == "" {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rt.commentarySeq++
	entry := CommentaryEntry{
		Seq:       rt.commentarySeq,
		Timestamp: now,
		Text:      txt,
	}
	rt.commentaries = append(rt.commentaries, entry)
	if len(rt.commentaries) > maxBufferedComments {
		rt.commentaries = append([]CommentaryEntry(nil), rt.commentaries[len(rt.commentaries)-maxBufferedComments:]...)
	}
	return rt.commentarySeq
}

func (s *Service) archiveTurnCommentaries(rt *runningTurn, finalAnswer string) {
	if rt == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(rt.commentaries) == 0 {
		return
	}
	cp := append([]CommentaryEntry(nil), rt.commentaries...)
	if normFinal := truncateSummary(strings.TrimSpace(finalAnswer), maxCommentaryBytes); normFinal != "" && len(cp) > 0 && cp[len(cp)-1].Text == normFinal {
		cp = cp[:len(cp)-1]
	}
	if len(cp) == 0 {
		return
	}
	if _, exists := s.turnCommentaries[rt.turnID]; !exists {
		s.turnCommentaryOrder = append(s.turnCommentaryOrder, rt.turnID)
		if len(s.turnCommentaryOrder) > 64 {
			evict := s.turnCommentaryOrder[0]
			s.turnCommentaryOrder = s.turnCommentaryOrder[1:]
			delete(s.turnCommentaries, evict)
		}
	}
	s.turnCommentaries[rt.turnID] = cp
}

func (s *Service) selectCommentaries(turnID string, afterSeq int64, defaultTail int) ([]CommentaryEntry, int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var src []CommentaryEntry
	if rt, ok := s.activeTurns[turnID]; ok {
		src = rt.commentaries
	} else {
		src = s.turnCommentaries[turnID]
	}
	if len(src) == 0 {
		return nil, 0
	}
	latest := src[len(src)-1].Seq
	if afterSeq > 0 {
		var out []CommentaryEntry
		for _, e := range src {
			if e.Seq > afterSeq {
				out = append(out, e)
			}
		}
		return out, latest
	}
	if defaultTail <= 0 || len(src) <= defaultTail {
		return append([]CommentaryEntry(nil), src...), latest
	}
	return append([]CommentaryEntry(nil), src[len(src)-defaultTail:]...), latest
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

// LegacyOperationLookup reconciles a pre-migration worker/turn row keyed by
// bare operation_id ONLY when the domain record itself carries durable
// ownership proof matching actor and target.
func (s *Service) LegacyOperationLookup(opName, rawOpID, actor, target string) (any, bool, error) {
	if rawOpID == "" {
		return nil, false, nil
	}
	switch opName {
	case "workers_create":
		ws, err := s.store.ListWorkersByOperationID(rawOpID, 2)
		if err != nil || len(ws) != 1 || !legacyWorkerMatchesActor(ws[0], actor, target) {
			return nil, false, err
		}
		return workerOpSummary(ws[0]), true, nil
	case "workers_send":
		ts, err := s.store.ListTurnsByOperationID(rawOpID, 2)
		if err != nil || len(ts) != 1 || !legacyTurnMatchesActor(ts[0], actor, target) {
			return nil, false, err
		}
		return turnOpSummary(ts[0]), true, nil
	default:
		return nil, false, nil
	}
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
