import test from 'node:test';
import assert from 'node:assert/strict';
import {newGraph} from '../web/graph-model.mjs';
import {graphEditor} from '../web/graph-editor.mjs';
import {setLocale, t} from '../web/i18n.mjs';
import {domFixture, descendants} from './web-fixture.mjs';

function emit(element, type, props = {}) {
  const event = new Event(type, {cancelable: true});
  Object.assign(event, {button: 0, clientX: 0, clientY: 0, pointerId: 1, ...props});
  element.dispatchEvent(event);
  return event;
}
function setup(tcase, {locked = false} = {}) {
  const dom = domFixture(); globalThis.document = dom.document; tcase.after(() => delete globalThis.document);
  setLocale('zh-CN');
  const container = dom.document.createElement('section'); let changes = 0, browsed = 0;
  const editor = graphEditor({container, onChange: () => changes++, browse: apply => { browsed++; apply('D:\\Tools\\deploy.ps1'); }});
  const graph = newGraph(); Object.assign(graph.nodes[1], {name: 'compile', script: 'C:\\step.cmd'});
  editor.load(graph); if (locked) editor.setLocked(true);
  const all = () => descendants(container);
  const nodeEl = kindOrName => all().find(el => el.dataset.nodeId && (el.dataset.kind === kindOrName || el.children[0]?.textContent === kindOrName));
  const port = (el, side) => el.children.find(child => child.dataset.port === side);
  const menuItems = () => all().find(el => el.className === 'graph-menu').children;
  const viewport = all().find(el => el.className === 'graph-viewport');
  return {dom, container, editor, graph, all, nodeEl, port, menuItems, viewport, changes: () => changes, browsed: () => browsed};
}

test('canvas context menus add, rename, choose a script for and delete steps', tcase => {
  const h = setup(tcase);
  emit(h.viewport, 'contextmenu', {clientX: 300, clientY: 200});
  assert.deepEqual(h.menuItems().map(item => item.textContent), [t('web.graph.add'), t('web.graph.fit')]);
  h.menuItems()[0].onclick();
  const added = h.editor.read.bind(h.editor);
  assert.throws(added, 'a new unconnected step is reported by validation');
  const step = h.nodeEl(t('web.graph.default_name', {index: 2}));
  assert.ok(step, 'the new step is drawn on the canvas');
  emit(step, 'contextmenu');
  assert.deepEqual(h.menuItems().map(item => item.textContent), [t('web.graph.rename'), t('web.graph.choose_script'), t('web.graph.remove')]);
  h.menuItems()[1].onclick();
  assert.equal(h.browsed(), 1);
  assert.equal(step.children[1].textContent, 'deploy.ps1', 'the card shows the chosen script file name');
  emit(step, 'contextmenu'); h.menuItems()[2].onclick();
  assert.equal(h.nodeEl(t('web.graph.default_name', {index: 2})), undefined);
  assert.doesNotThrow(() => h.editor.read());
  emit(h.nodeEl('start'), 'contextmenu');
  assert.equal(h.menuItems().length, 0, 'start and end have no edit menu');
});

test('dragging from an output port connects only to valid targets', tcase => {
  const h = setup(tcase);
  emit(h.viewport, 'dblclick', {clientX: 400, clientY: 300});
  const extra = h.nodeEl(t('web.graph.default_name', {index: 2})), start = h.nodeEl('start'), compile = h.nodeEl('compile'), end = h.nodeEl('end');
  const drag = (from, to) => { emit(h.port(from, 'out'), 'pointerdown'); emit(to, 'pointerover'); const drop = to.dataset.drop; emit(h.dom.document, 'pointerup'); return drop; };
  assert.equal(drag(compile, start), 'no', 'connections into start are refused while dragging');
  assert.equal(drag(start, compile), 'no', 'duplicate connections are refused');
  assert.equal(drag(start, extra), 'ok');
  assert.equal(drag(extra, end), 'ok');
  assert.equal(drag(compile, extra), 'ok');
  assert.equal(drag(extra, compile), 'no', 'cycles are refused');
  emit(extra, 'contextmenu'); h.menuItems()[1].onclick(); // a script makes the step valid
  const graph = h.editor.read();
  assert.equal(graph.edges.length, 5);
  assert.equal(extra.dataset.drop, undefined, 'drop hints are cleared after the drag');
});

