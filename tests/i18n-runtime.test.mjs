import assert from "node:assert/strict";
import test from "node:test";
import {desktopSettings} from "../web/desktop-settings.mjs";
import {applyI18n, getLocale, setLocale, t} from "../web/i18n.mjs";
import {localeController, resolveWebLocale} from "../web/locale-controller.mjs";
import {nodeSettings} from "../web/node-settings.mjs";
import {taskView} from "../web/task-view.mjs";
import {setLocale as setQuickLocale, t as qt} from "../internal/quickinput/assets/i18n.mjs";
import {messageRenderer} from "../internal/quickinput/assets/chat-renderer.mjs";
import {rendererDocument, withClass} from "./chat-renderer-fixture.mjs";
import {descendants, domFixture, tick} from "./web-fixture.mjs";

function makeStorage() {
  const map = new Map();
  return {
    getItem: key => (map.has(key) ? map.get(key) : null),
    setItem: (key, val) => map.set(key, String(val)),
    removeItem: key => map.delete(key),
    clear: () => map.clear(),
  };
}

function collectText(root) {
  return [root, ...descendants(root)]
    .map(el => el.textContent || "")
    .join(" ");
}

test("resolveWebLocale enforces 4-tier authority and pre-login sessionStorage isolation", async () => {
  // 1. Explicit locale overrides OS and browser.
  assert.equal(
    resolveWebLocale({localeMode: "en-US", effectiveLocale: "zh-CN", systemDetected: true, browserLanguages: ["zh-CN"]}),
    "en-US",
  );
  // 2. Auto + OS detected uses OS effectiveLocale over browser language.
  assert.equal(
    resolveWebLocale({localeMode: "auto", effectiveLocale: "zh-CN", systemDetected: true, browserLanguages: ["en-US"]}),
    "zh-CN",
  );
  // 3. Auto + OS undetected falls back to browser navigator.languages in preference order.
  assert.equal(
    resolveWebLocale({localeMode: "auto", effectiveLocale: "en-US", systemDetected: false, browserLanguages: ["zh-TW", "en-US"]}),
    "zh-CN",
  );
  // 4. Ultimate fallback when neither OS nor browser matches.
  assert.equal(
    resolveWebLocale({localeMode: "auto", effectiveLocale: "", systemDetected: false, browserLanguages: ["fr-FR"]}),
    "en-US",
  );

  // Pre-login temporary language switch uses sessionStorage without calling desktop.setLocale.
  const dom = domFixture();
  globalThis.document = dom.document;
  globalThis.localStorage = makeStorage();
  globalThis.sessionStorage = makeStorage();
  let saveCalls = 0;
  const ctrl = localeController({
    desktop: {setLocale: async () => { saveCalls++; return {ok: true}; }},
    listen: (el, name, fn) => el.addEventListener(name, fn),
    notice: () => {},
    onRelocalize: () => {},
    fetchHealth: async () => ({
      ok: true,
      json: async () => ({locale: "zh-CN", locale_mode: "auto", system_locale_detected: true}),
    }),
  });
  ctrl.dispose && await ctrl.calibrateUnauthenticated();
  assert.equal(getLocale(), "zh-CN");

  const topSelect = dom.select("#locale-select");
  topSelect.value = "en-US";
  topSelect.fire("change");
  await tick();
  assert.equal(getLocale(), "en-US");
  assert.equal(saveCalls, 0);
  assert.equal(globalThis.sessionStorage.getItem("asterhand-login-locale"), "en-US");

  // Brand subtitle hides in en-US and restores in zh-CN.
  const brandSub = dom.document.createElement("span");
  brandSub.setAttribute("data-i18n", "brand.subtitle");
  brandSub.setAttribute("data-i18n-brand-sub", "");
  const root = dom.document.createElement("div");
  root.querySelectorAll = () => [brandSub];
  setLocale("en-US");
  applyI18n(root);
  assert.equal(brandSub.hidden, true);
  setLocale("zh-CN");
  applyI18n(root);
  assert.equal(brandSub.hidden, false);
  assert.equal(brandSub.textContent, "星伴");

  // Logging in clears the temporary pre-login override and aligns to authoritative desktop status.
  ctrl.markAuthenticated();
  assert.equal(globalThis.sessionStorage.getItem("asterhand-login-locale"), null);
  ctrl.syncDesktopStatus({
    desired: {locale: "zh-CN"},
    effective_locale: "zh-CN",
    system_locale_detected: true,
  });
  assert.equal(getLocale(), "zh-CN");
  ctrl.dispose();
});

