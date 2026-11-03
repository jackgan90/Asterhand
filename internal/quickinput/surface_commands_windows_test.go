//go:build windows

package quickinput

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/localcommands"
	commandswire "github.com/jackgan90/Asterhand/internal/localcommands/wire"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

func commandSurface(t *testing.T, handler CommandHandler) *nativeSurface {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	parent := &remoteSurface{session: model.ID(), epoch: 1, initialized: true, draft: &Draft{}, out: make(chan hostipc.Message, 32), done: make(chan struct{}), commands: handler}
	send := func(m hostipc.Message) error {
		m.Version, m.Session = hostipc.Version, parent.session
		var encoded bytes.Buffer
		if err := hostipc.Write(&encoded, m, true); err != nil {
			return err
		}
		decoded, err := hostipc.Read(&encoded, true)
		if err != nil {
			return err
		}
		return parent.receive(decoded)
	}
	relay := newEditorRelay(1, 0, send, func(err error) { t.Errorf("relay: %v", err) })
	s, err := startConfiguredSurface(t.TempDir(), nil, func(err error) { t.Errorf("surface: %v", err) }, relay, &Draft{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	surfaceSync(t, s, func() { s.commandSend = send })
	var busy atomic.Bool
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case m := <-parent.out:
				var encoded bytes.Buffer
				if err := hostipc.Write(&encoded, m, false); err != nil {
					t.Error(err)
					return
				}
				decoded, err := hostipc.Read(&encoded, false)
				if err == nil {
					err = serveHostCommand(ctx, s, relay, &busy, decoded, send, func(err error) { t.Error(err) })
				}
				if err != nil && ctx.Err() == nil {
					t.Error(err)
					return
				}
			}
		}
	}()
	surfaceSync(t, s, func() { s.positionForShow(); s.showContent(true); uiDLL.NewProc("ShowWindow").Call(s.hwnd.Load(), 4) })
	return s
}

