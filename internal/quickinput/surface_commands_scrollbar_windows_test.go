//go:build windows

package quickinput

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	commandswire "github.com/jackgan90/Asterhand/internal/localcommands/wire"
	"github.com/jackgan90/Asterhand/internal/model"
)

func TestNativeLocalCommandsScrollbar(t *testing.T) {
	if os.Getenv("ASTERHAND_CHAT_GATE") != "1" {
		t.Skip("explicit native command test required")
	}
	if os.Getenv("ASTERHAND_CHAT_GATE_CHILD") != "1" {
		runChatGateAtMedium(t)
		return
	}
	entries := []commandswire.Entry{{ID: model.ID(), Revision: model.ID(), Name: "single", State: "ready"}}
	for i := range 14 {
		name := fmt.Sprintf("command%02d", i)
		if i < 3 {
			name = fmt.Sprintf("group%02d", i)
		}
		entries = append(entries, commandswire.Entry{ID: model.ID(), Revision: model.ID(), Name: name, State: "ready"})
	}
	catalog := model.ID()
	var empty atomic.Bool
	s := commandSurface(t, func(_ context.Context, _ commandswire.Request) commandswire.Response {
		if empty.Load() {
			return commandswire.Response{Catalog: catalog}
		}
		return commandswire.Response{Entries: entries, Catalog: catalog}
	})
	for _, name := range []string{"compact-scrollbar-regression.js", "command-scrollbar-regression.js"} {
		fixture, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", name))
		if err != nil {
			t.Fatal(err)
		}
		evaluate(t, s, string(fixture))
	}
	defer evaluate(t, s, `window.__scrollbarProbe.dispose();true`)
	for _, scenario := range []struct {
		query string
		count int
		empty bool
	}{{"/", 0, true}, {"/single", 1, false}, {"/group", 3, false}, {"/", 15, false}, {"/single", 1, false}, {"/not-found", 0, false}} {
		empty.Store(scenario.empty)
		evaluate(t, s, fmt.Sprintf(`window.__commandScrollDone=false;window.__commandScrollError='';window.__commandScrollProbe.open(%q,%d).then(result=>{window.__commandScrollResult=result;window.__commandScrollDone=true}).catch(error=>{window.__commandScrollError=String(error);window.__commandScrollDone=true});true`, scenario.query, scenario.count))
		waitChatScript(t, s, `window.__commandScrollDone`)
		if got := evaluate(t, s, `!window.__commandScrollError && window.__commandScrollResult.flashes.length===0`); got != "true" {
			t.Fatal(scenario.query, "transient scrollbar", evaluate(t, s, `window.__commandScrollError || window.__commandScrollResult`))
		}
		if scenario.count > 8 && evaluate(t, s, `window.__commandScrollProbe.sample().listBar`) != "true" {
			t.Fatal("large list lost its scrollbar")
		}
		if got := evaluate(t, s, `window.__commandScrollResult.jumps.length===0`); got != "true" {
			t.Fatal(scenario.query, "input focus underline moved", evaluate(t, s, `window.__commandScrollResult`))
		}
		t.Log(scenario.query, "stable input boundary and no transient scrollbar across held resize and allocated frames")
	}
	evaluate(t, s, `window.__commandScrollDone=false;window.__commandScrollProbe.overlap().then(result=>{window.__commandScrollResult=result;window.__commandScrollDone=true}).catch(error=>{window.__commandScrollError=String(error);window.__commandScrollDone=true});true`)
	waitChatScript(t, s, `window.__commandScrollDone`)
	if got := evaluate(t, s, `!window.__commandScrollError && window.__commandScrollResult.flashes.length===0 && window.__commandScrollResult.jumps.length===0`); got != "true" {
		t.Fatal("older shrink destabilized command layout during newer growth", evaluate(t, s, `window.__commandScrollError || window.__commandScrollResult`))
	}
	bounds := experienceState(t, s).bounds
	surfaceSync(t, s, func() {
		monitorID, _, _ := uiDLL.NewProc("MonitorFromWindow").Call(s.hwnd.Load(), 2)
		monitor, ok := monitorDetails(monitorID)
		if !ok {
			t.Error("monitor unavailable")
			return
		}
		s.applyBounds(windowBounds{bounds.Left, bounds.Top, bounds.Right, bounds.Top + scaled(300, monitor.dpi)})
	})
	waitChatScript(t, s, `innerHeight<305 && window.__commandScrollProbe.sample().listBar`)
	if got := evaluate(t, s, `!window.__commandScrollProbe.sample().outerBar && document.querySelector('footer').getBoundingClientRect().bottom<=innerHeight+1`); got != "true" {
		t.Fatal("constrained command list lost accessible controls", evaluate(t, s, `window.__commandScrollProbe.sample()`))
	}
	captureComposer(t, s, "commands-constrained-scrollbar")
	t.Log("older shrink ignored; confirmed height limit keeps list scrollable and footer visible")
}
