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
