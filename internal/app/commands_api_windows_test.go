//go:build windows

package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/localcommands"
	commandswire "github.com/jackgan90/Asterhand/internal/localcommands/wire"
)

func TestModelLessNodeLaunchesOrdinaryScriptAndStopsOnClose(t *testing.T) {
	c, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := c.Get()
	cfg.WebListen, cfg.PeerListen = "127.0.0.1:0", "127.0.0.1:0"
	if err := c.Save(cfg); err != nil {
		t.Fatal(err)
	}
	a, err := New(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer a.Close()
	if _, err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script, output := filepath.Join(dir, "ordinary.ps1"), filepath.Join(dir, "done.txt")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	body := `
$principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
$admin = $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
[IO.File]::WriteAllText(` + quote(output) + `, [string]$admin + ':' + $env:ASTERHAND_COMMAND_NODE_PRIVATE)
Start-Sleep -Seconds 30
[IO.File]::WriteAllText(` + quote(output+".late") + `, 'survived')`
	if err := os.WriteFile(script, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ASTERHAND_COMMAND_NODE_PRIVATE", "must-not-inherit")
	catalog, _ := a.Commands.Snapshot()
	saved, err := a.Commands.Save(localcommands.Mutation{Revision: catalog.Revision, Command: localcommands.Command{
		Name: "ordinary", Graph: apiTestGraph(script),
	}})
	if err != nil {
		t.Fatal(err)
	}
	command := saved.Catalog.Commands[0]
	response := a.commandCall(ctx, commandswire.Request{Action: "launch", ID: command.ID, Revision: command.Revision, Sequence: 1})
	if !response.Started {
		t.Fatal(response.Error)
	}
	for _, name := range []string{"runs", "conversations"} {
		if _, err := os.Stat(filepath.Join(c.Home, name)); !os.IsNotExist(err) {
			t.Fatal("local command initialized model runtime", name, err)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(output); err == nil && len(data) != 0 {
			if string(data) != "False:" {
				t.Fatalf("script elevated or inherited private environment: %q", data)
			}
			if err := a.Close(); err != nil {
				t.Fatal(err)
			}
			record := a.Commands.CurrentRun(command.ID)
			if record == nil || record.Result != "interrupted" || record.Reason != "node_shutdown" {
				t.Fatal("node close did not stop run", record)
			}
			if _, err := os.Stat(output + ".late"); !os.IsNotExist(err) {
				t.Fatal("script outlived node", err)
			}
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("managed script did not produce ordinary-user evidence")
}
