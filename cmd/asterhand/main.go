package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/environment"
	"github.com/jackgan90/Asterhand/internal/executor"
	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/launch"
	"github.com/jackgan90/Asterhand/internal/quickinput"
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
	loc := i18n.Effective()
	if command == "pick-local-script" {
		f := flag.NewFlagSet(command, flag.ContinueOnError)
		f.SetOutput(io.Discard)
		locale := f.String("locale", "", i18n.T(loc, "desktop.cli.flag.locale"))
		if err := f.Parse(args); err != nil {
			return err
		}
		if f.NArg() != 0 {
			return i18n.Err("err.cli.picker_no_args")
		}
		// The private parent pipe is kept open for the lifetime of the request.
		go func() { _, _ = io.Copy(io.Discard, os.Stdin); os.Exit(0) }()
		path, err := quickinput.RunScriptPicker(*locale)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(struct {
			Path string `json:"path"`
		}{path})
	}
	if command == "bundle-check" {
		f := flag.NewFlagSet(command, flag.ContinueOnError)
		base := f.String("directory", ".", i18n.T(loc, "desktop.cli.flag.directory"))
		if err := f.Parse(args); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return environment.CheckBundle(ctx, *base)
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	home := flags.String("home", config.DefaultHome(), i18n.T(loc, "desktop.cli.flag.home"))
	web := flags.String("web", "", i18n.T(loc, "desktop.cli.flag.web"))
	peers := flags.String("peer", "", i18n.T(loc, "desktop.cli.flag.peer"))
	agent := flags.String("agent-runtime", "", i18n.T(loc, "desktop.cli.flag.agent_runtime"))
	optionsPath := flags.String("runtime-options", "", i18n.T(loc, "desktop.cli.flag.runtime_options"))
	delay := flags.Int("acceptance-delay", 0, i18n.T(loc, "desktop.cli.flag.acceptance_delay"))
	open := flags.Bool("open", false, i18n.T(loc, "desktop.cli.flag.open"))
	basic := flags.Bool("basic", false, i18n.T(loc, "desktop.cli.flag.basic"))
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
		return i18n.Err("err.cli.available_commands")
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
		cfg.InvalidateModelDiagnostic()
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
		cfg.InvalidateModelDiagnostic()
	}
	for _, project := range cfg.Projects {
		if err = executor.ValidateWorkspace(cfg, c.Home, project.Root); err != nil {
			return err
		}
	}
	if *delay < 0 || *delay > 60 {
		return i18n.Err("err.cli.acceptance_delay_range")
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
