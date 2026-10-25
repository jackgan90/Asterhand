package network

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
)

func ValidateListener(address string) error {
	host, port, err := net.SplitHostPort(address)
	n, parseErr := strconv.Atoi(port)
	if err != nil || net.ParseIP(host) == nil || parseErr != nil || n < 1 || n > 65535 {
		return errors.New("节点监听地址格式为 IP:端口，端口范围 1—65535")
	}
	return nil
}

func ListenerMatches(configured, actual string) bool {
	ch, cp, ce := net.SplitHostPort(configured)
	ah, ap, ae := net.SplitHostPort(actual)
	c, a := net.ParseIP(ch), net.ParseIP(ah)
	return ce == nil && ae == nil && c != nil && a != nil && (cp == ap || cp == "0") &&
		(c.Equal(a) || (c.IsUnspecified() && a.IsUnspecified()))
}

func LoopbackEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return strings.EqualFold(u.Hostname(), "localhost") || (ip != nil && ip.IsLoopback())
}

func LoopbackListener(address string) bool {
	host, _, err := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	return err == nil && ip != nil && ip.IsLoopback()
}

func ConnectionWarning(endpoint, configured, actual string) string {
	if !ListenerMatches(configured, actual) {
		return "节点监听配置尚未生效：请停止本机节点，再运行 start.ps1。"
	}
	if LoopbackListener(actual) || LoopbackEndpoint(endpoint) {
		return "当前节点连接地址仅本机可达。双机使用请在节点设置中选择 Tailscale 地址并按提示重启。"
	}
	if endpoint == "" {
		return "尚未设置对外节点地址，请在节点设置中选择 Tailscale 地址。"
	}
	return ""
}