test("desktopSettings preserves concurrent revision guard across locale switches and reconciles lost responses", async () => {
  setLocale("zh-CN");
  const dom = domFixture();
  globalThis.document = dom.document;
  globalThis.window = new EventTarget();

  let server = {
    supported: true,
    registration: "registered",
    surface: "ready",
    effective_locale: "zh-CN",
    system_locale_detected: true,
    last_locale_operation: "",
    desired: {
      revision: "rev-1",
      enabled: true,
      binding: {modifiers: ["ctrl", "alt"], key: "KeyJ"},
      theme_id: "star",
      locale: "auto",
    },
  };
  const posted = [];
  let dropNextLocaleResponse = false;

  const component = desktopSettings(
    async () => structuredClone(server),
    async (path, body) => {
      posted.push({path, body: structuredClone(body)});
      if (path === "/api/desktop/locale") {
        if (body.expected_revision !== server.desired.revision) {
          const err = new Error("conflict");
          err.status = 409;
          err.details = {desktop: structuredClone(server)};
          throw err;
        }
        server.desired.locale = body.locale;
        server.effective_locale = body.locale === "auto" ? "zh-CN" : body.locale;
        server.desired.revision = "rev-" + (posted.length + 1);
        server.last_locale_operation = body.operation_id;
        if (dropNextLocaleResponse) {
          dropNextLocaleResponse = false;
          throw new TypeError("network connection lost after write");
        }
        return structuredClone(server);
      }
      if (path === "/api/desktop/settings") {
        if (body.expected_revision !== server.desired.revision) {
          const err = new Error("settings revision conflict");
          err.status = 409;
          err.details = {desktop: structuredClone(server)};
          throw err;
        }
        server.desired.theme_id = body.theme_id;
        server.desired.revision = "rev-" + (posted.length + 1);
        return structuredClone(server);
      }
      throw new Error(path);
    },
  );

  // Case 1: No external modification -> locale switch advances formBaseline.revision cleanly.
  await component.open();
  const cleanRes = await component.setLocale("en-US");
  assert.equal(cleanRes.ok, true);
  dom.select("#desktop-form").elements.theme.value = "frost";
  dom.select("#desktop-form").fire("submit");
  await tick();
  await tick();
  assert.equal(server.desired.theme_id, "frost");

  // Case 2: Page B concurrently modifies theme -> Page A switches locale -> Page A's formBaseline.revision MUST NOT advance!
  await component.open();
  const baselineBefore = server.desired.revision;
  dom.select("#desktop-key").value = "KeyM";
  server.desired.theme_id = "star";
  server.desired.revision = "rev-external-b";

  const conflictLocale = await component.setLocale("zh-CN");
  assert.equal(conflictLocale.ok, false);
  const secondLocale = await component.setLocale("en-US");
  assert.equal(secondLocale.ok, true);

  dom.select("#desktop-form").fire("submit");
  await tick();
  await tick();
  const lastSettingsPost = posted.filter(p => p.path === "/api/desktop/settings").at(-1);
  assert.equal(lastSettingsPost.body.expected_revision, baselineBefore);
  assert.equal(dom.select("#desktop-key").value, "KeyM");

  // Case 3: Lost HTTP response after server write reconciles via last_locale_operation.
  dropNextLocaleResponse = true;
  const reconciled = await component.setLocale("zh-CN");
  assert.equal(reconciled.ok, true);
  assert.equal(reconciled.status.desired.locale, "zh-CN");
});

