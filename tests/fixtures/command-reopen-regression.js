(() => {
  const host = window.chrome.webview, original = host.postMessage.bind(host);
  const panel = document.querySelector('#command-panel'), footer = document.querySelector('footer');
  let capture = null, lastHeight = innerHeight;
  host.postMessage = message => {
    if (message.kind === 'resize') {
      lastHeight = message.height;
      if (capture && !capture.done) capture.requests.push({height: message.height, listHidden: panel.hidden});
    }
    original(message);
  };
  const sample = () => ({viewport: innerHeight, listHidden: panel.hidden,
    listBottom: panel.getBoundingClientRect().bottom, footerTop: footer.getBoundingClientRect().top,
    footerBottom: footer.getBoundingClientRect().bottom});
  const shown = event => {
    if (event.data.kind !== 'visibility' || !event.data.visible || !capture || capture.sampling) return;
    const current = capture;
    current.sampling = true;
    let remaining = 8;
    const frame = () => {
      if (capture !== current) return;
      current.frames.push(sample());
      if (--remaining) requestAnimationFrame(frame);
      else current.done = true;
    };
    requestAnimationFrame(frame);
  };
  host.addEventListener('message', shown);
  window.__commandReopenProbe = {
    arm() { capture = {viewport: innerHeight, height: lastHeight, requests: [], frames: [], done: false, sampling: false}; },
    done() { return capture?.done === true; },
    result() {
      const badRequests = capture.requests.filter(request => request.height !== capture.height);
      const badFrames = capture.frames.filter(frame => Math.abs(frame.viewport - capture.viewport) > 1 ||
        frame.listHidden || frame.footerBottom > frame.viewport + 1 || frame.listBottom > frame.footerTop + 1);
      return {ok: capture.done && !badRequests.length && !badFrames.length,
        viewport: capture.viewport, frameCount: capture.frames.length,
        minHeight: Math.min(...capture.frames.map(frame => frame.viewport)),
        maxHeight: Math.max(...capture.frames.map(frame => frame.viewport)),
        requests: capture.requests, badRequests, badFrames};
    },
    dispose() { host.postMessage = original; host.removeEventListener('message', shown); capture = null; },
  };
  return true;
})();
