import {webview2Bridge} from './webview2-bridge.mjs';
import {commandClient} from './command-client.mjs';
import {applyI18n, setLocale, t, localizeError, formatDateTime} from './i18n.mjs';
import {runStoppable, runLabel, reasonLabel} from './command-run.mjs';
import {nodeName, nodeLabel} from './log-node.mjs';
import {logGraphView} from './log-graph.mjs';
import {logPane} from './log-pane.mjs';
import {logReadQueue} from './log-reader.mjs';

const bridge = webview2Bridge(), byID = id => document.getElementById(id);
const stop = byID('log-stop'), encoding = byID('log-encoding');
let epoch = '', commandID = '', record = null, stopping = false, refreshing = false;
let timer = null, visible = true, error = null;
const post = (kind, fields = {}) => bridge.send({version: 1, kind, generation: epoch, ...fields});
const client = commandClient(bridge, post), read = logReadQueue(client.call);
const graphView = logGraphView({byID, createPane: (nodeID, next) =>
  logPane({read, commandID, record: next, nodeID, encoding: encoding.value})});

function renderMetadata() {
  graphView.metadata(record);
  byID('log-name').textContent = record?.command?.name || t('brand.log_window_title');
  byID('log-state').textContent = runLabel(record);
  stop.disabled = stopping || !runStoppable(record);
  const details = [];
  if (record?.started_at) details.push(t('common.command.started', {time: formatDateTime(record.started_at)}));
  if (record?.ended_at) details.push(t('common.command.ended', {time: formatDateTime(record.ended_at)}));
  if (record) details.push(t('common.command.elapsed', {seconds: record.elapsed_seconds.toFixed(1)}));
  if (record?.command?.timeout_seconds) details.push(t('common.command.timeout_value', {seconds: record.command.timeout_seconds}));
  if (record?.exit?.code !== undefined) details.push(t('common.command.exit_code', {code: record.exit.code}));
  if (reasonLabel(record)) details.push(reasonLabel(record));
  const failed = record?.nodes?.find(node => node.node_id === record.failed_node_id);
  if (failed) details.push(t('common.graph.failed_detail', {name: nodeName(failed), reason: reasonLabel(failed) || nodeLabel(failed)}));
  byID('log-details').replaceChildren(...details.map(text => { const element = document.createElement('span'); element.textContent = text; return element; }));
  const warnings = [error && localizeError(error), record?.log_complete === false && t('common.command.log_incomplete'),
    record?.storage_error && t('common.command.storage_error'), record?.cleanup_pending && t('common.command.cleanup_pending')].filter(Boolean);
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
    const response = await client.call('status', {command_id: id});
    if (epoch !== generation || commandID !== id) return;
    record = response.run ?? null;
    error = record ? null : {messageKey: 'err.localcmd.no_run'};
    renderMetadata();
    graphView.refresh();
  } catch (err) {
    if (epoch === generation && commandID === id) { error = err; renderMetadata(); }
  } finally {
    refreshing = false;
    scheduleRefresh();
  }
}

encoding.onchange = () => graphView.setEncoding(encoding.value);
stop.onclick = async () => {
  if (stopping || !runStoppable(record)) return;
  stopping = true;
  renderMetadata();
  const runID = record.run_id;
  try {
    const response = await client.call('stop', {command_id: commandID, run_id: runID});
    if (record?.run_id === runID) { record = response.run; error = null; }
  } catch (err) { if (record?.run_id === runID) error = err; }
  finally { stopping = false; renderMetadata(); void refresh(); }
};

bridge.subscribe(message => {
  if (message.kind === 'init') {
    if (commandID !== message.command_id || epoch !== message.generation) { record = null; graphView.metadata(null); }
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
