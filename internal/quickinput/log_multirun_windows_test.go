//go:build windows

package quickinput

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/localcommands"
	commandswire "github.com/jackgan90/Asterhand/internal/localcommands/wire"
	"github.com/jackgan90/Asterhand/internal/model"
)

type nativeLogFixture struct {
	mu      sync.Mutex
	records []localcommands.RunRecord
	stopped chan string
}

func newNativeLogFixture(command string) *nativeLogFixture {
	f := &nativeLogFixture{stopped: make(chan string, 4)}
	node := model.ID()
	for i := 1; i <= 2; i++ {
		f.records = append(f.records, localcommands.RunRecord{Version: 1, ID: model.ID(), RoundID: "native-round", Sequence: uint64(i),
			Command: localcommands.Command{ID: command, Name: "parallel_demo"}, Phase: "running", Started: time.Now(), LogComplete: true,
			Nodes: []localcommands.NodeRecord{{NodeID: node, Kind: "script", Name: "build", Phase: "running", LogComplete: true}},
		})
	}
	return f
}

func (f *nativeLogFixture) reply(request commandswire.Request) commandswire.Response {
	f.mu.Lock()
	defer f.mu.Unlock()
	if request.Action == "runs" {
		data, _ := json.Marshal(localcommands.RunList{RoundID: "native-round", LatestID: f.records[len(f.records)-1].ID, Runs: f.records})
		return commandswire.Response{Runs: data}
	}
	for i := range f.records {
		record := &f.records[i]
		if record.ID != request.RunID {
			continue
		}
		if request.Action == "stop" {
			record.Phase, record.Result, record.Reason = "finished", "interrupted", "user_stop"
			record.Nodes[0].Phase, record.Nodes[0].Result = "finished", "interrupted"
			f.stopped <- record.ID
		}
		if request.Action == "status" || request.Action == "stop" {
			data, _ := json.Marshal(record)
			return commandswire.Response{Run: data}
		}
		chunks := []localcommands.LogChunk{}
		if request.Action == "tail_log" || request.Cursor == 0 {
			chunks = append(chunks, localcommands.LogChunk{NodeID: record.Nodes[0].NodeID, Stream: "stdout", Data: []byte(fmt.Sprintf("native-log %d\n", record.Sequence))})
		}
		data, _ := json.Marshal(localcommands.LogPage{Run: record, Start: 0, Next: 100, End: true, Chunks: chunks})
		return commandswire.Response{Log: data}
	}
	return commandswire.Response{Error: commandswire.FailKey("conflict", "err.localcmd.run_changed")}
}

func checkNativeMultiRunPanes(t *testing.T, s *nativeSurface, f *nativeLogFixture) {
	t.Helper()
	// Select an older instance without replacing the already-open latest pane.
	evaluate(t, s, `(()=>{const p=document.querySelector('#log-run-picker');p.value=p.options[0].value;p.dispatchEvent(new Event('change'));return true})()`)
	waitChatScript(t, s, `document.querySelector('#log-view-all').checked===false && document.querySelectorAll('#log-node-options input').length===1`)
	evaluate(t, s, `window.keptPane=document.querySelector('.log-pane');document.querySelector('#log-view-all').click();true`)
	waitChatScript(t, s, `document.querySelectorAll('.log-pane').length===2 && [...document.querySelectorAll('.log-pane-output')].every(e=>e.textContent.includes('native-log'))`)
	if got := evaluate(t, s, `window.keptPane===document.querySelectorAll('.log-pane')[0]`); got != "true" {
		t.Fatal("existing pane replaced", got)
	}
	evaluate(t, s, `document.querySelector('#log-node-options input').click();true`)
	waitChatScript(t, s, `document.querySelectorAll('.log-pane').length===3`)
	// Stop the older instance from its node pane; the latest instance remains active.
	evaluate(t, s, `document.querySelectorAll('.log-pane-stop')[2].click();true`)
	select {
	case id := <-f.stopped:
		if id != f.records[0].ID {
			t.Fatal("node pane stopped wrong instance", id)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("node pane did not request stop")
	}
	waitChatScript(t, s, `document.querySelectorAll('.log-pane-stop')[2].disabled && !document.querySelectorAll('.log-pane-stop')[0].disabled`)
	for _, locale := range []string{"zh-CN", "en-US"} {
		if err := s.UpdateLocale(locale); err != nil {
			t.Fatal(err)
		}
		waitChatScript(t, s, fmt.Sprintf(`document.documentElement.lang===%q`, locale))
		if got := evaluate(t, s, `document.querySelectorAll('.log-pane').length===3 && window.keptPane===document.querySelectorAll('.log-pane')[0]`); got != "true" {
			t.Fatal("locale reset selections", got)
		}
	}
	t.Log("three cross-instance/node panes preserved; node pane stopped only its own run; locale preserved pane identity")
	captureComposer(t, s, "multi-instance-logs")
}
