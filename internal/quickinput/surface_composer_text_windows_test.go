//go:build windows

package quickinput

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNativeCompactComposerTextGrowth(t *testing.T) {
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
	evaluate(t, s, `window.__textGrowthDone=false;window.__scrollbarProbe.growth({pauseAt:10}).then(result=>{window.__textGrowthResult=result;window.__textGrowthDone=true}).catch(error=>{window.__textGrowthError=String(error);window.__textGrowthDone=true});true`)
	waitChatScript(t, s, `window.__wrapPaused || window.__textGrowthDone`)
	evaluate(t, s, `window.__lateScrollDone=false;document.querySelector('#input').scrollTop=document.querySelector('#input').scrollHeight;requestAnimationFrame(()=>{window.__lateScrollTop=document.querySelector('#input').scrollTop;window.__lateScrollDone=true});true`)
	waitChatScript(t, s, `window.__lateScrollDone`)
	if result := evaluate(t, s, `window.__lateScrollTop<1`); result != "true" {
		t.Fatal("late caret scrolling exposed a clipped row", evaluate(t, s, `window.__lateScrollTop`))
	}
	captureComposer(t, s, "text-growth-pending")
	evaluate(t, s, `window.__wrapContinue=true;true`)
	waitChatScript(t, s, `window.__textGrowthDone`)
	if result := evaluate(t, s, `!window.__textGrowthError && window.__textGrowthResult.samples.every(value=>value.scrollTop<1)`); result != "true" {
		t.Fatal("text scrolled before the growing viewport was ready", evaluate(t, s, `window.__textGrowthError ?? window.__textGrowthResult.samples.filter(value=>value.scrollTop>=1)`))
	}
	composerText(t, s, "")
	waitChatScript(t, s, `window.__fitsRows(1)`)
	evaluate(t, s, `window.__pasteDone=false;window.__scrollbarProbe.hold();document.querySelector('#input').focus();document.execCommand('insertText',false,'paste\n'.repeat(30)+'end');window.__scrollbarProbe.frames(3).then(()=>{window.__pasteTop=document.querySelector('#input').scrollTop;window.__pasteDone=true});true`)
	waitChatScript(t, s, `window.__pasteDone`)
	if result := evaluate(t, s, `window.__pasteTop<1`); result != "true" {
		t.Fatal("long end insertion scrolled before growth completed", evaluate(t, s, `window.__pasteTop`))
	}
	evaluate(t, s, `window.__scrollbarProbe.release();true`)
	waitChatScript(t, s, `window.__fitsRows(10) && (()=>{const i=document.querySelector('#input');return i.scrollHeight-i.clientHeight-i.scrollTop<1})()`)
	evaluate(t, s, `window.__scrollbarProbe.hold();document.querySelector('#input').setSelectionRange(0,document.querySelector('#input').value.length);document.execCommand('delete');true`)
	waitChatScript(t, s, `window.__scrollbarProbe.queued().length===1`)
	evaluate(t, s, `document.execCommand('insertText',false,'overlap\n'.repeat(30)+'end');true`)
	waitChatScript(t, s, `window.__scrollbarProbe.queued().length===2`)
	evaluate(t, s, `window.__overlapDone=false;window.__scrollbarProbe.applyNext();window.__scrollbarProbe.wait(()=>innerHeight<200).then(async()=>{window.__overlapSamples=[];for(let i=0;i<3;i++){await window.__scrollbarProbe.frames(1);window.__overlapSamples.push(window.__scrollbarProbe.sample())}window.__overlapDone=true});true`)
	waitChatScript(t, s, `window.__overlapDone`)
	if result := evaluate(t, s, `window.__overlapSamples.every(value=>value.scrollTop<1)`); result != "true" {
		t.Fatal("older shrink released the pending newer growth", evaluate(t, s, `window.__overlapSamples`))
	}
	evaluate(t, s, `window.__scrollbarProbe.release();true`)
	waitChatScript(t, s, `window.__fitsRows(10) && (()=>{const i=document.querySelector('#input');return i.scrollHeight-i.clientHeight-i.scrollTop<1})()`)
	evaluate(t, s, `window.__scrollbarProbe.dispose();true`)
}
