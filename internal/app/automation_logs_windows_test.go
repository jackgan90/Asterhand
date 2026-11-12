//go:build windows

package app

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/localcommands"
	"github.com/jackgan90/Asterhand/internal/operations"
)

func TestPublicLogPagesFilterDecodeAndEscapeWithoutLoss(t *testing.T) {
	n := newNode(t, &fakeExecutor{})
	a := n.app
	_, token := grantTestAutomation(t, a, []string{"read", "edit", "execute"}, map[string][]string{"commands": {"*"}})
	script := filepath.Join(t.TempDir(), "bytes.ps1")
	want := strings.Repeat("\x00中文🙂\n", 6000)
	bytes := append([]byte(want), 0xe4)
	body := "$e=[Console]::OpenStandardError();$e.Write((New-Object byte[] (1100*1024)),0,1100*1024);$e.Flush();$b=[Convert]::FromBase64String('" + base64.StdEncoding.EncodeToString(bytes) + "');$s=[Console]::OpenStandardOutput();for($i=0;$i -lt $b.Length;$i+=127){$s.Write($b,$i,[Math]::Min(127,$b.Length-$i))};$s.Flush()"
	if err := os.WriteFile(script, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	created := testAutomationCall(a, token, "commands.create", operations.NewID(time.Now()), automation.CommandInput{Name: "log_bytes", Graph: apiTestGraph(script)})
	if created.Error != nil {
		t.Fatal(created)
	}
	var ref localcommands.CommandReference
	_ = json.Unmarshal(created.Receipt.Result, &ref)
	started := testAutomationCall(a, token, "commands.run", operations.NewID(time.Now()), automation.CommandVersion{ID: ref.ID, Revision: ref.Revision})
	if started.Error != nil {
		t.Fatal(started)
	}
	var run automation.RunView
	_ = json.Unmarshal(compactResult(started.Data), &run)
	await(t, "byte script finished", func() bool { state := a.Commands.Run(ref.ID, run.ID); return state != nil && state.Phase == "finished" })
	v := automation.LogRead{CommandID: ref.ID, RunID: run.ID, Stream: "stdout", Encoding: "utf-8", Format: "text"}
	var got strings.Builder
	for i := 0; i < 2000; i++ {
		reply := testAutomationCall(a, token, "logs.read", "", v)
		if reply.Error != nil {
			t.Fatal(reply)
		}
		if len(compactResult(reply)) > automation.MaxResponse {
			t.Fatal("unbounded escaped response")
		}
		var page publicLogPage
		_ = json.Unmarshal(compactResult(reply.Data), &page)
		for _, chunk := range page.Chunks {
			if chunk.Stream != "stdout" {
				t.Fatal("filter leaked stderr")
			}
			got.WriteString(chunk.Text)
		}
		if page.Finished {
			break
		}
		if page.Next == v.Cursor {
			t.Fatal("cursor did not advance")
		}
		v.Cursor = page.Next
		if i == 1999 {
			t.Fatal("too many pages")
		}
	}
	if got.String() != want+"�" {
		t.Fatalf("decoded bytes mismatch: got %d want %d", got.Len(), len(want)+3)
	}
	v.Stream = "stderr"
	if reply := testAutomationCall(a, token, "logs.read", "", v); reply.Error == nil || reply.Error.Code != "invalid_cursor" {
		t.Fatal("cursor crossed filter", reply)
	}
}
