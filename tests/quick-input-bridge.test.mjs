import test from "node:test";
import assert from "node:assert/strict";
import {webview2Bridge} from "../internal/quickinput/assets/webview2-bridge.mjs";
import {domFixture} from "./web-fixture.mjs";

test("bridge sends unchanged envelopes and releases its exact message listener", () => {
  const host = new EventTarget(), sent = [], received = [];
  host.postMessage = message => sent.push(message);
  const bridge = webview2Bridge(host);
  const release = bridge.subscribe(message => received.push(message));
  const message = {version:1,kind:"draft_updated",generation:"generation",text:"text",revision:1};
  bridge.send(message);
  const event = new Event("message"); event.data = message;
  host.dispatchEvent(event); release(); host.dispatchEvent(event);
  assert.deepEqual(sent,[message]); assert.deepEqual(received,[message]);
});

test("editor retains handshake, IME snapshot order, generation, theme, size guard and hide", async t => {
  const dom = domFixture(), host = new EventTarget(), sent = [];
  host.postMessage = message => sent.push(message);
  globalThis.document = dom.document;
  globalThis.window = Object.assign(new EventTarget(), {chrome:{webview:host}});
  const {editor} = await import("../internal/quickinput/assets/input.js");
  t.after(() => { editor.dispose(); delete globalThis.document; delete globalThis.window; });
  const receive = data => { const event = new Event("message"); event.data = data; host.dispatchEvent(event); };
  assert.deepEqual(sent[0],{version:1,kind:"ready",generation:""});
  receive({kind:"init",generation:"current",revision:4,text:"old",theme:"star"});
  const input = dom.select("#input");
  input.fire("compositionstart"); input.value = "new\r\n中文";
  receive({kind:"snapshot",id:"snapshot-id"});
  assert.equal(sent.length,1);
  input.fire("compositionend");
  assert.deepEqual(sent.slice(1).map(message => message.kind),["draft_updated","snapshot"]);
  assert.equal(sent.at(-1).revision,5); assert.equal(sent.at(-1).text,"new\n中文");
  assert.equal(sent.at(-1).generation,"current");
  receive({kind:"theme",theme:"frost"}); assert.equal(dom.document.documentElement.dataset.theme,"frost");
  input.value = "😀".repeat(4001); input.fire("input"); assert.equal(input.value,"new\n中文");
  const escape = new Event("keydown",{cancelable:true}); escape.key = "Escape"; input.dispatchEvent(escape);
  assert.equal(sent.at(-1).kind,"hide");
  editor.dispose(); receive({kind:"theme",theme:"star"}); assert.equal(dom.document.documentElement.dataset.theme,"frost");
});
