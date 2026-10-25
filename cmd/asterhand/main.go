package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/environment"
	"github.com/jackgan90/Asterhand/internal/executor"
	"github.com/jackgan90/Asterhand/internal/launch"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Asterhand:", err)
		os.Exit(1)
	}
}
func run() error {
	command := "start"
	args := os.Args[1:]
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		command = args[0]
		args = args[1:]
	}
	if command == "version" {
		fmt.Println(config.Version)
		return nil
	}
	if command == "bundle-check" {
		f := flag.NewFlagSet(command, flag.ContinueOnError)
		base := f.String("directory", ".", "只读检查安装包目录")
		if err := f.Parse(args); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return environment.CheckBundle(ctx, *base)
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	home := flags.String("home", config.DefaultHome(), "节点数据目录")
	web := flags.String("web", "", "本机 Web 监听地址")
	peers := flags.String("peer", "", "节点 TLS 监听地址")
	agent := flags.String("agent-runtime", "", "选择已安装的 agent runtime")
	optionsPath := flags.String("runtime-options", "", "所选 runtime 的选项 JSON 文件（不含凭据）")
	delay := flags.Int("acceptance-delay", 0, "隔离验收：文件创建前延迟秒数 (0—60)")
	open := flags.Bool("open", false, "启动后打开本机 Web")
	basic := flags.Bool("basic", false, "仅配置基础节点，不安装助手运行时")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if command == "open" || command == "status" {
		address, err := launch.ExistingURL(*home)
		if err != nil {
			return err
		}
		if command == "open" {
			return openBrowser(address)
		}
		fmt.Println("running")
		return nil
	}
	if command == "doctor" {
		c, err := config.Open(*home)
		if err != nil {
			return err
		}
		return doctor(c)
	}
	if command != "start" && command != "setup" && command != "repair" {
		return fmt.Errorf("可用命令: setup, start, open, status, doctor, repair, version")
	}
	c, unlock, err := config.OpenLocked(*home)
	if err != nil {
		return err
	}
	defer unlock()
	cfg := c.Get()
	if *agent != "" {
		cfg.AgentRuntime = *agent
	}
	if exe, err := os.Executable(); err == nil {
		cfg = environment.Discover(cfg, filepath.Dir(exe))
	}
	if *basic {
		cfg.Profile = "basic"
		cfg.ModelVerified = false
	}
	if *web != "" {
		cfg.WebListen = *web
	}
	if *peers != "" {
		cfg.PeerListen = *peers
	}
	if *optionsPath != "" {
		raw, err := os.ReadFile(*optionsPath)
		if err != nil {
			return err
		}
		cfg.SetRuntimeOptions(cfg.AgentRuntime, raw)
		cfg.MarkRuntimeOptionsExplicit(cfg.AgentRuntime, true)
	}
	if err = executor.ValidateSelection(cfg); err != nil {
		return err
	}
	if *agent != "" || *optionsPath != "" {
		cfg.ModelVerified = false
	}
	for _, project := range cfg.Projects {
		if err = executor.ValidateWorkspace(cfg, c.Home, project.Root); err != nil {
			return err
		}
	}
	if *delay < 0 || *delay > 60 {
		return fmt.Errorf("验收延迟必须为 0—60 秒")
	}
	cfg.AcceptanceDelay = *delay
	if err = c.Save(cfg); err != nil {
		return err
	}
	if command == "repair" {
		return doctor(c)
	}
	return runNode(c, *open)
}
func doctor(c *config.Manager) error {
	checks, checkErr := environment.Check(c)
	b, err := json.MarshalIndent(checks, "", "  ")
	if err == nil {
		fmt.Println(string(b))
	}
	if err != nil {
		return err
	}
	return checkErr
}
