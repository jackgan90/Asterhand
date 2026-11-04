import {t, localizeError} from './i18n.mjs';
import {node} from './ui.mjs';
import {timeoutSeconds} from './command-run.mjs';
import {copyGraph, newGraph, edgeIssue, graphIssue, removeGraphNode, graphLimits, graphContent} from './graph-model.mjs';
import {graphCanvas, edgeKey, nodeWidth, clampCoordinate, coordinateLimit} from './graph-canvas.mjs';
import {graphInspector} from './graph-inspector.mjs';
import {graphMenu} from './graph-menu.mjs';
import {graphHistory} from './graph-history.mjs';

// Node-style script graph editor. It edits an in-memory copy; the command form
// reads it back through read(), which validates with the shared graph model.
export function graphEditor({container, onChange, browse}) {
  let graph = null, selection = null, locked = false, error = null, fullscreen = false;
  let editKey = null, epoch = 0, browseRequest = 0, browseAllowed = false;
  const history = graphHistory();
  const labels = [], viewControls = [];
  const text = (tag, key, className) => { const el = node(tag, t(key), className); labels.push([el, 'text', key]); return el; };
  const button = (key, className = 'quiet') => { const el = text('button', key, className); el.type = 'button'; return el; };
  const label = (key, control) => { const el = node('label', null, 'field'); el.append(text('span', key), control); return el; };
  // Icon-like view buttons keep their name in title and aria-label.
  const iconButton = (glyph, key, className) => {
    const el = node('button', glyph, `quiet graph-icon ${className}`); el.type = 'button';
    labels.push([el, 'title', key]); el.title = t(key); el.setAttribute('aria-label', t(key));
    viewControls.push(el); return el;
  };

  const toolbar = node('div', null, 'graph-toolbar');
  const add = button('web.graph.add', 'secondary graph-add');
  const undo = button('web.graph.undo', 'quiet graph-undo'), redo = button('web.graph.redo', 'quiet graph-redo');
  const fit = button('web.graph.fit', 'quiet graph-fit'); viewControls.push(fit);
  const zoomOut = iconButton('−', 'web.graph.zoom_out', 'graph-zoom-out'), zoomLevel = node('span', '100%', 'graph-zoom-level');
  const zoomIn = iconButton('+', 'web.graph.zoom_in', 'graph-zoom-in');
  const expand = button('web.graph.fullscreen', 'quiet graph-fullscreen'); viewControls.push(expand);
  const parallel = node('input', null, 'graph-parallel');
  parallel.type = 'number'; parallel.min = '1'; parallel.max = String(graphLimits.parallelism);
  const parallelField = node('label', null, 'graph-parallel-field'); parallelField.append(text('span', 'web.graph.parallelism'), parallel);
  const spacer = node('span', null, 'graph-spacer');
  toolbar.append(add, undo, redo, fit, zoomOut, zoomLevel, zoomIn, expand, spacer, parallelField);

  const canvas = graphCanvas({
    select: next => select(next),
    beginMove: (id, source) => beginEdit(`${source}:${id}`, 'web.graph.action_move'),
    endMove: () => { if (typeof editKey === 'string' && /^(pointer|keyboard):/.test(editKey)) finishEdit(); },
    cancelMove: () => { if (typeof editKey === 'string' && editKey.startsWith('pointer:')) cancelEdit(); },
    move: (id, x, y) => { const n = find(id); if (n && (n.x !== x || n.y !== y)) { n.x = x; n.y = y; changed(); } },
    canConnect: (from, to) => !edgeIssue(graph, from, to),
    connect: (from, to) => edit('web.graph.action_connect', () => { graph.edges.push({from, to}); selection = {kind: 'edge', id: edgeKey({from, to})}; }),
    rename: (id, name) => { const n = find(id); if (n && n.name !== name) edit('web.graph.rename', () => { n.name = name; inspector.show(n, true); }); },
    add: point => addStep(point),
    menu: (target, at) => openMenu(target, at),
    viewChanged: zoom => { zoomLevel.textContent = `${Math.round(zoom * 100)}%`; },
  });
  const inspector = graphInspector({text, button, label,
    onEdit: (field, value, input) => {
      const n = selectedStep();
      if (n && !locked) { beginEdit(input ?? field, 'web.graph.action_properties'); n[field] = value; changed(false); }
    },
    onFinish: () => finishEdit(),
    onBrowse: () => chooseScript(selectedStep()?.id),
  });
  const body = node('div', null, 'graph-body');
  body.append(canvas.element, inspector.element);
  const hint = text('p', 'web.graph.hint', 'graph-hint'), message = node('p', null, 'graph-error');
  message.setAttribute('role', 'status');
  container.classList?.add('graph-editor');
  container.append(toolbar, body, hint, message);
  const menu = graphMenu(body);

  const find = id => graph?.nodes.find(n => n.id === id);
  const selectedStep = () => selection?.kind === 'node' ? find(selection.id) : null;
  const snapshot = () => ({graph, selection});
  function beginEdit(key, action) {
    if (editKey === key) return;
    finishEdit(); history.begin(snapshot(), action); editKey = key;
  }
  function finishEdit() {
    if (editKey === null) return;
    history.commit(snapshot()); editKey = null; paintHistory();
  }
  function restore(value) {
    if (!value) return;
    graph = value.graph; selection = value.selection;
    if (selection?.kind === 'node' && !find(selection.id) || selection?.kind === 'edge' && !graph.edges.some(edge => edgeKey(edge) === selection.id)) selection = null;
    parallel.value = graph.parallelism;
    inspector.show(selectedStep(), true);
    changed();
  }
  function cancelEdit() { const before = history.cancel(); editKey = null; restore(before); }
  function edit(action, apply) {
    if (locked) return;
    finishEdit(); history.begin(snapshot(), action); apply(); history.commit(snapshot()); changed();
  }
  function paintHistory() {
    const state = history.state(graph);
    for (const [control, action, shortcut] of [[undo, state.undo, 'Ctrl+Z'], [redo, state.redo, 'Ctrl+Y / Ctrl+Shift+Z']]) {
      control.disabled = locked || !action;
      const title = action ? t(control === undo ? 'web.graph.undo_action' : 'web.graph.redo_action', {action: t(action), shortcut}) : `${control.textContent} (${shortcut})`;
      control.title = title; control.setAttribute('aria-label', title);
    }
    if (!error) message.textContent = state.limited ? t('web.graph.history_limited') : '';
  }
  function travel(direction) {
    if (locked) return;
    canvas.finishRename(); canvas.cancelInteraction(); menu.close(); finishEdit();
    restore(history[direction]()); paintHistory();
    if (selection?.kind === 'node') canvas.focusNode(selection.id); else canvas.element.focus?.();
  }
  function finishEditing() { canvas.finishRename(); canvas.cancelInteraction(); finishEdit(); }
  function changed(repaint = true) {
    onChange(); error = null;
    if (repaint) paint(); else { canvas.render(graph, selection, locked); paintHistory(); }
  }
  function paint() {
    if (!graph) return;
    canvas.render(graph, selection, locked);
    inspector.show(selectedStep());
    message.textContent = error ? localizeError(error) : '';
    applyLock();
  }
  function select(next) {
    finishEdit();
    if (next?.kind === 'node' && !find(next.id)) next = null;
    selection = next;
    paint();
  }
  function addStep(point) {
    if (locked || !graph || graph.nodes.length >= graphLimits.nodes) return;
    const at = point ?? canvas.center();
    const n = {id: globalThis.crypto.randomUUID(), kind: 'script', name: t('web.graph.default_name', {index: graph.nodes.filter(item => item.kind === 'script').length + 1}),
      script: '', arguments: [], directory: '', failure_policy: 'abort',
      x: clampCoordinate(at.x - nodeWidth({kind: 'script'}) / 2), y: clampCoordinate(at.y - 32)};
    edit('web.graph.add', () => { graph.nodes.push(n); selection = {kind: 'node', id: n.id}; });
    canvas.focusNode(n.id);
  }
  function removeSelection() {
    if (locked || !selection) return;
    canvas.cancelInteraction();
    if (selection.kind === 'edge') {
      const index = graph.edges.findIndex(edge => edgeKey(edge) === selection.id);
      if (index >= 0) edit('web.graph.disconnect', () => { graph.edges.splice(index, 1); selection = null; });
    } else if (selectedStep()?.kind === 'script') edit('web.graph.remove', () => { removeGraphNode(graph, selection.id); selection = null; });
    canvas.element.focus?.();
  }
  function chooseScript(id) {
    if (locked || find(id)?.kind !== 'script') return;
    finishEdit();
    const owner = epoch, request = ++browseRequest;
    browse(path => {
      if (owner !== epoch || request !== browseRequest || locked && !browseAllowed) return;
      const n = find(id); if (!n || n.script === path) return;
      history.begin(snapshot(), 'web.graph.choose_script'); n.script = path; history.commit(snapshot());
      if (selectedStep()?.id === id) inspector.setScript(path);
      changed();
    });
  }
  function openMenu(target, at) {
    const items = [];
    if (target.kind === 'canvas') {
      items.push({label: t('web.graph.add'), disabled: locked || graph.nodes.length >= graphLimits.nodes, action: () => addStep(target.point)});
      items.push({label: t('web.graph.fit'), action: () => canvas.fit()});
    } else if (target.kind === 'node' && find(target.id)?.kind === 'script') {
      items.push({label: t('web.graph.rename'), disabled: locked, action: () => canvas.startRename(target.id)});
      items.push({label: t('web.graph.choose_script'), disabled: locked, action: () => chooseScript(target.id)});
      items.push({label: t('web.graph.remove'), disabled: locked, danger: true, action: removeSelection});
    } else if (target.kind === 'edge') {
      items.push({label: t('web.graph.disconnect'), disabled: locked, danger: true, action: removeSelection});
    }
    menu.show(at.x, at.y, items);
  }
  // Only the export is translated into 0-8000; draft coordinates and view stay.
  function layoutShift(candidate) {
    const xs = candidate.nodes.map(n => n.x), ys = candidate.nodes.map(n => n.y);
    if (Math.max(...xs) - Math.min(...xs) > coordinateLimit || Math.max(...ys) - Math.min(...ys) > coordinateLimit) {
      return {issue: {messageKey: 'err.graph.layout_too_large', messageArgs: {}}};
    }
    // With the span in range, at most one end can be outside 0..8000.
    const shift = values => {
      const low = Math.min(...values), high = Math.max(...values);
      return low < 0 ? -low : high > coordinateLimit ? coordinateLimit - high : 0;
    };
    return {x: shift(xs), y: shift(ys)};
  }
  function setFullscreen(value) {
    fullscreen = value;
    container.dataset.fullscreen = String(value);
    labels.find(entry => entry[0] === expand)[2] = value ? 'web.graph.exit_fullscreen' : 'web.graph.fullscreen';
    expand.textContent = t(value ? 'web.graph.exit_fullscreen' : 'web.graph.fullscreen');
    globalThis.requestAnimationFrame?.(() => canvas.fit());
  }
  function applyLock() {
    // Menu items manage their own state; view controls stay usable while locked.
    for (const control of container.querySelectorAll('button,input,select,textarea')) {
      if (!viewControls.includes(control) && !String(control.className).includes('graph-menu-item')) control.disabled = locked;
    }
    for (const control of viewControls) control.disabled = false;
    add.disabled = locked || (graph?.nodes.length ?? 0) >= graphLimits.nodes;
    inspector.setLocked(locked);
    paintHistory();
  }

  add.onclick = () => addStep();
  undo.onclick = () => travel('undo');
  redo.onclick = () => travel('redo');
  fit.onclick = () => canvas.fit();
  zoomIn.onclick = () => canvas.zoomBy(1.2);
  zoomOut.onclick = () => canvas.zoomBy(1 / 1.2);
  expand.onclick = () => setFullscreen(!fullscreen);
  parallel.oninput = () => { if (!locked) { beginEdit(parallel, 'web.graph.action_parallelism'); graph.parallelism = Number(parallel.value); changed(false); } };
  parallel.addEventListener('blur', finishEdit);
  container.addEventListener('keydown', event => {
    const typing = ['INPUT', 'TEXTAREA', 'SELECT'].includes(event.target?.tagName) || event.target?.isContentEditable;
    if (event.isComposing || event.keyCode === 229) return;
    const key = event.key.toLowerCase();
    if (event.ctrlKey && !event.altKey && !event.metaKey && !typing && (key === 'z' || key === 'y' && !event.shiftKey)) {
      event.preventDefault(); event.stopPropagation(); travel(key === 'y' || event.shiftKey ? 'redo' : 'undo'); return;
    }
    if (event.key === 'Escape' && fullscreen && !menu.open) { event.preventDefault(); event.stopPropagation(); setFullscreen(false); return; }
    if ((event.key === 'Delete' || event.key === 'Backspace') && !typing && selection) { event.preventDefault(); removeSelection(); }
  });

  return {
    // An explicit load starts fresh history. Saving uses read(), not load().
    load(value, {keepView = false} = {}) {
      canvas.cancelInteraction();
      history.reset(); editKey = null; epoch++; browseRequest++;
      const keep = keepView ? selection : null;
      graph = value ? copyGraph(value) : newGraph();
      if (!value) graph.nodes[1].name = t('web.graph.default_name', {index: 1});
      error = null; menu.close();
      parallel.value = graph.parallelism;
      const first = graph.nodes.find(n => n.kind === 'script');
      const kept = keep?.kind === 'node' ? find(keep.id) : keep?.kind === 'edge' ? graph.edges.find(edge => edgeKey(edge) === keep.id) : null;
      selection = kept ? keep : first ? {kind: 'node', id: first.id} : null;
      inspector.show(null);
      paint();
      if (!keepView) globalThis.requestAnimationFrame?.(() => canvas.fit());
    },
    finishEditing,
    // Export normalization never mutates the draft or its editing history.
    read() {
      finishEditing();
      const candidate = copyGraph(graph);
      for (const n of candidate.nodes) if (n.kind === 'script' && n.timeout_seconds != null) n.timeout_seconds = timeoutSeconds(n.timeout_seconds, 1);
      const shift = layoutShift(candidate);
      if (shift.issue) { error = shift.issue; paint(); throw error; }
      for (const n of candidate.nodes) { n.x += shift.x; n.y += shift.y; }
      error = graphIssue(candidate);
      if (error) { paint(); throw error; }
      paint();
      return candidate;
    },
    // Used for dirty comparison without validation errors or ending input.
    comparison() {
      const candidate = copyGraph(graph), shift = layoutShift(candidate);
      if (!shift.issue) for (const n of candidate.nodes) { n.x += shift.x; n.y += shift.y; }
      return graphContent(candidate);
    },
    // Only the unlocked -> locked transition voids gestures; repeated status
    // refreshes while locked must not interrupt panning, which stays allowed.
    setLocked(value, {allowBrowse = false} = {}) {
      const entering = value && !locked;
      if (value && !allowBrowse) browseRequest++;
      browseAllowed = allowBrowse;
      if (entering) { canvas.cancelInteraction(); finishEdit(); menu.close(); }
      locked = value;
      paint(); applyLock();
    },
    cancelInteraction() {
      canvas.cancelInteraction(); finishEdit(); history.reset(); editKey = null;
      epoch++; browseRequest++; menu.close(); paintHistory();
    },
    relocalize() {
      for (const [el, kind, key] of labels) {
        if (kind === 'text') el.textContent = t(key);
        else { el.title = t(key); el.setAttribute('aria-label', t(key)); }
      }
      inspector.relocalize(); menu.close(); paint();
    },
  };
}