test('Delete removes the selected connection or step but never start or end', tcase => {
  const h = setup(tcase);
  const key = (el, k) => { const event = emit(h.container, 'keydown', {key: k}); return event.defaultPrevented; };
  const hits = () => h.all().filter(el => el.attributes?.class === 'graph-edge-hit');
  assert.equal(hits().length, 2);
  emit(hits()[0], 'click');
  key(h.container, 'Delete');
  assert.equal(hits().length, 1);
  emit(h.nodeEl('start'), 'click'); key(h.container, 'Delete');
  assert.ok(h.nodeEl('start'));
  emit(h.nodeEl('compile'), 'click'); key(h.container, 'Backspace');
  assert.equal(h.nodeEl('compile'), undefined);
  assert.equal(hits().length, 0, 'connections of a deleted step go with it');
});

test('double click renames in place; Enter commits and Escape cancels', tcase => {
  const h = setup(tcase);
  const compile = h.nodeEl('compile');
  emit(compile, 'dblclick');
  let input = compile.children.find(el => el.className === 'graph-rename');
  input.value = 'build';
  emit(input, 'keydown', {key: 'Escape'});
  assert.equal(h.editor.read().nodes[1].name, 'compile');
  emit(compile, 'dblclick');
  input = compile.children.find(el => el.className === 'graph-rename');
  input.value = 'build';
  emit(input, 'keydown', {key: 'Enter'});
  assert.equal(h.editor.read().nodes[1].name, 'build');
  assert.equal(compile.children[0].textContent, 'build');
  const name = h.all().find(el => el.tagName === 'INPUT' && el.value === 'build');
  assert.ok(name, 'the inspector shows the new name');
  emit(h.nodeEl('end'), 'dblclick');
  assert.equal(h.nodeEl('end').children.some(el => el.className === 'graph-rename'), false);
});

test('locked graphs can be viewed and selected but not changed', tcase => {
  const h = setup(tcase, {locked: true});
  const before = h.changes();
  emit(h.port(h.nodeEl('compile'), 'out'), 'pointerdown'); emit(h.nodeEl('end'), 'pointerover'); emit(h.dom.document, 'pointerup');
  emit(h.viewport, 'dblclick');
  emit(h.nodeEl('compile'), 'click'); emit(h.container, 'keydown', {key: 'Delete'});
  emit(h.nodeEl('compile'), 'contextmenu');
  assert.ok(h.menuItems().every(item => item.disabled));
  assert.equal(h.changes(), before);
  assert.equal(h.nodeEl('compile').getAttribute('aria-pressed'), 'true', 'selection still works');
  const zoomIn = h.all().find(el => el.className?.includes('graph-zoom-in'));
  assert.equal(zoomIn.disabled, false, 'view controls stay usable');
  const level = h.all().find(el => el.className === 'graph-zoom-level');
  for (let i = 0; i < 10; i++) zoomIn.onclick();
  assert.equal(level.textContent, '200%', 'zoom is clamped');
});

test('Escape leaves full screen without reaching the dialog, and relocalizes view labels', tcase => {
  const h = setup(tcase);
  const expand = h.all().find(el => el.tagName === 'BUTTON' && el.textContent === t('web.graph.fullscreen'));
  expand.onclick();
  assert.equal(h.container.dataset.fullscreen, 'true');
  assert.equal(expand.textContent, t('web.graph.exit_fullscreen'));
  const escape = emit(h.container, 'keydown', {key: 'Escape'});
  assert.equal(escape.defaultPrevented, true, 'the dialog must not see this Escape');
  assert.equal(h.container.dataset.fullscreen, 'false');
  setLocale('en-US'); h.editor.relocalize();
  assert.equal(expand.textContent, 'Full screen');
  assert.ok(h.all().some(el => el.title === 'Zoom in'));
  setLocale('zh-CN');
});

