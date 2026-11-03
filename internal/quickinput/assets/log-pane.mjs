import {t, localizeError} from './i18n.mjs';
import {nodeName, nodeLabel, nodeState} from './log-node.mjs';
import {logReader} from './log-reader.mjs';
import {logLineMerger, labelLines} from './log-model.mjs';
import {runStoppable, reasonLabel} from './command-run.mjs';

// Each pane follows the newest output by default. Scrolling up pauses it and
// offers "back to latest"; earlier/later pages are reached at the edges.
export function logPane({read, commandID, record, nodeID = '', encoding, stop}) {
  const create = (tag, className) => {
    const element = document.createElement(tag);
    if (className) element.className = className;
    return element;
  };
  const button = (className, action) => {
    const element = create('button', className);
    element.type = 'button';
    element.onclick = () => { void reader.navigate(action); };
    return element;
  };
  const root = create('section', 'log-pane'), header = create('div', 'log-pane-heading');
  const title = create('strong'), status = create('span', 'log-node-state');
  header.append(title, status);
  const stopButton = create('button', 'log-pane-stop');
  let stopping = false, stopError = null, multiple = false;
  stopButton.type = 'button';
  stopButton.onclick = async () => {
    if (stopping || !runStoppable(run)) return;
    stopping = true; stopError = null; render();
    try { await stop?.(run.run_id); } catch (error) { stopError = error; }
    finally { stopping = false; render(); }
  };
  header.append(stopButton);
  const details = create('p', 'log-pane-details');
  const body = create('div', 'log-pane-body');
  const output = create('div', 'log-pane-output'), notice = create('p', 'log-pane-notice');
  output.tabIndex = 0;
  output.setAttribute('role', 'log');
  output.setAttribute('aria-live', 'off');
  notice.setAttribute('role', 'status');
  const earlier = button('log-pane-earlier', 'previous'), later = button('log-pane-later', 'next');
  const latest = button('log-pane-latest', 'latest');
  body.append(earlier, output, later, latest);
  root.append(header, details, notice, body);
  let run = record, displayed = 0;
  const lines = logLineMerger();
  const reader = logReader({read, commandID, runID: record.run_id, nodeID, encoding, change: render});
  const currentNode = () => nodeID ? run.nodes?.find(node => node.node_id === nodeID) : null;

  const sourceLabel = nodeKey => t('common.graph.log_prefix', {name: nodeName(run.nodes?.find(node => node.node_id === nodeKey))});
  // One element per chunk, as before line labelling: labels are written into
  // the chunk's text, so dense short lines do not multiply DOM elements beyond
  // the reader's existing frame and character bounds. The pieces are kept for
  // relocalizing the labels in place.
  function labelledText(element) {
    return labelLines(element.logPiece, sourceLabel(element.dataset.node));
  }
  function append(chunks) {
    const fragment = document.createDocumentFragment();
    for (const chunk of chunks) {
      if (!chunk.text) continue;
      displayed += chunk.text.length;
      const text = create('span');
      text.dataset.stream = chunk.stream;
      if (nodeID) text.textContent = chunk.text; // Node panes show the raw stream.
      else {
        text.dataset.node = chunk.node_id;
        text.logPiece = lines.split(chunk);
        if (!text.logPiece) continue;
        text.textContent = labelledText(text);
      }
      fragment.append(text);
    }
    output.append(fragment);
  }
  // Only facts that need attention are shown; normal runs show none.
  function problems(state) {
    const facts = [];
    const failed = state.phase === 'finished' && state.result && state.result !== 'succeeded';
    if (failed) facts.push(reasonLabel(state));
    if (failed && state.exit?.code !== undefined && state.exit.code !== 0) facts.push(t('common.command.exit_code', {code: state.exit.code}));
    if (state.log_complete === false) facts.push(t('common.command.log_incomplete'));
    if (run.storage_error) facts.push(t('common.command.storage_error'));
    if (run.cleanup_pending) facts.push(t('common.command.cleanup_pending'));
    return facts.filter(Boolean);
  }
  function label(element, key) {
    const text = t(key);
    if (element.textContent !== text) element.textContent = text;
  }
  function render(update = reader.state) {
    if (update.reset) { output.replaceChildren(); displayed = 0; lines.reset(); }
    if (update.chunks) {
      append(update.chunks);
      if (update.follow) output.scrollTop = output.scrollHeight;
      else if (update.reset) output.scrollTop = 0;
    }
    const node = currentNode();
    details.textContent = problems(node || run).join(' · ');
    details.hidden = !details.textContent;
    const name = nodeID ? nodeName(node) : t('common.graph.all_steps');
    title.textContent = multiple ? t('common.command.attempt_name', {sequence: run.sequence, name}) : name;
    title.title = title.textContent;
    status.hidden = !nodeID;
    status.textContent = nodeID ? nodeLabel(node) : '';
    root.dataset.state = nodeID ? nodeState(node) : '';
    output.setAttribute('aria-label', title.textContent);
    const {page, loading, follow, error} = update;
    label(stopButton, 'common.command.stop');
    // Whole-command stop lives in the window header; panes stop their own run
    // only when that is narrower (a node, or one of several runs).
    stopButton.hidden = !stop || !runStoppable(run) || !(nodeID || multiple);
    if (stopButton.disabled !== stopping) stopButton.disabled = stopping;
    notice.textContent = stopError || error ? localizeError(stopError || error) : loading && !displayed ? t('common.graph.loading_logs') :
      !displayed ? t(node?.phase === 'waiting' || node?.phase === 'ready' ? 'common.graph.not_started' : 'common.command.empty_log') : '';
    notice.dataset.error = String(Boolean(stopError || error));
    notice.hidden = !notice.textContent;
    label(earlier, 'common.command.earlier');
    label(later, 'common.command.later');
    label(latest, 'common.command.latest');
    earlier.hidden = !page || page.start === 0;
    later.hidden = follow || !page || page.end;
    latest.hidden = follow;
    // Background reads must not rewrite unchanged controls (focus and flicker).
    for (const control of [earlier, later, latest]) if (control.disabled !== Boolean(loading)) control.disabled = Boolean(loading);
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
    setEncoding(value) { return reader.setEncoding(value); },
    setVisible: reader.setVisible,
    showAttempt(value) { if (multiple !== value) { multiple = value; render(); } },
    dispose() { reader.dispose(); root.remove(); },
    relocalize() {
      render();
      for (const element of output.querySelectorAll('span')) if (element.logPiece) element.textContent = labelledText(element);
    },
  };
}
