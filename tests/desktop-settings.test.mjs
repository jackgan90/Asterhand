import test from "node:test";
import assert from "node:assert/strict";
import {desktopSettings} from "../web/desktop-settings.mjs";
import {setLocale} from "../web/i18n.mjs";
import {domFixture, deferred} from "./web-fixture.mjs";

setLocale("zh-CN");

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
  assert.equal(ui.elements.get("#desktop-record-status").textContent, "请按下新的组合键…（Esc 取消）");
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

for (const outcome of ["saved", "rejected"]) test(`theme applies on selection without unsaved shortcut edits (${outcome})`, async () => {
  const dom = domFixture(), writes = [];
  globalThis.document = dom.document; globalThis.window = new EventTarget();
  const radios = ["star", "frost"].map(value => Object.assign(dom.document.createElement("input"), {value}));
  dom.document.querySelectorAll = selector => selector.includes("ui-theme") ? radios : [];
  let current = {supported:true,registration:"registered",surface:"ready",desired:{revision:"initial",enabled:true,binding:{modifiers:["ctrl","alt"],key:"KeyJ"},theme_id:"star"}};
  const component = desktopSettings(async () => structuredClone(current), async (path, body) => {
    assert.equal(path, "/api/desktop/settings");
    writes.push(structuredClone(body));
    if (outcome === "rejected" && writes.length === 1) throw Object.assign(new Error("rejected"), {status:409});
    current = {...current, last_operation:body.operation_id, desired:{...current.desired, revision:"r"+writes.length, binding:body.binding, enabled:body.enabled, theme_id:body.theme_id}};
    return structuredClone(current);
  });
  await component.open();
  assert.equal(radios[0].checked, true);
  dom.select("#desktop-form").elements.key.value = "KeyK"; // unsaved shortcut edit
  radios[1].checked = true; radios[1].fire("change"); await tick(); await tick();
  assert.deepEqual(writes[0].binding, {modifiers:["ctrl","alt"], key:"KeyJ"}, "theme save must keep the saved shortcut");
  assert.equal(writes[0].theme_id, "frost");
  assert.equal(writes[0].expected_revision, "initial");
  if (outcome === "rejected") {
    assert.equal(radios[0].checked, true, "a rejected theme reverts the selection");
    assert.equal(dom.document.documentElement.dataset.theme, "star");
    assert.notEqual(dom.select("#theme-status").textContent, "");
    component.stop();
    return;
  }
  assert.equal(dom.document.documentElement.dataset.theme, "frost");
  dom.select("#desktop-form").fire("submit"); await tick(); await tick();
  assert.equal(writes[1].expected_revision, "r1", "a later shortcut save follows the theme revision");
  assert.equal(writes[1].binding.key, "KeyK");
  assert.equal(writes[1].theme_id, "frost");
  component.stop();
});

for (const writer of ["this page", "another page"]) test(`a lost theme response is reconciled by operation identity (${writer})`, async () => {
  const dom = domFixture(), writes = [];
  globalThis.document = dom.document; globalThis.window = new EventTarget();
  const radios = ["star", "frost"].map(value => Object.assign(dom.document.createElement("input"), {value}));
  dom.document.querySelectorAll = selector => selector.includes("ui-theme") ? radios : [];
  let current = {supported:true,registration:"registered",surface:"ready",desired:{revision:"r1",enabled:true,binding:{modifiers:["ctrl","alt"],key:"KeyJ"},theme_id:"star"}};
  const commit = (body, operation) => {
    current = {...current, last_operation:operation, desired:{...current.desired, revision:"r"+(Number(current.desired.revision.slice(1))+1), binding:body.binding, enabled:body.enabled, theme_id:body.theme_id}};
  };
  const component = desktopSettings(async () => structuredClone(current), async (path, body) => {
    writes.push(structuredClone(body));
    if (writes.length === 1) {
      // The server applies frost, but this page never receives the response.
      commit(body, writer === "this page" ? body.operation_id : "other-page-operation");
      throw new Error("connection reset");
    }
    if (body.expected_revision !== current.desired.revision) throw Object.assign(new Error("revision conflict"), {status:409});
    commit(body, body.operation_id);
    return structuredClone(current);
  });
  await component.open();
  dom.select("#desktop-form").elements.key.value = "KeyK"; // unsaved shortcut edit
  radios[1].checked = true; radios[1].fire("change"); await tick(); await tick(); await tick();
  assert.equal(dom.document.documentElement.dataset.theme, "frost");
  assert.equal(radios[1].checked, true);
  dom.select("#desktop-form").fire("submit"); await tick(); await tick(); await tick();
  if (writer === "this page") {
    assert.equal(dom.select("#theme-status").textContent, "同时应用于快捷面板和此页面。");
    assert.equal(writes[1].expected_revision, "r2", "the reconciled save advances the shortcut baseline");
    assert.equal(writes[1].theme_id, "frost");
    assert.equal(current.desired.binding.key, "KeyK", "the unsaved shortcut edit is kept and saved");
  } else {
    assert.equal(writes[1].expected_revision, "r1", "another page's save keeps conflict protection");
    assert.equal(current.desired.binding.key, "KeyJ");
  }
  component.stop();
});

