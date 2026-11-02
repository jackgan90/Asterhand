//go:build windows

package localcommands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWindowsGraphDependencyAndFailureCleanup(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "success"
		if fail {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			c := graphCommand(t)
			workspace := filepath.Dir(c.Script)
			bodies := []string{"", "@echo off\r\necho prepare\r\nexit /b 0\r\n",
				"@echo off\r\nping.exe -n 2 127.0.0.1 >nul\r\necho left>left.marker\r\nexit /b 0\r\n",
				"@echo off\r\necho right\r\necho right>right.marker\r\nexit /b 0\r\n",
				"@echo off\r\nif not exist left.marker exit /b 12\r\nif not exist right.marker exit /b 13\r\necho joined>joined.marker\r\necho joined\r\nexit /b 0\r\n", ""}
			if fail {
				bodies[2] = "@echo off\r\nping.exe -n 2 127.0.0.1 >nul\r\necho failed-left 1>&2\r\nexit /b 7\r\n"
				bodies[3] = "@echo off\r\nstart \"\" /b ping.exe -n 60 127.0.0.1\r\nexit /b 0\r\n"
			}
			for i, body := range bodies {
				if body == "" {
					continue
				}
				path := filepath.Join(workspace, c.Graph.Nodes[i].Name+".cmd")
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
				c.Graph.Nodes[i].Script = path
				c.Graph.Nodes[i].Directory = workspace
				c.Graph.Nodes[i].Arguments = nil
			}
			backend := NewPlatformBackend()
			manager := NewRunManager(t.TempDir(), backend, nil)
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := manager.Close(ctx); err != nil {
					t.Error(err)
				}
			})
			run, err := manager.Start(c)
			if err != nil {
				t.Fatal(err)
			}
			result := awaitFinished(t, manager, c.ID)
			if fail {
				if result.Result != "failed" || result.FailedNodeID != c.Graph.Nodes[2].ID {
					t.Fatal(result)
				}
				if _, err = os.Stat(filepath.Join(workspace, "joined.marker")); !os.IsNotExist(err) {
					t.Fatal("join ran after failure", err)
				}
			} else if result.Result != "succeeded" {
				t.Fatal(result)
			}
			for _, node := range result.Nodes {
				if node.RunID == "" {
					continue
				}
				if empty, err := backend.Reconcile(node.RunID); err != nil || !empty {
					t.Fatal("node process scope survived", node.Name, err)
				}
			}
			page, err := manager.Read(c.ID, run.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			var text strings.Builder
			for _, chunk := range page.Chunks {
				if chunk.NodeID == "" {
					t.Fatal("missing node source")
				}
				text.Write(chunk.Data)
			}
			if fail && !strings.Contains(text.String(), "failed-left") || !fail && !strings.Contains(text.String(), "joined") {
				t.Fatal(text.String())
			}
		})
	}
}
