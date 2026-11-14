//go:build windows

package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/localcommands"
)

func TestCommandNotificationsRealWindowsApp(t *testing.T) {
	if os.Getenv("ASTERHAND_NOTIFICATION_APP_TEST") != "1" {
		t.Skip("requires a test executable beside the built input host and an interactive desktop")
	}
	c, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, scenario := range []string{"success", "failure", "timeout", "disabled", "stop"} {
		t.Run(scenario, func(t *testing.T) {
			script := filepath.Join(c.Home, scenario+".cmd")
			body := "@echo off\r\necho notification-check\r\nexit /b 0\r\n"
			if scenario == "failure" {
				body = "@echo off\r\nexit /b 3\r\n"
			}
			if scenario == "timeout" || scenario == "stop" {
				body = "@echo off\r\nping -n 20 127.0.0.1 >nul\r\n"
			}
			if err := os.WriteFile(script, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			command := localcommands.Command{Name: scenario, Graph: apiTestGraph(script)}
			if scenario == "disabled" {
				command.NotifyOnCompletion = localcommands.NotificationOff
			}
			if scenario == "timeout" {
				seconds := int64(1)
				command.TimeoutSeconds = &seconds
			}
			catalog, _ := a.Commands.Snapshot()
			saved, err := a.Commands.Save(localcommands.Mutation{Revision: catalog.Revision, Command: command})
			if err != nil {
				t.Fatal(err)
			}
			command, err = a.Commands.Get(saved.ID)
			if err != nil {
				t.Fatal(err)
			}
			launched := a.Commands.Launch(context.Background(), command.ID, command.Revision)
			if !launched.Started {
				t.Fatal(launched.Error)
			}
			if scenario == "stop" {
				await(t, "accepted run", func() bool { return a.Commands.CurrentRun(command.ID) != nil })
				run := a.Commands.CurrentRun(command.ID)
				if _, err = a.Commands.StopRun(command.ID, run.ID); err != nil {
					t.Fatal(err)
				}
			}
			await(t, "script finished", func() bool { r := a.Commands.CurrentRun(command.ID); return r != nil && r.Phase == "finished" })
			record := a.Commands.CurrentRun(command.ID)
			expected := "succeeded"
			if scenario == "failure" || scenario == "timeout" {
				expected = "failed"
			}
			if scenario == "stop" {
				expected = "interrupted"
			}
			if record.Result != expected {
				t.Fatal(record)
			}
			if scenario == "disabled" || scenario == "stop" {
				time.Sleep(200 * time.Millisecond)
				if st := a.Notifications.Status(); st.Visible != 0 || st.Pending != 0 {
					t.Fatal("filtered completion appeared", st)
				}
			} else {
				await(t, "notification admitted by real host", func() bool { st := a.Notifications.Status(); return st.State == "available" && st.Visible > 0 })
				await(t, "notification hidden after dwell and exit", func() bool { st := a.Notifications.Status(); return st.Visible == 0 && st.Pending == 0 })
			}
		})
	}
}
