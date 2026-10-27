export const maxDraftBytes = 16000;
export function validDraft(text) {
  return typeof text === "string" && !text.includes("\r") &&
    (typeof text.isWellFormed !== "function" || text.isWellFormed()) &&
    new TextEncoder().encode(text).length <= maxDraftBytes;
}
export function normalizeNewlines(text) { return text.replace(/\r\n?/g, "\n"); }
export function insertedText(value, start, end, text) {
  return value.slice(0, start) + normalizeNewlines(text) + value.slice(end);
}

// Some IMEs dispatch the candidate-confirming Enter just after compositionend.
// Suppress that same key gesture, and repeated keydown, before send/hide actions.
export function inputKeyAction(event, composing, compositionEndedAt, now) {
  if (composing || event.isComposing || event.keyCode === 229) return '';
  if (event.key === 'Enter') {
    if (event.repeat || now - compositionEndedAt < 50) return 'consume';
    if (!event.shiftKey && !event.altKey && !event.metaKey) return 'send';
  }
  return event.key === 'Escape' ? 'hide' : '';
}
