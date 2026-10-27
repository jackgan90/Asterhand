(() => {
  const started = performance.now();
  const input = document.querySelector('#input');
  const panel = document.createElement('section');
  panel.style.cssText = 'height:320px;overflow:auto;white-space:pre-wrap';
  panel.setAttribute('aria-label', '聊天原型');
  const fragment = document.createDocumentFragment();
  const text = '中文 Markdown **示例**\n<script>window.__unsafe=true</script>\n' + 'x'.repeat(8000);
  for (let i = 0; i < 200; i++) {
    const message = document.createElement('article');
    const heading = document.createElement('strong');
    heading.textContent = i % 2 ? '助手' : '你';
    const body = document.createElement('pre');
    body.textContent = text;
    message.append(heading, body);
    fragment.append(message);
  }
  panel.append(fragment);
  input.before(panel);
  panel.scrollTop = panel.scrollHeight;
  const bottom = panel.scrollTop;
  panel.scrollTop = 0;
  const top = panel.scrollTop;
  panel.hidden = true;
  panel.hidden = false;
  document.documentElement.dataset.theme = 'frost';
  const elapsed = performance.now() - started;
  return {
    ok: panel.children.length === 200 && bottom > 0 && top === 0 && !window.__unsafe &&
      panel.querySelectorAll('script,img,iframe,a').length === 0 && document.querySelector('#input') === input,
    messages: panel.children.length,
    textBytes: new TextEncoder().encode(panel.textContent).length,
    renderMilliseconds: Math.round(elapsed),
  };
})()
