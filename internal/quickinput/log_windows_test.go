//go:build windows

package quickinput

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/localcommands"
	commandswire "github.com/jackgan90/Asterhand/internal/localcommands/wire"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

func TestManagedLogWindowIndependentHost(t *testing.T) {
	directory := os.Getenv("ASTERHAND_INPUT_HOST_TEST_DIRECTORY")
	if os.Getenv("ASTERHAND_DESKTOP_TEST") != "1" || directory == "" {
		t.Skip("requires built host and desktop validation")
	}
	home := t.TempDir()
	script := filepath.Join(home, "run.cmd")
	if err := os.WriteFile(script, []byte("@echo off\r\necho log-window-live\r\nping -n 60 127.0.0.1 >nul\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m := localcommands.NewRunManager(home, localcommands.NewPlatformBackend(), nil)
	defer m.Close(context.Background())
	c := localcommands.Command{ID: model.ID(), Name: "log_window", Graph: commandTestGraph(script)}
	run, err := m.Start(c)
	if err != nil {
		t.Fatal(err)
	}
	call := func(ctx context.Context, r commandswire.Request) commandswire.Response {
		var value any
		var err error
		switch r.Action {
		case "status":
			value = m.Run(r.ID, r.RunID)
		case "runs":
			data, _ := json.Marshal(m.Runs(r.ID))
			return commandswire.Response{Runs: data}
		case "stop_all":
			err = m.StopAll(r.ID, r.RunIDs)
		case "read_log", "tail_log":
			cursor := r.Cursor
			if r.Action == "tail_log" {
				cursor = -1
			}
			value, err = m.Read(r.ID, r.RunID, cursor)
		case "stop":
			value, err = m.Stop(r.ID, r.RunID, "user_stop")
		}
		if err != nil {
			return localcommands.Failure(err)
		}
		data, _ := json.Marshal(value)
		if r.Action == "read_log" || r.Action == "tail_log" {
			return commandswire.Response{Log: data}
		}
		return commandswire.Response{Run: data}
	}
	host := newLogWindowHost(home, call, func() (hostProcess, error) { return launchWindowsHostMode(directory, true) })
	defer host.Close()
	for range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		err = host.Open(ctx, c.ID)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		h := host.(*logHost)
		h.mu.Lock()
		connection := h.connection
		h.mu.Unlock()
		connection.close()
		if !m.Busy(c.ID) {
			t.Fatal("closing log host stopped script")
		}
	}
	if _, err = m.Stop(c.ID, run.ID, "user_stop"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if m.Current(c.ID).Result != "interrupted" {
		t.Fatal(m.Current(c.ID))
	}
}

func TestManagedLogNativeSurfaceThemesAndText(t *testing.T) {
	if os.Getenv("ASTERHAND_DESKTOP_TEST") != "1" {
		t.Skip("requires desktop validation")
	}
	if os.Getenv("ASTERHAND_CHAT_GATE_CHILD") != "1" {
		runChatGateAtMedium(t)
		return
	}
	requests := make(chan hostipc.Message, 8)
	log := &logSurface{id: model.ID(), send: func(m hostipc.Message) error { requests <- m; return nil }, routes: &sync.Map{}}
	fixture := newNativeLogFixture(log.id)
	s, err := startWindowSurface(t.TempDir(), nil, func(err error) { t.Errorf("surface: %v", err) }, nil, log)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	surfaceSync(t, s, func() {
		if err := s.showLogWindow(nil); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case m := <-requests:
				if m.Kind != hostipc.CommandCall {
					continue
				}
				log.routes.Delete(m.CommandID)
				log.inFlight.Add(-1)
				reply := fixture.reply(*m.Command)
				_ = s.dispatch(ctx, func() { _ = s.postJSON(map[string]any{"kind": "command_result", "id": m.CommandID, "response": reply}) })
			}
		}
	}()
	for _, theme := range []string{"star", "frost"} {
		for _, locale := range []string{"zh-CN", "en-US"} {
			if err = s.Update(theme); err != nil {
				t.Fatal(err)
			}
			if err = s.UpdateLocale(locale); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(2 * time.Second)
			for {
				got := evaluate(t, s, "[document.documentElement.lang,document.documentElement.dataset.theme]")
				expected, _ := json.Marshal([]string{locale, theme})
				if got == string(expected) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("preference update did not arrive: %s", got)
				}
				time.Sleep(10 * time.Millisecond)
			}
			waitChatScript(t, s, `document.querySelectorAll('.log-pane').length===1 && document.querySelector('.log-pane-output').textContent.includes('native-log')`)
			result := evaluate(t, s, `(()=>{const el=document.querySelector('.log-pane-output');const span=document.createElement('span');span.textContent='<script>window.pwned=1</script> 中文';el.append(span);return {lang:document.documentElement.lang,theme:document.documentElement.dataset.theme,text:el.textContent,safe:!window.pwned,toolbar:document.querySelectorAll('.log-pane-tools button').length}})()`)
			if !strings.Contains(result, `"safe":true`) || !strings.Contains(result, `"toolbar":4`) {
				t.Fatal(result)
			}
			t.Log(result)
		}
	}
	checkNativeMultiRunPanes(t, s, fixture)
	checkNativeLogCodecs(t, s)
}
