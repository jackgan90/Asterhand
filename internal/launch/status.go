package launch

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackgan90/Asterhand/internal/i18n"
)

type runtimeRecord struct {
	Address string `json:"address"`
	PID     int    `json:"pid"`
}

// ExistingURL is read-only: querying a stopped node must not initialize a new
// identity, rewrite permissions or leave an extra service lock behind.
func ExistingURL(home string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return existingURL(ctx, home)
}

func existingURL(ctx context.Context, home string) (string, error) {
	var record runtimeRecord
	if err := readJSON(filepath.Join(home, "runtime.json"), &record); err != nil {
		return "", i18n.Err("err.launch.not_running")
	}
	address, err := localAddress(record.Address)
	if err != nil {
		return "", err
	}
	if record.PID <= 0 {
		return "", i18n.Err("err.launch.missing_valid_pid")
	}
	if _, err = inspectProcess(record.PID); err != nil {
		return "", i18n.Wrap("err.launch.process_unavailable", nil, err)
	}
	var cfg struct {
		NodeID string `json:"node_id"`
	}
	if err = readJSON(filepath.Join(home, "config.json"), &cfg); err != nil || cfg.NodeID == "" {
		return "", i18n.Err("err.launch.invalid_identity")
	}
	if err = checkHealth(ctx, address, record.PID, cfg.NodeID); err != nil {
		return "", err
	}
	token, err := os.ReadFile(filepath.Join(home, "web-login.token"))
	if err != nil {
		return "", i18n.Err("err.launch.login_secret_unavailable")
	}
	if len(token) < 16 || len(token) > 256 || strings.ContainsAny(string(token), "\x00\r\n") {
		return "", i18n.Err("err.launch.login_secret_invalid")
	}
	return address + "/#login=" + url.QueryEscape(string(token)), nil
}

func localAddress(address string) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", i18n.Err("err.launch.invalid_address")
	}
	ip := net.ParseIP(host)
	value, err := strconv.Atoi(port)
	if ip == nil || !ip.IsLoopback() || err != nil || value < 1 || value > 65535 {
		return "", i18n.Err("err.launch.not_loopback_port")
	}
	return "http://" + net.JoinHostPort(ip.String(), strconv.Itoa(value)), nil
}

func checkHealth(ctx context.Context, address string, pid int, nodeID string) error {
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return i18n.Err("err.launch.redirect_forbidden")
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address+"/health", nil)
	if err != nil {
		return err
	}
	response, err := client.Do(req)
	if err != nil {
		return i18n.Wrap("err.launch.not_ready", nil, err)
	}
	defer response.Body.Close()
	var health struct {
		NodeID string `json:"node_id"`
		PID    int    `json:"pid"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&health) != nil || health.NodeID != nodeID {
		return i18n.Err("err.launch.port_identity_mismatch")
	}
	// Releases predating the desktop entry point report only node_id. They still
	// require the live process and local identity checks above.
	if health.PID != 0 && health.PID != pid {
		return i18n.Err("err.launch.port_pid_mismatch")
	}
	return nil
}
