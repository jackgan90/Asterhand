package config

import "testing"

func TestDiagnosticStateSurvivesRestartWithoutPersistingErrorText(t *testing.T) {
	manager, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := manager.Get()
	if c.DiagnosticStatus() != DiagnosticNotRun {
		t.Fatal("new config claims diagnostic evidence")
	}
	c.ModelVerified, c.VerifiedBundle, c.VerifiedExecution = true, "bundle", "selection"
	c.InvalidateModelDiagnostic()
	if c.ModelVerified || c.DiagnosticStatus() != DiagnosticStale {
		t.Fatal("old diagnostic did not expire")
	}
	c.ModelDiagnostic = DiagnosticFailed
	if err := manager.Save(c); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(manager.Home)
	if err != nil || reopened.Get().DiagnosticStatus() != DiagnosticFailed {
		t.Fatal("old success fingerprints hid failed test after restart", err)
	}
	c = reopened.Get()
	c.InvalidateModelDiagnostic()
	if c.DiagnosticStatus() != DiagnosticStale {
		t.Fatal("changed configuration reused old failure")
	}
}
