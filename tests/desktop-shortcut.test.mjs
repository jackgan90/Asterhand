import test from "node:test";
import assert from "node:assert/strict";
import {desktopShortcut} from "../web/desktop-shortcut.mjs";
import {setLocale} from "../web/i18n.mjs";
import {domFixture, tick} from "./web-fixture.mjs";

setLocale("zh-CN");

function mountShortcut(t, api, post) {
  const dom = domFixture();
  const field = dom.select("#desktop-shortcut-field");
  const input = dom.select("#desktop-shortcut");
  const status = dom.select("#desktop-shortcut-status");
  input.type = "checkbox";
  const prevDoc = Object.getOwnPropertyDescriptor(globalThis, "document");
  Object.defineProperty(globalThis, "document", {value: dom.document, configurable: true, writable: true});
  t.after(() => {
    if (prevDoc) Object.defineProperty(globalThis, "document", prevDoc);
    else delete globalThis.document;
  });
  const controller = desktopShortcut(api, post);
  return {dom, field, input, status, controller};
}

test("desktopShortcut hides on unsupported platforms and disables on uninstalled builds", async t => {
  let snapshot = {supported: false, available: false, enabled: false};
  const ui = mountShortcut(t, async () => snapshot, async () => { throw new Error("unexpected post"); });

  await ui.controller.open();
  assert.equal(ui.field.hidden, true);
  assert.equal(ui.input.disabled, true);

  snapshot = {supported: true, available: false, enabled: false, reason: "not_installed", message_key: "status.desktop.shortcut_not_installed"};
  await ui.controller.open();
  assert.equal(ui.field.hidden, false);
  assert.equal(ui.input.disabled, true);
  assert.equal(ui.input.checked, false);
  assert.equal(ui.status.textContent, "仅正式安装版本可管理桌面图标。");

  setLocale("en-US");
  t.after(() => setLocale("zh-CN"));
  ui.controller.relocalize();
  assert.equal(ui.status.textContent, "Desktop icon management is available only in installed builds.");
});

test("desktopShortcut toggles immediately, handles 409 conflicts, and reconciles lost replies", async t => {
  const posts = [];
  let getReply = {supported: true, available: true, enabled: false, conflict: false};
  let postHandler;
  const ui = mountShortcut(
    t,
    async () => structuredClone(getReply),
    async (_path, body) => {
      posts.push(structuredClone(body));
      return postHandler(body);
    },
  );

  await ui.controller.open();
  assert.equal(ui.field.hidden, false);
  assert.equal(ui.input.disabled, false);
  assert.equal(ui.input.checked, false);

  // Successful immediate ON toggle.
  postHandler = async body => ({supported: true, available: true, enabled: body.enabled, conflict: false, last_operation: body.operation_id});
  ui.input.checked = true;
  ui.input.fire("change");
  await tick();
  assert.equal(posts.length, 1);
  assert.equal(posts[0].enabled, true);
  assert.equal(ui.input.checked, true);
  assert.equal(ui.status.textContent, "在当前用户桌面创建或移除快捷方式。");

  // 409 external conflict reverts checkbox and paints error tone.
  postHandler = async () => {
    const err = new Error("Conflict");
    err.status = 409;
    err.messageKey = "err.launch.shortcut_conflict";
    err.details = {shortcut: {supported: true, available: true, enabled: false, conflict: true, message_key: "err.launch.shortcut_conflict"}};
    throw err;
  };
  ui.input.checked = true;
  ui.input.fire("change");
  await tick();
  assert.equal(ui.input.checked, false);
  assert.equal(ui.status.dataset.tone, "error");
  assert.equal(ui.status.textContent, "桌面已存在非本安装的同名快捷方式，为安全起见未覆盖");

  // Lost network response keeps pending operation_id for same-direction retry and reconciles on reopen.
  postHandler = async () => { throw new Error("network dropped"); };
  ui.input.checked = true;
  ui.input.fire("change");
  await tick();
  const droppedId = posts.at(-1).operation_id;

  ui.input.checked = true;
  ui.input.fire("change");
  await tick();
  assert.equal(posts.at(-1).operation_id, droppedId);

  getReply = {supported: true, available: true, enabled: true, conflict: false, last_operation: droppedId};
  await ui.controller.open();
  assert.equal(ui.input.checked, true);
  assert.equal(ui.status.dataset.tone, undefined);
  assert.equal(ui.status.textContent, "在当前用户桌面创建或移除快捷方式。");
});
