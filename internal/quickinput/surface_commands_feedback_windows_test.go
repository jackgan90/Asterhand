//go:build windows

package quickinput

import "testing"

func checkCommandErrorLifecycle(t *testing.T, s *nativeSurface, fail func(bool)) {
	t.Helper()
	fail(true)
	evaluate(t, s, `window.__commandHeightBefore=innerHeight;document.querySelector('.command-option').click();true`)
	waitChatScript(t, s, `!document.querySelector('.command-error').hidden && document.querySelector('.command-error').textContent.includes('测试启动失败')`)
	if got := evaluate(t, s, `(()=>{const row=document.querySelector('.command-option'),error=row.querySelector('.command-error');return innerHeight===window.__commandHeightBefore && error.getBoundingClientRect().left>row.querySelector('.command-info').getBoundingClientRect().left && error.getBoundingClientRect().right<=row.getBoundingClientRect().right && !document.querySelector('.command-feedback')})()`); got != "true" {
		t.Fatal("error changed height or escaped its command row", got)
	}
	captureComposer(t, s, "commands-inline-error-frost")
	if err := s.Update("star"); err != nil {
		t.Fatal(err)
	}
	captureComposer(t, s, "commands-inline-error-star")
	if err := s.Update("frost"); err != nil {
		t.Fatal(err)
	}
	// Cross both the old 1.6s feedback timeout and the visible catalog refresh.
	evaluate(t, s, `window.__errorWaitDone=false;setTimeout(()=>{window.__errorWaitDone=true},1800);true`)
	waitChatScript(t, s, `window.__errorWaitDone`)
	waitChatScript(t, s, `!document.querySelector('.command-error').hidden && document.querySelector('.command-error').textContent.includes('测试启动失败')`)
	fail(false)
	composerText(t, s, "/command01")
	waitChatScript(t, s, `document.querySelectorAll('.command-option').length===1`)
	evaluate(t, s, `document.querySelector('.command-option').click();true`)
	composerText(t, s, "/备份")
	waitChatScript(t, s, `!document.querySelector('.command-error').hidden && document.querySelector('.command-error').textContent.includes('测试启动失败')`)
	evaluate(t, s, `document.querySelector('#input').dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));true`)
	waitChatScript(t, s, `document.querySelector('#command-panel').hidden`)
	composerText(t, s, "")
	composerText(t, s, "/备份")
	waitChatScript(t, s, `!document.querySelector('.command-error').hidden && document.querySelector('.command-error').textContent.includes('测试启动失败')`)
	evaluate(t, s, `document.querySelector('.command-option').click();true`)
	waitChatScript(t, s, `document.querySelector('.command-error').hidden && document.querySelector('.command-error').textContent===''`)
	if got := evaluate(t, s, `!document.querySelector('#command-panel').textContent.includes('已启动')`); got != "true" {
		t.Fatal("success displayed feedback")
	}
}
