package quickinput

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"runtime"
	"sync"
	"time"

	"github.com/jackgan90/Asterhand/internal/i18n"
)

type Registrar interface {
	Register(Binding) (int, error)
	Unregister(int) error
}
type Surface interface {
	Toggle()
	Update(string) error
	UpdateLocale(string) error
	Snapshot(context.Context) error
	Close() error
}
type SurfaceFactory func() (Surface, error)
type RegistrationError struct {
	Code   string
	Detail string
	Key    string
	Args   map[string]string
}

func (e *RegistrationError) Error() string {
	if e == nil {
		return ""
	}
	if e.Key != "" {
		return i18n.LocalizeMessage(i18n.Effective(), e.Key, e.Args, e.Detail)
	}
	return e.Detail
}

func (e *RegistrationError) I18nKey() (string, map[string]string) {
	if e == nil {
		return "", nil
	}
	return e.Key, i18n.CloneArgs(e.Args)
}

type Error struct {
	Status  int
	Message string
	Key     string
	Args    map[string]string
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Key != "" {
		return i18n.LocalizeMessage(i18n.Effective(), e.Key, e.Args, e.Message)
	}
	return e.Message
}

func (e *Error) I18nKey() (string, map[string]string) {
	if e == nil {
		return "", nil
	}
	return e.Key, i18n.CloneArgs(e.Args)
}

func statusError(status int, key string, args ...map[string]string) *Error {
	var params map[string]string
	if len(args) > 0 {
		params = i18n.CloneArgs(args[0])
	}
	return &Error{Status: status, Message: i18n.T(i18n.Effective(), key, params), Key: key, Args: params}
}

func wrapStatusError(status int, err error) *Error {
	if key, args, ok := i18n.ErrorDetails(err); ok {
		return &Error{Status: status, Message: err.Error(), Key: key, Args: args}
	}
	return &Error{Status: status, Message: err.Error()}
}

type Status struct {
	Desired              Preferences       `json:"desired"`
	Registration         string            `json:"registration"`
	Effective            *Binding          `json:"effective_binding,omitempty"`
	Surface              string            `json:"surface"`
	Error                string            `json:"error,omitempty"`
	ErrorCode            string            `json:"error_code,omitempty"`
	ErrorKey             string            `json:"error_key,omitempty"`
	ErrorArgs            map[string]string `json:"error_args,omitempty"`
	CleanupPending       int               `json:"cleanup_pending"`
	Supported            bool              `json:"supported"`
	LastOperation        string            `json:"last_operation,omitempty"`
	LastLocaleOperation  string            `json:"last_locale_operation,omitempty"`
	EffectiveLocale      string            `json:"effective_locale,omitempty"`
	SystemLocaleDetected bool              `json:"system_locale_detected"`
	RuntimeVersion       string            `json:"runtime_version,omitempty"`
	RecoveryNotice       string            `json:"recovery_notice,omitempty"`
	RecoveryNoticeKey    string            `json:"recovery_notice_key,omitempty"`
}

func (s Status) Localize(locale string) Status {
	if !i18n.ValidLocale(locale) {
		locale = s.EffectiveLocale
	}
	if !i18n.ValidLocale(locale) {
		locale = i18n.Effective()
	}
	if s.ErrorKey != "" {
		s.Error = i18n.LocalizeMessage(locale, s.ErrorKey, s.ErrorArgs, s.Error)
	}
	if s.RecoveryNoticeKey != "" {
		s.RecoveryNotice = i18n.LocalizeMessage(locale, s.RecoveryNoticeKey, nil, s.RecoveryNotice)
	}
	return s
}

type Change struct {
	Operation string  `json:"operation_id"`
	Revision  string  `json:"expected_revision"`
	Enabled   bool    `json:"enabled"`
	Binding   Binding `json:"binding"`
	Theme     string  `json:"theme_id"`
	Locale    *string `json:"locale,omitempty"`
	Reset     bool    `json:"reset,omitempty"`
}

