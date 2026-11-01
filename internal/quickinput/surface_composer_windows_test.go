//go:build windows

package quickinput

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func composerSurface(t *testing.T) *nativeSurface {
	t.Helper()
	s, err := startNativeSurface(t.TempDir(), nil, func(err error) { t.Errorf("surface: %v", err) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	// Layout checks need a compositor, not a user foreground gesture. Display
	// the isolated window without trying to activate another application's focus.
	surfaceSync(t, s, func() {
		s.positionForShow()
		s.showContent(true)
		uiDLL.NewProc("ShowWindow").Call(s.hwnd.Load(), 4)
	})
	evaluate(t, s, `window.__composerReady=false;import('./input.js').then(({editor})=>{
      window.__editor=editor;
      window.__composerMetrics=()=>{const i=document.querySelector('#input'),s=getComputedStyle(i);return {
        height:i.getBoundingClientRect().height,line:parseFloat(s.lineHeight),padding:parseFloat(s.paddingTop)+parseFloat(s.paddingBottom),
        client:i.clientHeight,scroll:i.scrollHeight,top:i.scrollTop,viewport:innerHeight,expanded:!document.querySelector('#chat-panel').hidden}};
      window.__fitsRows=n=>{const m=window.__composerMetrics();return Math.abs(m.height-Math.ceil(n*m.line+m.padding))<1.1};
      window.__composerReady=true;
    });true`)
	waitChatScript(t, s, `window.__composerReady && window.__fitsRows(1) && innerHeight<240`)
	return s
}

func composerText(t *testing.T, s *nativeSurface, text string) {
	t.Helper()
	encoded, _ := json.Marshal(text)
	evaluate(t, s, `(()=>{const i=document.querySelector('#input');i.value=`+string(encoded)+`;i.setSelectionRange(i.value.length,i.value.length);i.dispatchEvent(new Event('input'));return true})()`)
}

func captureComposer(t *testing.T, s *nativeSurface, name string) {
	t.Helper()
	if directory := os.Getenv("ASTERHAND_COMPOSER_CAPTURE"); directory != "" {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		captureExperience(t, s, filepath.Join(directory, name+".png"))
	}
}

func TestNativeCompactComposerHeight(t *testing.T) {
	if os.Getenv("ASTERHAND_CHAT_GATE") != "1" {
		t.Skip("explicit native composer gate required")
	}
	if os.Getenv("ASTERHAND_CHAT_GATE_CHILD") != "1" {
		runChatGateAtMedium(t)
		return
	}
	s := composerSurface(t)
	captureComposer(t, s, "empty")
	// Find the real one-to-two-line boundary using the live textarea's scroll
	// geometry, independently of the production mirror's calculated row count.
	evaluate(t, s, `(()=>{const i=document.querySelector('#input'),line=parseFloat(getComputedStyle(i).lineHeight);
      for(let n=1;n<200;n++){i.value='中'.repeat(n);if(i.scrollHeight>i.clientHeight+line/2){window.__wrapAt=n;break}}
      i.value='中'.repeat(window.__wrapAt-1);i.dispatchEvent(new Event('input'));return true})()`)
	waitChatScript(t, s, `window.__wrapAt>1 && window.__fitsRows(1) && window.__composerMetrics().scroll<=window.__composerMetrics().client+1`)
	evaluate(t, s, `document.querySelector('#input').value+='中';document.querySelector('#input').dispatchEvent(new Event('input'));true`)
	waitChatScript(t, s, `window.__fitsRows(2)`)
	evaluate(t, s, `document.querySelector('#input').value=document.querySelector('#input').value.slice(0,-1);document.querySelector('#input').dispatchEvent(new Event('input'));true`)
	waitChatScript(t, s, `window.__fitsRows(1)`)
	for _, rows := range []int{2, 3, 9, 10, 11, 10, 9, 2, 1} {
		composerText(t, s, strings.Repeat("中文 English 😀\n", rows-1)+"末行")
		waitChatScript(t, s, fmt.Sprintf(`window.__fitsRows(%d)`, min(rows, 10)))
		if rows > 10 {
			waitChatScript(t, s, `(()=>{const m=window.__composerMetrics();return m.scroll>m.client+10})()`)
			captureComposer(t, s, "overflow")
		}
	}
	// No explicit newlines: real browser wrapping must cause the growth.
	for _, text := range []string{strings.Repeat("自动折行中文", 110), strings.Repeat("English words and emoji 😀 ", 60), "https://example.invalid/" + strings.Repeat("abcdefgh", 180)} {
		composerText(t, s, text)
		waitChatScript(t, s, `window.__fitsRows(10) && window.__composerMetrics().scroll>window.__composerMetrics().client`)
	}
	composerText(t, s, "尾部换行\n")
	waitChatScript(t, s, `window.__fitsRows(2)`)
	captureComposer(t, s, "two-lines")
	for _, theme := range []string{"star", "frost"} {
		if err := s.Update(theme); err != nil {
			t.Fatal(err)
		}
		waitChatScript(t, s, `document.documentElement.dataset.theme==='`+theme+`' && window.__fitsRows(2)`)
		captureComposer(t, s, "two-lines-"+theme)
	}
	// Staged text is hidden presentation, not an empty authoritative draft.
	evaluate(t, s, `window.__snap=window.__editor.snapshot();window.__editor.stageSubmission('layout-test',window.__snap);true`)
	waitChatScript(t, s, `window.__fitsRows(1)`)
	if result := evaluate(t, s, `window.__editor.restoreSubmission('layout-test')`); result != "true" {
		t.Fatal("staged draft was lost", result)
	}
	waitChatScript(t, s, `window.__fitsRows(2)`)
	evaluate(t, s, `document.querySelector('#chat-new').click();true`)
	waitChatScript(t, s, `!document.querySelector('#chat-draft-decision').hidden && document.querySelector('footer').getBoundingClientRect().bottom<=innerHeight+1`)
	evaluate(t, s, `document.querySelector('#chat-draft-cancel').click();true`)
	waitChatScript(t, s, `window.__fitsRows(2) && innerHeight<240`)
	evaluate(t, s, `document.querySelector('#chat-expand').click();true`)
	waitChatScript(t, s, `window.__composerMetrics().expanded && Math.abs(window.__composerMetrics().height-104)<1 && innerHeight>400`)
	composerText(t, s, strings.Repeat("长文本\n", 20))
	waitChatScript(t, s, `Math.abs(window.__composerMetrics().height-104)<1`)
	evaluate(t, s, `document.querySelector('#chat-expand').click();true`)
	waitChatScript(t, s, `window.__fitsRows(10)`)
	composerText(t, s, "")
	waitChatScript(t, s, `window.__fitsRows(1) && innerHeight<240`)
	composerText(t, s, strings.Repeat("a", 16000))
	waitChatScript(t, s, `window.__fitsRows(10) && document.querySelector('#input').value.length===16000`)
	t.Log("maximum draft geometry:", evaluate(t, s, `window.__composerMetrics()`))
	composerText(t, s, "")
	waitChatScript(t, s, `window.__fitsRows(1)`)
	t.Log("Real WebView2 single-line startup, wrap, cap, shrink, trailing newline, restore, controls and expanded layout passed.")
}

func TestResizeBridgeHeightAndGeneration(t *testing.T) {
	s := &nativeSurface{epoch: "layout-test", documentReady: true, height: initialSurfaceHeight}
	for _, height := range []int{0, minimumSurfaceHeight - 1, maximumSurfaceHeight + 1} {
		s.bridgeMessage(fmt.Sprintf(`{"version":1,"kind":"resize","generation":"layout-test","height":%d}`, height))
		if s.height != initialSurfaceHeight {
			t.Fatal("bridge accepted invalid height", height)
		}
	}
	s.bridgeMessage(`{"version":1,"kind":"resize","generation":"stale","height":160}`)
	if s.height != initialSurfaceHeight {
		t.Fatal("stale resize changed height")
	}
	s.bridgeMessage(fmt.Sprintf(`{"version":1,"kind":"resize","generation":"layout-test","height":160,"id":%q}`, strings.Repeat("x", 65)))
	if s.height != initialSurfaceHeight {
		t.Fatal("oversized resize identity changed height")
	}
	s.bridgeMessage(`{"version":1,"kind":"resize","generation":"layout-test","height":160}`)
	if s.height != 160 {
		t.Fatal("valid compact resize rejected")
	}
}

func TestNativeCompactComposerLifecycle(t *testing.T) {
	if os.Getenv("ASTERHAND_CHAT_GATE") != "1" {
		t.Skip("explicit native composer gate required")
	}
	if os.Getenv("ASTERHAND_CHAT_GATE_CHILD") != "1" {
		runChatGateAtMedium(t)
		return
	}
	s := composerSurface(t)
	text := strings.Repeat("history line\n", 20)
	composerText(t, s, text)
	waitChatScript(t, s, `window.__fitsRows(10)`)
	evaluate(t, s, `(()=>{const i=document.querySelector('#input');i.focus();i.setSelectionRange(0,0);i.scrollTop=56;window.__oldTop=i.scrollTop;return true})()`)
	if err := s.Update("frost"); err != nil {
		t.Fatal(err)
	}
	waitChatScript(t, s, `document.documentElement.dataset.theme==='frost' && Math.abs(window.__composerMetrics().top-window.__oldTop)<1`)
	evaluate(t, s, `(()=>{const i=document.querySelector('#input');i.dispatchEvent(new CompositionEvent('compositionstart'));i.value='临时组合\n'.repeat(4)+'候选';i.setSelectionRange(i.value.length,i.value.length);i.dispatchEvent(new Event('input'));return true})()`)
	waitChatScript(t, s, `window.__fitsRows(5)`)
	if accepted, _ := s.draft.Read(); accepted != text {
		t.Fatal("composition layout committed provisional text")
	}
	evaluate(t, s, `document.querySelector('#input').dispatchEvent(new CompositionEvent('compositionend'));true`)
	if err := s.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if accepted, _ := s.draft.Read(); accepted != strings.Repeat("临时组合\n", 4)+"候选" {
		t.Fatal("composition completion lost text")
	}
	surfaceSync(t, s, func() { s.hide(false) })
	composerText(t, s, strings.Repeat("restore\n", 6)+"end")
	surfaceSync(t, s, func() {
		s.showContent(true)
		uiDLL.NewProc("ShowWindow").Call(s.hwnd.Load(), 4)
	})
	waitChatScript(t, s, `window.__fitsRows(7)`)
	// The alternate experience strategy uses the same real browser measurement.
	// Replace only the isolated test editor, retaining the production CSP/bridge.
	epoch, _ := json.Marshal(s.epoch)
	evaluate(t, s, `window.__editor.dispose();window.__spareReady=false;import('./composer-layout.mjs').then(({ComposerLayout})=>{
      window.__spare=new ComposerLayout(document.querySelector('#input'),height=>window.chrome.webview.postMessage({version:1,kind:'resize',generation:`+string(epoch)+`,height}),{extraRows:1});
      window.__spare.setVisible(true);window.__spareReady=true;
    });true`)
	waitChatScript(t, s, `window.__spareReady && window.__fitsRows(8)`)
	evaluate(t, s, `document.querySelector('#input').value='';window.__spare.schedule();true`)
	waitChatScript(t, s, `window.__fitsRows(1)`)
	evaluate(t, s, `document.querySelector('#input').value='x\n'.repeat(20);window.__spare.schedule();true`)
	waitChatScript(t, s, `window.__fitsRows(10)`)
	evaluate(t, s, `window.__spare.dispose();true`)
	if result := evaluate(t, s, `document.querySelectorAll('.input-measure').length===0`); result != "true" {
		t.Fatal("disposed editor retained private measurement text", result)
	}
	t.Log("Real WebView2 passive scroll, IME draft isolation, hidden restore, spare-row strategy and measurement cleanup passed.")
}

func TestNativeCompactComposerPlacement(t *testing.T) {
	if os.Getenv("ASTERHAND_CHAT_GATE") != "1" {
		t.Skip("explicit native composer gate required")
	}
	if os.Getenv("ASTERHAND_CHAT_GATE_CHILD") != "1" {
		runChatGateAtMedium(t)
		return
	}
	s := composerSurface(t)
	var preferred windowPlacement
	var monitor surfaceMonitor
	surfaceSync(t, s, func() {
		monitorID, _, _ := uiDLL.NewProc("MonitorFromWindow").Call(s.hwnd.Load(), 2)
		var ok bool
		monitor, ok = monitorDetails(monitorID)
		if !ok {
			t.Error("monitor unavailable")
			return
		}
		point := windowPoint{monitor.work.Left + scaled(40, monitor.dpi), monitor.work.Bottom - scaled(220, monitor.dpi)}
		s.applyBounds(fitWindow(monitor.work, monitor.dpi, s.height, &point))
		s.windowEvent(s.hwnd.Load(), 0x0232, 0, 0)
		preferred = s.placements[monitor.name]
	})
	before := experienceState(t, s).bounds
	composerText(t, s, strings.Repeat("growth\n", 20))
	waitChatScript(t, s, `window.__fitsRows(10)`)
	grown := experienceState(t, s).bounds
	if grown.Bottom > monitor.work.Bottom || grown.Top >= before.Top {
		t.Fatalf("growth did not clamp to the work area: %+v -> %+v", before, grown)
	}
	surfaceSync(t, s, func() {
		s.hide(false)
		if s.placements[monitor.name] != preferred {
			t.Error("layout growth overwrote preferred position")
		}
		s.positionForShow()
		s.showContent(true)
		uiDLL.NewProc("ShowWindow").Call(s.hwnd.Load(), 4)
	})
	composerText(t, s, "")
	waitChatScript(t, s, `window.__fitsRows(1)`)
	if restored := experienceState(t, s).bounds; restored != before {
		t.Fatalf("shrink did not restore compact anchor: %+v -> %+v", before, restored)
	}
}

func TestNativeCompactComposerEditCaret(t *testing.T) {
	if os.Getenv("ASTERHAND_CHAT_GATE") != "1" {
		t.Skip("explicit native composer gate required")
	}
	if os.Getenv("ASTERHAND_CHAT_GATE_CHILD") != "1" {
		runChatGateAtMedium(t)
		return
	}
	s := composerSurface(t)
	fixture, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "compact-caret-regression.js"))
	if err != nil {
		t.Fatal(err)
	}
	if result := evaluate(t, s, string(fixture)); result != "true" {
		t.Fatal("word-wrap candidate unavailable", result)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Log("live caret diagnostics:", evaluate(t, s, `({equals:document.querySelector('#input').value===window.__editText,metrics:window.__composerMetrics(),caret:document.querySelector('#input').value ? window.__caretProbe.geometry() : null})`))
		}
	})
	evaluate(t, s, `(()=>{const i=document.querySelector('#input'),p=window.__caretProbe.candidate;i.value=p.text;i.focus();i.setSelectionRange(p.position,p.position);i.dispatchEvent(new Event('input'));return true})()`)
	waitChatScript(t, s, `window.__fitsRows(10)`)
	bounds := experienceState(t, s).bounds
	surfaceSync(t, s, func() {
		monitorID, _, _ := uiDLL.NewProc("MonitorFromWindow").Call(s.hwnd.Load(), 2)
		monitor, ok := monitorDetails(monitorID)
		if !ok {
			t.Error("monitor unavailable")
			return
		}
		s.applyBounds(windowBounds{bounds.Left, bounds.Top, bounds.Right, bounds.Top + scaled(180, monitor.dpi)})
	})
	waitChatScript(t, s, `innerHeight<185 && document.querySelector('#input').clientHeight<60`)
	evaluate(t, s, `(()=>{const i=document.querySelector('#input'),p=window.__caretProbe;i.scrollTop=(p.geometry().row-1)*p.line;window.__caretBefore=p.geometry();document.execCommand('insertText',false,'z');p.settle();return true})()`)
	waitChatScript(t, s, `window.__caretSettled`)
	if result := evaluate(t, s, `window.__caretBefore.visible && window.__caretAfter.visible`); result != "true" {
		t.Fatal("middle-word edit hid the caret", evaluate(t, s, `[window.__caretBefore,window.__caretAfter]`))
	}
	// Browser editing, undo and redo exercise native selection scrolling rather
	// than programmatically assigning a value then synthesizing an input event.
	evaluate(t, s, `(()=>{const i=document.querySelector('#input');i.value='';i.focus();window.__editText='native edit\n'.repeat(30)+'end';document.execCommand('insertText',false,window.__editText);window.__caretProbe.settle();return true})()`)
	waitChatScript(t, s, `window.__caretSettled`)
	t.Log("native long edit caret:", evaluate(t, s, `({caret:window.__caretAfter,value:document.querySelector('#input').value,position:document.querySelector('#input').selectionEnd})`))
	captureComposer(t, s, "native-edit-caret")
	if result := evaluate(t, s, `window.__caretAfter.visible`); result != "true" {
		t.Fatal("native long edit clipped the caret")
	}
	evaluate(t, s, `document.execCommand('undo');true`)
	waitChatScript(t, s, `document.querySelector('#input').value==='' && window.__fitsRows(1)`)
	evaluate(t, s, `document.execCommand('redo');window.__caretProbe.settle();true`)
	// Undo/redo can coalesce before layout, retaining the constrained viewport;
	// require the restored text and a live visible caret, not a fixed row count
	// or the probe's earlier two-frame snapshot while native resize is pending.
	waitChatScript(t, s, `document.querySelector('#input').value===window.__editText && window.__caretProbe.geometry().visible`)
	evaluate(t, s, `window.__caretProbe.dispose();true`)
}
