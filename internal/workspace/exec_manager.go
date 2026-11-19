package workspace

import (
	"context"
	"crypto/rand"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/platform/sandbox"
)

const quotaWatchInterval = 2 * time.Second

type execApprovalMeta struct {
	approvalMode     string
	userApproved     bool
	approvalDecision string
	approvalWaitMs   int64
}

type execSession struct {
	id               string
	scope            Scope
	turn             string
	callID           string
	summary          string
	wsRoot           string
	tempRoot         string
	escalated        bool
	approvalMode     string
	userApproved     bool
	approvalDecision string
	approvalWaitMs   int64
	proc             sandbox.SessionProcess
	started          time.Time
	slotHeld         bool
	inFgWait         bool
	detached         bool
	exited           bool
	finalized        bool
	activeDec        bool
	unsafeLinks      bool
	pollCursor       int64
	stopReason       string
	resumed          bool
	cancelLaunch     context.CancelFunc
	lockedFiles      []io.Closer
	closeLocksOnce   sync.Once
	fgCancelCh       chan struct{}
	fgCancelOnce     sync.Once
	detachCh         chan struct{}
}

func (sess *execSession) closeLockedFiles() {
	if sess == nil {
		return
	}
	sess.closeLocksOnce.Do(func() {
		if sess.cancelLaunch != nil {
			sess.cancelLaunch()
		}
		closeLockedHandles(sess.lockedFiles)
		sess.lockedFiles = nil
	})
}

type execManager struct {
	svc                       *Service
	backend                   sandbox.Backend
	tempRoot                  string
	cleanMu                   sync.RWMutex
	mu                        sync.Mutex
	sessions                  map[string]*execSession
	fgByCall                  map[string]*execSession
	turnCounts                map[string]int
	turnStopped               map[string]string
	canceledCalls             map[string]string
	watchdogTurns             map[string]bool
	settledNotify             chan struct{}
	approvals                 map[string]*approvalTicket
	fgApprovalsByCall         map[string]*approvalTicket
	resolvedApprovals         map[string]*approvalTicket
	testTempBytes             int64
	testTempFiles             int
	policyMu                  sync.RWMutex
	execMode                  string
	trustRev                  string
	trustRules                []config.TrustRule
	sessionRules              map[string][]config.TrustRule
	sessionWorkspaceEscalated map[string]bool
	persistTrustFn            func([]config.TrustRule) error
	onApprovalPending         func()
	onAfterClaimBeforeSpawn   func(callID string)
}

func newExecManager(svc *Service, backend sandbox.Backend, tempRoot string) *execManager {
	return &execManager{
		svc:                       svc,
		backend:                   backend,
		tempRoot:                  tempRoot,
		sessions:                  map[string]*execSession{},
		fgByCall:                  map[string]*execSession{},
		turnCounts:                map[string]int{},
		turnStopped:               map[string]string{},
		canceledCalls:             map[string]string{},
		watchdogTurns:             map[string]bool{},
		settledNotify:             make(chan struct{}),
		approvals:                 map[string]*approvalTicket{},
		fgApprovalsByCall:         map[string]*approvalTicket{},
		resolvedApprovals:         map[string]*approvalTicket{},
		execMode:                  config.ExecutionModeStandard,
		sessionRules:              map[string][]config.TrustRule{},
		sessionWorkspaceEscalated: map[string]bool{},
	}
}

func (m *execManager) Supported() bool {
	return m != nil && m.backend != nil && m.backend.Supported()
}

func (m *execManager) cleanupTemp(turn string, force bool) error {
	if !m.Supported() {
		return nil
	}
	m.cleanMu.Lock()
	defer m.cleanMu.Unlock()
	if !force {
		m.mu.Lock()
		for _, other := range m.sessions {
			if other.turn == turn && !other.exited {
				m.mu.Unlock()
				return nil
			}
		}
		m.mu.Unlock()
	}
	return m.backend.CleanupTemp(m.tempRoot)
}

func (m *execManager) PrepareAndClean() error {
	if err := os.MkdirAll(m.tempRoot, 0700); err != nil {
		return err
	}
	if !m.Supported() {
		return nil
	}
	if err := m.cleanupTemp("", true); err != nil {
		return err
	}
	return m.backend.Prepare(m.svc.root, m.tempRoot)
}

func (m *execManager) broadcastSettledLocked() {
	close(m.settledNotify)
	m.settledNotify = make(chan struct{})
}

