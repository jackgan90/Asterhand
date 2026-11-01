import test from 'node:test';
import assert from 'node:assert/strict';
import {ComposerLayout, compactRows} from '../internal/quickinput/assets/composer-layout.mjs';
import {composerFixture} from './composer-layout-fixture.mjs';

function harness(t, extraRows = 0, applyResize = true) {
  const dom = composerFixture(), heights = [], requests = [];
  globalThis.document = dom.document;
  globalThis.window = dom.view;
  const layout = new ComposerLayout(dom.input, (height, id) => {
    heights.push(height); requests.push({height, id});
    if (applyResize) {
      dom.view.innerHeight = height;
      layout.applied(id, height);
    }
  }, {extraRows});
  t.after(() => { layout.dispose(); delete globalThis.document; delete globalThis.window; });
  return {...dom, heights, requests, layout};
}

test('fit and spare-line strategies grow and shrink from current rows, with an empty one-line minimum', () => {
  for (const extra of [0, 1]) {
    assert.equal(compactRows('', 1, extra), 1);
    for (const rows of [1, 2, 9, 10, 11, 9, 2, 1]) {
      assert.equal(compactRows('text', rows, extra), Math.min(10, rows + extra));
    }
  }
});

test('layout grows and shrinks, deduplicates resize and keeps desired height separate from viewport', t => {
  const h = harness(t);
  h.layout.flush();
  const empty = h.heights.at(-1);
  assert.equal(h.input.style.height, '45px');
  h.input.value = 'long text'; h.setRows(11); h.layout.flush();
  assert.equal(h.input.style.height, '297px');
  const ten = h.heights.at(-1);
  h.view.innerHeight = 170; h.layout.flush();
  assert.equal(h.heights.at(-1), ten);
  assert.ok(Number.parseFloat(h.input.style.maxHeight) < 297);
  h.setRows(2); h.layout.flush();
  assert.equal(h.input.style.height, '73px');
  h.input.value = ''; h.layout.flush();
  assert.equal(h.heights.at(-1), empty);
  const count = h.heights.length;
  h.layout.flush();
  assert.equal(h.heights.length, count);
});

test('expanded layout removes compact overrides and auxiliary controls change only compact window height', t => {
  const h = harness(t);
  h.layout.flush();
  const empty = h.heights.at(-1);
  h.select('#chat-draft-decision').hidden = false;
  h.layout.flush();
  assert.equal(h.heights.at(-1), empty + 30);
  h.layout.setExpanded(true);
  assert.equal(h.heights.at(-1), 760);
  assert.equal(h.input.style.height, undefined);
  assert.equal(h.input.style.maxHeight, undefined);
  assert.equal(h.input.style.overflowY, undefined);
  h.input.value = 'draft'; h.setRows(4); h.layout.flush();
  assert.equal(h.heights.at(-1), 760);
  h.layout.setExpanded(false);
  assert.equal(h.input.style.height, '129px');
});

test('growth hides transient overflow until matching native viewport confirmation, while real limits remain scrollable', t => {
  const h = harness(t, 0, false);
  h.layout.flush();
  const first = h.requests.at(-1);
  h.view.innerHeight = first.height;
  h.input.value = 'wrap'; h.setRows(2); h.layout.flush();
  const second = h.requests.at(-1);
  assert.equal(h.input.style.overflowY, 'hidden');
  h.layout.applied(first.id, first.height); h.layout.flush();
  assert.equal(h.input.style.overflowY, 'hidden');
  h.layout.applied(second.id, second.height); h.layout.flush();
  assert.equal(h.input.style.overflowY, 'hidden'); // Renderer still has the old viewport.
  h.view.innerHeight = second.height; h.layout.flush();
  assert.equal(h.input.style.overflowY, 'hidden');
  h.view.innerHeight = first.height;
  h.layout.applied(second.id, first.height); h.layout.flush();
  assert.equal(h.input.style.overflowY, 'auto'); // Confirmed work-area constraint.
  h.setRows(3); h.layout.flush();
  const third = h.requests.at(-1);
  assert.equal(h.input.style.overflowY, 'auto'); // Existing limit survives a new request.
  h.layout.applied(third.id, third.height); h.layout.flush();
  assert.equal(h.input.style.overflowY, 'auto'); // New allocation has not reached the renderer yet.
  h.view.innerHeight = third.height; h.layout.flush();
  assert.equal(h.input.style.overflowY, 'hidden');
  h.input.value = 'overflow'; h.setRows(11); h.layout.flush();
  assert.equal(h.input.style.overflowY, 'auto');
  h.input.value = ''; h.setRows(1); h.layout.flush();
  assert.equal(h.input.style.overflowY, 'hidden');
});

