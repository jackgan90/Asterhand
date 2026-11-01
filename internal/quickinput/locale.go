package quickinput

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/model"
)

// LocaleChange updates only the persisted locale preference without touching
// hotkey registration, WebView2 surface initialization, or active recording leases.
type LocaleChange struct {
	Operation string `json:"operation_id"`
	Revision  string `json:"expected_revision"`
	Locale    string `json:"locale"`
}

func (c *Controller) SetLocale(ctx context.Context, change LocaleChange) (Status, error) {
	c.op.Lock()
	defer c.op.Unlock()
	if len(change.Operation) < 8 || len(change.Operation) > 100 {
		return c.Status(), statusError(400, "err.quickinput.invalid_op_id")
	}
	encoded, _ := json.Marshal(change)
	input := "locale:" + string(encoded)
	if previous, ok := c.operations[change.Operation]; ok && time.Now().Before(previous.expires) {
		if previous.input != input {
			return c.Status(), statusError(409, "err.quickinput.op_id_reused")
		}
		return previous.result, previous.err
	}
	result, err := c.setLocale(ctx, change)
	c.recordOperation(change.Operation, input, result, err)
	return result, err
}

func (c *Controller) setLocale(ctx context.Context, change LocaleChange) (Status, error) {
	before := c.Status()
	if c.isStopping() {
		return before, statusError(503, "err.app.node_stopping")
	}
	if !i18n.ValidPreference(change.Locale) {
		return before, statusError(400, "err.quickinput.prefs_invalid_locale")
	}
	if c.invalid {
		return before, statusError(409, "err.quickinput.config_invalid_reset")
	}
	if change.Revision != before.Desired.Revision {
		return before, statusError(409, "err.quickinput.revision_conflict")
	}
	if err := ctx.Err(); err != nil {
		return before, err
	}
	next := before.Desired
	next.Locale = change.Locale
	next.Revision = model.ID()
	if err := c.store.Save(next); err != nil {
		return c.Status(), i18n.Wrap("err.quickinput.not_committed", map[string]string{"err": err.Error()}, err)
	}
	// No context cancellation or rollback after persistent commit.
	i18n.SetDefaultPreference(next.Locale)
	effLocale, _ := i18n.ResolveLocale(next.Locale)
	c.mu.Lock()
	c.status.Desired = next
	c.status.LastLocaleOperation = change.Operation
	surface := c.surface
	if c.status.Surface != "ready" {
		surface = nil
	}
	c.mu.Unlock()
	if surface != nil {
		if err := surface.UpdateLocale(effLocale); err != nil {
			c.Failed(err)
		}
	}
	return c.Status(), nil
}