func (m *execManager) decrementTurnCountLocked(turn string) {
	if cur := m.turnCounts[turn]; cur > 1 {
		m.turnCounts[turn] = cur - 1
	} else {
		delete(m.turnCounts, turn)
	}
}

func (m *execManager) releaseTurnSlotLocked(sess *execSession) {
	if sess == nil || !sess.slotHeld {
		return
	}
	sess.slotHeld = false
	m.decrementTurnCountLocked(sess.turn)
}

func (m *execManager) clearTurnForStart(turn string) {
	if m == nil || turn == "" {
		return
	}
	m.mu.Lock()
	for _, sess := range m.sessions {
		if sess.turn == turn {
			sess.slotHeld = false
		}
	}
	delete(m.turnStopped, turn)
	delete(m.turnCounts, turn)
	m.mu.Unlock()
}

func (m *execManager) callStopReasonLocked(callID, turn string) string {
	if r := m.turnStopped[turn]; r != "" {
		return r
	}
	if r := m.canceledCalls[callID]; r != "" {
		return r
	}
	return ""
}

func (s *Service) checkCallCancelled(callID, turn string) error {
	if s.exec != nil {
		s.exec.mu.Lock()
		reason := s.exec.callStopReasonLocked(callID, turn)
		s.exec.mu.Unlock()
		if reason != "" {
			return fail(reason)
		}
	}
	s.sched.mu.Lock()
	e := s.sched.entries[callID]
	closed := e == nil || e.admissionClosed
	cancelReason := ""
	if e != nil {
		cancelReason = e.cancelReason
	}
	s.sched.mu.Unlock()
	if closed {
		if cancelReason == "" {
			cancelReason = "cancelled"
		}
		return fail(cancelReason)
	}
	return nil
}

func (m *execManager) CancelForegroundCall(callID, reason string) {
	if m == nil || callID == "" {
		return
	}
	if reason == "" {
		reason = "cancelled"
	}
	m.mu.Lock()
	if prev := m.canceledCalls[callID]; prev == "" || reason == "runtime_stopped" {
		m.canceledCalls[callID] = reason
	}
	sess := m.fgByCall[callID]
	var proc sandbox.SessionProcess
	var cancelLaunch context.CancelFunc
	if sess != nil {
		if sess.stopReason == "" {
			sess.stopReason = reason
		}
		proc = sess.proc
		cancelLaunch = sess.cancelLaunch
	}
	tk := m.fgApprovalsByCall[callID]
	wakeSched := false
	if tk != nil {
		wakeSched = m.cancelApprovalTicketLocked(tk, reason)
	} else {
		for _, rtk := range m.resolvedApprovals {
			if rtk.callID == callID && !rtk.settled && rtk.cancelReason == "" {
				rtk.cancelReason = reason
			}
		}
	}
	m.mu.Unlock()
	if cancelLaunch != nil {
		cancelLaunch()
	}
	if wakeSched {
		m.svc.sched.wake()
	}
	if sess != nil {
		sess.fgCancelOnce.Do(func() { close(sess.fgCancelCh) })
		if proc != nil {
			proc.Terminate(reason)
		}
	}
}

func (m *execManager) CleanupCallApproval(callID, reason string) {
	if m == nil || callID == "" {
		return
	}
	if reason == "" {
		reason = "cancelled"
	}
	m.mu.Lock()
	if prev := m.canceledCalls[callID]; prev == "" || reason == "runtime_stopped" {
		m.canceledCalls[callID] = reason
	}
	tk := m.fgApprovalsByCall[callID]
	wakeSched := false
	if tk != nil {
		wakeSched = m.cancelApprovalTicketLocked(tk, reason)
		delete(m.fgApprovalsByCall, callID)
		delete(m.approvals, tk.id)
		m.resolvedApprovals[tk.id] = tk
	} else {
		for _, rtk := range m.resolvedApprovals {
			if rtk.callID == callID && !rtk.settled && rtk.cancelReason == "" {
				rtk.cancelReason = reason
			}
		}
	}
	m.mu.Unlock()
	if wakeSched {
		m.svc.sched.wake()
	}
}

func (m *execManager) PurgeResolvedCallApproval(callID string) {
	if m == nil || callID == "" {
		return
	}
	m.mu.Lock()
	for id, tk := range m.resolvedApprovals {
		if tk.callID == callID && tk.settled {
			delete(m.resolvedApprovals, id)
		}
	}
	delete(m.canceledCalls, callID)
	m.mu.Unlock()
}