test("taskView and nodeSettings relocalize losslessly without dropping drafts or renaming default nodes", async ctx => {
  setLocale("zh-CN");
  const dom = domFixture();
  globalThis.document = dom.document;
  const origFormData = globalThis.FormData;
  globalThis.FormData = class {
    constructor(form) { this.form = form; }
    get(key) { const f = this.form.elements[key]; return f?.checked ? "on" : f?.value || ""; }
  };
  ctx.after(() => { globalThis.FormData = origFormData; });

  const view = taskView({
    guarded: (_el, fn) => fn(),
    callbacks: {answer: async () => {}, cancel: async () => {}, resolve: async () => {}, retry: async () => {}, refresh: async () => {}},
    submissionChanged: () => {},
  });

  const sampleViews = [{
    goal: "整理季度报告",
    out: null,
    job: {
      id: "job-1",
      owner: "node-local",
      state: "waiting_input",
      created: "2026-04-18T10:00:00Z",
      updated: "2026-04-18T10:01:00Z",
      execution_outcome: "running",
      verification_outcome: "pending",
      request: {goal: "整理季度报告"},
      interaction: {id: "int-1", state: "pending", question: "是否包含附录？"},
    },
  }];
  const selection = {info: {node_id: "node-local", name: "我的节点", name_default: true}, peers: []};
  view.render(sampleViews, selection);

  const cardsRoot = dom.select("#tasks");
  const answerInput = descendants(cardsRoot).find(el => el.tagName === "INPUT");
  assert.ok(answerInput, "expected waiting_input answer box");
  answerInput.focus();
  answerInput.value = "包含附录 A 与 B";
  answerInput.setSelectionRange(2, 5);

  view.detail(sampleViews[0].job);
  view.events([{seq: 1, type: "started", time: "2026-04-18T10:00:01Z", data: {step: 1}}]);
  const detailBox = dom.select("#detail");
  detailBox.scrollTop = 148;

  setLocale("en-US");
  view.relocalize();

  const updatedInput = descendants(cardsRoot).find(el => el.tagName === "INPUT");
  assert.equal(dom.document.activeElement, updatedInput);
  assert.equal(updatedInput.value, "包含附录 A 与 B");
  assert.equal(updatedInput.selectionStart, 2);
  assert.equal(updatedInput.selectionEnd, 5);
  const renderedCardText = collectText(cardsRoot);
  assert.equal(renderedCardText.includes(t("web.task.state.waiting_input")), true);
  assert.equal(renderedCardText.includes("My Node"), true);
  assert.equal(detailBox.scrollTop, 148);

  // nodeSettings: opening a fresh node (name_default=true) in en-US displays "My Node"
  // and saving without editing the name field sends name="我的节点" & name_edited=false.
  let savedPayload = null;
  const settingsForm = dom.select("#settings-form");
  const ns = nodeSettings({
    api: async path => {
      if (path === "/api/settings") {
        return {
          has_key: true,
          home: "C:/AsterhandData",
          providers: [{id: "deepseek", label: "DeepSeek", models: ["deepseek-chat"]}],
          runtimes: [{id: "openclaw", label: "OpenClaw", implemented: true, providers: ["deepseek"]}],
          config: {
            node_id: "node-1",
            name: "我的节点",
            name_default: true,
            endpoint: "",
            peer_listen: "127.0.0.1:17433",
            llm: {provider: "deepseek", model: "deepseek-chat", credential_ref: "default"},
            agent_runtime: "openclaw",
            projects: [{id: "workspace", name: "授权目录", name_custom: false, root: "C:/Work"}],
          },
        };
      }
      if (path === "/api/probe") return {state: "passed"};
      if (path === "/api/network") return {detail: "ok", message_key: "status.network.not_configured"};
      throw new Error(path);
    },
    post: async (_path, body) => {
      savedPayload = body;
      return {ready: true, chat_available: true, restart_required: false};
    },
    session: () => ({active: true}),
    desktop: {open: async () => {}},
    guarded: fn => fn(),
    notice: () => {},
    refreshPeers: async () => {},
    listen: (el, name, fn) => el.addEventListener(name, fn),
  });

  await ns.open();
  assert.equal(settingsForm.elements.name.value, "My Node");
  setLocale("zh-CN");
  ns.relocalize();
  assert.equal(settingsForm.elements.name.value, "我的节点");
  setLocale("en-US");
  ns.relocalize();
  assert.equal(settingsForm.elements.name.value, "My Node");

  settingsForm.fire("submit");
  await tick();
  await tick();
  assert.equal(savedPayload.name, "我的节点");
  assert.equal(savedPayload.name_edited, false);
  ns.stop();
});

