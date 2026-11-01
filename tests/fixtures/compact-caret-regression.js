// Test-only full-text DOM range is an independent reference for caret geometry.
// Product text measurement uses a textarea; this fixture never changes it.
(() => {
  const input = document.querySelector('#input'), style = getComputedStyle(input);
  const reference = document.createElement('div'), prefixMeasure = document.createElement('textarea');
  for (const node of [reference, prefixMeasure]) {
    for (const property of Array.from(style)) node.style.setProperty(property, style.getPropertyValue(property));
    Object.assign(node.style, {position:'fixed', left:'-20000px', top:'0', visibility:'hidden',
      height:'auto', minHeight:'0', maxHeight:'none', margin:'0', overflow:'visible'});
    node.setAttribute('aria-hidden', 'true');
    document.body.append(node);
  }
  reference.style.width = `${input.clientWidth}px`;
  prefixMeasure.style.width = `${input.getBoundingClientRect().width}px`;
  prefixMeasure.style.height = '0px';
  prefixMeasure.style.overflowY = 'auto';
  const line = parseFloat(style.lineHeight), paddingTop = parseFloat(style.paddingTop);
  const padding = paddingTop + parseFloat(style.paddingBottom);
  function geometry(text = input.value, position = input.selectionEnd) {
    reference.textContent = text;
    const range = document.createRange(), at = Math.min(position, text.length - 1);
    range.setStart(reference.firstChild, at); range.setEnd(reference.firstChild, at + 1);
    const row = 1 + Math.floor((range.getBoundingClientRect().top - reference.getBoundingClientRect().top - paddingTop) / line);
    const top = (row - 1) * line + paddingTop - input.scrollTop;
    return {row, top, bottom:top + line, scrollTop:input.scrollTop, client:input.clientHeight,
      visible:top >= -1 && top + line <= input.clientHeight + 1};
  }
  let candidate;
  for (let count = 1; count < 150; count++) {
    const prefix = 'intro\n'.repeat(15) + 'a '.repeat(count);
    const text = prefix + 'abcdefghijklmnop' + ' suffix words'.repeat(80), position = prefix.length + 2;
    prefixMeasure.value = text.slice(0, position);
    if (geometry(text, position).row > Math.round((prefixMeasure.scrollHeight - padding) / line)) {
      candidate = {text, position}; break;
    }
  }
  window.__caretProbe = {candidate, line, geometry,
    settle() {
      window.__caretSettled = false;
      requestAnimationFrame(() => requestAnimationFrame(() => { window.__caretAfter = geometry(); window.__caretSettled = true; }));
    },
    dispose() { reference.remove(); prefixMeasure.remove(); },
  };
  return Boolean(candidate);
})();
