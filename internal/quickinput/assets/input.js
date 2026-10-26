import {validDraft, normalizeNewlines, insertedText} from "./input-model.mjs";
import {webview2Bridge} from "./webview2-bridge.mjs";
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
  const send = (kind, extra = {}) => bridge.send({version:1,kind,generation,...extra});
  function commit() {
    if (!generation) return;
    const text = normalizeNewlines(input.value);
    if (!validDraft(text)) {
      input.value = previous;
      status.textContent = "本次编辑超出 16,000 UTF-8 字节，已保留原草稿";
      if (pendingSnapshot) { send("snapshot",{id:pendingSnapshot,text:previous,revision}); pendingSnapshot = ""; }
      return;
    }
    if (text !== previous) { previous = text; revision++; send("draft_updated",{text,revision}); }
    status.textContent = "草稿仅保留在本次运行中";
    const height = Math.max(240, Math.min(480, 164 + Math.min(12, input.value.split("\n").length) * 30));
    if (height !== lastHeight) { lastHeight = height; send("resize",{height}); }
    if (pendingSnapshot) { send("snapshot",{id:pendingSnapshot,text,revision}); pendingSnapshot = ""; }
  }
  listen(input, "compositionstart", () => {composing = true;});
  listen(input, "compositionend", () => {composing = false; commit();});
  listen(input, "input", () => { if (!composing) commit(); });
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
    if (event.key === "Escape" && !composing && !event.isComposing && event.keyCode !== 229) {
      event.preventDefault(); commit(); send("hide");
    }
  });
  listen(document, "dragover", event => event.preventDefault());
  listen(document, "drop", event => event.preventDefault());
  const unsubscribe = bridge.subscribe(message => {
    switch (message.kind) {
      case "init":
        generation = message.generation; revision = message.revision;
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
  return {dispose};
}
export const editor = inputEditor(webview2Bridge());
