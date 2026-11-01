// Uses the native resize hold/release hook from compact-scrollbar-regression.js.
(() => {
  const probe = window.__scrollbarProbe;
  const input = document.querySelector('#input'), list = document.querySelector('#command-list');
  const panel = document.querySelector('#command-panel');
  const sample = () => {
    const rect = input.getBoundingClientRect();
    return {...probe.sample(), inputTop:rect.top, inputBottom:rect.bottom, editorHeight:rect.height,
      listBar:list.scrollHeight > list.clientHeight && ['auto', 'scroll'].includes(getComputedStyle(list).overflowY),
      listHeight:list.clientHeight, count:list.children.length};
  };
  const edit = text => {
    input.value = text; input.setSelectionRange(text.length, text.length);
    input.dispatchEvent(new Event('input'));
  };
  window.__commandScrollProbe = {sample, edit,
    async open(text, count) {
      edit('');
      await probe.wait(() => panel.hidden && innerHeight < 240);
      await probe.frames(3);
      input.focus();
      const before = sample();
      probe.hold();
      edit(text);
      await probe.wait(() => list.children.length === count && probe.queued().length > 0);
      const samples = [];
      for (let frame = 0; frame < 4; frame++) { await probe.frames(1); samples.push(sample()); }
      probe.release();
      await probe.wait(() => innerHeight > 240);
      for (let frame = 0; frame < 6; frame++) { await probe.frames(1); samples.push(sample()); }
      return {before, samples, flashes:samples.filter(value => value.outerBar || count <= 8 && value.listBar),
        jumps:samples.filter(value => Math.abs(value.inputBottom - before.inputBottom) > 1 ||
          Math.abs(value.editorHeight - before.editorHeight) > 1)};
    },
    async overlap() {
      const before = sample();
      probe.hold();
      edit('');
      await probe.wait(() => probe.queued().length === 1);
      edit('/group');
      await probe.wait(() => probe.queued().length === 2);
      probe.applyNext();
      await probe.wait(() => innerHeight < 240);
      const samples = [];
      for (let frame = 0; frame < 4; frame++) { await probe.frames(1); samples.push(sample()); }
      probe.release();
      await probe.wait(() => innerHeight > 300);
      for (let frame = 0; frame < 6; frame++) { await probe.frames(1); samples.push(sample()); }
      return {before, samples, flashes:samples.filter(value => value.outerBar || value.listBar),
        jumps:samples.filter(value => Math.abs(value.inputBottom - before.inputBottom) > 1 ||
          Math.abs(value.editorHeight - before.editorHeight) > 1)};
    },
  };
  return true;
})();