type Controller struct {
	op                sync.Mutex
	mu                sync.Mutex
	home              string
	store             *PreferencesStore
	status            Status
	registrar         Registrar
	factory           SurfaceFactory
	surface           Surface
	surfaceGeneration uint64
	closingFailed     bool
	active            int
	retired           []int
	cleanupScheduled  bool
	stopping          bool
	invalid           bool
	operations        map[string]operation
	record            *recording
	draft             Draft
	chat              *chatController
	commands          CommandHandler
	readyAt           time.Time
	recoveryUsed      bool
	pendingActivation activationIntent
	captureIntent     func() activationIntent
}

type operation struct {
	input   string
	result  Status
	err     error
	expires time.Time
}

func New(home string) *Controller {
	c := &Controller{home: home, store: NewPreferencesStore(home), operations: map[string]operation{}, captureIntent: captureActivation}
	c.status = Status{Desired: Defaults(), Registration: "initializing", Surface: "not_created", Supported: runtime.GOOS == "windows"}
	if p, err := c.store.Load(); err == nil {
		c.status.Desired = p
		i18n.SetDefaultPreference(p.Locale)
	} else {
		i18n.SetDefaultPreference("auto")
		c.invalid = true
		c.status.Registration, c.status.Error = "config_invalid", err.Error()
		c.status.ErrorCode = "config_invalid"
		c.status.ErrorKey, c.status.ErrorArgs, _ = i18n.ErrorDetails(err)
	}
	if !c.status.Supported {
		c.status.Registration = "unsupported"
	}
	return c
}

func (c *Controller) Status() Status {
	c.mu.Lock()
	eff, detected := i18n.ResolveLocale(c.status.Desired.Locale)
	c.status.EffectiveLocale = eff
	c.status.SystemLocaleDetected = detected
	if c.status.ErrorKey != "" {
		c.status.Error = i18n.LocalizeMessage(eff, c.status.ErrorKey, c.status.ErrorArgs, c.status.Error)
	}
	if c.status.RecoveryNoticeKey != "" {
		c.status.RecoveryNotice = i18n.LocalizeMessage(eff, c.status.RecoveryNoticeKey, nil, c.status.RecoveryNotice)
	}
	b, _ := json.Marshal(c.status)
	c.mu.Unlock()
	var result Status
	_ = json.Unmarshal(b, &result)
	return result
}

func (c *Controller) Attach(registrar Registrar, factory SurfaceFactory) {
	c.op.Lock()
	c.registrar, c.factory = registrar, factory
	c.op.Unlock()
	go func() { _, _ = c.Retry() }()
}

func (c *Controller) setError(state string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.Registration, c.status.Error = state, err.Error()
	c.status.ErrorCode = state
	c.status.ErrorKey, c.status.ErrorArgs, _ = i18n.ErrorDetails(err)
	var nativeError *RegistrationError
	if errors.As(err, &nativeError) {
		c.status.ErrorCode = nativeError.Code
	}
}

func (c *Controller) clearErrorLocked() {
	c.status.Error, c.status.ErrorCode, c.status.ErrorKey, c.status.ErrorArgs = "", "", "", nil
}

func (c *Controller) ReadySurface() (Surface, error) {
	if c.closingFailed {
		return nil, i18n.Err("err.quickinput.old_surface_cleanup")
	}
	if c.surface != nil {
		return c.surface, nil
	}
	if c.factory == nil {
		return nil, i18n.Err("err.quickinput.desktop_not_connected")
	}
	c.mu.Lock()
	c.status.Surface = "initializing"
	c.surfaceGeneration++
	c.mu.Unlock()
	surface, err := c.factory()
	if err != nil {
		c.mu.Lock()
		c.status.Surface = "failed"
		if surface != nil {
			c.surface = surface
			c.closingFailed = true
			c.status.Surface = "closing"
		}
		c.mu.Unlock()
		return nil, err
	}
	c.mu.Lock()
	c.surface = surface
	c.status.Surface = "ready"
	c.readyAt = time.Now()
	c.mu.Unlock()
	if c.isStopping() {
		c.closeSurface()
		return nil, statusError(503, "err.app.node_stopping")
	}
	st := c.Status()
	if err := surface.Update(st.Desired.Theme); err != nil {
		c.failSurface(err)
		return nil, err
	}
	if err := surface.UpdateLocale(st.EffectiveLocale); err != nil {
		c.failSurface(err)
		return nil, err
	}
	return surface, nil
}

