import test from "node:test";
import assert from "node:assert/strict";
import {desktopSettings} from "../web/desktop-settings.mjs";
import {domFixture, deferred} from "./web-fixture.mjs";

function fixture(post) {
  class Element {
    constructor() { this.listeners = {}; this.options = []; this.hidden = false; this.value = ""; this.checked = false; }
    addEventListener(name, fn) { this.listeners[name] = fn; this["on"+name] = fn; }
    append(option) { this.options.push(option); }
    focus() {}
  }
  const elements = new Map();
  const select = id => { if (!elements.has(id)) elements.set(id, new Element()); return elements.get(id); };
  const fields = {};
  for (const name of ["ctrl", "alt", "shift", "key", "enabled", "theme"]) fields[name] = name === "key" ? select("#desktop-key") : new Element();
  select("#desktop-form").elements = fields;
  globalThis.document = {querySelector:select, createElement:() => new Element()};
  const events = {};
  globalThis.window = {addEventListener:(name, fn) => events[name] = fn};
  const current = {supported:true, registration:"registered", surface:"ready", cleanup_pending:0,
    desired:{revision:"initial", enabled:true, binding:{modifiers:["ctrl", "alt"], key:"KeyJ"}, theme_id:"star"}};
  const component = desktopSettings(async () => current, post);
  return {elements, fields, events, component};
}
const tick = () => new Promise(resolve => setImmediate(resolve));

for (const outcome of ["success", "failure"]) test(`late poll ${outcome} cannot overwrite or cancel a newer recording`, async t => {
  const polls = new Map(), cancelled = [];
  let starts = 0;
  const ui = fixture(async (path, body) => {
    if (path.endsWith("/start")) return {recording_id:"lease"+(++starts)};
    if (path.endsWith("/cancel")) { cancelled.push(body.recording_id); return {}; }
    if (path.endsWith("/poll")) return new Promise((resolve, reject) => polls.set(body.recording_id, {resolve, reject}));
    throw new Error(path);
  });
  t.after(async () => { ui.events.blur(); await tick(); for (const [id, reply] of polls) reply.resolve({recording_id:id}); await tick(); });
  await ui.component.open();
  await ui.elements.get("#desktop-record-start").onclick();
  ui.elements.get("#desktop-record-cancel").onclick(); await tick();
  await ui.elements.get("#desktop-record-start").onclick();
  if (outcome === "success") polls.get("lease1").resolve({recording_id:"lease1", binding:{modifiers:["ctrl", "alt"], key:"KeyX"}});
  else polls.get("lease1").reject(new Error("late failure from an ended lease"));
  await tick();
  assert.equal(ui.fields.key.value, "KeyJ");
  assert.deepEqual(cancelled, ["lease1"]);
});

test("blur before start response releases the late lease without recording", async t => {
  let resolveStart;
  const cancelled = [], calls = [];
  const ui = fixture(async (path, body) => {
    calls.push(path);
    if (path.endsWith("/start")) return new Promise(resolve => resolveStart = resolve);
    if (path.endsWith("/cancel")) { cancelled.push(body.recording_id); return {}; }
    if (path.endsWith("/poll")) return new Promise(() => {});
    throw new Error(path);
  });
  t.after(async () => { ui.events.blur(); await tick(); });
  await ui.component.open();
  const starting = ui.elements.get("#desktop-record-start").onclick();
  await tick(); ui.events.blur(); await tick();
  resolveStart({recording_id:"blurred-lease"});
  await starting; await tick();
  assert.deepEqual(cancelled, ["blurred-lease"]);
  assert.equal(calls.filter(path => path.endsWith("/poll")).length, 0);
});

test("a new recording waits until the previous cancellation finishes", async t => {
  let starts = 0, resolveCancel;
  const polls = new Map();
  const ui = fixture(async (path, body) => {
    if (path.endsWith("/start")) return {recording_id:"lease"+(++starts)};
    if (path.endsWith("/cancel") && body.recording_id === "lease1") return new Promise(resolve => resolveCancel = resolve);
    if (path.endsWith("/cancel")) return {};
    if (path.endsWith("/poll")) return new Promise(resolve => polls.set(body.recording_id, resolve));
    throw new Error(path);
  });
  t.after(async () => { resolveCancel?.({}); ui.events.blur(); await tick(); for (const [id, resolve] of polls) resolve({recording_id:id}); await tick(); });
  await ui.component.open();
  await ui.elements.get("#desktop-record-start").onclick();
  polls.get("lease1")({recording_id:"lease1", binding:{modifiers:["ctrl", "alt"], key:"KeyK"}});
  await tick();
  const starting = ui.elements.get("#desktop-record-start").onclick();
  await tick();
  assert.equal(starts, 1);
  resolveCancel({}); await starting; await tick();
  assert.equal(starts, 2);
  assert.equal(ui.elements.get("#desktop-record-status").textContent, "请按组合键，30 秒内有效。Esc 取消。");
});

for (const confirmed of [false,true]) test(`page resume reconciles a pending desktop save (confirmed=${confirmed})`, async () => {
  const dom = domFixture(), replies = [], writes = [];
  globalThis.document = dom.document; globalThis.window = new EventTarget();
  let scope = {active:true}, reads = 0;
  const current = {supported:true,registration:"registered",surface:"ready",desired:{revision:"initial",enabled:true,binding:{modifiers:["ctrl","alt"],key:"KeyJ"},theme_id:"star"}};
  const component = desktopSettings(async () => { reads++; return structuredClone(current); }, async (path, body) => {
    assert.equal(path,"/api/desktop/settings");
    const reply = deferred(); replies.push(reply); writes.push(structuredClone(body)); return reply.promise;
  }, {session:() => scope});
  await component.open();
  dom.select("#desktop-form").elements.theme.value = "frost";
  dom.select("#desktop-form").fire("submit"); await tick();
  assert.equal(dom.select("#desktop-fields").disabled,true);
  scope.active = false; component.stop(); scope = {active:true};
  if (confirmed) {
    current.last_operation = writes[0].operation_id;
    current.desired.revision = "confirmed";
    current.desired.theme_id = "frost";
  }
  await component.open();
  assert.equal(reads,2);
  assert.equal(dom.select("#desktop-fields").disabled,false);
  assert.equal(dom.select("#desktop-save").textContent,confirmed ? "保存并应用" : "重试上次保存");
  dom.select("#desktop-form").fire("submit"); await tick();
  assert.equal(writes.length,2);
  if (!confirmed) assert.deepEqual(writes[1],writes[0]);
  else assert.notEqual(writes[1].operation_id,writes[0].operation_id);
  replies[0].resolve({...current,registration:"old response"}); await tick();
  // The ended transaction must not unlock the new in-flight transaction.
  dom.select("#desktop-form").fire("submit"); await tick();
  assert.equal(writes.length,2);
  assert.equal(dom.select("#desktop-fields").disabled,true);
  replies[1].resolve(current); await tick();
  assert.equal(dom.select("#desktop-fields").disabled,false);
  assert.doesNotMatch(dom.select("#desktop-state").textContent,/old response/);
  component.stop();
});
