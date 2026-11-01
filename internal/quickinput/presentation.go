package quickinput

import "github.com/jackgan90/Asterhand/internal/i18n"

// MenuLabel distinguishes a configured shortcut from an actually usable one.
func (s Status) MenuLabel() string {
	locale := s.EffectiveLocale
	if !i18n.ValidLocale(locale) {
		locale = i18n.Effective()
	}
	if !s.Supported {
		return i18n.T(locale, "desktop.quickinput.menu.unsupported")
	}
	if !s.Desired.Enabled {
		return i18n.T(locale, "desktop.quickinput.menu.disabled")
	}
	if s.Registration == "recovering" {
		return i18n.T(locale, "desktop.quickinput.menu.recovering")
	}
	if s.Surface == "initializing" || s.Registration == "initializing" {
		return i18n.T(locale, "desktop.quickinput.menu.initializing")
	}
	if s.Registration == "registered" && s.Surface == "ready" && s.Effective != nil {
		return i18n.T(locale, "desktop.quickinput.menu.active", map[string]string{"binding": s.Effective.String()})
	}
	if s.Registration == "conflict" {
		return i18n.T(locale, "desktop.quickinput.menu.conflict")
	}
	return i18n.T(locale, "desktop.quickinput.menu.unavailable")
}
