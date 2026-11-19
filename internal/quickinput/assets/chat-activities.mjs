import {t} from './i18n.mjs';
import {approvalCard} from './chat-approval.mjs';
const states={queued:'common.workspace.state.queued',started:'common.workspace.state.started',awaiting_approval:'common.workspace.state.awaiting_approval',completed:'common.workspace.state.completed',failed:'common.workspace.state.failed',denied:'common.workspace.state.denied',cancelled:'common.workspace.state.cancelled',unknown:'common.workspace.state.unknown',reconciled:'common.workspace.state.reconciled'};
const steps={pending:'common.workspace.step.pending',in_progress:'common.workspace.step.in_progress',completed:'common.workspace.step.completed'};

// The owning history message fixes this node's position. A result refresh only
// changes its status; it never appends the call to a separate activity list.
export function toolActivity(row, onApprove) {
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
  let current, signature, hasSummary = null, label, progress, card = null, lines = [];
  const space = document.createTextNode(' ');
  const localizeOwn = () => {
    const summary = typeof current.summary === 'string' ? current.summary : '';
    const stateKey = current.detail === 'approval_timeout'
      ? 'common.workspace.state.approval_timeout'
      : (states[current.state] ?? states.unknown);
    const stateLabel = t(stateKey);
    if (name.textContent !== current.tool) name.textContent = current.tool;
    if (target.textContent !== summary) target.textContent = summary;
    const nextStateText = ` · ${stateLabel}`;
    if (state.textContent !== nextStateText) state.textContent = nextStateText;
    const nextHasSummary = Boolean(summary);
    if (hasSummary !== nextHasSummary) {
      hasSummary = nextHasSummary;
      head.replaceChildren(...(nextHasSummary ? [name, space, target, state] : [name, state]));
    }
    const nextTitle = summary ? `${current.tool} ${summary} · ${stateLabel}` : '';
    if (head.title !== nextTitle) head.title = nextTitle;
    if (label) {
      const nextDeclared = t('common.workspace.progress_declared');
      if (label.textContent !== nextDeclared) label.textContent = nextDeclared;
    }
    lines.forEach(({node, step}) => {
      const nextLine = `${step.step} · ${t(steps[step.status] ?? steps.pending)}`;
      if (node.textContent !== nextLine) node.textContent = nextLine;
    });
  };
  item.relocalize = () => {
    localizeOwn();
    card?.relocalize?.();
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
    const showCard = current.state === 'awaiting_approval' && current.approval?.status === 'pending' && Boolean(current.approval?.id);
    if (showCard) {
      if (card && card.dataset?.approvalId === current.approval.id) {
        card.update(current.approval);
      } else {
        card?.remove();
        card = approvalCard(current.approval, onApprove);
        item.append(card);
      }
    } else if (card) {
      card.remove();
      card = null;
    }
    localizeOwn();
  };
  item.update(row);
  return item;
}