test('plain wheel keeps scrolling the page, Ctrl + wheel zooms, and saved data keeps its format', tcase => {
  const h = setup(tcase);
  const level = h.all().find(el => el.className === 'graph-zoom-level');
  const before = level.textContent;
  assert.equal(emit(h.viewport, 'wheel', {deltaY: 120}).defaultPrevented, false);
  assert.equal(level.textContent, before);
  assert.equal(emit(h.viewport, 'wheel', {deltaY: -400, ctrlKey: true}).defaultPrevented, true);
  assert.notEqual(level.textContent, before);
  const saved = h.editor.read();
  assert.deepEqual(Object.keys(saved).sort(), ['edges', 'nodes', 'parallelism']);
  assert.deepEqual(saved.nodes.map(n => Object.keys(n).sort()), h.graph.nodes.map(n => Object.keys(n).sort()));
  assert.deepEqual(saved.edges, h.graph.edges);
});

test('More settings stays as the user left it when switching steps', tcase => {
  const h = setup(tcase);
  emit(h.viewport, 'dblclick', {clientX: 400, clientY: 300});
  const compile = h.nodeEl('compile'), other = h.nodeEl(t('web.graph.default_name', {index: 2}));
  const more = h.all().find(el => el.className === 'graph-more'), summary = more.children[0];
  // A summary click toggles the details element after its click listeners run.
  const toggle = () => { emit(summary, 'click'); more.open = !more.open; };
  emit(compile, 'click');
  assert.equal(more.open, false);
  toggle();
  emit(other, 'click');
  assert.equal(more.open, true, 'an expanded section stays expanded on the next step');
  toggle();
  emit(compile, 'click');
  assert.equal(more.open, false, 'a collapsed section stays collapsed');
  const timeout = descendants(more).find(el => el.tagName === 'INPUT' && el.type === 'number');
  timeout.value = '30'; timeout.oninput();
  emit(other, 'click'); emit(compile, 'click');
  assert.equal(more.open, true, 'a step that uses these settings always shows them');
});

test('dragging after a save or reload starts from the current coordinates', tcase => {
  const h = setup(tcase);
  const drag = dx => {
    const el = h.nodeEl('compile');
    emit(el, 'pointerdown', {clientX: 0, clientY: 0});
    el.onpointermove({clientX: dx, clientY: 0}); el.onpointerup({});
    return h.editor.read().nodes.find(n => n.kind === 'script').x;
  };
  assert.equal(drag(40), 330);
  h.editor.load(h.editor.read()); // save and reload the same graph
  assert.equal(drag(40), 370);
  assert.equal(drag(40), 410, 'consecutive drags keep accumulating');
  const moved = h.editor.read(); moved.nodes.find(n => n.kind === 'script').x = 100;
  h.editor.load(moved); // same IDs, different coordinates
  assert.equal(drag(40), 140, 'the reused card must not jump back to stale coordinates');
});

test('a lock, a reload or pointer cancel during a gesture voids it', tcase => {
  const h = setup(tcase);
  emit(h.viewport, 'dblclick', {clientX: 400, clientY: 300});
  const extra = () => h.nodeEl(t('web.graph.default_name', {index: 2}));
  const preview = () => h.all().some(el => String(el.attributes?.class).includes('graph-edge-preview'));
  const edges = () => h.all().filter(el => el.attributes?.class === 'graph-edge-hit').length;
  // Lock while dragging a connection, then release.
  emit(h.port(h.nodeEl('start'), 'out'), 'pointerdown'); emit(extra(), 'pointerover');
  h.editor.setLocked(true);
  assert.equal(preview(), false, 'locking ends the connection gesture');
  emit(h.dom.document, 'pointerup');
  assert.equal(edges(), 2);
  h.editor.setLocked(false);
  // Pointer cancel clears the preview; a later release commits nothing.
  emit(h.port(h.nodeEl('start'), 'out'), 'pointerdown'); emit(extra(), 'pointerover');
  emit(h.dom.document, 'pointercancel');
  assert.equal(preview(), false);
  assert.equal(h.viewport.dataset.connecting, undefined);
  emit(h.dom.document, 'pointerup');
  assert.equal(edges(), 2);
  // Reloading another graph during a connection voids it.
  emit(h.port(h.nodeEl('start'), 'out'), 'pointerdown'); emit(h.nodeEl('end'), 'pointerover');
  const reloaded = newGraph(); Object.assign(reloaded.nodes[1], {name: 'other', script: 'C:\\other.cmd'});
  h.editor.load(reloaded);
  emit(h.dom.document, 'pointerup');
  assert.equal(h.editor.read().edges.length, 2);
  // Locking during a node drag stops further movement.
  const card = h.nodeEl('other'), x = h.editor.read().nodes[1].x;
  emit(card, 'pointerdown', {clientX: 0, clientY: 0});
  h.editor.setLocked(true);
  card.onpointermove?.({clientX: 80, clientY: 0});
  assert.equal(h.editor.read().nodes[1].x, x);
  assert.equal(card.onpointermove, null, 'the drag handlers were released');
});