test("quickinput chat-renderer relocalizes wrapper and buttons in place without re-running Markdown or KaTeX", t => {
  rendererDocument(t);
  setQuickLocale("zh-CN");
  const zhDelivery = qt("quickinput.outbox.accepted_failed");
  const zhReadFull = qt("quickinput.render.read_full_message");

  const list = document.createElement("div");
  const render = messageRenderer(list, {copy: () => {}, expand: () => {}});
  const messages = [
    {
      id: "m-user",
      role: "user",
      blocks: [{type: "text", text: "请推导欧拉公式"}],
      timestamp: "2026-04-18T10:00:00Z",
      delivery_status_key: "quickinput.outbox.accepted_failed",
      delivery_status: zhDelivery,
    },
    {
      id: "m-assistant",
      role: "assistant",
      blocks: [{type: "text", text: "公式如下：" + String.raw`\(e^{i\pi} + 1 = 0\)` + "\n\n```\nconsole.log(1)\n```"}],
      timestamp: "2026-04-18T10:00:05Z",
      truncated: true,
    },
  ];

  render(messages);
  assert.equal(list.children.length, 2);

  const userArticle = list.children[0];
  const assistantArticle = list.children[1];
  const assistantBodyBefore = withClass(assistantArticle, "chat-message-body")[0];
  const mathNodesBefore = assistantArticle.querySelectorAll("*").filter(n => n.tagName === "math");
  assert.equal(mathNodesBefore.length, 1);
  assert.equal(userArticle.textContent.includes("你"), true);
  assert.equal(userArticle.textContent.includes(zhDelivery), true);
  assert.equal(assistantArticle.textContent.includes("助手"), true);
  assert.equal(assistantArticle.textContent.includes("复制代码"), true);
  assert.equal(assistantArticle.textContent.includes("复制原文"), true);
  assert.equal(assistantArticle.textContent.includes(zhReadFull), true);

  // Switch quickinput locale to English and relocalize in place!
  setQuickLocale("en-US");
  render.relocalize();

  const assistantBodyAfter = withClass(assistantArticle, "chat-message-body")[0];
  const mathNodesAfter = assistantArticle.querySelectorAll("*").filter(n => n.tagName === "math");
  assert.strictEqual(assistantBodyAfter, assistantBodyBefore, "chat-message-body DOM node must be preserved");
  assert.strictEqual(mathNodesAfter[0], mathNodesBefore[0], "KaTeX <math> DOM node must not be recreated");

  assert.equal(userArticle.textContent.includes("You"), true);
  assert.equal(userArticle.textContent.includes("Delivered, reply failed"), true);
  assert.equal(assistantArticle.textContent.includes("Assistant"), true);
  assert.equal(assistantArticle.textContent.includes("Copy code"), true);
  assert.equal(assistantArticle.textContent.includes("Copy source"), true);
  assert.equal(assistantArticle.textContent.includes("Read full message"), true);
});