func (m *execManager) InitiateTurnStop(turn, reason string) {
	if m == nil || turn == "" {
		return
	}
	if reason == "" {
		reason = "cancelled"
	}
	m.mu.Lock()
	if prev := m.turnStopped[turn]; prev == "" || reason == "runtime_stopped" {
		m.turnStopped[turn] = reason
	}
	var procs []sandbox.SessionProcess
	var cancelFns []context.CancelFunc
	for _, sess := range m.sessions {
		if sess.turn != turn {
			continue
		}
		if sess.stopReason == "" || reason == "runtime_stopped" {
			sess.stopReason = reason
		}
		if sess.cancelLaunch != nil {
			cancelFns = append(cancelFns, sess.cancelLaunch)
		}
		sess.fgCancelOnce.Do(func() { close(sess.fgCancelCh) })
		if !sess.exited && sess.proc != nil {
			procs = append(procs, sess.proc)
		}
	}
	wakeSched := false
	for _, tk := range m.approvals {
		if tk.scope.Turn == turn && m.cancelApprovalTicketLocked(tk, reason) {
			wakeSched = true
		}
	}
	m.mu.Unlock()
	for _, fn := range cancelFns {
		fn()
	}
	if wakeSched {
		m.svc.sched.wake()
	}
	for _, proc := range procs {
		proc.Terminate(reason)
	}
}

func (m *execManager) preRegisterSpawningSession(callID string, scope Scope, summary, command string, escalated bool, meta execApprovalMeta, lockedFiles []io.Closer, cancelLaunch context.CancelFunc) (*execSession, error) {
	if summary == "" {
		if clean, err := sanitizeSummary(redactCommandSummary(command)); err == nil {
			summary = clean
		}
	}
	wsRoot := ""
	if m.svc != nil {
		wsRoot = m.svc.root
	}
	sess := &execSession{
		id:               "es_" + rand.Text(),
		scope:            scope,
		turn:             scope.Turn,
		callID:           callID,
		summary:          summary,
		wsRoot:           wsRoot,
		tempRoot:         m.tempRoot,
		escalated:        escalated,
		approvalMode:     meta.approvalMode,
		userApproved:     meta.userApproved,
		approvalDecision: meta.approvalDecision,
		approvalWaitMs:   meta.approvalWaitMs,
		started:          time.Now().UTC(),
		slotHeld:         true,
		inFgWait:         true,
		cancelLaunch:     cancelLaunch,
		lockedFiles:      lockedFiles,
		fgCancelCh:       make(chan struct{}),
		detachCh:         make(chan struct{}),
	}
	m.mu.Lock()
	if stopReason := m.callStopReasonLocked(callID, scope.Turn); stopReason != "" {
		m.mu.Unlock()
		return nil, fail(stopReason)
	}
	if m.turnCounts[scope.Turn] >= MaxExecSessionsPerTurn {
		m.mu.Unlock()
		return nil, fail("exec_session_limit")
	}
	m.turnCounts[scope.Turn]++
	m.sessions[sess.id] = sess
	m.fgByCall[callID] = sess
	m.mu.Unlock()
	return sess, nil
}

func (s *Service) admitSessionResume(callID, turn string, sess *execSession, resume func() error) error {
	s.sched.mu.Lock()
	e := s.sched.entries[callID]
	closed := e == nil || e.admissionClosed
	cancelReason := ""
	if e != nil {
		cancelReason = e.cancelReason
	}
	s.sched.mu.Unlock()
	if closed {
		if cancelReason == "" {
			cancelReason = "cancelled"
		}
		return sandbox.NewError(cancelReason)
	}
	m := s.exec
	m.mu.Lock()
	stopReason := m.callStopReasonLocked(callID, turn)
	if stopReason == "" && sess.stopReason != "" {
		stopReason = sess.stopReason
	}
	if stopReason != "" {
		m.mu.Unlock()
		return sandbox.NewError(stopReason)
	}
	err := resume()
	if err == nil {
		sess.resumed = true
	}
	m.mu.Unlock()
	return err
}

func (m *execManager) attachLaunchedProc(callID, turn string, sess *execSession, proc sandbox.SessionProcess) (bool, string) {
	m.mu.Lock()
	sess.proc = proc
	stopReason := m.callStopReasonLocked(callID, turn)
	if stopReason == "" && sess.stopReason != "" {
		stopReason = sess.stopReason
	}
	stopped := stopReason != ""
	if stopped {
		sess.stopReason = stopReason
		delete(m.fgByCall, callID)
	}
	m.mu.Unlock()
	return stopped, stopReason
}