func TestNativeLocalCommandsWorkflow(t *testing.T) {
	if os.Getenv("ASTERHAND_CHAT_GATE") != "1" {
		t.Skip("explicit native command test required")
	}
	if os.Getenv("ASTERHAND_CHAT_GATE_CHILD") != "1" {
		runChatGateAtMedium(t)
		return
	}
	var starts atomic.Int32
	var launchFails atomic.Bool
	r := localcommands.Open(t.TempDir(), func(localcommands.Command) error {
		starts.Add(1)
		if launchFails.Load() {
			return fmt.Errorf("测试启动失败：请检查脚本")
		}
		return nil
	})
	script := filepath.Join(t.TempDir(), "safe.ps1")
	if err := os.WriteFile(script, []byte("exit 0"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := range 12 {
		catalog, _ := r.Snapshot()
		c := localcommands.Command{Name: fmt.Sprintf("command%02d", i), Script: script, Arguments: []string{fmt.Sprint(i)}, Description: "测试脚本功能", Aliases: []string{fmt.Sprintf("别名%d", i)}}
		if i == 0 {
			c.Aliases = []string{"备份"}
		}
		if i == 11 {
			c.Name = "missing"
			c.Script = filepath.Join(filepath.Dir(script), "missing.ps1")
		}
		if _, err := r.Save(localcommands.Mutation{Revision: catalog.Revision, Command: c}); err != nil {
			t.Fatal(err)
		}
	}
	opened := make(chan string, 2)
	s := commandSurface(t, func(ctx context.Context, request commandswire.Request) commandswire.Response {
		switch request.Action {
		case "list":
			return r.List(request)
		case "launch":
			return r.Launch(ctx, request.ID, request.Revision)
		case "settings":
			opened <- request.ID
		}
		return commandswire.Response{}
	})
	composerText(t, s, "/")
	waitChatScript(t, s, `document.querySelectorAll('.command-option').length===12 && document.querySelector('#command-list').scrollHeight>document.querySelector('#command-list').clientHeight`)
	waitChatScript(t, s, `document.querySelector('[data-state=missing]')!==null`)
	if got := evaluate(t, s, `document.querySelectorAll('.command-option[aria-selected=true]').length===1 && innerHeight<=800`); got != "true" {
		t.Fatal(got)
	}
	captureComposer(t, s, "commands-many-star")
	evaluate(t, s, `(()=>{const i=document.querySelector('#input');for(let n=0;n<9;n++)i.dispatchEvent(new KeyboardEvent('keydown',{key:'ArrowDown',bubbles:true,cancelable:true}));return true})()`)
	waitChatScript(t, s, `document.querySelector('#command-list').scrollTop>0`)
	composerText(t, s, "/备份")
	waitChatScript(t, s, `document.querySelectorAll('.command-option').length===1 && document.querySelector('.command-option').textContent.includes('备份') && document.querySelector('#command-list').scrollHeight<=document.querySelector('#command-list').clientHeight+1`)
	if err := s.Update("frost"); err != nil {
		t.Fatal(err)
	}
	captureComposer(t, s, "commands-filtered-frost")
	evaluate(t, s, `(()=>{const i=document.querySelector('#input');i.dispatchEvent(new CompositionEvent('compositionstart'));i.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',isComposing:true,bubbles:true,cancelable:true}));i.dispatchEvent(new CompositionEvent('compositionend'));i.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true,cancelable:true}));return true})()`)
	if starts.Load() != 0 {
		t.Fatal("IME confirmation launched command")
	}
	time.Sleep(70 * time.Millisecond)
	evaluate(t, s, `document.querySelector('.command-option').click();true`)
	deadline := time.Now().Add(time.Second)
	for starts.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if starts.Load() != 1 {
		t.Fatal("first command did not launch")
	}
	if got := evaluate(t, s, `!document.querySelector('#command-panel').textContent.includes('已启动') && document.querySelector('.command-error').hidden`); got != "true" {
		t.Fatal("successful launch displayed feedback")
	}
	evaluate(t, s, `(()=>{const i=document.querySelector('#input');for(let n=0;n<2;n++)i.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true,cancelable:true}));i.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',repeat:true,bubbles:true,cancelable:true}));return true})()`)
	deadline = time.Now().Add(time.Second)
	for starts.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if starts.Load() != 3 {
		t.Fatal("repeat launch count", starts.Load())
	}
	if got := evaluate(t, s, `document.querySelector('#input').value==='/备份' && document.querySelector('#chat-panel').hidden`); got != "true" {
		t.Fatal("command changed chat", got)
	}
	checkCommandErrorLifecycle(t, s, launchFails.Store)
	composerText(t, s, "/missing")
	waitChatScript(t, s, `document.querySelectorAll('.command-option').length===1 && document.querySelector('.command-option').dataset.state==='missing'`)
	evaluate(t, s, `document.querySelector('.command-option').click();true`)
	waitChatScript(t, s, `document.querySelector('.command-error').textContent==='命令脚本缺失，请更新配置' && !document.querySelector('.command-error').hidden`)
	captureComposer(t, s, "commands-missing-frost")
	evaluate(t, s, `document.querySelector('.command-error').click();true`)
	select {
	case id := <-opened:
		if id == "" {
			t.Fatal("missing command target")
		}
	case <-time.After(time.Second):
		t.Fatal("settings not requested")
	}
	checkCommandEscapeAndExpanded(t, s)
}

func checkCommandEscapeAndExpanded(t *testing.T, s *nativeSurface) {
	t.Helper()
	composerText(t, s, "/")
	waitChatScript(t, s, `document.querySelector('#command-message').textContent==='' && document.querySelector('.command-option[data-state=missing] .command-error').textContent==='命令脚本缺失，请更新配置'`)
	evaluate(t, s, `document.querySelector('#chat-expand').click();true`)
	waitChatScript(t, s, `!document.querySelector('#chat-panel').hidden && !document.querySelector('#command-panel').hidden && document.querySelector('#command-list').clientHeight>=40`)
	if got := evaluate(t, s, `document.querySelector('#command-panel').getBoundingClientRect().bottom<=document.querySelector('footer').getBoundingClientRect().top`); got != "true" {
		t.Fatal("command list escaped viewport", got)
	}
	captureComposer(t, s, "commands-expanded-frost")
	composerText(t, s, "/not-found")
	waitChatScript(t, s, `document.querySelectorAll('.command-option').length===0 && document.querySelector('#command-empty').textContent==='未找到命令'`)
	pressCommandEscapeAndWaitHidden(t, s)
	if got := evaluate(t, s, `document.querySelector('#input').value==='/not-found'`); got != "true" {
		t.Fatal(got)
	}
	surfaceSync(t, s, func() { s.showContent(true); uiDLL.NewProc("ShowWindow").Call(s.hwnd.Load(), 4) })
	waitChatScript(t, s, `!document.querySelector('#command-panel').hidden`)
	composerText(t, s, "/plain")
	evaluate(t, s, `document.querySelector('#command-as-text').click();true`)
	// The fake surface has no model client: quoting must still be a normal
	// draft operation; any failed chat send retains its original quoted body.
	waitChatScript(t, s, `document.querySelector('#command-panel').hidden`)
}

func TestNativeCommandEscapeHidesSurface(t *testing.T) {
	if os.Getenv("ASTERHAND_CHAT_GATE") != "1" {
		t.Skip("explicit native command test required")
	}
	if os.Getenv("ASTERHAND_CHAT_GATE_CHILD") != "1" {
		runChatGateAtMedium(t)
		return
	}
	var launches atomic.Int32
	s := commandSurface(t, func(_ context.Context, request commandswire.Request) commandswire.Response {
		if request.Action == "launch" {
			launches.Add(1)
		}
		return commandswire.Response{Entries: []commandswire.Entry{{ID: model.ID(), Name: "safe", State: "ready", ExecutionMode: "unmanaged"}}}
	})
	for _, query := range []string{"/", "/safe", "/not-found"} {
		composerText(t, s, query)
		count := 1
		if query == "/not-found" {
			count = 0
		}
		waitChatScript(t, s, fmt.Sprintf(`!document.querySelector('#command-panel').hidden && document.querySelectorAll('.command-option').length===%d`, count))
		if query == "/" {
			evaluate(t, s, `(()=>{const input=document.querySelector('#input');for(const fields of [{isComposing:true},{keyCode:229}]) input.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true,...fields}));input.dispatchEvent(new CompositionEvent('compositionstart'));input.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));return true})()`)
			if !experienceState(t, s).visible {
				t.Fatal("IME Escape hid the command window")
			}
			evaluate(t, s, `document.querySelector('#input').dispatchEvent(new CompositionEvent('compositionend'));true`)
		}
		pressCommandEscapeAndWaitHidden(t, s)
		if got := evaluate(t, s, fmt.Sprintf(`document.querySelector('#input').value==='%s'`, query)); got != "true" {
			t.Fatal("Escape changed the command query", got)
		}
		surfaceSync(t, s, func() { s.showContent(true); uiDLL.NewProc("ShowWindow").Call(s.hwnd.Load(), 4) })
		waitChatScript(t, s, `!document.querySelector('#command-panel').hidden`)
	}
	if launches.Load() != 0 {
		t.Fatal("Escape launched a command")
	}
}

func pressCommandEscapeAndWaitHidden(t *testing.T, s *nativeSurface) {
	t.Helper()
	evaluate(t, s, `document.querySelector('#input').dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));true`)
	for deadline := time.Now().Add(time.Second); experienceState(t, s).visible; {
		if time.Now().After(deadline) {
			t.Fatal("one Escape must hide the entire native window in command mode")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