test("desktopSettings rejects out-of-order stale GET snapshots and post-write delayed reads", async () => {
  setLocale("zh-CN");
  const dom = domFixture();
  globalThis.document = dom.document;
  globalThis.window = new EventTarget();
  globalThis.localStorage = makeStorage();
  globalThis.sessionStorage = makeStorage();

  const pendingGets = [];
  let server = {
    supported: true,
    registration: "registered",
    surface: "ready",
    effective_locale: "zh-CN",
    system_locale_detected: true,
    last_locale_operation: "",
    desired: {
      revision: "rev-1",
      enabled: true,
      binding: {modifiers: ["ctrl", "alt"], key: "KeyJ"},
      theme_id: "star",
      locale: "zh-CN",
    },
  };

  const component = desktopSettings(
    () => new Promise(resolve => {
      const snapshotAtStart = structuredClone(server);
      pendingGets.push(() => resolve(snapshotAtStart));
    }),
    async (path, body) => {
      if (path === "/api/desktop/locale") {
        server.desired.locale = body.locale;
        server.effective_locale = body.locale;
        server.desired.revision = "rev-3";
        server.last_locale_operation = body.operation_id;
        return structuredClone(server);
      }
      throw new Error(path);
    },
  );

  // GET A captures old zh-CN (rev-1) but returns after GET B (en-US, rev-2).
  const readAPromise = component.reconcileLocale();
  server.desired.locale = "en-US";
  server.effective_locale = "en-US";
  server.desired.revision = "rev-2";
  const readBPromise = component.reconcileLocale();

  assert.equal(pendingGets.length, 2);
  pendingGets[1](); // Resolve newer GET B first
  const recB = await readBPromise;
  assert.equal(recB.outcome, "synced");
  assert.equal(recB.status.desired.locale, "en-US");
  assert.equal(recB.status.desired.revision, "rev-2");

  pendingGets[0](); // Resolve older GET A second
  const recA = await readAPromise;
  assert.equal(recA.outcome, "stale");
  assert.equal(component.status().desired.locale, "en-US");
  assert.equal(component.status().desired.revision, "rev-2");

  // GET C starts before a write (rev-2 en-US) and resolves after setLocale("zh-CN") commits rev-3.
  const readCPromise = component.reconcileLocale();
  assert.equal(pendingGets.length, 3);
  const writeRes = await component.setLocale("zh-CN");
  assert.equal(writeRes.ok, true);
  assert.equal(component.status().desired.revision, "rev-3");

  pendingGets[2]();
  const recC = await readCPromise;
  assert.equal(recC.outcome, "stale");
  assert.equal(component.status().desired.locale, "zh-CN");
  assert.equal(component.status().desired.revision, "rev-3");

  // Suspend/resume regression: old session issues read #5 (delayed) -> stop() ->
  // new session issues read -> old read resolves first -> new read resolves with en-US.
  let activeScope = {active: true};
  const lifecycleGets = [];
  const resumedComponent = desktopSettings(
    () => new Promise(resolve => {
      const snapshotAtStart = structuredClone(server);
      lifecycleGets.push(() => resolve(snapshotAtStart));
    }),
    async () => { throw new Error("unexpected post"); },
    {session: () => activeScope},
  );
  for (let i = 0; i < 4; i++) {
    const p = resumedComponent.reconcileLocale();
    lifecycleGets.at(-1)();
    await p;
  }
  // Old session's 5th read captures old zh-CN (rev-3) and stays in flight across stop().
  const delayedOldSessionRead = resumedComponent.reconcileLocale();
  assert.equal(lifecycleGets.length, 5);

  activeScope.active = false;
  resumedComponent.stop();
  activeScope = {active: true};

  // While suspended, server switched to en-US (rev-6); new session starts its first read.
  server.desired.locale = "en-US";
  server.effective_locale = "en-US";
  server.desired.revision = "rev-6";
  const newSessionRead = resumedComponent.reconcileLocale();
  assert.equal(lifecycleGets.length, 6);

  // Old session read (#5) returns BEFORE new session read (#6) — must not poison clock!
  lifecycleGets[4]();
  const oldRec = await delayedOldSessionRead;
  assert.equal(oldRec.outcome, "stale");

  lifecycleGets[5]();
  const newRec = await newSessionRead;
  assert.equal(newRec.outcome, "synced");
  assert.equal(newRec.status.desired.locale, "en-US");
  assert.equal(resumedComponent.status().desired.locale, "en-US");
  assert.equal(resumedComponent.status().desired.revision, "rev-6");
});

