import {validDraft, normalizeNewlines, insertedText} from "./input-model.mjs";
const bridge = window.chrome.webview;
const input = document.querySelector("#input"), status = document.querySelector("#status");
let generation = "", revision = 0, composing = false, previous = "", pendingSnapshot = "";
let lastHeight = 240;
const send = (kind, extra = {}) => bridge.postMessage({version:1,kind,generation,...extra});
function commit() {
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
input.addEventListener("compositionstart", () => {composing = true;});
input.addEventListener("compositionend", () => {composing = false; commit();});
input.addEventListener("input", () => { if (!composing) commit(); });
input.addEventListener("beforeinput", event => {
  if (composing || event.isComposing || !event.inputType.startsWith("insert") || event.data === null) return;
  if (!validDraft(insertedText(input.value,input.selectionStart,input.selectionEnd,event.data))) {
    event.preventDefault(); status.textContent = "本次编辑超出 16,000 UTF-8 字节";
  }
});
input.addEventListener("paste", event => {
  const text = event.clipboardData.getData("text/plain");
  if (!validDraft(insertedText(input.value,input.selectionStart,input.selectionEnd,text))) {
    event.preventDefault(); status.textContent = "粘贴内容超出 16,000 UTF-8 字节，原草稿和剪贴板已保留";
  }
});
input.addEventListener("keydown", event => {
  if (event.key === "Escape" && !composing && !event.isComposing && event.keyCode !== 229) {
    event.preventDefault(); commit(); send("hide");
  }
});
document.addEventListener("dragover", event => event.preventDefault());
document.addEventListener("drop", event => event.preventDefault());
bridge.addEventListener("message", event => {
  const message = event.data;
  switch (message.kind) {
    case "init":
      generation = message.generation; revision = message.revision;
      input.value = previous = message.text; document.documentElement.dataset.theme = message.theme;
      break;
    case "theme": document.documentElement.dataset.theme = message.theme; break;
    case "focus": input.focus(); break;
    case "snapshot": pendingSnapshot = message.id; if (!composing) commit(); break;
    case "error": status.textContent = message.message; break;
  }
});
send("ready");