func (c *Controller) Retry() (Status, error) {
	c.op.Lock()
	defer c.op.Unlock()
	if c.isStopping() {
		return c.Status(), statusError(503, "err.app.node_stopping")
	}
	if c.invalid {
		return c.Status(), statusError(409, "err.quickinput.config_invalid_restore")
	}
	c.recoveryUsed = false
	if !c.Status().Supported || c.registrar == nil {
		return c.Status(), statusError(503, "err.quickinput.no_interactive_desktop")
	}
	if err := c.cleanup(); err != nil {
		return c.Status(), err
	}
	if !c.Status().Desired.Enabled {
		c.mu.Lock()
		c.status.Registration = "disabled"
		c.clearErrorLocked()
		c.mu.Unlock()
		return c.Status(), nil
	}
	if _, err := c.ReadySurface(); err != nil {
		c.setError("unavailable", err)
		return c.Status(), err
	}
	if c.active != 0 {
		c.mu.Lock()
		c.status.Registration = "registered"
		c.clearErrorLocked()
		c.mu.Unlock()
		return c.Status(), nil
	}
	id, err := c.registrar.Register(c.Status().Desired.Binding)
	if err != nil {
		c.registrationFailed(err)
		return c.Status(), err
	}
	c.mu.Lock()
	c.active = id
	c.status.Registration = "registered"
	c.clearErrorLocked()
	b := c.status.Desired.Binding
	c.status.Effective = &b
	c.mu.Unlock()
	return c.Status(), nil
}

func (c *Controller) registrationFailed(err error) {
	state := "unavailable"
	var re *RegistrationError
	if errors.As(err, &re) && re.Code == "conflict" {
		state = "conflict"
	}
	c.setError(state, err)
}

func (c *Controller) OnHotkey(id int, combination ...uint32) {
	c.mu.Lock()
	if c.stopping || c.active == 0 || c.active != id {
		c.mu.Unlock()
		return
	}
	if len(combination) == 2 {
		modifiers, key, _ := c.status.Desired.Binding.WindowsKeys()
		if combination[0] != modifiers&7 || combination[1] != key {
			c.mu.Unlock()
			return
		}
	}
	if c.record != nil && time.Now().Before(c.record.Expires) {
		b := c.status.Desired.Binding
		c.record.Binding = &b
		c.mu.Unlock()
		return
	}
	c.record = nil
	if c.status.Registration == "recovering" {
		c.pendingActivation = c.captureIntent()
		c.mu.Unlock()
		return
	}
	s := c.surface
	if c.status.Surface != "ready" {
		s = nil
	}
	c.mu.Unlock()
	if s != nil {
		s.Toggle()
	}
}

