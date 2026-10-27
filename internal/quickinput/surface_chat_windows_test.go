//go:build windows

package quickinput

import (
	"bytes"
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/conversation/wire"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

type surfaceChatFixture struct {
	mu           sync.Mutex
	conversation string
	turn         *wire.Turn
	submitted    chan wire.Request
	release      chan struct{}
	historyCalls int
	createCalls  int
	text         string
	reject       bool
}

func (f *surfaceChatFixture) Call(ctx context.Context, request wire.Request) (wire.Response, error) {
	response := wire.Response{Target: wire.Target{Node: strings.Repeat("a", 32), Role: "default"}, CanSend: true}
	if request.Action == "submit" {
		f.submitted <- request
		select {
		case <-f.release:
		case <-ctx.Done():
			return response, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	conversation := wire.Conversation{ID: f.conversation, State: "ready", UpdatedAt: time.Now()}
	switch request.Action {
	case "list":
		response.Conversations = []wire.Conversation{conversation}
	case "create":
		f.createCalls++
		response.Conversation = &conversation
	case "submit":
		if f.reject {
			return response, wire.Fail("rejected", "fixture rejected input")
		}
		f.text = request.Text
		f.turn = &wire.Turn{ID: model.ID(), UserMessage: model.ID(), Operation: request.Operation, Conversation: request.Conversation, Delivery: "accepted", Run: "completed", Persistence: "saved", AssistantMessage: model.ID()}
		response.Turn = f.turn
	case "history", "reconcile":
		if request.Action == "history" {
			f.historyCalls++
		}
		response.Conversation, response.Turn = &conversation, f.turn
		response.Page = &wire.Page{Messages: []wire.Message{{ID: "safe-message", Role: "assistant", Blocks: []wire.Block{{Type: "text", Text: "# 标题\n**安全文字**\n<script>window.__unsafe=1</script>\n[外链](https://example.invalid)\n```js\nconst a = '<img src=x onerror=alert(1)>';\n```"}}}}}
	}
	if response.Page != nil && f.turn != nil {
		response.Page.Messages = append(response.Page.Messages, wire.Message{ID: f.turn.UserMessage, Role: "user", Turn: f.turn.ID, Blocks: []wire.Block{{Type: "text", Text: f.text}}})
	}
	return response, nil
}

func TestNativeChatPanelWorkflow(t *testing.T) {
	if os.Getenv("ASTERHAND_CHAT_GATE") != "1" {
		t.Skip("explicit native chat test required")
	}
	if os.Getenv("ASTERHAND_CHAT_GATE_CHILD") != "1" {
		runChatGateAtMedium(t)
		return
	}
	if err := checkInteractiveDesktop(); err != nil {
		t.Fatal(err)
	}
	fixture := &surfaceChatFixture{conversation: model.ID(), submitted: make(chan wire.Request, 2), release: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	chat := &chatController{client: fixture, ctx: ctx, cancel: cancel, pending: map[string]*chatSubmission{}}
	defer chat.close()
	parent := &remoteSurface{session: model.ID(), epoch: 1, initialized: true, draft: &Draft{}, out: make(chan hostipc.Message, 32), done: make(chan struct{}), chat: chat}
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
	surface, err := startConfiguredSurface(t.TempDir(), nil, func(err error) { t.Errorf("surface: %v", err) }, relay, &Draft{})
	if err != nil {
		t.Fatal(err)
	}
	defer surface.Close()
	if err = surface.dispatch(ctx, func() { surface.chatSend = send }); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case m := <-parent.out:
				var encoded bytes.Buffer
				if err := hostipc.Write(&encoded, m, false); err != nil {
					t.Errorf("parent frame: %v", err)
					return
				}
				decoded, err := hostipc.Read(&encoded, false)
				if err != nil {
					t.Errorf("host frame: %v", err)
					return
				}
				if decoded.Kind == hostipc.Accepted {
					err = relay.accept(decoded)
				} else {
					err = surface.dispatch(ctx, func() {
						_ = surface.postJSON(map[string]any{"kind": "chat_result", "id": decoded.ChatID, "reply": decoded.ChatReply})
					})
				}
				if err != nil && ctx.Err() == nil {
					t.Errorf("host delivery: %v", err)
					return
				}
			}
		}
	}()
	defer cancel()
	surface.Toggle()
	waitChatScript(t, surface, `!document.querySelector('#chat-send').disabled`)
	waitChatScript(t, surface, `document.querySelector('#chat-conversation-name').textContent.startsWith('对话 ')`)
	fixture.mu.Lock()
	historyCalls := fixture.historyCalls
	fixture.mu.Unlock()
	if historyCalls != 0 {
		t.Fatal("collapsed input loaded native history", historyCalls)
	}
	evaluate(t, surface, `document.querySelector('#chat-expand').click();true`)
	waitChatScript(t, surface, `document.querySelector('#chat-messages').children.length===1`)
	if result := evaluate(t, surface, `!window.__unsafe && document.querySelectorAll('#chat-messages script,#chat-messages a,#chat-messages img').length===0 && document.querySelector('#chat-messages code').textContent.includes('<img')`); result != "true" {
		t.Fatal("unsafe rendering", result)
	}
	evaluate(t, surface, `document.querySelector('#chat-expand').click();true`)
	evaluate(t, surface, `document.querySelector('#input').value='first 中文';document.querySelector('#input').dispatchEvent(new Event('input'));document.querySelector('#input').dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true,cancelable:true}));true`)
	select {
	case request := <-fixture.submitted:
		if request.Text != "first 中文" {
			t.Fatal(request.Text)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Enter did not submit")
	}
	waitChatScript(t, surface, `[...document.querySelectorAll('.chat-user')].some(node=>node.textContent.includes('first 中文'))`)
	waitChatScript(t, surface, `document.querySelector('#input').value==='' && !document.querySelector('#chat-panel').hidden`)
	if text, _ := parent.draft.Read(); text != "first 中文" {
		t.Fatal("pending submission lost parent recovery snapshot", text)
	}
	evaluate(t, surface, `window.__pendingBubble=[...document.querySelectorAll('.chat-user')].find(node=>node.textContent.includes('first 中文'));true`)
	evaluate(t, surface, `document.querySelector('#input').value='newer draft';document.querySelector('#input').dispatchEvent(new Event('input'));true`)
	close(fixture.release)
	waitChatScript(t, surface, `document.querySelector('#chat-status').textContent.includes('回复完成')`)
	waitChatScript(t, surface, `document.querySelectorAll('.chat-user').length===1 && document.querySelector('.chat-user')===window.__pendingBubble`)
	if result := evaluate(t, surface, `document.querySelectorAll('.chat-delivery-status').length===0`); result != "true" {
		t.Fatal("successful receipt added an error label", result)
	}
	if result := evaluate(t, surface, `document.querySelector('#input').value`); result != `"newer draft"` {
		t.Fatal("receipt lost newer draft", result)
	}
	if result := evaluate(t, surface, `document.querySelector('#input').dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',shiftKey:true,cancelable:true,bubbles:true}))`); result != "true" {
		t.Fatal("Shift+Enter default newline was prevented", result)
	}
	select {
	case <-fixture.submitted:
		t.Fatal("Shift+Enter submitted")
	case <-time.After(100 * time.Millisecond):
	}
	evaluate(t, surface, `document.querySelector('#input').dispatchEvent(new CompositionEvent('compositionstart'));document.querySelector('#input').dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',ctrlKey:true,bubbles:true}));true`)
	select {
	case <-fixture.submitted:
		t.Fatal("IME composition submitted")
	case <-time.After(150 * time.Millisecond):
	}
	evaluate(t, surface, `document.querySelector('#input').dispatchEvent(new CompositionEvent('compositionend'));document.querySelector('#input').dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',cancelable:true,bubbles:true}));document.querySelector('#chat-pin').click();true`)
	select {
	case <-fixture.submitted:
		t.Fatal("composition-ending Enter submitted")
	case <-time.After(100 * time.Millisecond):
	}
	var pinned bool
	for deadline := time.Now().Add(time.Second); !pinned && time.Now().Before(deadline); {
		_ = surface.dispatch(ctx, func() { pinned = surface.pinned })
		if !pinned {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if !pinned {
		t.Fatal("pin did not reach native host")
	}
	fixture.mu.Lock()
	fixture.reject = true
	fixture.mu.Unlock()
	evaluate(t, surface, `document.querySelector('#input').dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',ctrlKey:true,cancelable:true,bubbles:true}));true`)
	select {
	case <-fixture.submitted:
	case <-time.After(3 * time.Second):
		t.Fatal("Ctrl+Enter compatibility failed")
	}
	waitChatScript(t, surface, `[...document.querySelectorAll('.chat-delivery-status')].some(node=>node.textContent.includes('消息发送失败'))`)
	evaluate(t, surface, `document.querySelector('#chat-new').click();true`)
	waitChatScript(t, surface, `!document.querySelector('#chat-draft-decision').hidden`)
	evaluate(t, surface, `document.querySelector('#chat-draft-cancel').click();true`)
	if result := evaluate(t, surface, `document.querySelector('#input').value`); result != `"newer draft"` {
		t.Fatal("switch cancellation changed draft", result)
	}
	evaluate(t, surface, `document.querySelector('#chat-new').click();document.querySelector('#input').value='later revision';document.querySelector('#input').dispatchEvent(new Event('input'));document.querySelector('#chat-draft-discard').click();true`)
	waitChatScript(t, surface, `document.querySelector('#chat-status').textContent.includes('草稿已变化')`)
	if result := evaluate(t, surface, `document.querySelector('#input').value`); result != `"later revision"` {
		t.Fatal("late discard lost newer revision", result)
	}
	fixture.mu.Lock()
	createCalls := fixture.createCalls
	fixture.mu.Unlock()
	if createCalls != 0 {
		t.Fatal("cancelled switch created conversation", createCalls)
	}
	if result := evaluate(t, surface, `[localStorage.length,sessionStorage.length]`); result != "[0,0]" {
		t.Fatal("chat persisted browser state", result)
	}
	evaluate(t, surface, `window.__chatRender=null;import('./chat-renderer.mjs').then(({messageRenderer})=>{
      const container=document.querySelector('#chat-messages');container.replaceChildren();
      const text='## 长消息\n正文🙂'.repeat(400);
      const messages=Array.from({length:200},(_,index)=>({id:'render-'+index,role:'assistant',blocks:[{type:'text',text}]}));
      const start=performance.now();messageRenderer(container,{copy(){},expand(){}})(messages);
      window.__chatRender={count:container.children.length,bytes:new TextEncoder().encode(text).length*200,ms:performance.now()-start};
    });true`)
	waitChatScript(t, surface, `window.__chatRender?.count===200`)
	t.Log("production renderer:", evaluate(t, surface, `window.__chatRender`))
	if result := evaluate(t, surface, `document.querySelectorAll('#chat-messages *').length<2000`); result != "true" {
		t.Fatal("dense markdown expanded DOM without limit")
	}
}

func waitChatScript(t *testing.T, surface *nativeSurface, script string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if evaluate(t, surface, script) == "true" {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("chat condition not satisfied: %s", script)
}
