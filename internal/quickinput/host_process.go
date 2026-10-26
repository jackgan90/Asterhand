package quickinput

import (
	"io"
	"time"
)

const hostStartTimeout = 15 * time.Second
const hostStopTimeout = 12 * time.Second

// HostLauncher and ActivationBroker are intentionally narrow platform seams.
// Handle types, privilege mechanisms, and browser engines stay in platform code.
type hostLauncher func() (hostProcess, error)

type hostProcess interface {
	Input() io.WriteCloser
	Output() io.ReadCloser
	Exited() <-chan struct{}
	Stop() error
	GrantForeground() error
}

type activationIntent interface{ valid() bool }

func applyActivation(intent activationIntent, surface Surface) {
	if intent != nil && intent.valid() && surface != nil {
		surface.Toggle()
	}
}
