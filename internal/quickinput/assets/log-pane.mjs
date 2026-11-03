import {t, localizeError} from './i18n.mjs';
import {nodeName, nodeLabel, nodeState} from './log-node.mjs';
import {logReader} from './log-reader.mjs';
import {runStoppable, reasonLabel} from './command-run.mjs';
import {instanceLabel} from './log-graph.mjs';

const actionKeys = {first: 'common.command.first', previous: 'common.command.previous', next: 'common.command.next', latest: 'common.command.latest'};

export function logPane({read, commandID, record, nodeID = '', encoding, stop}) {
  const create = (tag, className) => {
    const element = document.createElement(tag);
    if (className) element.className = className;
    return element;
  };
  const root = create('section', 'log-pane'), header = create('div', 'log-pane-heading');
  const title = create('strong'), status = create('span', 'log-node-state');
  header.append(title, status);
  const stopButton = create('button', 'log-pane-stop'), charset = create('select');
  let stopping = false, stopError = null;
  for (const [value, key] of [['utf-8','utf8'], ['gbk','gbk'], ['big5','big5'], ['utf-16le','utf16']]) {
    const option = create('option'); option.value = value; option.dataset.key = `common.command.${key}`; charset.append(option);
  }
  charset.value = encoding;
  charset.onchange = () => { void reader.setEncoding(charset.value); };
  stopButton.type = 'button';
  stopButton.onclick = async () => {
    if (stopping || !runStoppable(run)) return;
    stopping = true; stopError = null; render();
    try { await stop?.(run.run_id); } catch (error) { stopError = error; }
    finally { stopping = false; render(); }
  };
  header.append(stopButton);
  const details = create('p', 'log-pane-details');
  const output = create('div', 'log-pane-output'), notice = create('p', 'log-pane-notice');
  output.tabIndex = 0;
  output.setAttribute('role', 'log');
  output.setAttribute('aria-live', 'off');
  notice.setAttribute('role', 'status');
  const toolbar = create('nav', 'log-pane-tools'), buttons = new Map();
  for (const action of ['first', 'previous', 'next', 'latest']) {
    const button = create('button');
    button.type = 'button';
    button.onclick = () => { void reader.navigate(action); };
    buttons.set(action, button);
    toolbar.append(button);
  }
  const following = create('span'), range = create('span');
  following.className = 'log-pane-follow';
  range.className = 'log-pane-range';
  toolbar.append(charset, following, range);
  root.append(header, details, notice, output, toolbar);
  let run = record, displayed = 0;
  const reader = logReader({read, commandID, runID: record.run_id, nodeID, encoding, change: render});
  const currentNode = () => nodeID ? run.nodes?.find(node => node.node_id === nodeID) : null;

  function append(chunks) {
    const fragment = document.createDocumentFragment();
    for (const chunk of chunks) {
      if (!chunk.text) continue;
      if (!nodeID) {
        const source = create('span', 'log-source');
        source.dataset.node = chunk.node_id;
        source.textContent = t('common.graph.log_prefix', {name: nodeName(run.nodes?.find(node => node.node_id === chunk.node_id))});
        fragment.append(source);
      }
      const text = create('span');
      text.dataset.stream = chunk.stream;
      text.textContent = chunk.text;
      fragment.append(text);
      displayed += chunk.text.length;
    }
    output.append(fragment);
  }

  function render(update = reader.state) {
    if (update.reset) { output.replaceChildren(); displayed = 0; }
    if (update.chunks) {
      append(update.chunks);
      if (update.follow) output.scrollTop = output.scrollHeight;
      else if (update.reset) output.scrollTop = 0;
    }
    const node = currentNode();
    const state = node || run;
    const facts = [reasonLabel(state), t('common.command.elapsed', {seconds: (state.elapsed_seconds ?? 0).toFixed(1)})];
    if (state.exit?.code !== undefined) facts.push(t('common.command.exit_code', {code: state.exit.code}));
    if (state.log_complete === false) facts.push(t('common.command.log_incomplete'));
    if (run.storage_error) facts.push(t('common.command.storage_error'));
    if (run.cleanup_pending) facts.push(t('common.command.cleanup_pending'));
    details.textContent = facts.filter(Boolean).join(' · ');
    title.textContent = instanceLabel(run) + ' · ' + (nodeID ? nodeName(node) : t('common.graph.all'));
    title.title = title.textContent;
    status.hidden = !nodeID;
    status.textContent = nodeID ? nodeLabel(node) : '';
    root.dataset.state = nodeID ? nodeState(node) : '';
    output.setAttribute('aria-label', title.textContent);
    const {page, loading, follow, error} = update;
    const stopLabel = t('common.command.stop'), stopDisabled = !stop || stopping || !runStoppable(run);
    if (stopButton.textContent !== stopLabel) stopButton.textContent = stopLabel;
    if (stopButton.disabled !== stopDisabled) stopButton.disabled = stopDisabled;
    charset.setAttribute('aria-label', t('common.command.encoding'));
    for (const option of charset.children) option.textContent = t(option.dataset.key);
    notice.textContent = stopError || error ? localizeError(stopError || error) : loading && !displayed ? t('common.graph.loading_logs') :
      !displayed ? t(node?.phase === 'waiting' || node?.phase === 'ready' ? 'common.graph.not_started' : 'common.command.empty_log') : '';
    notice.dataset.error = String(Boolean(error));
    notice.hidden = !notice.textContent;
    const disabled = {
      first: loading || !page || page.start === 0,
      previous: loading || !page || page.start === 0,
      next: loading || !page || page.end,
      latest: loading,
    };
    for (const [action, button] of buttons) {
      const label = t(actionKeys[action]);
      if (button.textContent !== label) button.textContent = label;
      if (button.disabled !== disabled[action]) button.disabled = disabled[action];
    }
    following.textContent = t(follow ? 'common.command.following' : 'common.command.paused');
    range.textContent = page ? t('common.command.range', {start: page.start, end: page.next}) : '';
    root.setAttribute('aria-busy', String(loading));
  }

  output.addEventListener('wheel', event => { if (event.deltaY < 0) reader.pause(); }, {passive: true});
  output.addEventListener('scroll', () => {
    if (output.scrollHeight - output.scrollTop - output.clientHeight > 40) reader.pause();
  }, {passive: true});
  reader.setRecord(currentNode() ?? run);
  render();
  return {
    element: root,
    metadata(value) { run = value; reader.setRecord(currentNode() ?? run); render(); },
    refresh: reader.refresh,
    setEncoding(value) { charset.value = value; return reader.setEncoding(value); },
    setVisible: reader.setVisible,
    dispose() { reader.dispose(); root.remove(); },
    relocalize() {
      render();
      for (const source of output.querySelectorAll('.log-source')) {
        source.textContent = t('common.graph.log_prefix', {name: nodeName(run.nodes?.find(node => node.node_id === source.dataset.node))});
      }
    },
  };
}