for (const writer of ["this page", "another page"]) test(`an unknown theme save is settled by a later snapshot after the immediate check fails (${writer})`, async () => {
  const dom = domFixture(), writes = [];
  globalThis.document = dom.document; globalThis.window = new EventTarget();
  const radios = ["star", "frost"].map(value => Object.assign(dom.document.createElement("input"), {value}));
  dom.document.querySelectorAll = selector => selector.includes("ui-theme") ? radios : [];
  let current = {supported:true,registration:"registered",surface:"ready",desired:{revision:"r1",enabled:true,binding:{modifiers:["ctrl","alt"],key:"KeyJ"},theme_id:"star"}};
  let offline = false;
  const commit = (body, operation) => {
    current = {...current, last_operation:operation, desired:{...current.desired, revision:"r"+(Number(current.desired.revision.slice(1))+1), binding:body.binding, enabled:body.enabled, theme_id:body.theme_id}};
  };
  const component = desktopSettings(async () => {
    if (offline) throw new Error("temporarily unreachable");
    return structuredClone(current);
  }, async (path, body) => {
    writes.push(structuredClone(body));
    if (writes.length === 1) {
      commit(body, writer === "this page" ? body.operation_id : "other-page-operation");
      offline = true; // the response and the immediate check are both lost
      throw new Error("connection reset");
    }
    if (body.expected_revision !== current.desired.revision) throw Object.assign(new Error("revision conflict"), {status:409});
    commit(body, body.operation_id);
    return structuredClone(current);
  });
  await component.open();
  dom.select("#desktop-form").elements.key.value = "KeyK"; // unsaved shortcut edit
  radios[1].checked = true; radios[1].fire("change"); await tick(); await tick(); await tick();
  assert.equal(dom.document.documentElement.dataset.theme, "star", "an unconfirmed theme is not shown as applied");
  offline = false;
  await component.reconcileLocale();
  assert.equal(dom.document.documentElement.dataset.theme, "frost");
  dom.select("#desktop-form").fire("submit"); await tick(); await tick(); await tick();
  if (writer === "this page") {
    assert.equal(writes[1].expected_revision, "r2", "the later snapshot confirms this operation and advances the baseline");
    assert.equal(writes[1].theme_id, "frost");
    assert.equal(current.desired.binding.key, "KeyK", "the unsaved shortcut edit is kept and saved");
    assert.equal(dom.select("#theme-status").textContent, "同时应用于快捷面板和此页面。");
  } else {
    assert.equal(writes[1].expected_revision, "r1", "another page's save keeps conflict protection");
    assert.equal(current.desired.binding.key, "KeyJ");
  }
  component.stop();
});

for (const [writer, next] of [["this page", "save again"], ["this page", "refresh then save"], ["another page", "save again"]]) test(`a theme confirmed during an unresolved shortcut save updates the baseline once that save ends (${writer}, ${next})`, async () => {
  const dom = domFixture(), writes = [];
  globalThis.document = dom.document; globalThis.window = new EventTarget();
  const radios = ["star", "frost"].map(value => Object.assign(dom.document.createElement("input"), {value}));
  dom.document.querySelectorAll = selector => selector.includes("ui-theme") ? radios : [];
  let current = {supported:true,registration:"registered",surface:"ready",desired:{revision:"r1",enabled:true,binding:{modifiers:["ctrl","alt"],key:"KeyJ"},theme_id:"star"}};
  let offline = false;
  const commit = (body, operation) => {
    current = {...current, last_operation:operation, desired:{...current.desired, revision:"r"+(Number(current.desired.revision.slice(1))+1), binding:body.binding, enabled:body.enabled, theme_id:body.theme_id}};
  };
  const component = desktopSettings(async () => {
    if (offline) throw new Error("temporarily unreachable");
    return structuredClone(current);
  }, async (path, body) => {
    writes.push(structuredClone(body));
    if (writes.length === 1) {
      commit(body, writer === "this page" ? body.operation_id : "other-page-operation");
      offline = true;
      throw new Error("connection reset");
    }
    if (body.expected_revision !== current.desired.revision) throw Object.assign(new Error("revision conflict"), {status:409});
    commit(body, body.operation_id);
    return structuredClone(current);
  });
  const settle = async () => { for (let i = 0; i < 6; i++) await tick(); };
  await component.open();
  dom.select("#desktop-form").elements.key.value = "KeyK"; // unsaved shortcut edit
  radios[1].checked = true; radios[1].fire("change"); await settle();
  offline = false;
  // The user saves before any automatic refresh: the old baseline is rejected,
  // and the error check is what first confirms the earlier theme save.
  dom.select("#desktop-form").fire("submit"); await settle();
  assert.equal(writes[1].expected_revision, "r1");
  if (next === "refresh then save") await component.reconcileLocale();
  dom.select("#desktop-form").fire("submit"); await settle();
  const last = writes.at(-1);
  if (writer === "this page") {
    assert.equal(last.expected_revision, "r2", "the confirmed theme revision becomes the shortcut baseline");
    assert.equal(last.theme_id, "frost");
    assert.equal(current.desired.binding.key, "KeyK", "the unsaved shortcut edit is kept and saved");
    assert.equal(current.desired.theme_id, "frost");
  } else {
    assert.equal(last.expected_revision, "r1", "another page's save keeps conflict protection");
    assert.equal(current.desired.binding.key, "KeyJ");
  }
  component.stop();
});
