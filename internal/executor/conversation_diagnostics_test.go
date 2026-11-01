package executor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChatStartupDiagnosticsDropSecretsAndBoundOutput(t *testing.T) {
	root := t.TempDir()
	d := newChatDiagnostics(root)
	secret := "fixture-secret-token-and-private-message"
	for i := 0; i < 100; i++ {
		d.Write([]byte("Bearer " + secret + "\n[gateway] startup phase: cli.bootstrap.admission.database-readiness starting total=123ms private=" + secret + "\n"))
		d.Write([]byte("[gateway] startup phase: cli.bootstrap.admission.database-readiness 20ms total=143ms\n"))
	}
	d.Write([]byte(strings.Repeat("x", 9000) + "[gateway] startup phase: ready 1ms total=144ms\n"))
	d.Write([]byte("[gateway] startup phase: " + secret + " 1ms total=144ms\n"))
	d.Write([]byte("[state/agent-db] agent database stale lease: private=" + secret + "\n"))
	d.finish("startup_timeout")
	d.Write([]byte("[gateway] startup phase: ready 1ms total=150ms\n"))
	data, err := os.ReadFile(d.path)
	if err != nil {
		t.Fatal(err)
	}
	var report chatStartupReport
	if err = json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) || strings.Contains(string(data), "runtime_ready") || len(data) > 16*1024 || len(report.Events) > 48 || report.Outcome != "startup_timeout" {
		t.Fatal("unbounded or unsafe diagnostics", string(data))
	}
	newChatDiagnostics(root)
	if prior, err := os.ReadFile(filepath.Join(root, "startup-diagnostic.previous.json")); err != nil || string(prior) != string(data) {
		t.Fatal("previous bounded attempt not retained", err)
	}
}

func TestChatStartupDiagnosticWriteFailureIsNonBlocking(t *testing.T) {
	d := newChatDiagnostics(filepath.Join(t.TempDir(), "missing"))
	if n, err := d.Write([]byte("[gateway] startup phase: ready 1ms total=2ms\n")); err != nil || n == 0 {
		t.Fatal(n, err)
	}
	d.finish("bootstrap_failed")
}

func TestChatStartupDiagnosticsRejectUnsafePreviousReport(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "startup-diagnostic.json")
	for _, data := range []string{strings.Repeat("x", 17000), `{"version":1,"outcome":"ready","events":[{"stage":"secret-content"}]}`} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		newChatDiagnostics(root)
		if _, err := os.Stat(filepath.Join(root, "startup-diagnostic.previous.json")); !os.IsNotExist(err) {
			t.Fatal("unsafe prior report retained", err)
		}
	}
}
