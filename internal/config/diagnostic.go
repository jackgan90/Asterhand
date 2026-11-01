package config

type DiagnosticState string

const (
	DiagnosticNotRun DiagnosticState = "not_run"
	DiagnosticPassed DiagnosticState = "passed"
	DiagnosticFailed DiagnosticState = "failed"
	DiagnosticStale  DiagnosticState = "stale"
)

// DiagnosticStatus also reads older configurations that only stored success.
func (c Config) DiagnosticStatus() DiagnosticState {
	if c.ModelVerified {
		return DiagnosticPassed
	}
	if c.ModelDiagnostic == DiagnosticFailed || c.ModelDiagnostic == DiagnosticStale {
		return c.ModelDiagnostic
	}
	if c.VerifiedBundle != "" || c.VerifiedExecution != "" {
		return DiagnosticStale
	}
	return DiagnosticNotRun
}

func (c *Config) InvalidateModelDiagnostic() {
	state := c.DiagnosticStatus()
	c.ModelVerified = false
	c.ModelDiagnostic = DiagnosticNotRun
	if state != DiagnosticNotRun {
		c.ModelDiagnostic = DiagnosticStale
	}
}
