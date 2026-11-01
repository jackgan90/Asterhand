package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/jackgan90/Asterhand/internal/app"
	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/desktop"
	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/launch"
)

func runNode(c *config.Manager, open bool) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	a, err := app.New(c, nil)
	if err != nil {
		return err
	}
	if _, err = a.Start(ctx); err != nil {
		a.Close()
		return err
	}
	cleanupDone := make(chan struct{})
	tray, err := desktop.Start(desktop.Options{
		State: func() desktop.State {
			cfg := c.Get()
			name := cfg.Name
			if cfg.NameDefault {
				loc := i18n.Effective()
				if a.Input != nil {
					if eff := a.Input.Status().EffectiveLocale; i18n.ValidLocale(eff) {
						loc = eff
					}
				}
				name = i18n.T(loc, "common.default_node_name")
			}
			return desktop.State{Name: name, Stopping: a.Stopping()}
		},
		OpenWeb: func() error {
			address, err := launch.ExistingURL(c.Home)
			if err != nil {
				return err
			}
			return openBrowser(address)
		},
		LogDirectory: c.Home,
		Stop:         a.RequestStop,
		CleanupDone:  cleanupDone,
		Input:        a.Input,
	})
	if err != nil {
		a.Close()
		return err
	}
	defer tray.Close()
	defer close(cleanupDone)
	// Stale metadata remains on abnormal exit and is verified against the live
	// process and node identity by the launcher before it is ever reused.
	runtimePath := filepath.Join(c.Home, "runtime.json")
	record, _ := json.Marshal(map[string]any{"address": a.Address(), "pid": os.Getpid()})
	if err = config.AtomicFile(runtimePath, record, 0600); err != nil {
		a.Close()
		return err
	}
	defer os.Remove(runtimePath)
	fmt.Println(i18n.T(i18n.Effective(), "desktop.cli.startup_banner", map[string]string{"version": config.Version, "address": a.Address(), "home": c.Home}))
	if open {
		address, openErr := launch.ExistingURL(c.Home)
		if openErr == nil {
			openErr = openBrowser(address)
		}
		if openErr != nil {
			fmt.Fprintln(os.Stderr, i18n.T(i18n.Effective(), "desktop.cli.browser_open_failed", map[string]string{"err": openErr.Error()}))
		}
	}
	select {
	case <-ctx.Done():
	case <-a.Done():
	}
	return a.Close()
}
