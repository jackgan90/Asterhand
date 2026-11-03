//go:build !windows

package localcommands

import "time"

type unsupportedBackend struct{}

func NewPlatformBackend() ExecutionBackend { return unsupportedBackend{} }
func (unsupportedBackend) Available() bool { return false }
func (unsupportedBackend) Start(ScriptSpec, string) (Execution, error) {
	return nil, problem("unsupported", "err.localcmd.managed_unavailable")
}
func (unsupportedBackend) Reconcile(string) (bool, error) {
	return false, problem("unsupported", "err.localcmd.managed_unavailable")
}

var clockOrigin = time.Now()

func (platformClock) Now() time.Duration { return time.Since(clockOrigin) }
