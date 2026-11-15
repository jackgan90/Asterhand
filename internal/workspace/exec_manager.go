package workspace

import (
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/jackgan90/Asterhand/internal/platform/sandbox"
)

const quotaWatchInterval = 2 * time.Second

type execSession struct {
	id           string
	scope        Scope
	turn         string
	callID       string
	summary      string
	proc         sandbox.SessionProcess
	started      time.Time
	slotHeld     bool
	inFgWait     bool
	detached     bool
	exited       bool
	finalized    bool
	activeDec    bool
	unsafeLinks  bool
	pollCursor   int64
	stopReason   string
	fgCancelCh   chan struct{}
	fgCancelOnce sync.Once
	detachCh     chan struct{}
}

type execManager struct {
	svc           *Service
	backend       sandbox.Backend
	tempRoot      string
	cleanMu       sync.RWMutex
	mu            sync.Mutex
	sessions      map[string]*execSession
	fgByCall      map[string]*execSession
	turnCounts    map[string]int
	turnStopped   map[string]string
	watchdogTurns map[string]bool
	settledNotify chan struct{}
	testTempBytes int64
	testTempFiles int
}

func newExecManager(svc *Service, backend sandbox.Backend, tempRoot string) *execManager {
	return &execManager{
		svc:           svc,
		backend:       backend,
		tempRoot:      tempRoot,
		sessions:      map[string]*execSession{},
		fgByCall:      map[string]*execSession{},
		turnCounts:    map[string]int{},
		turnStopped:   map[string]string{},
		watchdogTurns: map[string]bool{},
		settledNotify: make(chan struct{}),
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

func (m *execManager) CancelForegroundCall(callID, reason string) {
	if m == nil || callID == "" {
		return
	}
	if reason == "" {
		reason = "cancelled"
	}
	m.mu.Lock()
	sess := m.fgByCall[callID]
	if sess != nil && sess.stopReason == "" {
		sess.stopReason = reason
	}
	m.mu.Unlock()
	if sess != nil {
		sess.fgCancelOnce.Do(func() { close(sess.fgCancelCh) })
		if sess.proc != nil {
			sess.proc.Terminate(reason)
		}
	}
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
	for _, sess := range m.sessions {
		if sess.turn != turn {
			continue
		}
		if sess.stopReason == "" || reason == "runtime_stopped" {
			sess.stopReason = reason
		}
		sess.fgCancelOnce.Do(func() { close(sess.fgCancelCh) })
		if !sess.exited && sess.proc != nil {
			procs = append(procs, sess.proc)
		}
	}
	m.mu.Unlock()
	for _, proc := range procs {
		proc.Terminate(reason)
	}
}

// TurnSettled reports whether every session in turn has exited, drained OS
// process handles/pipes, and completed any detached es_*.json finalization.
func (m *execManager) TurnSettled(turn string) (bool, error) {
	if m == nil || turn == "" {
		return true, nil
	}
	m.mu.Lock()
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
	delete(m.turnCounts, turn)
	delete(m.turnStopped, turn)
	m.mu.Unlock()

	for _, sess := range toClose {
		if sess.proc != nil {
			_ = sess.proc.Close()
		}
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
	m.mu.Unlock()
	for turn := range turns {
		m.InitiateTurnStop(turn, "runtime_stopped")
	}

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
			break
		}
		step := min(time.Until(deadline), 25*time.Millisecond)
		timer := time.NewTimer(step)
		select {
		case <-waitCh:
			timer.Stop()
		case <-timer.C:
		}
	}

	m.mu.Lock()
	toClose := make([]*execSession, 0, len(m.sessions))
	for id, sess := range m.sessions {
		sess.slotHeld = false
		toClose = append(toClose, sess)
		delete(m.sessions, id)
	}
	m.fgByCall = map[string]*execSession{}
	m.turnCounts = map[string]int{}
	m.turnStopped = map[string]string{}
	m.mu.Unlock()

	m.svc.sched.activeExec.Store(0)
	m.svc.sched.wake()
	for _, sess := range toClose {
		if sess.proc != nil {
			_ = sess.proc.Close()
		}
	}
	return m.cleanupTemp("", true)
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
