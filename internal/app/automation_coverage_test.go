package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/localcommands"
)

func TestAutomationCoverage(t *testing.T) {
	var manifest struct {
		Version int `json:"api_version"`
		Groups  []struct {
			ID         string   `json:"id"`
			Operations []string `json:"operations"`
			Tests      []string `json:"tests"`
		} `json:"groups"`
	}
	root := filepath.Join("..", "..")
	b, err := os.ReadFile(filepath.Join(root, "contracts", "automation-coverage.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version != 1 {
		t.Fatal("unsupported manifest")
	}
	names := map[string]string{}
	for _, group := range manifest.Groups {
		for _, name := range group.Operations {
			if names[name] != "" {
				t.Fatal("duplicate coverage", name)
			}
			names[name] = group.ID
		}
		for _, file := range group.Tests {
			if _, err := os.Stat(filepath.Join(root, file)); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, d := range AutomationDefinitions() {
		if names[d.Name] == "" {
			t.Errorf("operation %s lacks coverage and validation evidence", d.Name)
		}
		delete(names, d.Name)
		if d.Schema["type"] != "object" || d.OutputSchema["type"] != "object" {
			t.Errorf("%s lacks shared schemas", d.Name)
		}
		data, ok := d.OutputSchema["properties"].(map[string]any)["data"].(map[string]any)
		if !ok || len(data) == 0 {
			t.Errorf("%s lacks an explicit result schema", d.Name)
		}
	}
	for name := range names {
		t.Errorf("coverage advertises missing operation %s", name)
	}
	// Verify "." -> "_" MCP tool-name projection for managed harnesses matches
	// ^[a-zA-Z0-9_-]+$ (no dots or illegal chars) and has zero collisions.
	seenMCP := map[string]string{}
	for _, d := range AutomationDefinitions() {
		mapped := strings.ReplaceAll(d.Name, ".", "_")
		for i := 0; i < len(mapped); i++ {
			c := mapped[i]
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
				t.Errorf("MCP tool name %q (from %s) contains invalid character %q; only [a-zA-Z0-9_-] is allowed", mapped, d.Name, c)
				break
			}
		}
		if prev, dup := seenMCP[mapped]; dup {
			t.Errorf("MCP underscore tool name collision: %s and %s both map to %s", prev, d.Name, mapped)
		}
		seenMCP[mapped] = d.Name
	}
}

func TestCommandAndGraphSchemasAreSelfDescribing(t *testing.T) {
	defs := AutomationDefinitions()
	if server := automation.NewMCPServer(nil, automation.Principal{Roles: []string{"read", "edit", "execute"}}, defs); server == nil {
		t.Fatal("failed to construct MCP server with enriched schemas")
	}
	byName := map[string]automation.Definition{}
	for _, d := range defs {
		byName[d.Name] = d
	}
	for _, op := range []string{"commands.create", "commands.patch", "commands.validate", "commands.run", "logs.read"} {
		if _, ok := byName[op]; !ok {
			t.Fatalf("missing definition %s", op)
		}
	}
	createText := string(compactResult(byName["commands.create"].Schema))
	for _, want := range []string{
		"^[A-Za-z][A-Za-z0-9_-]{0,63}$",
		"^[a-zA-Z0-9_-]{16,64}$",
		"0..16 aliases",
		"max 8 active instances per command, 32 node-wide",
		"0..8000",
		"1..16",
		".bat, .cmd, or .ps1",
		"non-breakaway Job object",
		"only 'abort'",
	} {
		if !strings.Contains(createText, want) {
			t.Fatalf("commands.create schema missing %q", want)
		}
	}
	validateOut := string(compactResult(byName["commands.validate"].OutputSchema))
	if !strings.Contains(validateOut, "empty string on unsaved commands.validate output") {
		t.Fatalf("commands.validate output schema missing empty id/revision note")
	}
	n := newNode(t, &fakeExecutor{})
	a := n.app
	_, token := grantTestAutomation(t, a, []string{"read", "edit"}, map[string][]string{"commands": {"*"}})
	valid := automation.CommandValidate{Candidate: automation.CommandInput{Name: "valid_cmd", Graph: apiTestGraph(filepath.Join(t.TempDir(), "missing.ps1"))}}
	reply := testAutomationCall(a, token, "commands.validate", "", valid)
	if reply.Error != nil {
		t.Fatalf("expected valid candidate, got %v", reply.Error)
	}
	shortID := localcommands.Command{Name: "short_id", Graph: apiTestGraph(filepath.Join(t.TempDir(), "missing.ps1"))}
	shortID.Graph.Nodes[1].ID = "short"
	badIDReply := testAutomationCall(a, token, "commands.validate", "", automation.CommandValidate{Candidate: automation.CommandInput{Name: shortID.Name, Graph: shortID.Graph}})
	if badIDReply.Error == nil || badIDReply.Error.MessageKey != "err.graph.identity" || !strings.Contains(badIDReply.Error.Message, "16") {
		t.Fatalf("unexpected identity validation error: %+v", badIDReply.Error)
	}
	badStart := apiTestGraph(filepath.Join(t.TempDir(), "missing.ps1"))
	badStart.Nodes[0].Script = filepath.Join(t.TempDir(), "x.ps1")
	badStartReply := testAutomationCall(a, token, "commands.validate", "", automation.CommandValidate{Candidate: automation.CommandInput{Name: "bad_start", Graph: badStart}})
	if badStartReply.Error == nil || badStartReply.Error.MessageKey != "err.graph.node" || !strings.Contains(badStartReply.Error.Message, "start/script/end") {
		t.Fatalf("unexpected node validation error: %+v", badStartReply.Error)
	}
}