test('IME composition keeps the inline rename open until the candidate is confirmed', tcase => {
  const h = setup(tcase);
  const compile = h.nodeEl('compile');
  const input = () => compile.children.find(el => el.className === 'graph-rename');
  emit(compile, 'dblclick');
  input().value = 'zhong';
  emit(input(), 'compositionstart');
  const composingEnter = emit(input(), 'keydown', {key: 'Enter', isComposing: true});
  assert.ok(input(), 'Enter that confirms a candidate keeps editing');
  assert.equal(composingEnter.defaultPrevented, false, 'the input method handles that Enter');
  emit(input(), 'keydown', {key: 'Escape', keyCode: 229});
  assert.ok(input(), 'Escape inside a composition keeps editing');
  input().value = '中文'; emit(input(), 'compositionend');
  emit(input(), 'keydown', {key: 'Enter'});
  assert.equal(input(), undefined);
  assert.equal(h.editor.read().nodes[1].name, '中文');
  emit(compile, 'dblclick'); input().value = 'discard';
  emit(input(), 'keydown', {key: 'Escape'});
  assert.equal(h.editor.read().nodes[1].name, '中文', 'plain Escape cancels');
});

test('repeated lock refreshes keep an allowed pan going; entering the lock or closing still cancels', tcase => {
  const h = setup(tcase, {locked: true});
  const stage = h.all().find(el => el.className === 'graph-stage');
  emit(h.viewport, 'pointerdown', {clientX: 0, clientY: 0});
  h.viewport.onpointermove({clientX: 30, clientY: 20});
  h.editor.setLocked(true); // background status refresh while already locked
  h.viewport.onpointermove({clientX: 90, clientY: 20});
  assert.match(stage.style.transform, /translate\(90px, 20px\)/, 'the pan keeps following the pointer');
  h.viewport.onpointerup({});
  // Closing the editor ends an ongoing pan.
  emit(h.viewport, 'pointerdown', {clientX: 0, clientY: 0});
  h.editor.cancelInteraction();
  assert.equal(h.viewport.onpointermove, null);
  // Entering the lock from unlocked still voids a node drag.
  h.editor.setLocked(false);
  const card = h.nodeEl('compile'), x = h.editor.read().nodes[1].x;
  emit(card, 'pointerdown', {clientX: 0, clientY: 0});
  h.editor.setLocked(true);
  assert.equal(card.onpointermove, null);
  assert.equal(h.editor.read().nodes[1].x, x);
});

test('steps can be dragged above or left of the origin and are saved back in range', tcase => {
  const h = setup(tcase);
  const card = h.nodeEl('compile'), stage = h.all().find(el => el.className === 'graph-stage');
  emit(card, 'pointerdown', {clientX: 0, clientY: 0});
  card.onpointermove({clientX: -400, clientY: -250}); card.onpointerup({});
  assert.equal(card.style.top, '-150px', 'dragging upward is not stopped at the origin');
  const saved = h.editor.read();
  const step = saved.nodes.find(n => n.kind === 'script'), start = saved.nodes.find(n => n.kind === 'start');
  assert.deepEqual([step.x, step.y], [0, 0], 'the layout is shifted so every coordinate is valid');
  assert.deepEqual([start.x - step.x, start.y - step.y], [40 + 110, 100 + 150], 'relative positions are kept');
  assert.match(stage.style.transform, /translate\(-110px, -150px\)/, 'the view moves with the shift, so nothing jumps');
  assert.ok(saved.nodes.every(n => n.x >= 0 && n.y >= 0 && n.x <= 8000 && n.y <= 8000));
});

