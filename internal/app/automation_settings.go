package app

import (
	"context"
	"errors"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/executor"
	"github.com/jackgan90/Asterhand/internal/llm"
	"github.com/jackgan90/Asterhand/internal/operations"
	"github.com/jackgan90/Asterhand/internal/quickinput"
)

func nodeSettingsRevision(c config.Config) string {
	if c.Revision != "" {
		return c.Revision
	}
	return operations.Hash([]any{c.Name, c.NameDefault, c.LLM, c.AgentRuntime})
}
func nodeSettingsView(c config.Config) map[string]any {
	return map[string]any{"revision": nodeSettingsRevision(c), "name": c.Name, "name_default": c.NameDefault, "provider": c.LLM.Provider, "model": c.LLM.Model}
}

func desktopView(st quickinput.Status) quickinput.Status { st.Desired.Publication = nil; return st }
func desktopError(err error) error {
	if err == nil {
		return nil
	}
	e := automation.PublicError(err)
	var q *quickinput.Error
	if errors.As(err, &q) {
		switch q.Status {
		case 400:
			e.Code = "invalid_parameters"
		case 409:
			e.Code = "conflict"
		case 503:
			e.Code = "desktop_unavailable"
		}
	}
	var registration *quickinput.RegistrationError
	if errors.As(err, &registration) {
		e.Code = registration.Code
	}
	return e
}

func (a *App) registerAutomationSettings(r *automation.Registry) {
	nodeAccess := func(p automation.Principal, v automation.Empty) error { return p.Require("node", r.Node) }
	automation.Register(r, automation.Definition{Name: "settings.get", Description: "Read ordinary node and desktop settings with separate revisions; no credentials or trust settings", Role: "read"}, nil, nodeAccess,
		func(ctx context.Context, call automation.Call, v automation.Empty) (automation.Outcome, error) {
			return automation.Outcome{Data: map[string]any{"node": nodeSettingsView(a.Config.Get()), "desktop": desktopView(a.Input.Status())}}, nil
		})
	automation.Register(r, automation.Definition{Name: "settings.patch", Description: "Atomically update one ordinary settings object against its expected revision", Role: "edit", Write: true, Batch: true}, func(v automation.SettingsPatch) string { return r.Node }, func(p automation.Principal, v automation.SettingsPatch) error { return p.Require("node", r.Node) }, a.automationSettingsPatch)
	automation.Register(r, automation.Definition{Name: "settings.check_hotkey", Description: "Check a normalized hotkey using the existing registration controller", Role: "edit", Write: true}, func(v automation.HotkeyCheck) string { return r.Node }, func(p automation.Principal, v automation.HotkeyCheck) error { return p.Require("node", r.Node) },
		func(ctx context.Context, call automation.Call, v automation.HotkeyCheck) (automation.Outcome, error) {
			err := a.Input.Check(v.Binding)
			return automation.Outcome{Data: desktopView(a.Input.Status()), Summary: map[string]bool{"available": err == nil}, Accepted: err == nil}, desktopError(err)
		})
	automation.Register(r, automation.Definition{Name: "desktop.retry", Description: "Explicitly retry the existing host and hotkey recovery, preserving saved settings and disabled state", Role: "edit", Write: true, Control: true}, func(v automation.Empty) string { return r.Node }, nodeAccess,
		func(ctx context.Context, call automation.Call, v automation.Empty) (automation.Outcome, error) {
			if err := call.Check(); err != nil {
				return automation.Outcome{}, err
			}
			st, err := a.Input.Retry()
			return automation.Outcome{Data: desktopView(st), Summary: map[string]string{"registration": st.Registration, "surface": st.Surface}, Accepted: err == nil}, desktopError(err)
		})
	automation.Register(r, automation.Definition{Name: "models.list", Description: "List models supported by the current provider and indicate whether its existing credential is configured", Role: "read"}, nil, nodeAccess,
		func(ctx context.Context, call automation.Call, v automation.Empty) (automation.Outcome, error) {
			c := a.Config.Get()
			provider, err := llm.Resolve(c.LLM.Provider)
			if err != nil {
				return automation.Outcome{}, err
			}
			_, credentialErr := a.Config.Key()
			return automation.Outcome{Data: map[string]any{"provider": provider.Describe(), "configured": credentialErr == nil, "selected": c.LLM.Model, "revision": nodeSettingsRevision(c)}}, nil
		})
	automation.Register(r, automation.Definition{Name: "models.select", Description: "Select a supported model using the already configured provider credential", Role: "edit", Write: true, Batch: true}, func(v automation.ModelSelect) string { return r.Node }, func(p automation.Principal, v automation.ModelSelect) error { return p.Require("node", r.Node) },
		func(ctx context.Context, call automation.Call, v automation.ModelSelect) (automation.Outcome, error) {
			return a.automationSettingsPatch(ctx, call, automation.SettingsPatch{Settings: "node", Revision: v.Revision, Model: &v.Model})
		})
}

