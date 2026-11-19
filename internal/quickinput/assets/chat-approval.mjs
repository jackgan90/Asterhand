import {t} from './i18n.mjs';

function titleKey(kind) {
  if (kind === 'folder') return 'quickinput.approval.ask_folder';
  if (kind === 'command') return 'quickinput.approval.ask_command';
  return 'quickinput.approval.ask_system';
}

function makePill(text, tooltip = '') {
  const pill = document.createElement('code');
  pill.className = 'tool-approval-pill';
  pill.textContent = text;
  if (tooltip) pill.title = tooltip;
  return pill;
}

function buttonSpecs(current, submitting) {
  if (submitting) {
    return [{decision: 'submitting', key: 'quickinput.approval.submitting', primary: true, disabled: true}];
  }
  const specs = [];
  if (current.can_allow_session) {
    specs.push({decision: 'allow_session', key: 'quickinput.approval.allow_session', primary: true});
  }
  specs.push({decision: 'allow_once', key: 'quickinput.approval.allow_once', primary: !current.can_allow_session});
  if (current.can_allow_always) {
    specs.push({decision: 'allow_always', key: 'quickinput.approval.allow_always'});
  }
  specs.push({decision: 'deny', key: 'quickinput.approval.deny'});
  return specs;
}

export function approvalCard(initialApproval, onDecision) {
  const box = document.createElement('div');
  box.className = 'tool-approval-card';
  const head = document.createElement('div');
  head.className = 'tool-approval-head';
  const title = document.createElement('span');
  title.className = 'tool-approval-title';
  const reason = document.createElement('p');
  reason.className = 'tool-approval-reason';
  const note = document.createElement('p');
  note.className = 'tool-approval-note';
  const details = document.createElement('details');
  details.className = 'tool-approval-details';
  const summary = document.createElement('summary');
  const pre = document.createElement('code');
  pre.className = 'tool-approval-cmd';
  details.append(summary, pre);
  const actions = document.createElement('div');
  actions.className = 'tool-approval-actions';
  box.append(head, reason, note, details, actions);

  let current = initialApproval;
  let submitting = false;
  let pillSignature = null;
  const buttonsByDecision = new Map();

  const getButton = decision => {
    let btn = buttonsByDecision.get(decision);
    if (btn) return btn;
    btn = document.createElement('button');
    btn.type = 'button';
    btn.dataset.decision = decision;
    btn.addEventListener('click', () => {
      if (submitting || !onDecision || btn.disabled || decision === 'submitting') return;
      submitting = true;
      renderButtons();
      Promise.resolve(onDecision(current.id, decision)).catch(() => {
        submitting = false;
        renderButtons();
      });
    });
    buttonsByDecision.set(decision, btn);
    return btn;
  };

  function renderButtons() {
    const specs = buttonSpecs(current, submitting);
    const nextButtons = specs.map(spec => {
      const btn = getButton(spec.decision);
      const label = t(spec.key);
      if (btn.textContent !== label) btn.textContent = label;
      btn.classList.toggle('tool-approval-primary', Boolean(spec.primary));
      btn.disabled = Boolean(spec.disabled) || submitting || !onDecision;
      return btn;
    });
    const sameOrder = actions.children.length === nextButtons.length &&
      nextButtons.every((btn, idx) => actions.children[idx] === btn);
    if (!sameOrder) {
      actions.replaceChildren(...nextButtons);
    }
  }

  box.relocalize = () => {
    const nextTitle = t(titleKey(current.kind));
    if (title.textContent !== nextTitle) title.textContent = nextTitle;
    const nextNote = t('quickinput.approval.scope_note');
    if (note.textContent !== nextNote) note.textContent = nextNote;
    const nextSummary = t('quickinput.approval.view_command');
    if (summary.textContent !== nextSummary) summary.textContent = nextSummary;
    renderButtons();
  };

  box.update = value => {
    if (current && value && current.id !== value.id) submitting = false;
    current = value;
    if (box.dataset.approvalId !== current.id) box.dataset.approvalId = current.id;
    const targets = Array.isArray(current.targets) ? current.targets.filter(Boolean) : [];
    const nextPillSignature = JSON.stringify([current.kind || '', current.target || '', targets]);
    if (pillSignature !== nextPillSignature) {
      pillSignature = nextPillSignature;
      const pills = current.kind === 'folder' && targets.length > 0
        ? targets.map(target => makePill(target))
        : current.target
          ? [makePill(current.target, targets[0] && targets[0] !== current.target ? targets[0] : '')]
          : [];
      head.replaceChildren(title, ...pills);
    }
    const nextReason = current.reason || '';
    if (reason.textContent !== nextReason) reason.textContent = nextReason;
    reason.hidden = !nextReason;
    const nextCommand = current.command_preview || '';
    details.hidden = !nextCommand;
    if (pre.textContent !== nextCommand) pre.textContent = nextCommand;
    box.relocalize();
  };

  box.update(initialApproval);
  return box;
}
