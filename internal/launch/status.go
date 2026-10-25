package launch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
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
		return "", errors.New("节点尚未启动或运行记录无效")
	}
	address, err := localAddress(record.Address)
	if err != nil {
		return "", err
	}
	if record.PID <= 0 {
		return "", errors.New("运行记录缺少有效的节点进程 ID")
	}
	if _, err = inspectProcess(record.PID); err != nil {
		return "", fmt.Errorf("运行记录中的节点进程不可用：%w", err)
	}
	var cfg struct {
		NodeID string `json:"node_id"`
	}
	if err = readJSON(filepath.Join(home, "config.json"), &cfg); err != nil || cfg.NodeID == "" {
		return "", errors.New("节点配置或节点身份无效")
	}
	if err = checkHealth(ctx, address, record.PID, cfg.NodeID); err != nil {
		return "", err
	}
	token, err := os.ReadFile(filepath.Join(home, "web-login.token"))
	if err != nil {
		return "", errors.New("节点登录凭据不可用，请检查数据目录")
	}
	if len(token) < 16 || len(token) > 256 || strings.ContainsAny(string(token), "\x00\r\n") {
		return "", errors.New("节点登录凭据格式无效")
	}
	return address + "/#login=" + url.QueryEscape(string(token)), nil
}

func localAddress(address string) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", errors.New("运行记录中的本机地址格式无效")
	}
	ip := net.ParseIP(host)
	value, err := strconv.Atoi(port)
	if ip == nil || !ip.IsLoopback() || err != nil || value < 1 || value > 65535 {
		return "", errors.New("运行记录中的地址必须是有效的本机 loopback 端口")
	}
	return "http://" + net.JoinHostPort(ip.String(), strconv.Itoa(value)), nil
}

func checkHealth(ctx context.Context, address string, pid int, nodeID string) error {
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("本机健康检查不允许重定向")
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address+"/health", nil)
	if err != nil {
		return err
	}
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("节点尚未就绪，请检查 error.log：%w", err)
	}
	defer response.Body.Close()
	var health struct {
		NodeID string `json:"node_id"`
		PID    int    `json:"pid"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&health) != nil || health.NodeID != nodeID {
		return errors.New("监听端口的节点身份校验失败")
	}
	// Releases predating the desktop entry point report only node_id. They still
	// require the live process and local identity checks above.
	if health.PID != 0 && health.PID != pid {
		return errors.New("监听端口与运行记录中的节点进程不一致")
	}
	return nil
}