func (a *App) automationSettingsPatch(ctx context.Context, call automation.Call, v automation.SettingsPatch) (automation.Outcome, error) {
	if v.Settings == "desktop" {
		if v.Name != nil || v.Model != nil {
			return automation.Outcome{}, automation.Fail("invalid_parameters", "Node and desktop fields must be separate patches")
		}
		return a.automationDesktopPatch(ctx, call, v)
	}
	if v.Settings != "node" || v.Enabled != nil || v.Binding != nil || v.Theme != nil || v.Locale != nil || v.Reset {
		return automation.Outcome{}, automation.Fail("invalid_parameters", "Select node or desktop and supply only that object's fields")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	before := a.Config.Get()
	c := before
	if nodeSettingsRevision(c) != v.Revision {
		return automation.Outcome{}, automation.Fail("conflict", "Node settings revision changed")
	}
	if v.Name != nil {
		if err := validateNodeName(*v.Name); err != nil {
			return automation.Outcome{}, err
		}
		c.Name, c.NameDefault = *v.Name, false
	}
	if v.Model != nil && *v.Model != before.LLM.Model {
		if _, err := a.Config.Key(); err != nil {
			return automation.Outcome{}, automation.Fail("not_configured", "Configure the provider credential in the human settings first")
		}
		c.LLM.Model = *v.Model
	}
	if err := executor.ValidateSelection(c); err != nil {
		return automation.Outcome{}, err
	}
	release, err := a.settingsAdmissionLocked(before, c, false)
	if err != nil {
		return automation.Outcome{}, err
	}
	defer release()
	if err = call.Check(); err != nil {
		return automation.Outcome{}, err
	}
	changed := executor.ExecutionIdentity(before) != executor.ExecutionIdentity(c)
	if changed {
		c.InvalidateModelDiagnostic()
	}
	err = a.Config.SaveOperation(c, call.Identity)
	current := a.Config.Get()
	published := current.Publication != nil && current.Publication.Identity == call.Identity
	if published {
		if changed {
			a.probeStatus, a.probeError, a.probeErrorKey, a.probeErrorArgs = string(current.DiagnosticStatus()), "", "", nil
		}
		a.requestPeerSync()
		a.notifyRefresh()
	}
	return automation.Outcome{Data: nodeSettingsView(current), Summary: map[string]string{"revision": current.Revision}, Accepted: published, Unknown: published && err != nil}, err
}

func (a *App) automationDesktopPatch(ctx context.Context, call automation.Call, v automation.SettingsPatch) (automation.Outcome, error) {
	before := a.Input.Status().Desired
	if v.Revision != before.Revision {
		return automation.Outcome{}, automation.Fail("conflict", "Desktop settings revision changed")
	}
	if err := call.Check(); err != nil {
		return automation.Outcome{}, err
	}
	var st quickinput.Status
	var err error
	if v.Locale != nil && v.Enabled == nil && v.Binding == nil && v.Theme == nil && !v.Reset {
		st, err = a.Input.LocaleOperation(ctx, quickinput.LocaleChange{Operation: call.Identity.NativeID(), Revision: v.Revision, Locale: *v.Locale}, call.Identity)
	} else {
		change := quickinput.Change{Operation: call.Identity.NativeID(), Revision: v.Revision, Enabled: before.Enabled, Binding: before.Binding, Theme: before.Theme, Locale: v.Locale, Reset: v.Reset}
		if v.Enabled != nil {
			change.Enabled = *v.Enabled
		}
		if v.Binding != nil {
			change.Binding = *v.Binding
		}
		if v.Theme != nil {
			change.Theme = *v.Theme
		}
		st, err = a.Input.ApplyOperation(ctx, change, call.Identity)
	}
	published := st.Desired.Publication != nil && st.Desired.Publication.Identity == call.Identity
	if err == nil || published {
		a.notifyRefresh()
		if a.CommandLogs != nil {
			a.CommandLogs.Update(st.Desired.Theme, st.EffectiveLocale)
		}
	}
	return automation.Outcome{Data: desktopView(st), Summary: map[string]string{"revision": st.Desired.Revision}, Accepted: published || err == nil, Unknown: published && err != nil}, desktopError(err)
}