test('visible work is coalesced, hidden work is cancelled, disposal releases observers and the private mirror', async t => {
  const h = harness(t, 1);
  h.input.value = 'draft'; h.setRows(2);
  h.layout.setVisible(true);
  await Promise.resolve();
  h.flushFrames();
  assert.equal(h.input.style.height, '101px');
  h.layout.schedule(); h.layout.schedule(true);
  assert.equal(h.frames.size, 1);
  h.input.scrollTop = 37;
  h.flushFrames();
  assert.equal(h.input.scrollTop, 37);
  h.layout.schedule(); h.layout.setVisible(false);
  assert.equal(h.frames.size, 0);
  assert.ok(h.observers.every(observer => observer.targets.length === 0));
  h.setRows(1); h.layout.schedule();
  assert.equal(h.frames.size, 0);
  h.layout.setVisible(true);
  assert.equal(h.input.style.height, '73px');
  h.layout.dispose(); h.layout.schedule(); h.layout.flush();
  assert.equal(h.document.body.children.length, 0);
  assert.equal(h.frames.size, 0);
  assert.ok(h.observers.every(observer => observer.targets.length === 0));
});

test('only focused end edits follow full content; middle edits and passive updates retain the current viewport', t => {
  const h = harness(t);
  h.input.value = 'full words after the caret'; h.setRows(20);
  h.input.scrollHeight = 800;
  h.document.activeElement = h.input;
  h.layout.setVisible(true);
  h.input.selectionStart = h.input.selectionEnd = 3;
  h.input.scrollTop = 57;
  h.layout.schedule(true); h.flushFrames();
  assert.equal(h.input.scrollTop, 57);
  h.input.selectionStart = h.input.selectionEnd = h.input.value.length;
  h.layout.schedule(true); h.flushFrames();
  assert.equal(h.input.scrollTop, 800);
  h.input.scrollTop = 57;
  h.layout.schedule(); h.flushFrames();
  assert.equal(h.input.scrollTop, 57);
});

test('end follow waits for growing viewport, anchors late scrolling and resumes on a real height limit', t => {
  const h = harness(t, 0, false);
  h.layout.flush();
  h.view.innerHeight = h.requests.at(-1).height;
  h.layout.setVisible(true);
  h.document.activeElement = h.input;
  h.input.value = 'new row'; h.setRows(2);
  h.input.selectionStart = h.input.selectionEnd = h.input.value.length;
  h.input.scrollHeight = 73; h.input.scrollTop = 28;
  h.layout.schedule(true); h.flushFrames();
  assert.equal(h.input.scrollTop, 0);
  h.input.scrollTop = 28; h.input.fire('scroll');
  assert.equal(h.input.scrollTop, 0);
  h.layout.setVisible(false);
  h.input.scrollTop = 28; h.input.fire('scroll');
  assert.equal(h.input.scrollTop, 28);
  h.layout.setVisible(true);
  assert.equal(h.input.scrollTop, 0);
  const request = h.requests.at(-1);
  h.layout.applied(request.id, h.view.innerHeight); h.flushFrames();
  assert.equal(h.input.scrollTop, 73); // Confirmed cap must still reveal the end.
  h.input.scrollTop = 17; h.input.fire('scroll');
  assert.equal(h.input.scrollTop, 17);
  h.layout.schedule(); h.flushFrames();
  assert.equal(h.input.scrollTop, 17);
});

test('an external viewport shrink does not restart a completed input growth', t => {
  const h = harness(t);
  h.document.activeElement = h.input;
  h.input.value = 'four rows'; h.setRows(4);
  h.input.selectionStart = h.input.selectionEnd = h.input.value.length;
  h.input.scrollHeight = 129;
  h.layout.setVisible(true);
  const request = h.requests.at(-1);
  h.layout.applied(request.id, request.height); h.flushFrames();
  h.view.innerHeight = 140;
  h.layout.schedule(true); h.flushFrames();
  assert.equal(h.input.scrollTop, 129);
});

test('an older shrink cannot settle a newer growth that temporarily fits the old large viewport', t => {
  const h = harness(t, 0, false);
  h.document.activeElement = h.input;
  const edit = (text, rows, scrollHeight) => {
    h.input.value = text; h.setRows(rows); h.input.scrollHeight = scrollHeight;
    h.input.selectionStart = h.input.selectionEnd = text.length;
    h.layout.schedule(true); h.flushFrames();
  };
  h.layout.setVisible(true);
  edit('long text', 20, 800);
  let request = h.requests.at(-1);
  h.view.innerHeight = request.height;
  h.layout.applied(request.id, request.height); h.flushFrames();
  edit('', 1, 45);
  const shrink = h.requests.at(-1);
  edit('new long text', 20, 800);
  const growth = h.requests.at(-1);
  h.view.innerHeight = shrink.height;
  h.layout.applied(shrink.id, shrink.height);
  h.layout.schedule(); h.flushFrames();
  assert.equal(h.input.scrollTop, 0);
  h.layout.applied(growth.id, growth.height); h.flushFrames();
  assert.equal(h.input.scrollTop, 0);
  h.view.innerHeight = growth.height;
  h.layout.schedule(); h.flushFrames();
  assert.equal(h.input.scrollTop, 800);
});
