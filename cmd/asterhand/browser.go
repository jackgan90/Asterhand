package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
)

func openBrowser(address string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", address).Start()
	case "darwin":
		return exec.Command("open", address).Start()
	default:
		return exec.Command("xdg-open", address).Start()
	}
}
func existingURL(c *config.Manager) (string, error) {
	b, err := os.ReadFile(filepath.Join(c.Home, "runtime.json"))
	if err != nil {
		return "", errors.New("节点尚未启动")
	}
	var record struct {
		Address string `json:"address"`
	}
	if err = json.Unmarshal(b, &record); err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 2 * time.Second}
	res, err := client.Get("http://" + record.Address + "/health")
	if err != nil {
		return "", errors.New("节点尚未启动")
	}
	defer res.Body.Close()
	var health struct {
		NodeID string `json:"node_id"`
	}
	if json.NewDecoder(res.Body).Decode(&health) != nil || health.NodeID != c.Get().NodeID {
		return "", errors.New("监听端口属于其他服务")
	}
	b, err = os.ReadFile(filepath.Join(c.Home, "web-login.token"))
	if err != nil {
		return "", err
	}
	return "http://" + record.Address + "/#login=" + string(b), nil
}