func (m *execManager) clearFgCall(callID string) {
	m.mu.Lock()
	delete(m.fgByCall, callID)
	m.mu.Unlock()
}

func (m *execManager) rollbackSpawningSession(callID string, sess *execSession) {
	m.mu.Lock()
	if m.fgByCall[callID] == sess {
		delete(m.fgByCall, callID)
	}
	delete(m.sessions, sess.id)
	sess.exited = true
	sess.inFgWait = false
	m.releaseTurnSlotLocked(sess)
	close(sess.detachCh)
	m.broadcastSettledLocked()
	m.mu.Unlock()
	sess.closeLockedFiles()
}

// TurnSettled reports whether every session in turn has exited, drained OS
// process handles/pipes, and completed any detached es_*.json finalization.
func (m *execManager) TurnSettled(turn string) (bool, error) {
	if m == nil || turn == "" {
		return true, nil
	}
	m.mu.Lock()
	for _, tk := range m.approvals {
		if tk.scope.Turn == turn && !tk.settled {
			m.mu.Unlock()
			return false, nil
		}
	}
	for _, tk := range m.resolvedApprovals {
		if tk.scope.Turn == turn && !tk.settled {
			m.mu.Unlock()
			return false, nil
		}
	}
	list := make([]*execSession, 0)
	for _, sess := range m.sessions {
		if sess.turn == turn {
			if sess.inFgWait || !sess.exited || (sess.detached && !sess.finalized) {
				m.mu.Unlock()
				return false, nil
			}
			list = append(list, sess)
		}
	}
	m.mu.Unlock()
	for _, sess := range list {
		if sess.proc == nil {
			continue
		}
		stopped, err := sess.proc.ProcessesStopped()
		if err != nil {
			return false, err
		}
		if !stopped {
			return false, nil
		}
	}
	return true, nil
}

func (m *execManager) ReleaseTurnSessions(turn string) error {
	if m == nil || turn == "" {
		return nil
	}
	m.mu.Lock()
	var toClose []*execSession
	for id, sess := range m.sessions {
		if sess.turn == turn {
			sess.slotHeld = false
			toClose = append(toClose, sess)
			delete(m.sessions, id)
		}
	}
	wakeSched := false
	for id, tk := range m.approvals {
		if tk.scope.Turn == turn {
			if m.cancelApprovalTicketLocked(tk, "cancelled") {
				wakeSched = true
			}
			delete(m.fgApprovalsByCall, tk.callID)
			delete(m.approvals, id)
		}
	}
	for id, tk := range m.resolvedApprovals {
		if tk.scope.Turn == turn {
			delete(m.resolvedApprovals, id)
		}
	}
	delete(m.turnCounts, turn)
	delete(m.turnStopped, turn)
	m.mu.Unlock()
	if wakeSched {
		m.svc.sched.wake()
	}

	for _, sess := range toClose {
		if sess.proc != nil {
			_ = sess.proc.Close()
		}
		sess.closeLockedFiles()
	}
	return m.cleanupTemp(turn, true)
}

func (m *execManager) StopAll(budget time.Duration) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	turns := map[string]bool{}
	for _, sess := range m.sessions {
		if sess.turn != "" {
			turns[sess.turn] = true
		}
	}
	for _, tk := range m.approvals {
		if tk.scope.Turn != "" {
			turns[tk.scope.Turn] = true
		}
		m.cancelApprovalTicketLocked(tk, "runtime_stopped")
	}
	m.mu.Unlock()
	for turn := range turns {
		m.InitiateTurnStop(turn, "runtime_stopped")
	}

	m.waitAllExecSettled(budget)

	m.mu.Lock()
	toClose := make([]*execSession, 0, len(m.sessions))
	for id, sess := range m.sessions {
		sess.slotHeld = false
		toClose = append(toClose, sess)
		delete(m.sessions, id)
	}
	for _, tk := range m.approvals {
		m.cancelApprovalTicketLocked(tk, "runtime_stopped")
	}
	m.approvals = map[string]*approvalTicket{}
	m.fgApprovalsByCall = map[string]*approvalTicket{}
	m.resolvedApprovals = map[string]*approvalTicket{}
	m.fgByCall = map[string]*execSession{}
	m.turnCounts = map[string]int{}
	m.turnStopped = map[string]string{}
	m.canceledCalls = map[string]string{}
	m.mu.Unlock()

	m.svc.sched.activeExec.Store(0)
	m.svc.sched.awaitingApproval.Store(0)
	m.svc.sched.wake()
	for _, sess := range toClose {
		if sess.proc != nil {
			_ = sess.proc.Close()
		}
		sess.closeLockedFiles()
	}
	return m.cleanupTemp("", true)
}

