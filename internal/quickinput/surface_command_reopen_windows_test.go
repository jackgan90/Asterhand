//go:build windows

package quickinput

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	commandswire "github.com/jackgan90/Asterhand/internal/localcommands/wire"
	"github.com/jackgan90/Asterhand/internal/model"
)

func TestNativeCommandReopenFrames(t *testing.T) {
	if os.Getenv("ASTERHAND_CHAT_GATE") != "1" {
		t.Skip("explicit native command test required")
	}
	if os.Getenv("ASTERHAND_CHAT_GATE_CHILD") != "1" {
		runChatGateAtMedium(t)
		return
	}
	entries := []commandswire.Entry{{ID: model.ID(), Name: "single", State: "ready"}}
	for index := range 11 {
		entries = append(entries, commandswire.Entry{ID: model.ID(), Name: fmt.Sprintf("group%02d", index), State: "ready"})
	}
	catalog := model.ID()
	s := commandSurface(t, func(_ context.Context, _ commandswire.Request) commandswire.Response {
		return commandswire.Response{Entries: entries, Catalog: catalog}
	})
	fixture, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "command-reopen-regression.js"))
	if err != nil {
		t.Fatal(err)
	}
	evaluate(t, s, string(fixture))
	defer evaluate(t, s, `window.__commandReopenProbe.dispose();true`)
	for _, scenario := range []struct {
		query    string
		count    int
		cycles   int
		expanded bool
	}{{"/single", 1, 5, false}, {"/", 12, 2, false}, {"/not-found", 0, 2, false}, {"/single", 1, 2, true}} {
		composerText(t, s, scenario.query)
		if scenario.expanded {
			evaluate(t, s, `document.querySelector('#chat-expand').click();true`)
		}
		waitChatScript(t, s, fmt.Sprintf(`!document.querySelector('#command-panel').hidden && document.querySelectorAll('.command-option').length===%d`, scenario.count))
		// Let the new query's layout request run before recording its settled
		// baseline. The previous query's allocation marker may still be present.
		evaluate(t, s, `window.__commandReopenReady=false;requestAnimationFrame(()=>requestAnimationFrame(()=>{window.__commandReopenReady=true}));true`)
		waitChatScript(t, s, `window.__commandReopenReady && document.querySelector('#command-panel').dataset.layoutSettled==='true' && document.querySelector('footer').getBoundingClientRect().bottom<=innerHeight+1`)
		for cycle := range scenario.cycles {
			evaluate(t, s, `window.__commandReopenProbe.arm();true`)
			pressCommandEscapeAndWaitHidden(t, s)
			// Include immediate and delayed reopens instead of relying on a debounce.
			time.Sleep(time.Duration(cycle%3) * 15 * time.Millisecond)
			surfaceSync(t, s, func() {
				s.showContent(true)
				uiDLL.NewProc("ShowWindow").Call(s.hwnd.Load(), 4)
				_ = s.postJSON(map[string]string{"kind": "focus"})
			})
			waitChatScript(t, s, `window.__commandReopenProbe.done()`)
			result := evaluate(t, s, `window.__commandReopenProbe.result()`)
			if !strings.Contains(result, `"ok":true`) {
				t.Fatalf("query=%s expanded=%v cycle=%d: %s", scenario.query, scenario.expanded, cycle, result)
			}
			t.Logf("query=%s expanded=%v cycle=%d: %s", scenario.query, scenario.expanded, cycle, result)
		}
	}
}
