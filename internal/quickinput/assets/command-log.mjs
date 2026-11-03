import {webview2Bridge} from './webview2-bridge.mjs';
import {commandClient} from './command-client.mjs';
import {applyI18n, setLocale, t, localizeError, formatDateTime} from './i18n.mjs';
import {runStoppable, runLabel, reasonLabel} from './command-run.mjs';
import {nodeName, nodeLabel, nodeState} from './log-node.mjs';
import {logGraphView} from './log-graph.mjs';
import {logPane} from './log-pane.mjs';
import {logReadQueue} from './log-reader.mjs';

const bridge = webview2Bridge(), byID = id => document.getElementById(id);
const stop = byID('log-stop'), encoding = byID('log-encoding');
let epoch = '', commandID = '', record = null, stopping = false, refreshing = false;
let runs = {runs: []};
let timer = null, visible = true, error = null;
const post = (kind, fields = {}) => bridge.send({version: 1, kind, generation: epoch, ...fields});
const client = commandClient(bridge, post), read = logReadQueue(client.call);
const graphView = logGraphView({byID, changed: () => void refresh(), createPane: (nodeID, next) =>
  logPane({read, commandID, record: next, nodeID, encoding: encoding.value, stop: stopRun})});

// The header shows name, status and duration; times move to the tooltip and
// exit details appear only when the run did not succeed.
function renderMetadata() {
  byID('log-name').textContent = record?.command?.name || t('brand.log_window_title');
  const state = byID('log-state');
  state.textContent = runLabel(record);
  state.dataset.state = record ? nodeState(record) : '';
  byID('log-elapsed').textContent = record ? t('common.command.elapsed', {seconds: record.elapsed_seconds.toFixed(1)}) : '';
  const stoppable = runs.runs.some(runStoppable);
  stop.hidden = !stoppable && !stopping;
  stop.disabled = stopping || !stoppable;
  const times = [];
  if (record?.started_at) times.push(t('common.command.started', {time: formatDateTime(record.started_at)}));
  if (record?.ended_at) times.push(t('common.command.ended', {time: formatDateTime(record.ended_at)}));
  if (record?.command?.timeout_seconds) times.push(t('common.command.timeout_value', {seconds: record.command.timeout_seconds}));
  byID('log-name').title = times.join('\n');
  const failed = record?.phase === 'finished' && record.result && record.result !== 'succeeded';
  const failedNode = record?.nodes?.find(node => node.node_id === record.failed_node_id);
  const warnings = [error && localizeError(error),
    failed && reasonLabel(record),
    failed && record.exit?.code !== undefined && record.exit.code !== 0 && t('common.command.exit_code', {code: record.exit.code}),
    failedNode && t('common.graph.failed_detail', {name: nodeName(failedNode), reason: reasonLabel(failedNode) || nodeLabel(failedNode)}),
    record?.log_complete === false && t('common.command.log_incomplete'),
    record?.storage_error && t('common.command.storage_error'), (record?.cleanup_pending || runs.cleanup_pending) && t('common.command.cleanup_pending')].filter(Boolean);
  byID('log-warning').textContent = warnings.join(' · ');
  byID('log-warning').hidden = !warnings.length;
}

function scheduleRefresh() {
  clearTimeout(timer);
  timer = epoch && visible ? setTimeout(refresh, 500) : null;
}

async function refresh() {
  clearTimeout(timer);
  if (!epoch || !visible) return;
  if (refreshing) { scheduleRefresh(); return; }
  refreshing = true;
  const generation = epoch, id = commandID;
  try {
    const response = await client.call('runs', {command_id: id});
    if (epoch !== generation || commandID !== id) return;
    const next = response.runs || {runs: []}, records = new Map();
    const ids = new Set(graphView.required(next));
    if (next.latest_run_id) ids.add(next.latest_run_id);
    for (const runID of ids) {
      const detail = await client.call('status', {command_id: id, run_id: runID});
      if (epoch !== generation || commandID !== id) return;
      if (detail.run) records.set(runID, detail.run);
    }
    runs = next; record = records.get(next.latest_run_id) || null;
    error = record ? null : {messageKey: 'err.localcmd.no_run'};
    renderMetadata();
    graphView.metadata(next, records);
    graphView.refresh();
  } catch (err) {
    if (epoch === generation && commandID === id) { error = err; renderMetadata(); }
  } finally {
    refreshing = false;
    scheduleRefresh();
  }
}

encoding.onchange = () => graphView.setEncoding(encoding.value);
const menu = byID('log-menu');
document.addEventListener?.('pointerdown', event => { if (menu.open && !menu.contains(event.target)) menu.open = false; });
menu.addEventListener('keydown', event => { if (event.key === 'Escape') menu.open = false; });
async function stopRun(runID) {
  await client.call('stop', {command_id: commandID, run_id: runID});
  void refresh();
}
stop.onclick = async () => {
  const ids = runs.runs.filter(runStoppable).map(run => run.run_id);
  if (stopping || !ids.length) return;
  stopping = true;
  renderMetadata();
  try {
    await client.call('stop_all', {command_id: commandID, run_ids: ids});
    error = null;
  } catch (err) { error = err; }
  finally { stopping = false; renderMetadata(); void refresh(); }
};

bridge.subscribe(message => {
  if (message.kind === 'init') {
    if (commandID !== message.command_id || epoch !== message.generation) { record = null; runs = {runs: []}; graphView.metadata(null); }
    epoch = message.generation; commandID = message.command_id; setLocale(message.locale);
    document.documentElement.dataset.theme = message.theme; applyI18n(); post('initialized'); void refresh();
  }
  if (message.kind === 'theme') document.documentElement.dataset.theme = message.theme;
  if (message.kind === 'locale') { setLocale(message.locale); applyI18n(); renderMetadata(); graphView.relocalize(); }
  if (message.kind === 'visibility') {
    visible = message.visible; graphView.setVisible(visible);
    if (visible) void refresh(); else clearTimeout(timer);
  }
});
post('ready');
