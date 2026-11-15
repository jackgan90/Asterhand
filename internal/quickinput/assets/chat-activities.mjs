import {t} from './i18n.mjs';
const states={queued:'common.workspace.state.queued',started:'common.workspace.state.started',completed:'common.workspace.state.completed',failed:'common.workspace.state.failed',cancelled:'common.workspace.state.cancelled',unknown:'common.workspace.state.unknown',reconciled:'common.workspace.state.reconciled'};
const steps={pending:'common.workspace.step.pending',in_progress:'common.workspace.step.in_progress',completed:'common.workspace.step.completed'};

// The owning history message fixes this node's position. A result refresh only
// changes its status; it never appends the call to a separate activity list.
export function toolActivity(row) {
  const item = document.createElement('div');
  item.className = 'tool-activity';
  const head = document.createElement('div');
  head.className = 'tool-activity-head';
  const name = document.createElement('span');
  name.className = 'tool-activity-name';
  const target = document.createElement('span');
  target.className = 'tool-activity-target';
  const state = document.createElement('span');
  state.className = 'tool-activity-state';
  item.append(head);
  let current, signature, label, progress, lines = [];
  item.relocalize = () => {
    const summary = typeof current.summary === 'string' ? current.summary : '';
    const stateLabel = t(states[current.state] ?? states.unknown);
    name.textContent = current.tool;
    target.textContent = summary;
    state.textContent = ` · ${stateLabel}`;
    head.replaceChildren(...(summary ? [name, document.createTextNode(' '), target, state] : [name, state]));
    head.title = summary ? `${current.tool} ${summary} · ${stateLabel}` : '';
    if (label) label.textContent = t('common.workspace.progress_declared');
    lines.forEach(({node, step}) => { node.textContent = `${step.step} · ${t(steps[step.status] ?? steps.pending)}`; });
  };
  item.update = value => {
    current = value;
    item.dataset.callId = current.id;
    item.dataset.state = current.state;
    const next = JSON.stringify(current.progress ?? []);
    if (signature !== next) {
      signature = next;
      label?.remove(); progress?.remove();
      label = progress = null; lines = [];
      if (current.progress?.length) {
        label = document.createElement('small');
        progress = document.createElement('ul');
        for (const step of current.progress) {
          const node = document.createElement('li');
          lines.push({node, step}); progress.append(node);
        }
        item.append(label, progress);
      }
    }
    item.relocalize();
  };
  item.update(row);
  return item;
}
