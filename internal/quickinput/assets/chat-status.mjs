import {turnStatus, awaitingReply} from './chat-model.mjs';

// Business state controls the animation. Text is never parsed to infer a turn.
export function replyStatusView(container) {
  const label = document.createElement('span');
  const dots = document.createElement('span');
  dots.className = 'reply-dots';
  dots.setAttribute('aria-hidden', 'true');
  container.replaceChildren(label, dots);
  let visible = false, replying = false;
  const update = () => { dots.hidden = !visible || !replying; dots.textContent = replying ? '...' : ''; };
  function message(text, active = false) {
    // Avoid replacing the live-region text on every reconciliation tick.
    if (label.textContent !== text) label.textContent = text;
    container.title = text;
    replying = active;
    update();
  }
  update();
  return {
    message,
    turn(turn, suffix = '', submitting = false) {
      message([turnStatus(turn, submitting), suffix].filter(Boolean).join(' · '),
        submitting || awaitingReply(turn));
    },
    visible(value) { visible = value; update(); },
    dispose() { visible = false; replying = false; update(); },
  };
}
