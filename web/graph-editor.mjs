import {t, localizeError} from './i18n.mjs';
import {node} from './ui.mjs';
import {timeoutSeconds} from './command-run.mjs';
import {copyGraph, newGraph, edgeIssue, graphIssue, removeGraphNode, graphLimits} from './graph-model.mjs';
import {graphCanvas, edgeKey, nodeWidth, clampCoordinate, coordinateLimit} from './graph-canvas.mjs';
import {graphInspector} from './graph-inspector.mjs';
import {graphMenu} from './graph-menu.mjs';

// Node-style script graph editor. It edits an in-memory copy; the command form
// reads it back through read(), which validates with the shared graph model.
export function graphEditor({container, onChange, browse}) {
  let graph = null, selection = null, locked = false, error = null, fullscreen = false;
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
  const fit = button('web.graph.fit', 'quiet graph-fit'); viewControls.push(fit);
  const zoomOut = iconButton('−', 'web.graph.zoom_out', 'graph-zoom-out'), zoomLevel = node('span', '100%', 'graph-zoom-level');
  const zoomIn = iconButton('+', 'web.graph.zoom_in', 'graph-zoom-in');
  const expand = button('web.graph.fullscreen', 'quiet graph-fullscreen'); viewControls.push(expand);
  const parallel = node('input', null, 'graph-parallel');
  parallel.type = 'number'; parallel.min = '1'; parallel.max = String(graphLimits.parallelism);
  const parallelField = node('label', null, 'graph-parallel-field'); parallelField.append(text('span', 'web.graph.parallelism'), parallel);
  const spacer = node('span', null, 'graph-spacer');
  toolbar.append(add, fit, zoomOut, zoomLevel, zoomIn, expand, spacer, parallelField);

  const canvas = graphCanvas({
    select: next => select(next),
    move: (id, x, y) => { const n = find(id); if (n && (n.x !== x || n.y !== y)) { n.x = x; n.y = y; changed(); } },
    canConnect: (from, to) => !edgeIssue(graph, from, to),
    connect: (from, to) => { graph.edges.push({from, to}); select({kind: 'edge', id: edgeKey({from, to})}); changed(); },
    rename: (id, name) => { const n = find(id); if (n && n.name !== name) { n.name = name; inspector.show(n, true); changed(); } },
    add: point => addStep(point),
    menu: (target, at) => openMenu(target, at),
    viewChanged: zoom => { zoomLevel.textContent = `${Math.round(zoom * 100)}%`; },
  });
  const inspector = graphInspector({text, button, label,
    onEdit: (field, value) => { const n = selectedStep(); if (n && !locked) { n[field] = value; changed(false); } },
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
  function changed(repaint = true) { onChange(); error = null; if (repaint) paint(); else { canvas.render(graph, selection, locked); message.textContent = ''; } }
  function paint() {
    if (!graph) return;
    canvas.render(graph, selection, locked);
    inspector.show(selectedStep());
    message.textContent = error ? localizeError(error) : '';
    applyLock();
  }
  function select(next) {
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
    graph.nodes.push(n);
    selection = {kind: 'node', id: n.id};
    changed();
  }
  function removeSelection() {
    if (locked || !selection) return;
    if (selection.kind === 'edge') {
      const index = graph.edges.findIndex(edge => edgeKey(edge) === selection.id);
      if (index >= 0) { graph.edges.splice(index, 1); selection = null; changed(); }
    } else if (removeGraphNode(graph, selection.id)) { selection = null; changed(); }
  }
  function chooseScript(id) {
    if (locked || find(id)?.kind !== 'script') return;
    browse(path => {
      const n = find(id); if (!n) return;
      n.script = path;
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
  // Saved coordinates must be 0-8000 on each axis. A layout that was moved
  // above or left of the origin is translated back as a whole, never squeezed;
  // a layout wider or taller than the saved range cannot be saved.
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
  }  function setFullscreen(value) {
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
  }

  add.onclick = () => addStep();
  fit.onclick = () => canvas.fit();
  zoomIn.onclick = () => canvas.zoomBy(1.2);
  zoomOut.onclick = () => canvas.zoomBy(1 / 1.2);
  expand.onclick = () => setFullscreen(!fullscreen);
  parallel.oninput = () => { if (!locked) { graph.parallelism = Number(parallel.value); onChange(); } };
  container.addEventListener('keydown', event => {
    const typing = ['INPUT', 'TEXTAREA', 'SELECT'].includes(event.target?.tagName);
    if (event.key === 'Escape' && fullscreen && !menu.open) { event.preventDefault(); event.stopPropagation(); setFullscreen(false); return; }
    if ((event.key === 'Delete' || event.key === 'Backspace') && !typing && selection) { event.preventDefault(); removeSelection(); }
  });

  return {
    // keepView: the same command was just saved, so the user's pan, zoom and
    // selection stay; opening or switching commands fits the view.
    load(value, {keepView = false} = {}) {
      canvas.cancelInteraction();
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
    // Validates a candidate copy first; the editor's graph and view change only
    // when everything passes, together, so a failure leaves the draft untouched.
    read() {
      const candidate = copyGraph(graph);
      for (const n of candidate.nodes) if (n.kind === 'script' && n.timeout_seconds != null) n.timeout_seconds = timeoutSeconds(n.timeout_seconds, 1);
      const shift = layoutShift(candidate);
      if (shift.issue) { error = shift.issue; paint(); throw error; }
      for (const n of candidate.nodes) { n.x += shift.x; n.y += shift.y; }
      error = graphIssue(candidate);
      if (error) { paint(); throw error; }
      graph = candidate;
      if (shift.x || shift.y) canvas.panBy(-shift.x * canvas.zoom, -shift.y * canvas.zoom);
      paint();
      return copyGraph(graph);
    },
    // Only the unlocked -> locked transition voids gestures; repeated status
    // refreshes while locked must not interrupt panning, which stays allowed.
    setLocked(value) {
      const entering = value && !locked;
      locked = value;
      if (entering) { canvas.cancelInteraction(); menu.close(); }
      paint(); applyLock();
    },
    cancelInteraction() { canvas.cancelInteraction(); menu.close(); },
    relocalize() {
      for (const [el, kind, key] of labels) {
        if (kind === 'text') el.textContent = t(key);
        else { el.title = t(key); el.setAttribute('aria-label', t(key)); }
      }
      inspector.relocalize(); menu.close(); paint();
    },
  };
}
