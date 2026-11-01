import {validDraft, normalizeNewlines, insertedText, inputKeyAction} from "./input-model.mjs";
import {webview2Bridge} from "./webview2-bridge.mjs";
import {chatPanel} from "./chat-ui.mjs";
import {ComposerLayout} from "./composer-layout.mjs";
import {commandPanel} from "./command-ui.mjs";
import {quotedCommand} from "./command-model.mjs";
export function inputEditor(bridge) {
  const cleanup = [];
  const listen = (target, event, callback) => {
    target.addEventListener(event, callback);
    cleanup.push(() => target.removeEventListener(event, callback));
  };
  const input = document.querySelector("#input"), status = document.querySelector("#status");
  input.disabled = true;
  let generation = "", revision = 0, composing = false, previous = "", pendingSnapshot = "";
  let staged = null;
  let submit = () => {};
  let compositionEndedAt = -Infinity;
  const send = (kind, extra = {}) => bridge.send({version:1,kind,generation,...extra});
  const layout = new ComposerLayout(input, (height, id) => { if (generation) send('resize', {height, id}); });
  let commands;
  function commit(edited = false) {
    if (!generation) return;
    const text = normalizeNewlines(input.value);
    if (!validDraft(text)) {
      input.value = staged ? "" : previous;
      layout.schedule();
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
    layout.schedule(edited);
    if (pendingSnapshot) { send("snapshot",{id:pendingSnapshot,text:previous,revision}); pendingSnapshot = ""; }
    commands?.changed();
  }
  listen(input, "compositionstart", () => {composing = true;});
  listen(input, "compositionend", () => {composing = false; compositionEndedAt = performance.now(); commit(true);});
  listen(input, "input", () => { if (!composing) commit(true); else layout.schedule(true); });
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
    if (!composing && !event.isComposing && event.keyCode !== 229 && commands.key(event)) { event.preventDefault(); return; }
    if (!action) return;
    event.preventDefault();
    if (action === 'consume') return;
    commit();
    if (action === 'send') { if (!commands.submit()) submit(); }
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
        layout.flush();
        send("initialized");
        break;
      case "theme": document.documentElement.dataset.theme = message.theme; layout.schedule(); break;
      case "layout_applied":
        if (message.generation === generation) layout.applied(message.id, message.height);
        break;
      case "visibility": layout.setVisible(message.visible); break;
      case "focus": layout.setVisible(true); input.focus(); break;
      case "snapshot": pendingSnapshot = message.id; if (!composing) commit(); break;
      case "error": status.textContent = message.message; break;
    }
  });
  commands = commandPanel(bridge, {input, post:send, changed:() => layout.schedule(), sendText:() => {
    const text = quotedCommand(input.value);
    if (composing || !validDraft(text)) { status.textContent = '请先完成输入法候选，或缩短待引用文本'; return; }
    input.value = text; commit(true); commands.changed(); submit();
  }});
  function dispose() { unsubscribe(); commands.dispose(); layout.dispose(); for (const remove of cleanup) remove(); }
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
      layout.schedule();
      commands.changed();
      return true;
    },
    restoreSubmission(operation) {
      if (composing || staged?.operation !== operation || staged.revision !== revision) return false;
      staged = null;
      input.value = previous;
      layout.schedule();
      commands.changed();
      return true;
    },
    clearIfRevision(expected) {
      if (composing || revision !== expected) return false;
      staged = null;
      input.value = "";
      commit();
      return true;
    },
    setExpanded(value) { layout.setExpanded(value); },
    commandSubmit() { return commands.submit(); },
  };
}
const bridge = webview2Bridge();
export const editor = inputEditor(bridge);
export const chat = chatPanel(bridge, editor);
