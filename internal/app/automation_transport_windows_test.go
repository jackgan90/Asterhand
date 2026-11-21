//go:build windows

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/operations"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestAutomationMCPChild(t *testing.T) {
	if os.Getenv("ASTERHAND_AUTOMATION_MCP_CHILD") != "1" {
		return
	}
	err := automation.RunMCP(context.Background(), []string{"--home", os.Getenv("ASTERHAND_AUTOMATION_TEST_HOME")}, os.Stderr, AutomationDefinitions())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestCLIAndRealMCPStdioSharePipeBusinessOperation(t *testing.T) {
	n := newNode(t, &fakeExecutor{})
	a := n.app

	args := automation.CommandInput{Name: "from_cli", Graph: apiTestGraph(filepath.Join(t.TempDir(), "missing.ps1"))}
	id := operations.NewID(time.Now())
	var out, diagnostics bytes.Buffer
	cli := []string{"commands_create", "--home", a.Config.Home, "--input", "-", "--operation-id", id}
	if binary := os.Getenv("ASTERHAND_AUTOMATION_BINARY"); binary != "" {
		cmd := exec.Command(binary, append([]string{"automation"}, cli...)...)
		cmd.Stdin = bytes.NewReader(compactResult(args))
		cmd.Stdout = &out
		cmd.Stderr = &diagnostics
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if err := cmd.Run(); err != nil {
			t.Fatal(err, out.String(), diagnostics.String())
		}
	} else if err := automation.RunCLI(context.Background(), cli, bytes.NewReader(compactResult(args)), &out, &diagnostics, AutomationDefinitions()); err != nil {
		t.Fatal(err, out.String())
	}
	var created automation.Response
	if err := json.Unmarshal(out.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Error != nil || created.Receipt == nil || !created.Receipt.Accepted {
		t.Fatal(out.String())
	}
	var ref struct {
		ID string `json:"command_id"`
	}
	_ = json.Unmarshal(created.Receipt.Result, &ref)
	if ref.ID == "" {
		t.Fatal(created)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestAutomationMCPChild$")
	cmd.Env = append(os.Environ(), "ASTERHAND_AUTOMATION_MCP_CHILD=1", "ASTERHAND_AUTOMATION_TEST_HOME="+a.Config.Home)
	if binary := os.Getenv("ASTERHAND_AUTOMATION_BINARY"); binary != "" {
		cmd = exec.Command(binary, "mcp", "--home", a.Config.Home)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "integration", Version: "1"}, nil).Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err, stderr.String())
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	foundRun := false
	for _, tool := range listed.Tools {
		if tool.Name == "commands_run" {
			foundRun = true
		}
		if tool.Name == "automation.grant" {
			t.Fatal("unexpected management tool")
		}
	}
	if !foundRun {
		t.Fatal("local MCP did not expose the ordinary command runner")
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "commands_get", Arguments: map[string]string{"command_id": ref.ID}})
	if err != nil || result.IsError {
		t.Fatal(result, err)
	}
	var same struct {
		Data struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	if err = json.Unmarshal(compactResult(result.StructuredContent), &same); err != nil || same.Data.Name != "from_cli" {
		t.Fatal(result, err)
	}
	// Replaying through the other adapter must return the original receipt.
	var input map[string]any
	_ = json.Unmarshal(compactResult(args), &input)
	input["operation_id"] = id
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "commands_create", Arguments: input})
	if err != nil || result.IsError {
		t.Fatal(result, err)
	}
	catalog, _ := a.Commands.Snapshot()
	if len(catalog.Commands) != 1 {
		t.Fatal("cross-adapter replay duplicated create")
	}

}
