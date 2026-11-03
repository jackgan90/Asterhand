import {t, formatDateTime} from './i18n.mjs';
import {runLabel} from './command-run.mjs';
import {nodeName, nodeLabel, nodeState, nodeActive} from './log-node.mjs';

export const paneKey = (runID, nodeID = '') => `${runID}:${nodeID}`;
export const instanceLabel = run => t('common.command.instance_label', {
  sequence: run.sequence, time: formatDateTime(run.started_at), state: runLabel(run),
});
const maxPanes = 8;

// Selection uses both identities: graphs in different runs share node IDs.
export function runLogSelection() {
  let round = '', inspected = '', mode = 'selected';
  const selected = new Map();
  return {
    get inspected() { return inspected; },
    get mode() { return mode; },
    inspect(id) { inspected = id; },
    active() { mode = 'active'; selected.clear(); },
    reset(list) {
      if (round !== list.round_id) {
        round = list.round_id; inspected = list.latest_run_id || ''; mode = 'selected'; selected.clear();
        if (inspected) selected.set(paneKey(inspected), {runID: inspected, nodeID: ''});
      }
    },
    has(runID, nodeID = '') { return selected.has(paneKey(runID, nodeID)); },
    toggle(runID, nodeID, checked) {
      if (mode !== 'selected') selected.clear();
      mode = 'selected';
      const key = paneKey(runID, nodeID);
      if (!checked) selected.delete(key);
      else if (selected.size < maxPanes || selected.has(key)) selected.set(key, {runID, nodeID});
    },
    required(list) {
      const ids = new Set([inspected]);
      for (const choice of selected.values()) ids.add(choice.runID);
      if (mode === 'active') for (const run of list.runs) if (run.phase !== 'finished') ids.add(run.run_id);
      return [...ids].filter(id => list.runs.some(run => run.run_id === id));
    },
    choices(list, records) {
      if (mode === 'selected') return [...selected.values()];
      return list.runs.flatMap(run => (records.get(run.run_id)?.nodes || [])
        .filter(nodeActive).map(node => ({runID: run.run_id, nodeID: node.node_id})));
    },
  };
}

export function logGraphView({byID, createPane, changed = () => {}}) {
  const panel = byID('log-graph'), nodes = byID('log-nodes'), options = byID('log-node-options');
  const layout = byID('log-columns'), empty = byID('log-empty'), picker = byID('log-view-picker');
  const all = byID('log-view-all'), active = byID('log-view-active'), summary = byID('log-view-summary');
  const runPicker = byID('log-run-picker'), limit = byID('log-limit');
  const selection = runLogSelection(), entries = new Map(), panes = new Map(), runOptions = new Map();
  let list = {runs: []}, records = new Map(), nodeIdentity = '', visible = true;

  function selectNode(nodeID, checked) {
    selection.toggle(selection.inspected, nodeID, checked); render(); refresh(); changed();
  }
  function entry(node) {
    const button = document.createElement('button'); button.type = 'button';
    button.disabled = node.kind !== 'script';
    button.onclick = () => selectNode(node.node_id, !selection.has(selection.inspected, node.node_id));
    nodes.append(button);
    if (node.kind !== 'script') return {button};
    const label = document.createElement('label'), input = document.createElement('input');
    const name = document.createElement('span'), state = document.createElement('span');
    input.type = 'checkbox'; input.onchange = () => selectNode(node.node_id, input.checked);
    label.className = 'log-node-option'; state.className = 'log-node-state';
    label.append(input, name, state); options.append(label);
    return {button, label, input, name, state};
  }
  function render() {
    panel.hidden = list.runs.length === 0;
    const retained = new Set(list.runs.map(run => run.run_id));
    for (const [id, option] of runOptions) if (!retained.has(id)) { option.remove(); runOptions.delete(id); }
    for (const run of list.runs) {
      let option = runOptions.get(run.run_id);
      if (!option) { option = document.createElement('option'); option.value = run.run_id; runPicker.append(option); runOptions.set(run.run_id, option); }
      option.textContent = instanceLabel(run);
    }
    runPicker.value = selection.inspected;
    if (nodeIdentity !== selection.inspected) {
      nodeIdentity = selection.inspected; entries.clear(); nodes.replaceChildren(); options.replaceChildren();
    }
    const record = records.get(selection.inspected);
    for (const node of record?.nodes || []) {
      let value = entries.get(node.node_id);
      if (!value) { value = entry(node); entries.set(node.node_id, value); }
      const {button, input, label, name, state} = value;
      const chosen = selection.has(selection.inspected, node.node_id);
      button.textContent = t('common.graph.node_line', {name: nodeName(node), state: nodeLabel(node)});
      button.dataset.state = nodeState(node); button.dataset.failed = String(node.node_id === record.failed_node_id);
      button.setAttribute('aria-pressed', String(chosen));
      if (input) { input.checked = chosen; name.textContent = nodeName(node); state.textContent = nodeLabel(node); label.dataset.state = nodeState(node); }
    }
    all.checked = selection.has(selection.inspected); active.checked = selection.mode === 'active';
    const choices = selection.choices(list, records);
    limit.hidden = choices.length < maxPanes;
    summary.textContent = selection.mode === 'active' ? t('common.graph.active_logs') : t('common.graph.selected_count', {count: choices.length});
    const shown = choices.slice(0, maxPanes).filter(choice => records.has(choice.runID));
    const keys = shown.map(choice => paneKey(choice.runID, choice.nodeID));
    for (const [key, pane] of panes) if (!keys.includes(key)) { pane.dispose(); panes.delete(key); }
    shown.forEach(({runID, nodeID}, index) => {
      const key = paneKey(runID, nodeID), run = records.get(runID);
      let pane = panes.get(key);
      if (!pane) { pane = createPane(nodeID, run); pane.setVisible(visible); panes.set(key, pane); }
      if (layout.children[index] !== pane.element) layout.insertBefore(pane.element, layout.children[index] ?? null);
      pane.metadata(run);
    });
    layout.dataset.mode = shown.length > 1 ? 'split' : 'merged'; layout.hidden = shown.length === 0;
    empty.hidden = shown.length > 0;
    empty.textContent = t(selection.mode === 'active' ? 'common.graph.no_active' : 'common.graph.choose_nodes');
  }
  function refresh() { if (visible) for (const pane of panes.values()) void pane.refresh(); }
  all.onchange = () => selectNode('', all.checked);
  active.onchange = () => { if (active.checked) { selection.active(); render(); refresh(); changed(); } };
  runPicker.onchange = () => { selection.inspect(runPicker.value); changed(); };
  picker.addEventListener('keydown', event => { if (event.key === 'Escape') { picker.open = false; summary.focus(); } });
  document.addEventListener('pointerdown', event => { if (!picker.contains(event.target)) picker.open = false; });
  return {
    required(next) { selection.reset(next); return selection.required(next); },
    metadata(next = {runs: []}, nextRecords = new Map()) {
      list = next || {runs: []}; records = nextRecords; selection.reset(list); render();
    },
    refresh,
    setVisible(value) { visible = value; for (const pane of panes.values()) pane.setVisible(value); },
    setEncoding(value) { for (const pane of panes.values()) void pane.setEncoding(value); },
    relocalize() { render(); for (const pane of panes.values()) pane.relocalize(); },
  };
}
