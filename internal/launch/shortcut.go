package launch

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackgan90/Asterhand/internal/i18n"
)

const (
	shortcutOpTTL = 10 * time.Minute
	shortcutOpCap = 256
)

type ShortcutStatus struct {
	Supported     bool              `json:"supported"`
	Available     bool              `json:"available"`
	Enabled       bool              `json:"enabled"`
	Conflict      bool              `json:"conflict"`
	Reason        string            `json:"reason,omitempty"`
	Message       string            `json:"message,omitempty"`
	MessageKey    string            `json:"message_key,omitempty"`
	MessageArgs   map[string]string `json:"message_args,omitempty"`
	LastOperation string            `json:"last_operation,omitempty"`
}

func (s ShortcutStatus) Localize(locale string) ShortcutStatus {
	s.MessageArgs = i18n.CloneArgs(s.MessageArgs)
	if s.MessageKey != "" {
		s.Message = i18n.LocalizeMessage(locale, s.MessageKey, s.MessageArgs, s.Message)
	}
	return s
}

type ShortcutChange struct {
	Operation string `json:"operation_id"`
	Enabled   bool   `json:"enabled"`
}

type ShortcutError struct {
	Status  int
	Code    string
	Message string
	Key     string
	Args    map[string]string
}

func NewShortcutError(status int, code, key string, args map[string]string) *ShortcutError {
	cloned := i18n.CloneArgs(args)
	return &ShortcutError{
		Status:  status,
		Code:    code,
		Key:     key,
		Args:    cloned,
		Message: i18n.LocalizeMessage(i18n.Effective(), key, cloned, ""),
	}
}

func (e *ShortcutError) Error() string {
	if e == nil {
		return ""
	}
	if e.Key != "" {
		return i18n.LocalizeMessage(i18n.Effective(), e.Key, e.Args, e.Message)
	}
	return e.Message
}

func (e *ShortcutError) I18nKey() (string, map[string]string) {
	if e == nil {
		return "", nil
	}
	return e.Key, i18n.CloneArgs(e.Args)
}

type ShortcutOptions struct {
	Home       string
	Executable func() (string, error)
	DesktopDir func() (string, error)
}

type shortcutOpRecord struct {
	enabled bool
	status  ShortcutStatus
	err     error
	expires time.Time
}

type ShortcutManager struct {
	mu     sync.Mutex
	opts   ShortcutOptions
	lastOp string
	ops    map[string]shortcutOpRecord
}

func NewShortcutManager(home string) *ShortcutManager {
	return NewShortcutManagerWith(ShortcutOptions{Home: home})
}

func NewShortcutManagerWith(opts ShortcutOptions) *ShortcutManager {
	if opts.Executable == nil {
		opts.Executable = os.Executable
	}
	if opts.DesktopDir == nil {
		opts.DesktopDir = defaultDesktopDir
	}
	return &ShortcutManager{
		opts: opts,
		ops:  map[string]shortcutOpRecord{},
	}
}

func (m *ShortcutManager) Status() ShortcutStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusLocked().Localize(i18n.Effective())
}

func (m *ShortcutManager) statusLocked() ShortcutStatus {
	st := m.inspectLocked()
	st.LastOperation = m.lastOp
	return st
}

func (m *ShortcutManager) Set(ctx context.Context, change ShortcutChange) (ShortcutStatus, error) {
	if !validShortcutOperationID(change.Operation) {
		return m.Status(), NewShortcutError(400, "invalid_parameters", "err.quickinput.invalid_op_id", nil)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	if prev, ok := m.ops[change.Operation]; ok && now.Before(prev.expires) {
		if prev.enabled != change.Enabled {
			return m.statusLocked().Localize(i18n.Effective()), NewShortcutError(409, "conflict", "err.quickinput.op_id_reused", nil)
		}
		return prev.status.Localize(i18n.Effective()), prev.err
	}
	if !platformShortcutSupported() {
		st := ShortcutStatus{Supported: false}
		return st, NewShortcutError(400, "unsupported", "err.launch.shortcut_unsupported", nil)
	}
	st, err := m.applyPlatformLocked(ctx, change.Enabled)
	if err == nil {
		m.lastOp = change.Operation
	}
	st.LastOperation = m.lastOp
	st = st.Localize(i18n.Effective())
	m.recordOpLocked(change.Operation, shortcutOpRecord{
		enabled: change.Enabled,
		status:  st,
		err:     err,
		expires: now.Add(shortcutOpTTL),
	}, now)
	return st, err
}

func (m *ShortcutManager) recordOpLocked(id string, rec shortcutOpRecord, now time.Time) {
	for k, v := range m.ops {
		if !now.Before(v.expires) {
			delete(m.ops, k)
		}
	}
	if len(m.ops) >= shortcutOpCap {
		oldestKey := ""
		var oldestTime time.Time
		for k, v := range m.ops {
			if oldestKey == "" || v.expires.Before(oldestTime) {
				oldestKey = k
				oldestTime = v.expires
			}
		}
		if oldestKey != "" {
			delete(m.ops, oldestKey)
		}
	}
	m.ops[id] = rec
}

func validShortcutOperationID(id string) bool {
	if len(id) < 8 || len(id) > 100 || strings.TrimSpace(id) != id {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] <= ' ' || id[i] > '~' {
			return false
		}
	}
	return true
}
