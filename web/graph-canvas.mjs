import {t} from './i18n.mjs';
import {node} from './ui.mjs';

export const zoomLimits = {min: 0.4, max: 2};
// Saved coordinates are 0-8000. While editing, steps may also move left of or
// above the origin; the editor shifts the layout back into range on read().
export const coordinateLimit = 8000;
const height = 64, portGap = 7;
export const clampCoordinate = value => clamp(Math.round(value), -coordinateLimit, coordinateLimit);
export const nodeWidth = n => n.kind === 'script' ? 200 : 140;
const clamp = (value, low, high) => Math.min(high, Math.max(low, value));
const rectOf = element => element.getBoundingClientRect?.() ?? {left: 0, top: 0, width: 800, height: 420};
const nodeName = n => n.kind === 'start' ? t('common.graph.start') : n.kind === 'end' ? t('common.graph.end') : n.name || t('common.graph.unnamed');
const fileName = path => String(path || '').split(/[\\/]/).pop();
export const edgeKey = edge => `${edge.from}>${edge.to}`;

// Draws steps and connections and turns pointer gestures into intents. The
// editor owns the graph; the canvas only reports what the user asked for.
export function graphCanvas(intents) {
  const viewport = node('div', null, 'graph-viewport'), stage = node('div', null, 'graph-stage');
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.classList.add('graph-edges');
  viewport.tabIndex = 0;
  viewport.setAttribute('role', 'application');
  stage.append(svg); viewport.append(stage);
  const cards = new Map();
  let graph = null, selection = null, locked = false, view = {x: 0, y: 0, zoom: 1};
  let connecting = null, renaming = null, gesture = null;

  const svgNode = (tag, attributes = {}) => {
    const element = document.createElementNS(svg.namespaceURI, tag);
    for (const [key, value] of Object.entries(attributes)) element.setAttribute(key, value);
    return element;
  };
  const toGraph = (clientX, clientY) => {
    const rect = rectOf(viewport);
    return {x: (clientX - rect.left - view.x) / view.zoom, y: (clientY - rect.top - view.y) / view.zoom};
  };
  const toHost = (clientX, clientY) => { const rect = rectOf(viewport); return {x: clientX - rect.left, y: clientY - rect.top}; };
  function applyView() {
    stage.style.transform = `translate(${view.x}px, ${view.y}px) scale(${view.zoom})`;
    viewport.style.backgroundSize = `${20 * view.zoom}px ${20 * view.zoom}px`;
    viewport.style.backgroundPosition = `${view.x}px ${view.y}px`;
    intents.viewChanged?.(view.zoom);
  }
  function zoomAt(factor, clientX, clientY) {
    const rect = rectOf(viewport);
    const px = clientX === undefined ? rect.width / 2 : clientX - rect.left, py = clientY === undefined ? rect.height / 2 : clientY - rect.top;
    const zoom = clamp(view.zoom * factor, zoomLimits.min, zoomLimits.max);
    view = {x: px - (px - view.x) * zoom / view.zoom, y: py - (py - view.y) * zoom / view.zoom, zoom};
    applyView();
  }
  function fit() {
    if (!graph?.nodes.length) return;
    const rect = rectOf(viewport), pad = 48;
    const minX = Math.min(...graph.nodes.map(n => n.x)), minY = Math.min(...graph.nodes.map(n => n.y));
    const maxX = Math.max(...graph.nodes.map(n => n.x + nodeWidth(n))), maxY = Math.max(...graph.nodes.map(n => n.y + height));
    const zoom = clamp(Math.min((rect.width - pad * 2) / (maxX - minX), (rect.height - pad * 2) / (maxY - minY), 1), zoomLimits.min, 1);
    view = {zoom, x: (rect.width - (maxX - minX) * zoom) / 2 - minX * zoom, y: (rect.height - (maxY - minY) * zoom) / 2 - minY * zoom};
    applyView();
  }
  // Pointer capture keeps a drag alive when the pointer leaves its element.
  // Every running gesture registers a cancel function, so locking, loading
  // another graph or closing the editor can end it without committing.
  function track(element, event, move, end = () => {}) {
    cancelGesture();
    element.setPointerCapture?.(event.pointerId);
    const start = {x: event.clientX, y: event.clientY};
    let moved = false;
    const clear = () => {
      element.onpointermove = element.onpointerup = element.onpointercancel = null;
      element.releasePointerCapture?.(event.pointerId);
      if (gesture?.clear === clear) gesture = null;
    };
    element.onpointermove = next => {
      if (Math.abs(next.clientX - start.x) + Math.abs(next.clientY - start.y) > 3) moved = true;
      if (moved) move(next.clientX - start.x, next.clientY - start.y);
    };
    element.onpointerup = () => { clear(); end(moved); };
    element.onpointercancel = clear;
    gesture = {clear};
  }
  function cancelGesture() { gesture?.clear(); gesture = null; }

  function startConnect(from, event) {
    cancelGesture();
    const owner = graph, preview = svgNode('path', {class: 'graph-edge graph-edge-preview'});
    svg.append(preview);
    connecting = {from, target: '', preview};
    viewport.dataset.connecting = 'true';
    const anchor = graph.nodes.find(n => n.id === from);
    const draw = (x, y) => preview.setAttribute('d', curve(anchor.x + nodeWidth(anchor) + portGap, anchor.y + height / 2, x, y));
    const point = toGraph(event.clientX, event.clientY); draw(point.x, point.y);
    const move = next => { const p = toGraph(next.clientX, next.clientY); draw(p.x, p.y); };
    const clear = () => {
      document.removeEventListener('pointermove', move); document.removeEventListener('pointerup', up);
      document.removeEventListener('pointercancel', clear);
      preview.remove(); connecting = null; delete viewport.dataset.connecting;
      for (const card of cards.values()) delete card.element.dataset.drop;
      if (gesture?.clear === clear) gesture = null;
    };
    function up() {
      const {target} = connecting;
      clear();
      // A lock or a newly loaded graph during the drag voids the connection.
      if (!locked && graph === owner && target && intents.canConnect(from, target)) intents.connect(from, target);
    }
    document.addEventListener('pointermove', move); document.addEventListener('pointerup', up);
    document.addEventListener('pointercancel', clear);
    gesture = {clear};
  }
  function hover(id, entering) {
    if (!connecting || id === connecting.from) return;
    const card = cards.get(id);
    if (!entering) { if (connecting.target === id) connecting.target = ''; delete card.element.dataset.drop; return; }
    connecting.target = id;
    card.element.dataset.drop = intents.canConnect(connecting.from, id) ? 'ok' : 'no';
  }

  function card(n) {
    const element = node('div', null, 'graph-node'), title = node('span', null, 'graph-node-title'), detail = node('span', null, 'graph-node-detail');
    element.tabIndex = 0; element.setAttribute('role', 'button');
    element.dataset.kind = n.kind; element.dataset.nodeId = n.id;
    element.append(title, detail);
    const ports = {};
    for (const side of ['in', 'out']) {
      if ((side === 'in' && n.kind === 'start') || (side === 'out' && n.kind === 'end')) continue;
      const port = node('span', null, 'graph-port'); port.dataset.port = side; element.append(port); ports[side] = port;
    }
    ports.out?.addEventListener('pointerdown', event => {
      event.stopPropagation(); event.preventDefault?.();
      if (!locked && event.button === 0) startConnect(n.id, event);
    });
    element.addEventListener('pointerdown', event => {
      if (event.button !== 0 || renaming?.id === n.id) return;
      event.stopPropagation();
      intents.select({kind: 'node', id: n.id});
      if (locked) return;
      // Cards outlive graph reloads; always start from the current node data.
      const owner = graph, current = graph.nodes.find(item => item.id === n.id);
      if (!current) return;
      const origin = {x: current.x, y: current.y};
      track(element, event, (dx, dy) => {
        if (locked || graph !== owner) return;
        intents.move(n.id, clampCoordinate(origin.x + dx / view.zoom), clampCoordinate(origin.y + dy / view.zoom));
      });
    });
    element.addEventListener('click', () => intents.select({kind: 'node', id: n.id}));
    element.addEventListener('dblclick', event => { event.stopPropagation(); startRename(n.id); });
    element.addEventListener('contextmenu', event => {
      event.preventDefault(); event.stopPropagation();
      intents.select({kind: 'node', id: n.id});
      intents.menu({kind: 'node', id: n.id}, toHost(event.clientX, event.clientY));
    });
    element.addEventListener('pointerover', () => hover(n.id, true));
    element.addEventListener('pointerleave', () => hover(n.id, false));
    element.addEventListener('keydown', event => {
      if (renaming) return;
      const step = event.shiftKey ? 1 : 10;
      const moves = {ArrowLeft: [-step, 0], ArrowRight: [step, 0], ArrowUp: [0, -step], ArrowDown: [0, step]};
      if (event.key === 'F2') { event.preventDefault(); startRename(n.id); }
      else if (moves[event.key] && !locked) {
        event.preventDefault();
        const current = graph.nodes.find(item => item.id === n.id), [dx, dy] = moves[event.key];
        intents.move(n.id, clampCoordinate(current.x + dx), clampCoordinate(current.y + dy));
      }
    });
    stage.append(element);
    return {element, title, detail};
  }
  // Inline rename replaces the title with an input; Enter commits, Esc cancels.
  function startRename(id) {
    const n = graph?.nodes.find(item => item.id === id);
    if (locked || n?.kind !== 'script' || renaming) return;
    const {element, title} = cards.get(id), input = node('input', null, 'graph-rename');
    input.value = n.name; input.maxLength = 128;
    input.setAttribute('aria-label', t('web.graph.node_name'));
    renaming = {id, input, cancel: () => finish(false)};
    title.hidden = true; element.insertBefore(input, title);
    let composing = false;
    const finish = commit => {
      if (renaming?.input !== input) return;
      renaming = null; input.remove(); title.hidden = false;
      if (commit && !locked && input.value.trim()) intents.rename(id, input.value.trim());
      element.focus?.();
    };
    // Enter/Escape inside an IME composition belong to the input method.
    input.addEventListener('compositionstart', () => { composing = true; });
    input.addEventListener('compositionend', () => { composing = false; });
    input.addEventListener('keydown', event => {
      event.stopPropagation();
      if (composing || event.isComposing || event.keyCode === 229) return;
      if (event.key === 'Enter') { event.preventDefault(); finish(true); }
      if (event.key === 'Escape') { event.preventDefault(); finish(false); }
    });
    input.addEventListener('blur', () => finish(true));
    input.addEventListener('pointerdown', event => event.stopPropagation());
    input.focus?.(); input.select?.();
  }

  function curve(x, y, tx, ty) {
    const bend = Math.max(48, Math.abs(tx - x) / 2);
    return `M ${x} ${y} C ${x + bend} ${y}, ${tx - bend} ${ty}, ${tx} ${ty}`;
  }
  function drawEdges() {
    for (const child of [...svg.children]) if (child !== connecting?.preview) child.remove();
    const definitions = svgNode('defs'), marker = svgNode('marker', {id: 'graph-arrow', viewBox: '0 0 10 10', refX: '9', refY: '5', markerWidth: '6', markerHeight: '6', orient: 'auto-start-reverse'});
    marker.append(svgNode('path', {d: 'M 0 0 L 10 5 L 0 10 z'})); definitions.append(marker); svg.insertBefore(definitions, svg.children[0] ?? null);
    for (const edge of graph.edges) {
      const from = graph.nodes.find(n => n.id === edge.from), to = graph.nodes.find(n => n.id === edge.to);
      if (!from || !to) continue;
      // Ends stop outside the port circles so arrowheads stay visible.
      const d = curve(from.x + nodeWidth(from) + portGap, from.y + height / 2, to.x - portGap, to.y + height / 2), key = edgeKey(edge);
      const group = svgNode('g', {class: 'graph-edge-group'}), hit = svgNode('path', {d, class: 'graph-edge-hit'});
      const line = svgNode('path', {d, class: 'graph-edge', 'marker-end': 'url(#graph-arrow)'});
      if (selection?.kind === 'edge' && selection.id === key) group.dataset.selected = 'true';
      const title = svgNode('title'); title.textContent = t('web.graph.edge_label', {from: nodeName(from), to: nodeName(to)});
      group.append(title, hit, line);
      hit.addEventListener('pointerdown', event => { event.stopPropagation(); intents.select({kind: 'edge', id: key}); });
      hit.addEventListener('click', () => intents.select({kind: 'edge', id: key}));
      hit.addEventListener('contextmenu', event => {
        event.preventDefault(); event.stopPropagation();
        intents.select({kind: 'edge', id: key});
        intents.menu({kind: 'edge', id: key}, toHost(event.clientX, event.clientY));
      });
      svg.insertBefore(group, connecting?.preview ?? null);
    }
  }
  function render(next, nextSelection, nextLocked) {
    graph = next; selection = nextSelection; locked = nextLocked;
    viewport.dataset.locked = String(locked);
    const ids = new Set(graph.nodes.map(n => n.id));
    for (const [id, entry] of cards) if (!ids.has(id)) { entry.element.remove(); cards.delete(id); }
    for (const n of graph.nodes) {
      if (!cards.has(n.id)) cards.set(n.id, card(n));
      const {element, title, detail} = cards.get(n.id);
      if (title.textContent !== nodeName(n)) title.textContent = nodeName(n);
      const file = n.kind === 'script' ? fileName(n.script) : '';
      detail.textContent = n.kind === 'script' ? file || t('web.graph.no_script') : '';
      detail.hidden = n.kind !== 'script';
      element.dataset.missing = String(n.kind === 'script' && !file);
      element.title = n.kind === 'script' ? [nodeName(n), n.script].filter(Boolean).join('\n') : nodeName(n);
      element.style.left = `${n.x}px`; element.style.top = `${n.y}px`;
      element.setAttribute('aria-pressed', String(selection?.kind === 'node' && selection.id === n.id));
    }
    drawEdges();
  }

  viewport.addEventListener('pointerdown', event => {
    if (event.button !== 0 && event.button !== 1) return;
    viewport.focus?.();
    const origin = {...view};
    track(viewport, event, (dx, dy) => { view = {...view, x: origin.x + dx, y: origin.y + dy}; applyView(); },
      moved => { if (!moved) intents.select(null); });
  });
  // Plain wheel keeps scrolling the dialog; Ctrl + wheel zooms around the pointer.
  viewport.addEventListener('wheel', event => {
    if (!event.ctrlKey) return;
    event.preventDefault();
    zoomAt(Math.exp(-event.deltaY / 400), event.clientX, event.clientY);
  }, {passive: false});
  viewport.addEventListener('dblclick', event => { if (!locked) intents.add(toGraph(event.clientX, event.clientY)); });
  viewport.addEventListener('contextmenu', event => {
    event.preventDefault();
    intents.menu({kind: 'canvas', point: toGraph(event.clientX, event.clientY)}, toHost(event.clientX, event.clientY));
  });
  return {
    element: viewport,
    render,
    fit,
    zoomBy: factor => zoomAt(factor),
    // Moves the view by screen pixels, e.g. to hide a layout shift.
    panBy(dx, dy) { view = {...view, x: view.x + dx, y: view.y + dy}; applyView(); },
    get zoom() { return view.zoom; },
    // Graph point at the visible centre, for steps added from the toolbar.
    center() { const rect = rectOf(viewport); return toGraph(rect.left + rect.width / 2, rect.top + rect.height / 2); },
    startRename,
    // Ends any drag, connection or rename without committing it.
    cancelInteraction() { cancelGesture(); renaming?.cancel(); },
    focusNode(id) { cards.get(id)?.element.focus?.(); },
  };
}
