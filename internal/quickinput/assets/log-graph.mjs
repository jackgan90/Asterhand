import {t, formatDateTime} from './i18n.mjs';
import {runLabel} from './command-run.mjs';
import {nodeName, nodeLabel, nodeState, nodeActive} from './log-node.mjs';

export const paneKey = (runID, nodeID = '') => `${runID}:${nodeID}`;
export const instanceLabel = run => t('common.command.instance_label', {
  sequence: run.sequence, time: formatDateTime(run.started_at), state: runLabel(run),
});
const maxPanes = 8;

// Selection uses both identities: graphs in different runs share node IDs.
// Modes: "merged" shows the inspected run's combined log, "active" follows
// running nodes, and "selected" keeps a fixed set of run/node panes.
export function runLogSelection() {
  let round = '', inspected = '', mode = 'merged';
  const selected = new Map();
  const add = (runID, nodeID) => { if (selected.size < maxPanes || selected.has(paneKey(runID, nodeID))) selected.set(paneKey(runID, nodeID), {runID, nodeID}); };
  return {
    get inspected() { return inspected; },
    get mode() { return mode; },
    inspect(id) { inspected = id; },
    merged() { mode = 'merged'; selected.clear(); },
    active() { mode = 'active'; selected.clear(); },
    // Entering the fixed mode keeps what is currently on screen.
    custom(seed = []) {
      if (mode === 'selected') return;
      selected.clear();
      for (const choice of seed) add(choice.runID, choice.nodeID);
      mode = 'selected';
    },
    reset(list) {
      if (round !== list.round_id) {
        round = list.round_id; inspected = list.latest_run_id || ''; mode = 'merged'; selected.clear();
      }
    },
    has(runID, nodeID = '') {
      if (mode === 'merged') return nodeID === '' && runID === inspected;
      return mode === 'selected' && selected.has(paneKey(runID, nodeID));
    },
    toggle(runID, nodeID, checked) {
      if (mode !== 'selected') selected.clear();
      mode = 'selected';
      if (!checked) selected.delete(paneKey(runID, nodeID));
      else add(runID, nodeID);
    },
    required(list) {
      const ids = new Set([inspected]);
      for (const choice of selected.values()) ids.add(choice.runID);
      if (mode === 'active') for (const run of list.runs) if (run.phase !== 'finished') ids.add(run.run_id);
      return [...ids].filter(id => list.runs.some(run => run.run_id === id));
    },
    choices(list, records) {
      if (mode === 'merged') return inspected ? [{runID: inspected, nodeID: ''}] : [];
      if (mode === 'selected') return [...selected.values()];
      return list.runs.flatMap(run => (records.get(run.run_id)?.nodes || [])
        .filter(nodeActive).map(node => ({runID: run.run_id, nodeID: node.node_id})));
    },
  };
}

export function logGraphView({byID, createPane, changed = () => {}}) {
  const panel = byID('log-graph'), nodes = byID('log-nodes');
  const layout = byID('log-columns'), empty = byID('log-empty');
  const modes = {merged: byID('log-view-all'), active: byID('log-view-active'), selected: byID('log-view-custom')};
  const runPicker = byID('log-run-picker'), runField = byID('log-run-field'), limit = byID('log-limit');
  const selection = runLogSelection(), chips = new Map(), panes = new Map(), runOptions = new Map();
  let list = {runs: []}, records = new Map(), nodeIdentity = '', visible = true;

  function update() { render(); refresh(); changed(); }
  // A chip click enters the fixed mode, keeping the panes already on screen.
  function toggleChip(nodeID) {
    selection.custom(selection.choices(list, records));
    selection.toggle(selection.inspected, nodeID, !selection.has(selection.inspected, nodeID));
    update();
  }
  function chip(nodeID) {
    const button = document.createElement('button'); button.type = 'button'; button.className = 'log-chip';
    button.onclick = () => toggleChip(nodeID);
    nodes.append(button);
    return button;
  }
  function paintChip(button, node, record) {
    const chosen = selection.mode === 'selected' && selection.has(selection.inspected, node?.node_id ?? '');
    const name = node ? nodeName(node) : t('common.graph.all_steps');
    if (button.textContent !== name) button.textContent = name;
    const state = node ? nodeLabel(node) : runLabel(record);
    button.title = state;
    button.setAttribute('aria-label', node ? t('common.graph.node_line', {name, state}) : name);
    button.dataset.state = node ? nodeState(node) : '';
    button.dataset.failed = String(Boolean(node) && node.node_id === record.failed_node_id);
    button.setAttribute('aria-pressed', String(chosen));
  }
  function renderRuns() {
    runField.hidden = list.runs.length <= 1;
    const retained = new Set(list.runs.map(run => run.run_id));
    for (const [id, option] of runOptions) if (!retained.has(id)) { option.remove(); runOptions.delete(id); }
    for (const run of list.runs) {
      let option = runOptions.get(run.run_id);
      if (!option) { option = document.createElement('option'); option.value = run.run_id; runPicker.append(option); runOptions.set(run.run_id, option); }
      option.textContent = instanceLabel(run);
    }
    runPicker.value = selection.inspected;
  }
  function renderChips() {
    if (nodeIdentity !== selection.inspected) {
      nodeIdentity = selection.inspected; chips.clear(); nodes.replaceChildren();
    }
    const record = records.get(selection.inspected);
    if (!record) return;
    if (!chips.has('')) chips.set('', chip(''));
    paintChip(chips.get(''), null, record);
    for (const node of record.nodes || []) {
      if (node.kind !== 'script') continue;
      if (!chips.has(node.node_id)) chips.set(node.node_id, chip(node.node_id));
      paintChip(chips.get(node.node_id), node, record);
    }
  }
  function render() {
    panel.hidden = list.runs.length === 0;
    renderRuns();
    renderChips();
    for (const [mode, button] of Object.entries(modes)) button.setAttribute('aria-pressed', String(selection.mode === mode));
    panel.dataset.mode = selection.mode;
    const choices = selection.choices(list, records);
    limit.hidden = choices.length < maxPanes;
    const shown = choices.slice(0, maxPanes).filter(choice => records.has(choice.runID));
    const keys = shown.map(choice => paneKey(choice.runID, choice.nodeID));
    for (const [key, pane] of panes) if (!keys.includes(key)) { pane.dispose(); panes.delete(key); }
    shown.forEach(({runID, nodeID}, index) => {
      const key = paneKey(runID, nodeID), run = records.get(runID);
      let pane = panes.get(key);
      if (!pane) { pane = createPane(nodeID, run); pane.setVisible(visible); panes.set(key, pane); }
      if (layout.children[index] !== pane.element) layout.insertBefore(pane.element, layout.children[index] ?? null);
      pane.showAttempt?.(list.runs.length > 1);
      pane.metadata(run);
    });
    layout.dataset.mode = shown.length > 1 ? 'split' : 'merged'; layout.hidden = shown.length === 0;
    empty.hidden = shown.length > 0;
    empty.textContent = t(selection.mode === 'active' ? 'common.graph.no_active' : 'common.graph.choose_nodes');
  }
  function refresh() { if (visible) for (const pane of panes.values()) void pane.refresh(); }
  modes.merged.onclick = () => { selection.merged(); update(); };
  modes.active.onclick = () => { selection.active(); update(); };
  modes.selected.onclick = () => { selection.custom(selection.choices(list, records)); update(); };
  runPicker.onchange = () => { selection.inspect(runPicker.value); changed(); };
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
