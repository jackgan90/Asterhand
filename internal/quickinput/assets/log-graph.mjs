import {t, formatDateTime} from './i18n.mjs';
import {reasonLabel} from './command-run.mjs';
import {nodeName, nodeLabel, nodeState, logSelection} from './log-node.mjs';

export function logGraphView({byID, createPane}) {
  const panel = byID('log-graph'), nodes = byID('log-nodes'), options = byID('log-node-options');
  const layout = byID('log-columns'), empty = byID('log-empty'), picker = byID('log-view-picker');
  const all = byID('log-view-all'), active = byID('log-view-active'), summary = byID('log-view-summary');
  const selection = logSelection(), entries = new Map(), panes = new Map();
  let record = null, identity = '', visible = true;

  function entry(node) {
    const button = document.createElement('button');
    button.type = 'button';
    button.disabled = node.kind !== 'script';
    button.onclick = () => selectNode(node.node_id, !selection.has(node.node_id));
    nodes.append(button);
    if (node.kind !== 'script') return {button};
    const label = document.createElement('label'), input = document.createElement('input');
    const name = document.createElement('span'), state = document.createElement('span');
    input.type = 'checkbox';
    input.onchange = () => selectNode(node.node_id, input.checked);
    label.className = 'log-node-option';
    state.className = 'log-node-state';
    label.append(input, name, state);
    options.append(label);
    return {button, label, input, name, state};
  }

  function updateEntry(node) {
    let value = entries.get(node.node_id);
    if (!value) { value = entry(node); entries.set(node.node_id, value); }
    const {button, input, label, name, state} = value;
    button.textContent = t('common.graph.node_line', {name: nodeName(node), state: nodeLabel(node)});
    button.dataset.state = nodeState(node);
    button.dataset.failed = String(node.node_id === record.failed_node_id);
    button.setAttribute('aria-pressed', String(selection.has(node.node_id)));
    const details = [reasonLabel(node)];
    if (node.started_at) details.push(t('common.command.started', {time: formatDateTime(node.started_at)}));
    if (node.ended_at) details.push(t('common.command.ended', {time: formatDateTime(node.ended_at)}));
    details.push(t('common.command.elapsed', {seconds: (node.elapsed_seconds ?? 0).toFixed(1)}));
    if (node.exit?.code !== undefined) details.push(t('common.command.exit_code', {code: node.exit.code}));
    button.title = details.filter(Boolean).join(' · ');
    if (input) {
      input.checked = selection.has(node.node_id);
      name.textContent = nodeName(node);
      state.textContent = nodeLabel(node);
      label.dataset.state = nodeState(node);
    }
  }

  function render() {
    panel.hidden = record?.command?.kind !== 'graph';
    for (const node of record?.nodes ?? []) updateEntry(node);
    all.checked = selection.mode === 'all';
    active.checked = selection.mode === 'active';
    const chosen = selection.nodes(record?.nodes ?? []);
    summary.textContent = selection.mode === 'all' ? t('common.graph.all') : selection.mode === 'active' ?
      t('common.graph.active_logs') : t('common.graph.selected_count', {count: chosen.length});
    const ids = !record ? [] : panel.hidden || selection.mode === 'all' ? [''] : chosen.map(node => node.node_id);
    for (const [id, pane] of panes) if (!ids.includes(id)) { pane.dispose(); panes.delete(id); }
    ids.forEach((id, index) => {
      let pane = panes.get(id);
      if (!pane) { pane = createPane(id, record); pane.setVisible(visible); panes.set(id, pane); }
      // Only insert new/misplaced panes; existing focused controls and output
      // stay mounted when polling state, changing language or adding a pane.
      if (layout.children[index] !== pane.element) layout.insertBefore(pane.element, layout.children[index] ?? null);
      pane.metadata(record);
    });
    layout.dataset.mode = selection.mode === 'all' || panel.hidden ? 'merged' : 'split';
    layout.hidden = ids.length === 0;
    empty.hidden = !record || ids.length > 0;
    empty.textContent = t(selection.mode === 'active' ? 'common.graph.no_active' : 'common.graph.choose_nodes');
  }

  function refresh() { if (visible) for (const pane of panes.values()) void pane.refresh(); }
  function selectNode(id, checked) { selection.toggle(id, checked); render(); refresh(); }
  all.onchange = () => { if (all.checked) { selection.choose('all'); render(); refresh(); } };
  active.onchange = () => { if (active.checked) { selection.choose('active'); render(); refresh(); } };
  picker.addEventListener('keydown', event => {
    if (event.key === 'Escape') { picker.open = false; summary.focus(); }
  });
  document.addEventListener('pointerdown', event => { if (!picker.contains(event.target)) picker.open = false; });

  return {
    metadata(next) {
      record = next;
      if (identity !== (record?.run_id ?? '')) {
        identity = record?.run_id ?? '';
        for (const pane of panes.values()) pane.dispose();
        panes.clear(); entries.clear(); nodes.replaceChildren(); options.replaceChildren(); selection.choose('all');
      }
      render();
    },
    refresh,
    setVisible(value) { visible = value; for (const pane of panes.values()) pane.setVisible(value); },
    setEncoding(value) { for (const pane of panes.values()) void pane.setEncoding(value); },
    relocalize() { render(); for (const pane of panes.values()) pane.relocalize(); },
  };
}
