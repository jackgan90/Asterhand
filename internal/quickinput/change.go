package quickinput

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/model"
)

func (c *Controller) Apply(ctx context.Context, change Change) (Status, error) {
	c.op.Lock()
	defer c.op.Unlock()
	if len(change.Operation) < 8 || len(change.Operation) > 100 {
		return c.Status(), statusError(400, "err.quickinput.invalid_op_id")
	}
	encoded, _ := json.Marshal(change)
	input := "apply:" + string(encoded)
	if previous, ok := c.operations[change.Operation]; ok && time.Now().Before(previous.expires) {
		if previous.input != input {
			return c.Status(), statusError(409, "err.quickinput.op_id_reused")
		}
		return previous.result, previous.err
	}
	result, err := c.apply(ctx, change)
	c.recordOperation(change.Operation, input, result, err)
	return result, err
}

func (c *Controller) recordOperation(id, input string, result Status, err error) {
	for key, entry := range c.operations {
		if time.Now().After(entry.expires) {
			delete(c.operations, key)
		}
	}
	if len(c.operations) >= 64 {
		var oldest string
		var expires time.Time
		for key, entry := range c.operations {
			if oldest == "" || entry.expires.Before(expires) {
				oldest, expires = key, entry.expires
			}
		}
		delete(c.operations, oldest)
	}
	c.operations[id] = operation{input, result, err, time.Now().Add(5 * time.Minute)}
}

func (c *Controller) apply(ctx context.Context, change Change) (Status, error) {
	before := c.Status()
	if c.isStopping() {
		return before, statusError(503, "err.app.node_stopping")
	}
	if !before.Supported || c.registrar == nil {
		return before, statusError(503, "err.quickinput.no_interactive_desktop")
	}
	if change.Revision != before.Desired.Revision {
		return before, statusError(409, "err.quickinput.revision_conflict")
	}
	if c.invalid && !change.Reset {
		return before, statusError(409, "err.quickinput.config_invalid_reset")
	}
	locale := before.Desired.Locale
	if locale == "" {
		locale = "auto"
	}
	if change.Locale != nil {
		locale = *change.Locale
	}
	next := Preferences{Schema: 1, Revision: model.ID(), Enabled: change.Enabled, Binding: change.Binding, Theme: change.Theme, Locale: locale}
	if change.Reset {
		next = Defaults()
	}
	next, err := next.validated()
	if err != nil {
		return before, wrapStatusError(400, err)
	}
	needsRegistration := !next.Binding.Equal(before.Desired.Binding) || (next.Enabled && c.active == 0)
	if err := c.cleanup(); err != nil && needsRegistration {
		return c.Status(), err
	}
	if !c.invalid && next.Enabled == before.Desired.Enabled && next.Binding.Equal(before.Desired.Binding) && next.Theme == before.Desired.Theme && next.Locale == before.Desired.Locale && (!next.Enabled || c.active != 0) {
		return c.Status(), nil
	}
	if err = ctx.Err(); err != nil {
		return before, err
	}
	if !next.Enabled && c.surface != nil {
		if err = c.surface.Snapshot(ctx); err != nil {
			return before, i18n.Wrap("err.quickinput.finish_input_retry", map[string]string{"err": err.Error()}, err)
		}
	}
	if next.Enabled {
		wasAbsent := c.surface == nil
		if wasAbsent && !before.Desired.Enabled {
			defer func() {
				if c.Status().Desired.Revision == before.Desired.Revision {
					c.closeSurface()
				}
			}()
		}
		if _, err = c.ReadySurface(); err != nil {
			c.setError("unavailable", err)
			return c.Status(), err
		}
	}
	candidate := 0
	if needsRegistration {
		candidate, err = c.registrar.Register(next.Binding)
		if err != nil {
			if c.active == 0 {
				c.registrationFailed(err)
			}
			return c.Status(), err
		}
	}
	return c.persist(ctx, change, next, candidate)
}

func (c *Controller) persist(ctx context.Context, change Change, next Preferences, candidate int) (Status, error) {
	var err error
	if err = ctx.Err(); err == nil && c.isStopping() {
		err = statusError(503, "err.app.node_stopping")
	}
	if err == nil && c.invalid {
		err = c.store.PreserveInvalid()
	}
	if err == nil {
		err = c.store.Save(next)
	}
	if err != nil {
		if candidate != 0 {
			c.retire(candidate)
		}
		return c.Status(), i18n.Wrap("err.quickinput.not_committed", map[string]string{"err": err.Error()}, err)
	}
	// No context cancellation after this persistent commit point.
	c.invalid = false
	c.publish(change, next, candidate)
	return c.Status(), nil
}

func (c *Controller) publish(change Change, next Preferences, candidate int) {
	i18n.SetDefaultPreference(next.Locale)
	effLocale, _ := i18n.ResolveLocale(next.Locale)
	c.mu.Lock()
	old := c.active
	c.status.Desired, c.status.LastOperation = next, change.Operation
	c.clearErrorLocked()
	c.record = nil
	if next.Enabled {
		if candidate != 0 {
			c.active = candidate
		}
		b := next.Binding
		c.status.Effective = &b
		c.status.Registration = "registered"
	} else {
		c.active = 0
		c.status.Effective = nil
		c.status.Registration = "disabled"
	}
	c.mu.Unlock()
	if old != 0 && old != c.active {
		c.retire(old)
	}
	if !next.Enabled && candidate != 0 {
		c.retire(candidate)
	}
	if next.Enabled && c.surface != nil {
		if err := c.surface.Update(next.Theme); err != nil {
			c.Failed(err)
		} else if err := c.surface.UpdateLocale(effLocale); err != nil {
			c.Failed(err)
		}
	} else if c.surface != nil {
		c.closeSurface()
	}
}

func (c *Controller) Check(binding Binding) error {
	c.op.Lock()
	defer c.op.Unlock()
	if c.isStopping() || c.registrar == nil {
		return statusError(503, "err.quickinput.desktop_unavailable_stopping")
	}
	if err := c.cleanup(); err != nil {
		return err
	}
	normal, err := binding.Normalize()
	if err != nil {
		return wrapStatusError(400, err)
	}
	if c.active != 0 && normal.Equal(c.Status().Desired.Binding) {
		return nil
	}
	id, err := c.registrar.Register(normal)
	if err != nil {
		return err
	}
	c.retire(id)
	if len(c.retired) != 0 {
		return statusError(409, "err.quickinput.temp_reg_cleanup")
	}
	return nil
}
