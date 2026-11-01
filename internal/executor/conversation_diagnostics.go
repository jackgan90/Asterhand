package executor

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
)

// Raw runtime output can contain credentials and conversation text. Only these
// fixed stage names and numeric timing/exit fields may reach the diagnostic file.
var chatStartupPhases = map[string]string{
	"entry.run-main-import":                      "entry_import",
	"cli.bootstrap.admission.database-readiness": "database_inspection",
	"cli.bootstrap.admission.session-readiness":  "session_inspection",
	"cli.bootstrap.admission.plugin-config":      "plugin_configuration",
	"cli.command.config-ready":                   "configuration",
	"cli.bootstrap.gateway-lock":                 "gateway_lock",
	"gateway.server-start-import":                "server_import",
	"state.schema-preflight":                     "schema_preflight",
	"config.auth":                                "authentication",
	"sessions.admission":                         "session_admission",
	"http.bound":                                 "http_bound",
	"ready":                                      "runtime_ready",
}

var chatPhasePattern = regexp.MustCompile(`\[gateway\] startup phase: ([a-z.-]+) (starting|[0-9.]+ms) total=[0-9.]+ms`)
var chatExitPattern = regexp.MustCompile(`^\[asterhand\] gateway exited code=(-?[0-9]+|null) signal=(none|SIGTERM|SIGKILL|SIGINT|SIGABRT|SIGSEGV)$`)

type chatStartupEvent struct {
	Stage     string `json:"stage"`
	ElapsedMS int64  `json:"elapsed_ms"`
}

type chatStartupReport struct {
	Version         int                `json:"version"`
	Started         time.Time          `json:"started_at"`
	Outcome         string             `json:"outcome"`
	ExitCode        *int               `json:"exit_code,omitempty"`
	GatewayExitCode *int64             `json:"gateway_exit_code,omitempty"`
	GatewaySignal   string             `json:"gateway_signal,omitempty"`
	Events          []chatStartupEvent `json:"events"`
}

type chatDiagnostics struct {
	mu       sync.Mutex
	path     string
	started  time.Time
	report   chatStartupReport
	line     []byte
	dropping bool
	finished bool
}

func newChatDiagnostics(root string) *chatDiagnostics {
	now := time.Now()
	d := &chatDiagnostics{path: filepath.Join(root, "startup-diagnostic.json"), started: now,
		report: chatStartupReport{Version: 1, Started: now.UTC(), Outcome: "starting"}}
	// Keep at most the current attempt and one bounded previous report.
	if file, err := os.Open(d.path); err == nil {
		prior, err := io.ReadAll(io.LimitReader(file, 16*1024+1))
		file.Close()
		var valid chatStartupReport
		if err == nil && len(prior) <= 16*1024 && json.Unmarshal(prior, &valid) == nil && validChatStartupReport(valid) {
			filtered, _ := json.MarshalIndent(valid, "", "  ")
			_ = config.AtomicFile(filepath.Join(root, "startup-diagnostic.previous.json"), filtered, 0600)
		}
	}
	d.record("launch")
	return d
}

func validChatStartupStage(stage string) bool {
	switch stage {
	case "launch", "starting", "ready", "bundle_invalid", "preparation_failed", "launch_failed", "bootstrap_failed", "startup_timeout", "startup_cancelled", "gateway_timeout", "gateway_exited", "database_stale_lease", "database_integrity_check", "database_recovered", "gateway_authenticated", "port_in_use", "module_missing":
		return true
	}
	for _, name := range chatStartupPhases {
		if stage == name+"_started" || stage == name+"_completed" {
			return true
		}
	}
	return false
}

func validChatStartupReport(report chatStartupReport) bool {
	if report.Version != 1 || !validChatStartupStage(report.Outcome) || len(report.Events) > 48 {
		return false
	}
	for _, event := range report.Events {
		if !validChatStartupStage(event.Stage) {
			return false
		}
	}
	switch report.GatewaySignal {
	case "", "none", "SIGTERM", "SIGKILL", "SIGINT", "SIGABRT", "SIGSEGV":
		return true
	default:
		return false
	}
}

func (d *chatDiagnostics) saveLocked() {
	data, err := json.MarshalIndent(d.report, "", "  ")
	if err == nil {
		_ = config.AtomicFile(d.path, data, 0600)
	}
}

func (d *chatDiagnostics) addLocked(stage string) {
	if !validChatStartupStage(stage) {
		return
	}
	events := d.report.Events
	if len(events) != 0 && events[len(events)-1].Stage == stage {
		return
	}
	if len(events) == 48 {
		events = events[1:]
	}
	d.report.Events = append(events, chatStartupEvent{Stage: stage, ElapsedMS: time.Since(d.started).Milliseconds()})
	d.saveLocked()
}

func (d *chatDiagnostics) record(stage string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.addLocked(stage)
}

func (d *chatDiagnostics) finish(outcome string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.finished = true
	d.line = nil
	d.report.Outcome = outcome
	d.addLocked(outcome)
}

func (d *chatDiagnostics) exited(code int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.report.ExitCode = &code
	d.saveLocked()
}

func (d *chatDiagnostics) failedIfStarting() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.finished {
		d.finished = true
		d.report.Outcome = "preparation_failed"
		d.addLocked("preparation_failed")
	}
}

// Write drains all bytes, retaining at most one 8 KiB partial line. Oversized
// lines are discarded through the next newline, never scanned as fragments.
func (d *chatDiagnostics) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := len(p)
	if d.finished {
		return n, nil
	}
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		part := p
		if i >= 0 {
			part = p[:i]
		}
		if !d.dropping && len(d.line)+len(part) <= 8192 {
			d.line = append(d.line, part...)
		} else {
			d.dropping = true
			d.line = nil
		}
		if i < 0 {
			break
		}
		if !d.dropping {
			d.consumeLocked(string(d.line))
		}
		d.line, d.dropping = nil, false
		p = p[i+1:]
	}
	return n, nil
}

func (d *chatDiagnostics) consumeLocked(line string) {
	if match := chatExitPattern.FindStringSubmatch(strings.TrimSpace(line)); len(match) != 0 {
		if code, err := strconv.ParseInt(match[1], 10, 64); err == nil && code >= -2147483648 && code <= 4294967295 {
			d.report.GatewayExitCode = &code
		}
		d.report.GatewaySignal = match[2]
		d.addLocked("gateway_exited")
	}
	if match := chatPhasePattern.FindStringSubmatch(line); len(match) != 0 {
		if stage := chatStartupPhases[match[1]]; stage != "" {
			if match[2] == "starting" {
				stage += "_started"
			} else {
				stage += "_completed"
			}
			d.addLocked(stage)
		}
	}
	for _, marker := range []struct{ text, stage string }{
		{"agent database stale lease:", "database_stale_lease"},
		{"agent database integrity gate", "database_integrity_check"},
		{"agent database recovered after background inspection and preparation", "database_recovered"},
		{"[asterhand] gateway authenticated", "gateway_authenticated"},
		{"[asterhand] gateway startup timeout", "gateway_timeout"},
		{"EADDRINUSE", "port_in_use"},
		{"MODULE_NOT_FOUND", "module_missing"},
		{"ERR_MODULE_NOT_FOUND", "module_missing"},
	} {
		if strings.Contains(line, marker.text) {
			d.addLocked(marker.stage)
		}
	}
}
