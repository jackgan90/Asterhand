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
)

type Status struct {
	Installed bool   `json:"installed"`
	State     string `json:"state"`
	Version   string `json:"version,omitempty"`
	IPv4      string `json:"ipv4,omitempty"`
	Endpoint  string `json:"endpoint,omitempty"`
	Listen    string `json:"listen,omitempty"`
	Detail    string `json:"detail"`
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
		return Status{State: "not_installed", Detail: "未安装 Tailscale。运行 install.ps1 完成安装与登录引导。"}
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "status", "--json")
	hideWindow(cmd)
	b, err := cmd.Output()
	if len(b) > 2*1024*1024 {
		return Status{Installed: true, State: "error", Detail: "Tailscale 状态输出过大"}
	}
	var raw struct {
		Version      string
		BackendState string
		TailscaleIPs []string
		Self         *struct{ TailscaleIPs []string }
	}
	if json.Unmarshal(b, &raw) != nil {
		return Status{Installed: true, State: "error", Detail: "Tailscale 服务不可用，请打开 Tailscale 并检查登录状态。"}
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
		s.Detail = "请在 Tailscale 中登录，并将 A、B 加入同一个 Tailnet。"
		return s
	}
	if s.IPv4 == "" {
		s.Detail = "Tailscale 已启动，但尚未取得节点 IPv4 地址。"
		return s
	}
	s.Listen = net.JoinHostPort(s.IPv4, strings.TrimPrefix(port, ":"))
	s.Endpoint = "https://" + s.Listen
	s.Detail = "Tailscale 已连接。配对时仍会校验目标节点的独立 TLS 身份。"
	return s
}
