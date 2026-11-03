import {t, localizeError} from './i18n.mjs';
import {nodeName, nodeLabel, nodeState} from './log-node.mjs';
import {logReader} from './log-reader.mjs';

const actionKeys = {first: 'common.command.first', previous: 'common.command.previous', next: 'common.command.next', latest: 'common.command.latest'};

export function logPane({read, commandID, record, nodeID = '', encoding}) {
  const create = (tag, className) => {
    const element = document.createElement(tag);
    if (className) element.className = className;
    return element;
  };
  const root = create('section', 'log-pane'), header = create('div', 'log-pane-heading');
  const title = create('strong'), status = create('span', 'log-node-state');
  header.append(title, status);
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
    if (!nodeID) button.id = `log-${action}`;
    buttons.set(action, button);
    toolbar.append(button);
  }
  const following = create('span'), range = create('span');
  following.className = 'log-pane-follow';
  range.className = 'log-pane-range';
  toolbar.append(following, range);
  if (!nodeID) { output.id = 'log-output'; following.id = 'log-follow'; range.id = 'log-range'; }
  root.append(header, notice, output, toolbar);
  let run = record, displayed = 0;
  const reader = logReader({read, commandID, runID: record.run_id, nodeID, encoding, change: render});
  const currentNode = () => nodeID ? run.nodes?.find(node => node.node_id === nodeID) : null;

  function append(chunks) {
    const fragment = document.createDocumentFragment();
    for (const chunk of chunks) {
      if (!chunk.text) continue;
      if (!nodeID && run.command?.kind === 'graph') {
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
    title.textContent = nodeID ? nodeName(node) : t(run.command?.kind === 'graph' ? 'common.graph.all' : 'common.command.logs');
    title.title = title.textContent;
    status.hidden = !nodeID;
    status.textContent = nodeID ? nodeLabel(node) : '';
    root.dataset.state = nodeID ? nodeState(node) : '';
    output.setAttribute('aria-label', title.textContent);
    const {page, loading, follow, error} = update;
    notice.textContent = error ? localizeError(error) : loading && !displayed ? t('common.graph.loading_logs') :
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
    setEncoding: reader.setEncoding,
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
