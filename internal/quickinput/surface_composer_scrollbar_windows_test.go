//go:build windows

package quickinput

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNativeCompactComposerScrollbar(t *testing.T) {
	if os.Getenv("ASTERHAND_CHAT_GATE") != "1" {
		t.Skip("explicit native composer gate required")
	}
	if os.Getenv("ASTERHAND_CHAT_GATE_CHILD") != "1" {
		runChatGateAtMedium(t)
		return
	}
	s := composerSurface(t)
	fixture, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "compact-scrollbar-regression.js"))
	if err != nil {
		t.Fatal(err)
	}
	evaluate(t, s, string(fixture))
	evaluate(t, s, `window.__scrollbarDone=false;window.__scrollbarProbe.growth().then(result=>{window.__scrollbarResult=result;window.__scrollbarDone=true}).catch(error=>{window.__scrollbarError=String(error);window.__scrollbarDone=true});true`)
	waitChatScript(t, s, `window.__scrollbarDone`)
	if result := evaluate(t, s, `!window.__scrollbarError && window.__scrollbarResult.flashes.length===0`); result != "true" {
		t.Fatal("scrollbar appeared during auto growth", evaluate(t, s, `window.__scrollbarError ?? window.__scrollbarResult.flashes`))
	}
	t.Log("actual wrap growth from 1 to 10 rows: no transient inner or outer scrollbar")
	evaluate(t, s, `document.execCommand('insertText',false,'\nextra');true`)
	waitChatScript(t, s, `window.__scrollbarProbe.sample().inputBar`)
	composerText(t, s, "a\nb\nc\nd")
	waitChatScript(t, s, `window.__fitsRows(4) && !window.__scrollbarProbe.sample().inputBar`)
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
	waitChatScript(t, s, `innerHeight<185 && window.__scrollbarProbe.sample().inputBar`)
	evaluate(t, s, `window.__constrainedDone=false;window.__scrollbarProbe.hold();document.querySelector('#input').focus();document.querySelector('#input').setSelectionRange(7,7);document.execCommand('insertText',false,'\ne');(async()=>{window.__constrainedSamples=[];for(let i=0;i<3;i++){await window.__scrollbarProbe.frames(1);window.__constrainedSamples.push(window.__scrollbarProbe.sample())}window.__constrainedDone=true})();true`)
	waitChatScript(t, s, `window.__constrainedDone`)
	if result := evaluate(t, s, `window.__constrainedSamples.every(value=>value.inputBar)`); result != "true" {
		t.Fatal("confirmed limit lost its scrollbar while waiting", evaluate(t, s, `window.__constrainedSamples`))
	}
	request, err := decodeBridgeMessage(evaluate(t, s, `window.__scrollbarProbe.queued().at(-1)`))
	if err != nil {
		t.Fatal(err)
	}
	surfaceSync(t, s, func() {
		// Model a native work-area cap that allocates the same actual height.
		s.height, s.layoutID = request.Height, request.ID
		s.postLayoutApplied()
	})
	waitChatScript(t, s, `window.__scrollbarProbe.sample().inputBar`)
	evaluate(t, s, `window.__scrollbarProbe.discard();true`)
	surfaceSync(t, s, func() { s.positionCurrent(nil) })
	waitChatScript(t, s, `window.__fitsRows(5) && !window.__scrollbarProbe.sample().inputBar`)
	composerText(t, s, "")
	waitChatScript(t, s, `window.__fitsRows(1) && !window.__scrollbarProbe.sample().inputBar`)
	evaluate(t, s, `window.__scrollbarProbe.dispose();true`)
}
