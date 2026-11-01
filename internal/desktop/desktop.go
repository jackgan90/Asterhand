// Package desktop contains the narrow OS boundary for the node's desktop UI.
// It owns no task state and never uses a remote control endpoint to stop a node.
package desktop

import (
	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/quickinput"
)

type State struct {
	Name     string
	Stopping bool
}

type Options struct {
	State        func() State
	OpenWeb      func() error
	LogDirectory string
	Stop         func(confirmed bool) bool
	CleanupDone  <-chan struct{}
	Input        *quickinput.Controller
}

func (o Options) locale() string {
	if o.Input != nil {
		if loc := o.Input.Status().EffectiveLocale; i18n.ValidLocale(loc) {
			return loc
		}
	}
	return i18n.Effective()
}

func (s State) textFor(locale string) string {
	if s.Stopping {
		return i18n.T(locale, "desktop.state.stopping")
	}
	return i18n.T(locale, "desktop.state.running")
}

func (s State) text() string {
	return s.textFor(i18n.Effective())
}