func (c *Controller) Open() error {
	intent := c.captureIntent()
	c.mu.Lock()
	if !c.stopping && c.status.Registration == "recovering" {
		c.pendingActivation = intent
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	c.op.Lock()
	defer c.op.Unlock()
	if c.isStopping() {
		return i18n.Err("err.app.node_stopping")
	}
	if c.invalid {
		return i18n.Err("err.quickinput.restore_in_web")
	}
	if c.registrar == nil || !c.Status().Supported {
		return i18n.Err("err.quickinput.no_desktop_generic")
	}
	if !c.Status().Desired.Enabled {
		return i18n.Err("err.quickinput.enable_in_web")
	}
	c.recoveryUsed = false
	s, err := c.ReadySurface()
	if err == nil {
		if c.active == 0 {
			id, registrationErr := c.registrar.Register(c.Status().Desired.Binding)
			if registrationErr != nil {
				c.registrationFailed(registrationErr)
			} else {
				c.mu.Lock()
				c.active = id
				b := c.status.Desired.Binding
				c.status.Effective = &b
				c.status.Registration = "registered"
				c.clearErrorLocked()
				c.mu.Unlock()
			}
		}
		applyActivation(intent, s)
	} else {
		c.setError("unavailable", err)
	}
	return err
}

func (c *Controller) Failed(err error) {
	c.mu.Lock()
	generation := c.surfaceGeneration
	c.mu.Unlock()
	c.failedGeneration(generation, err)
}

func (c *Controller) failedGeneration(generation uint64, err error) {
	var nativeError *RegistrationError
	if errors.As(err, &nativeError) && nativeError.Code == "focus_denied" {
		c.mu.Lock()
		if generation == c.surfaceGeneration {
			c.status.Error = err.Error()
			c.status.ErrorCode = nativeError.Code
			c.status.ErrorKey, c.status.ErrorArgs, _ = i18n.ErrorDetails(err)
		}
		c.mu.Unlock()
		return
	}
	go func() {
		c.op.Lock()
		defer c.op.Unlock()
		if c.isStopping() {
			return
		}
		c.mu.Lock()
		if generation != c.surfaceGeneration {
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()
		c.recoverSurface(err)
	}()
}

func (c *Controller) failSurface(err error) {
	c.mu.Lock()
	id := c.active
	c.active = 0
	c.status.Effective = nil
	c.pendingActivation = nil
	c.status.RecoveryNoticeKey = "status.quickinput.recovery_rebuilt"
	c.status.RecoveryNotice = i18n.T(i18n.Effective(), "status.quickinput.recovery_rebuilt")
	c.mu.Unlock()
	if id != 0 {
		c.retire(id)
	}
	c.setError("unavailable", err)
	c.closeSurface()
	c.mu.Lock()
	if !c.closingFailed {
		c.status.Surface = "failed"
	}
	c.mu.Unlock()
}

// Called under op. An unconfirmed teardown prevents a second host using UDF.
func (c *Controller) closeSurface() {
	if c.surface == nil {
		return
	}
	c.mu.Lock()
	c.surfaceGeneration++ // Invalidate callbacks before closing the old host.
	c.status.Surface = "closing"
	c.mu.Unlock()
	err := c.surface.Close()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.closingFailed = true
		c.status.Surface = "closing"
		c.status.Error = err.Error()
		c.status.ErrorKey, c.status.ErrorArgs, _ = i18n.ErrorDetails(err)
		log.Print(i18n.T(i18n.Effective(), "err.quickinput.cleanup_failed_log", map[string]string{"err": err.Error()}))
		return
	}
	c.surface = nil
	c.status.Surface = "not_created"
}

func (c *Controller) cleanup() error {
	remaining := []int{}
	for _, id := range c.retired {
		if c.registrar.Unregister(id) != nil {
			remaining = append(remaining, id)
		}
	}
	c.retired = remaining
	c.mu.Lock()
	c.status.CleanupPending = len(remaining)
	c.mu.Unlock()
	if len(remaining) != 0 {
		return statusError(409, "err.quickinput.old_reg_cleanup")
	}
	return nil
}

func (c *Controller) retire(id int) {
	if c.registrar.Unregister(id) != nil {
		c.retired = append(c.retired, id)
	}
	c.mu.Lock()
	c.status.CleanupPending = len(c.retired)
	c.mu.Unlock()
	if len(c.retired) != 0 && !c.cleanupScheduled && !c.isStopping() {
		c.cleanupScheduled = true
		go c.retryCleanup()
	}
}

func (c *Controller) retryCleanup() {
	for _, delay := range []time.Duration{100 * time.Millisecond, 300 * time.Millisecond, time.Second} {
		time.Sleep(delay)
		c.op.Lock()
		if c.isStopping() || c.cleanup() == nil {
			c.cleanupScheduled = false
			c.op.Unlock()
			return
		}
		c.op.Unlock()
	}
	c.op.Lock()
	c.cleanupScheduled = false
	c.op.Unlock()
}

func (c *Controller) BeginStop() {
	c.mu.Lock()
	c.stopping = true
	c.record = nil
	c.pendingActivation = nil
	c.mu.Unlock()
}

func (c *Controller) isStopping() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopping
}

func (c *Controller) Close() {
	c.op.Lock()
	defer c.op.Unlock()
	c.mu.Lock()
	c.stopping = true
	id := c.active
	c.active = 0
	c.record = nil
	c.status.Effective = nil
	c.status.Registration = "disabled"
	c.mu.Unlock()
	if id != 0 {
		c.retire(id)
	}
	if c.registrar != nil {
		_ = c.cleanup()
	}
	if c.surface != nil {
		c.closeSurface()
	}
	if c.chat != nil {
		c.chat.close()
	}
	c.draft.Clear()
}