test("taskView.relocalize preserves live form, input, and disabled submit state during in-flight answer", async () => {
  setLocale("zh-CN");
  const dom = domFixture();
  globalThis.document = dom.document;

  let resolveAnswer;
  let answerCalls = 0;
  let refreshCalls = 0;
  const view = taskView({
    guarded: fn => fn(),
    callbacks: {
      answer: () => {
        answerCalls++;
        return new Promise(resolve => { resolveAnswer = resolve; });
      },
      cancel: async () => {},
      resolve: async () => {},
      detail: async () => {},
      refresh: async () => { refreshCalls++; },
    },
    submissionChanged: () => {},
  });

  view.render([{
    goal: "核对账单",
    out: null,
    job: {
      id: "job-inflight",
      owner: "node-local",
      state: "waiting_input",
      created: "2026-04-18T10:00:00Z",
      updated: "2026-04-18T10:01:00Z",
      request: {goal: "核对账单"},
      interaction: {id: "int-inflight", state: "pending", question: "确认继续？"},
    },
  }], {info: {node_id: "node-local", name: "我的节点", name_default: true}, peers: []});

  const cardsRoot = dom.select("#tasks");
  const formBefore = descendants(cardsRoot).find(el => el.tagName === "FORM");
  const inputBefore = descendants(cardsRoot).find(el => el.tagName === "INPUT");
  const buttonBefore = descendants(cardsRoot).find(el => el.tagName === "BUTTON");
  inputBefore.value = "确认";

  // Simulate browser blur on button click followed by form submit.
  inputBefore.blur();
  formBefore.fire("submit");
  await tick();

  assert.equal(answerCalls, 1);
  assert.equal(buttonBefore.disabled, true);
  assert.equal(view.editing(), true, "in-flight answer must keep editing() true against SSE refreshes");

  // Switch locale mid-submission: DOM nodes and disabled button state must survive intact.
  setLocale("en-US");
  view.relocalize();

  const formAfter = descendants(cardsRoot).find(el => el.tagName === "FORM");
  const inputAfter = descendants(cardsRoot).find(el => el.tagName === "INPUT");
  const buttonAfter = descendants(cardsRoot).find(el => el.tagName === "BUTTON");
  assert.strictEqual(formAfter, formBefore);
  assert.strictEqual(inputAfter, inputBefore);
  assert.strictEqual(buttonAfter, buttonBefore);
  assert.equal(buttonAfter.textContent, t("web.task.answer_btn"));
  assert.equal(buttonAfter.disabled, true);

  // Attempting another submit while disabled must not send a duplicate request.
  formAfter.fire("submit");
  await tick();
  assert.equal(answerCalls, 1);

  resolveAnswer(true);
  await tick();
  await tick();
  assert.equal(buttonAfter.disabled, false);
  assert.equal(view.editing(), false);
  assert.equal(refreshCalls, 1);
});

test("localeController storage events respect pre-login sessionStorage override and reconcile server state when authenticated", async () => {
  setLocale("zh-CN");
  const dom = domFixture();
  globalThis.document = dom.document;
  const win = new EventTarget();
  globalThis.window = win;
  globalThis.localStorage = makeStorage();
  globalThis.sessionStorage = makeStorage();

  let reconcileCalls = 0;
  let serverStatus = {
    desired: {locale: "en-US"},
    effective_locale: "en-US",
    system_locale_detected: true,
  };
  const ctrl = localeController({
    desktop: {
      setLocale: async () => ({ok: true, status: serverStatus}),
      reconcileLocale: async () => {
        reconcileCalls++;
        return {outcome: "synced", status: serverStatus};
      },
    },
    listen: (el, name, fn) => el.addEventListener(name, fn),
    notice: () => {},
    onRelocalize: () => {},
    fetchHealth: async () => ({
      ok: true,
      json: async () => ({locale: "zh-CN", locale_mode: "auto", system_locale_detected: true}),
    }),
  });
  await ctrl.calibrateUnauthenticated();

  // User picks temporary English on the login page.
  const topSelect = dom.select("#locale-select");
  topSelect.value = "en-US";
  topSelect.fire("change");
  await tick();
  assert.equal(getLocale(), "en-US");

  // Another tab writes zh-CN to localStorage -> must NOT overwrite this tab's pre-login override.
  const fireStorage = (key, newValue) => {
    const ev = new Event("storage");
    ev.key = key;
    ev.newValue = newValue;
    win.dispatchEvent(ev);
  };
  fireStorage("asterhand-locale-pref", "zh-CN");
  fireStorage("asterhand-locale", "zh-CN");
  assert.equal(getLocale(), "en-US");
  assert.equal(topSelect.value, "en-US");

  // Once authenticated, storage events reconcile against authoritative server state rather than raw localStorage.
  ctrl.markAuthenticated();
  setLocale("zh-CN");
  fireStorage("asterhand-locale", "zh-CN");
  await tick();
  await tick();
  assert.equal(reconcileCalls, 1);
  assert.equal(getLocale(), "en-US");
  assert.equal(topSelect.value, "en-US");
  ctrl.dispose();
});