function spreadGraph(axis, values) {
  const graph = newGraph();
  Object.assign(graph.nodes[1], {name: 'compile', script: 'C:\\step.cmd'});
  graph.nodes.forEach((n, i) => { n[axis] = values[i]; });
  return graph;
}
for (const axis of ['x', 'y']) test(`layouts wider than the saved range are refused without squeezing (${axis})`, tcase => {
  const h = setup(tcase);
  const stage = h.all().find(el => el.className === 'graph-stage');
  const nudge = (kind, delta) => {
    const card = h.nodeEl(kind);
    emit(card, 'pointerdown', {clientX: 0, clientY: 0});
    card.onpointermove(axis === 'x' ? {clientX: delta, clientY: 0} : {clientX: 0, clientY: delta}); card.onpointerup({});
  };
  // Span exactly 8000 after moving past the origin: shifted as a whole.
  h.editor.load(spreadGraph(axis, [0, 7800, 7900]));
  nudge('start', -100);
  assert.deepEqual(h.editor.read().nodes.map(n => n[axis]), [0, 7900, 8000]);
  // Span above 8000: refused, draft and view untouched.
  h.editor.load(spreadGraph(axis, [0, 7900, 8000]));
  nudge('start', -100);
  const view = stage.style.transform, position = h.nodeEl('start').style[axis === 'x' ? 'left' : 'top'];
  assert.throws(() => h.editor.read(), error => error.messageKey === 'err.graph.layout_too_large');
  assert.equal(stage.style.transform, view);
  assert.equal(h.nodeEl('start').style[axis === 'x' ? 'left' : 'top'], position);
  assert.ok(h.all().find(el => el.className === 'graph-error').textContent.includes('8000'));
  nudge('start', 100); // move back into range: saving works and nothing was squeezed
  assert.deepEqual(h.editor.read().nodes.map(n => n[axis]), [0, 7900, 8000]);
});

test('a later validation failure leaves negative coordinates and the view untouched', tcase => {
  const h = setup(tcase);
  const card = h.nodeEl('compile'), stage = h.all().find(el => el.className === 'graph-stage');
  emit(card, 'pointerdown', {clientX: 0, clientY: 0});
  card.onpointermove({clientX: -400, clientY: -250}); card.onpointerup({});
  const more = h.all().find(el => el.className === 'graph-more');
  const timeout = descendants(more).find(el => el.tagName === 'INPUT' && el.type === 'number');
  timeout.value = '10000000000'; timeout.oninput();
  const view = stage.style.transform;
  assert.throws(() => h.editor.read(), error => error.messageKey === 'err.localcmd.invalid_timeout');
  assert.equal(stage.style.transform, view, 'the view did not move');
  assert.deepEqual([card.style.left, card.style.top], ['-110px', '-150px'], 'the draft keeps its coordinates');
  timeout.value = '30'; timeout.oninput();
  const saved = h.editor.read();
  assert.deepEqual([saved.nodes[1].x, saved.nodes[1].y], [0, 0]);
  assert.deepEqual([card.style.left, card.style.top], ['0px', '0px'], 'nodes and view move together');
  assert.match(stage.style.transform, /translate\(-110px, -150px\)/);
});

test('reloading after a save keeps the view and selection; opening a command fits it', tcase => {
  globalThis.requestAnimationFrame = callback => callback();
  tcase.after(() => delete globalThis.requestAnimationFrame);
  const h = setup(tcase);
  const stage = h.all().find(el => el.className === 'graph-stage');
  h.all().find(el => el.className?.includes('graph-zoom-in')).onclick();
  emit(h.nodeEl('end'), 'click');
  const view = stage.style.transform;
  h.editor.load(h.editor.read(), {keepView: true});
  assert.equal(stage.style.transform, view, 'no automatic fit after saving the same command');
  assert.equal(h.nodeEl('end').getAttribute('aria-pressed'), 'true');
  h.editor.load(h.editor.read());
  assert.notEqual(stage.style.transform, view, 'opening a command fits the view');
});
