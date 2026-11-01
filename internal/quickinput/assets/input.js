import {validDraft, normalizeNewlines, insertedText, inputKeyAction} from "./input-model.mjs";
import {webview2Bridge} from "./webview2-bridge.mjs";
import {chatPanel} from "./chat-ui.mjs";
export function inputEditor(bridge) {
  const cleanup = [];
  const listen = (target, event, callback) => {
    target.addEventListener(event, callback);
    cleanup.push(() => target.removeEventListener(event, callback));
  };
  const input = document.querySelector("#input"), status = document.querySelector("#status");
  input.disabled = true;
  let generation = "", revision = 0, composing = false, previous = "", pendingSnapshot = "";
  let lastHeight = 240;
  let staged = null;
  let expanded = false, submit = () => {};
  let compositionEndedAt = -Infinity;
  const send = (kind, extra = {}) => bridge.send({version:1,kind,generation,...extra});
  function commit(edited = false) {
    if (!generation) return;
    const text = normalizeNewlines(input.value);
    if (!validDraft(text)) {
      input.value = staged ? "" : previous;
      status.textContent = "本次编辑超出 16,000 UTF-8 字节，已保留原草稿";
      if (pendingSnapshot) { send("snapshot",{id:pendingSnapshot,text:previous,revision}); pendingSnapshot = ""; }
      return;
    }
    // Hiding a submitted draft is presentation only until the host confirms receipt.
    // A real edit, including retyping identical text, starts a distinct revision.
    const changedAfterSend = Boolean(staged && (edited || text));
    if (changedAfterSend) staged = null;
    if (!staged && (changedAfterSend || text !== previous)) {
      previous = text; revision++; send("draft_updated",{text,revision});
    }
    status.textContent = "";
    const height = expanded ? 760 : Math.max(240, Math.min(480, 164 + Math.min(12, input.value.split("\n").length) * 30));
    if (height !== lastHeight) { lastHeight = height; send("resize",{height}); }
    if (pendingSnapshot) { send("snapshot",{id:pendingSnapshot,text:previous,revision}); pendingSnapshot = ""; }
  }
  listen(input, "compositionstart", () => {composing = true;});
  listen(input, "compositionend", () => {composing = false; compositionEndedAt = performance.now(); commit(true);});
  listen(input, "input", () => { if (!composing) commit(true); });
  listen(input, "beforeinput", event => {
    if (composing || event.isComposing || !event.inputType.startsWith("insert") || event.data === null) return;
    if (!validDraft(insertedText(input.value,input.selectionStart,input.selectionEnd,event.data))) {
      event.preventDefault(); status.textContent = "本次编辑超出 16,000 UTF-8 字节";
    }
  });
  listen(input, "paste", event => {
    const text = event.clipboardData.getData("text/plain");
    if (!validDraft(insertedText(input.value,input.selectionStart,input.selectionEnd,text))) {
      event.preventDefault(); status.textContent = "粘贴内容超出 16,000 UTF-8 字节，原草稿和剪贴板已保留";
    }
  });
  listen(input, "keydown", event => {
    const action = inputKeyAction(event, composing, compositionEndedAt, performance.now());
    if (!action) return;
    event.preventDefault();
    if (action === 'consume') return;
    commit();
    if (action === 'send') submit();
    else send("hide");
  });
  listen(document, "dragover", event => event.preventDefault());
  listen(document, "drop", event => event.preventDefault());
  const unsubscribe = bridge.subscribe(message => {
    switch (message.kind) {
      case "init":
        generation = message.generation; revision = message.revision; staged = null;
        input.value = previous = message.text; document.documentElement.dataset.theme = message.theme;
        input.disabled = false;
        send("initialized");
        break;
      case "theme": document.documentElement.dataset.theme = message.theme; break;
      case "focus": input.focus(); break;
      case "snapshot": pendingSnapshot = message.id; if (!composing) commit(); break;
      case "error": status.textContent = message.message; break;
    }
  });
  function dispose() { unsubscribe(); for (const remove of cleanup) remove(); }
  listen(window, "pagehide", dispose);
  send("ready");
  return {
    dispose,
    post: send,
    onSubmit(callback) { submit = callback; },
    snapshot() { if (!generation || composing) return null; commit(); return {text: staged ? "" : previous, revision}; },
    stageSubmission(operation, snapshot) {
      if (composing || staged || revision !== snapshot.revision || previous !== snapshot.text) return false;
      staged = {operation, revision};
      input.value = "";
      return true;
    },
    restoreSubmission(operation) {
      if (composing || staged?.operation !== operation || staged.revision !== revision) return false;
      staged = null;
      input.value = previous;
      return true;
    },
    restoreQueued(text, expected) {
      if (!generation || composing || staged || revision !== expected || input.value) return false;
      input.value = text;
      commit();
      input.focus();
      return true;
    },
    clearIfRevision(expected) {
      if (composing || revision !== expected) return false;
      staged = null;
      input.value = "";
      commit();
      return true;
    },
    setExpanded(value) { expanded = value; commit(); },
  };
}
const bridge = webview2Bridge();
export const editor = inputEditor(bridge);
export const chat = chatPanel(bridge, editor);
