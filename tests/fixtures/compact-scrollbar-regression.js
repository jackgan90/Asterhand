// Exercise actual frames while the native resize is deliberately held back.
(() => {
  const input = document.querySelector('#input'), main = document.querySelector('main');
  const host = window.chrome.webview, original = host.postMessage.bind(host);
  const frames = count => new Promise(resolve => {
    const next = () => --count > 0 ? requestAnimationFrame(next) : resolve();
    requestAnimationFrame(next);
  });
  let hold = false, queued = [];
  host.postMessage = message => {
    if (hold && message.kind === 'resize') queued.push(message);
    else original(message);
  };
  const sample = () => {
    const overflows = node => ['auto', 'scroll'].includes(getComputedStyle(node).overflowY) && node.scrollHeight > node.clientHeight;
    return {inputBar:overflows(input), outerBar:overflows(main), inputHeight:input.clientHeight,
      contentHeight:input.scrollHeight, viewport:innerHeight, scrollTop:input.scrollTop};
  };
  const wait = async predicate => {
    for (let index = 0; index < 180; index++) { if (predicate()) return; await frames(1); }
    throw Error('scrollbar fixture did not settle: ' + JSON.stringify(sample()));
  };
  const release = () => { hold = false; const pending = queued; queued = []; pending.forEach(message => original(message)); };
  window.__scrollbarProbe = {sample, frames, wait, release,
    hold() { hold = true; },
    queued() { return queued; },
    applyNext() { const message = queued.shift(); if (message) original(message); return message; },
    discard() { hold = false; queued = []; },
    dispose() { host.postMessage = original; },
    async growth({pauseAt = 0} = {}) {
      const line = parseFloat(getComputedStyle(input).lineHeight);
      let wrapAt = 0;
      for (let count = 1; count < 200; count++) {
        input.value = '中'.repeat(count);
        if (input.scrollHeight > input.clientHeight + line / 2) { wrapAt = count; break; }
      }
      if (!wrapAt) throw Error('could not find the real wrapping boundary');
      const samples = [];
      for (let rows = 1; rows < 10; rows++) {
        input.value = 'line\n'.repeat(rows - 1) + '中'.repeat(wrapAt - 1);
        input.dispatchEvent(new Event('input'));
        await wait(() => window.__fitsRows(rows));
        hold = true;
        input.focus(); input.setSelectionRange(input.value.length, input.value.length);
        document.execCommand('insertText', false, '中');
        for (let frame = 0; frame < 3; frame++) { await frames(1); samples.push({rows:rows + 1, frame, ...sample()}); }
        if (rows + 1 === pauseAt) { window.__wrapPaused = true; await wait(() => window.__wrapContinue); }
        release();
        await wait(() => window.__fitsRows(rows + 1));
        await frames(2);
        samples.push({rows:rows + 1, settled:true, ...sample()});
      }
      return {samples, flashes:samples.filter(value => value.inputBar || value.outerBar)};
    },
  };
  return true;
})();
