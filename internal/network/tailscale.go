package network

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/jackgan90/Asterhand/internal/i18n"
)

type Status struct {
	Installed   bool              `json:"installed"`
	State       string            `json:"state"`
	Version     string            `json:"version,omitempty"`
	IPv4        string            `json:"ipv4,omitempty"`
	Endpoint    string            `json:"endpoint,omitempty"`
	Listen      string            `json:"listen,omitempty"`
	Detail      string            `json:"detail"`
	MessageKey  string            `json:"message_key,omitempty"`
	MessageArgs map[string]string `json:"message_args,omitempty"`
}

func (s Status) Localize(locale string) Status {
	s.MessageArgs = i18n.CloneArgs(s.MessageArgs)
	s.Detail = i18n.LocalizeMessage(locale, s.MessageKey, s.MessageArgs, s.Detail)
	return s
}

func statusWithKey(s Status, key string) Status {
	s.MessageKey = key
	s.Detail = i18n.T(i18n.Effective(), key)
	return s
}

func Executable() string {
	if runtime.GOOS == "windows" {
		p := filepath.Join(os.Getenv("ProgramFiles"), "Tailscale", "tailscale.exe")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	p, _ := exec.LookPath("tailscale")
	return p
}
func Inspect(ctx context.Context, port string) Status {
	path := Executable()
	if path == "" {
		return statusWithKey(Status{State: "not_installed"}, "status.tailscale.not_installed")
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "status", "--json")
	hideWindow(cmd)
	b, err := cmd.Output()
	if len(b) > 2*1024*1024 {
		return statusWithKey(Status{Installed: true, State: "error"}, "status.tailscale.output_oversized")
	}
	var raw struct {
		Version      string
		BackendState string
		TailscaleIPs []string
		Self         *struct{ TailscaleIPs []string }
	}
	if json.Unmarshal(b, &raw) != nil {
		return statusWithKey(Status{Installed: true, State: "error"}, "status.tailscale.service_unavailable")
	}
	s := Status{Installed: true, Version: raw.Version, State: raw.BackendState}
	ips := raw.TailscaleIPs
	if raw.Self != nil {
		ips = append(ips, raw.Self.TailscaleIPs...)
	}
	for _, value := range ips {
		ip := net.ParseIP(value)
		if ip != nil && ip.To4() != nil {
			s.IPv4 = ip.String()
			break
		}
	}
	if err != nil || raw.BackendState != "Running" {
		return statusWithKey(s, "status.tailscale.login_required")
	}
	if s.IPv4 == "" {
		return statusWithKey(s, "status.tailscale.no_address")
	}
	s.Listen = net.JoinHostPort(s.IPv4, strings.TrimPrefix(port, ":"))
	s.Endpoint = "https://" + s.Listen
	return statusWithKey(s, "status.tailscale.connected")
}
