package network

import (
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackgan90/Asterhand/internal/i18n"
)

func ValidateListener(address string) error {
	host, port, err := net.SplitHostPort(address)
	n, parseErr := strconv.Atoi(port)
	if err != nil || net.ParseIP(host) == nil || parseErr != nil || n < 1 || n > 65535 {
		return i18n.Err("err.network.listen_format")
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

func ConnectionWarningKey(endpoint, configured, actual string) string {
	if !ListenerMatches(configured, actual) {
		return "status.network.restart_required"
	}
	if LoopbackListener(actual) || LoopbackEndpoint(endpoint) {
		return "status.network.loopback_only"
	}
	if endpoint == "" {
		return "status.network.endpoint_missing"
	}
	return ""
}

func ConnectionWarningFor(locale, endpoint, configured, actual string) string {
	key := ConnectionWarningKey(endpoint, configured, actual)
	if key == "" {
		return ""
	}
	return i18n.T(locale, key)
}

func ConnectionWarning(endpoint, configured, actual string) string {
	return ConnectionWarningFor(i18n.Effective(), endpoint, configured, actual)
}