func (m *execManager) waitAllExecSettled(budget time.Duration) {
	deadline := time.Now().Add(budget)
	for {
		settled := true
		m.mu.Lock()
		procs := make([]sandbox.SessionProcess, 0, len(m.sessions))
		for _, sess := range m.sessions {
			if sess.inFgWait || !sess.exited || (sess.detached && !sess.finalized) {
				settled = false
				break
			}
			if sess.proc != nil {
				procs = append(procs, sess.proc)
			}
		}
		if settled {
			for _, tk := range m.approvals {
				if !tk.settled {
					settled = false
					break
				}
			}
		}
		if settled {
			for _, tk := range m.resolvedApprovals {
				if !tk.settled {
					settled = false
					break
				}
			}
		}
		waitCh := m.settledNotify
		m.mu.Unlock()
		if settled {
			for _, p := range procs {
				stopped, err := p.ProcessesStopped()
				if err != nil || !stopped {
					settled = false
					break
				}
			}
		}
		if settled || time.Now().After(deadline) {
			return
		}
		step := min(time.Until(deadline), 25*time.Millisecond)
		timer := time.NewTimer(step)
		select {
		case <-waitCh:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (m *execManager) ensureWatchdog(turn string) {
	if turn == "" {
		return
	}
	m.mu.Lock()
	if m.watchdogTurns[turn] {
		m.mu.Unlock()
		return
	}
	m.watchdogTurns[turn] = true
	m.mu.Unlock()
	go m.runWatchdog(turn)
}

func (m *execManager) runWatchdog(turn string) {
	ticker := time.NewTicker(quotaWatchInterval)
	defer func() {
		ticker.Stop()
		m.mu.Lock()
		delete(m.watchdogTurns, turn)
		m.mu.Unlock()
	}()
	for range ticker.C {
		if !m.hasLiveTurnSessions(turn) {
			return
		}
		if reason := m.checkTurnQuota(); reason != "" {
			if reason == "workspace_limit" {
				m.svc.mu.Lock()
				m.svc.overQuota = true
				m.svc.mu.Unlock()
			}
			m.InitiateTurnStop(turn, reason)
			return
		}
	}
}

func (m *execManager) hasLiveTurnSessions(turn string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.turnStopped[turn] != "" {
		return false
	}
	for _, sess := range m.sessions {
		if sess.turn == turn && !sess.exited {
			return true
		}
	}
	return false
}

func (m *execManager) checkTurnQuota() string {
	if _, _, err := m.svc.measureWorkspaceDisk(0, 0); err != nil {
		if Code(err) == "workspace_limit" {
			return "workspace_limit"
		}
	}
	bytesCap, filesCap := int64(MaxSandboxTempBytes), MaxSandboxTempFiles
	m.mu.Lock()
	if m.testTempBytes > 0 {
		bytesCap = m.testTempBytes
	}
	if m.testTempFiles > 0 {
		filesCap = m.testTempFiles
	}
	m.mu.Unlock()
	if _, _, err := measureSandboxTemp(m.tempRoot, bytesCap, filesCap); err != nil {
		return "sandbox_temp_limit"
	}
	return ""
}

func measureSandboxTemp(tempRoot string, maxBytes int64, maxFiles int) (int64, int, error) {
	var totalBytes int64
	totalFiles := 0
	var walk func(dir string) error
	walk = func(dir string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		for _, item := range entries {
			if item.Type()&(os.ModeSymlink|os.ModeIrregular) != 0 {
				continue
			}
			info, err := item.Info()
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return err
			}
			if hasReparseAttribute(info) {
				continue
			}
			totalFiles++
			if totalFiles > maxFiles {
				return fail("sandbox_temp_limit")
			}
			full := filepath.Join(dir, item.Name())
			if info.IsDir() {
				if err := walk(full); err != nil {
					return err
				}
				continue
			}
			totalBytes += info.Size()
			if totalBytes > maxBytes {
				return fail("sandbox_temp_limit")
			}
		}
		return nil
	}
	if err := walk(tempRoot); err != nil {
		return 0, 0, err
	}
	return totalBytes, totalFiles, nil
}

func sortExecSessionSummaries(items []ExecSessionSummary) {
	sort.Slice(items, func(i, j int) bool { return items[i].SessionID < items[j].SessionID })
}
