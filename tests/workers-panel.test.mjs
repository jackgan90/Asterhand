import test from "node:test";
import assert from "node:assert/strict";

class FakeElement {
  constructor(tag, id = "") {
    this.tagName = tag.toUpperCase();
    this.id = id;
    this.className = "";
    this.dataset = {};
    this.children = [];
    this.listeners = new Map();
    this._text = "";
    this.open = false;
    this.disabled = false;
    this.checked = false;
    this.title = "";
    this.scrollTop = 0;
    this.scrollHeight = 0;
    this.clientHeight = 200;
  }
  get textContent() {
    if (this.children.length === 0) return this._text;
    return this._text + this.children.map(c => c.textContent).join("");
  }
  set textContent(v) {
    this._text = String(v ?? "");
    this.children = [];
  }
  append(...nodes) {
    for (const n of nodes) {
      if (typeof n === "string") {
        this._text += n;
      } else if (n) {
        n.parentElement = this;
        this.children.push(n);
      }
    }
    this.scrollHeight = Math.max(this.scrollHeight, this.children.length * 40);
  }
  replaceChildren(...nodes) {
    this._text = "";
    this.children = [];
    this.scrollTop = 0;
    this.scrollHeight = 0;
    this.append(...nodes);
  }
  addEventListener(type, fn) {
    if (!this.listeners.has(type)) this.listeners.set(type, []);
    this.listeners.get(type).push(fn);
  }
  removeEventListener(type, fn) {
    const arr = this.listeners.get(type);
    if (!arr) return;
    const i = arr.indexOf(fn);
    if (i >= 0) arr.splice(i, 1);
  }
  click() {
    for (const fn of this.listeners.get("click") || []) fn({target: this});
  }
  showModal() {
    this.open = true;
  }
  close() {
    this.open = false;
    for (const fn of this.listeners.get("close") || []) fn({target: this});
  }
  querySelector(selector) {
    const cls = selector.startsWith(".") ? selector.slice(1) : "";
    const walk = (el) => {
      for (const c of el.children) {
        if (cls && c.className.split(/\s+/).includes(cls)) return c;
        const found = walk(c);
        if (found) return found;
      }
      return null;
    };
    return walk(this);
  }
  querySelectorAll(selector) {
    const cls = selector.startsWith(".") ? selector.slice(1) : "";
    const out = [];
    const walk = (el) => {
      for (const c of el.children) {
        if (cls && c.className.split(/\s+/).includes(cls)) out.push(c);
        walk(c);
      }
    };
    walk(this);
    return out;
  }
}

function installFakeDOM() {
  const byId = new Map();
  for (const [id, tag] of [
    ["coding-workers", "dialog"],
    ["workers-open", "button"],
    ["workers-refresh", "button"],
    ["workers-include-closed", "input"],
    ["workers-status", "div"],
    ["workers-interactions", "section"],
    ["workers-list", "aside"],
    ["workers-detail", "section"],
  ]) {
    byId.set(id, new FakeElement(tag, id));
  }
  globalThis.document = {
    documentElement: {lang: "zh-CN"},
    createElement: tag => new FakeElement(tag),
    querySelector: sel => sel.startsWith("#") ? (byId.get(sel.slice(1)) || null) : null,
  };
  return byId;
}

test("switching to a newly created worker clears previous worker turn and logs", async () => {
  const byId = installFakeDOM();
  const {workersPanel} = await import("../web/workers-panel.mjs");

  const wOld = {worker_id: "w_old", alias: "old-reviewer", harness_id: "codex", access_mode: "read_only", state: "idle", workspace_root: "F:\\projA"};
  const wNew = {worker_id: "w_new", alias: "new-worker", harness_id: "codex", access_mode: "read_only", state: "idle", workspace_root: "F:\\projB"};
  const tOld = {turn_id: "turn_old", worker_id: "w_old", state: "completed", answer_available: true};

  let workerList = [wOld];
  const api = async (url) => {
    if (url.startsWith("/api/workers/list")) return {items: workerList};
    if (url.includes("worker_id=w_old")) return {worker: wOld, turns: [tOld]};
    if (url.includes("worker_id=w_new")) return {worker: wNew, turns: []};
    if (url.includes("turn_id=turn_old")) {
      return {
        turn: tOld,
        entries: [{seq: 1, timestamp: "2026-11-21T09:00:00Z", kind: "command", summary: "OLD_LOG_LINE"}],
        result: {text: "OLD_ANSWER_TEXT"},
      };
    }
    throw new Error("unexpected url " + url);
  };

  const panel = workersPanel({
    api,
    post: async () => ({}),
    listen: (t, e, cb) => t.addEventListener(e, cb),
    session: () => ({active: true}),
    notice: () => {},
  });

  await panel.open();
  const detailEl = byId.get("workers-detail");
  assert.match(detailEl.textContent, /OLD_ANSWER_TEXT/);
  assert.match(detailEl.textContent, /OLD_LOG_LINE/);

  // New worker created (0 turns yet) -> user switches to w_new.
  workerList = [wNew, wOld];
  await byId.get("workers-refresh").click();
  await new Promise(r => setTimeout(r, 10));
  const buttons = byId.get("workers-list").querySelectorAll(".worker-list-item");
  assert.equal(buttons.length, 2);
  buttons[0].click(); // w_new
  await new Promise(r => setTimeout(r, 20));

  assert.match(detailEl.textContent, /new-worker/);
  assert.doesNotMatch(detailEl.textContent, /OLD_ANSWER_TEXT/);
  assert.doesNotMatch(detailEl.textContent, /OLD_LOG_LINE/);
  assert.doesNotMatch(detailEl.textContent, /turn_old/);
  panel.stop();
});

test("live polling appends log entries in-place without jumping scrollTop back to top", async () => {
  const byId = installFakeDOM();
  const {workersPanel} = await import("../web/workers-panel.mjs");

  const wRun = {worker_id: "w_run", alias: "live-worker", harness_id: "codex", access_mode: "read_only", state: "running", workspace_root: "F:\\projA"};
  const tRun = {turn_id: "turn_run", worker_id: "w_run", state: "running"};

  let pollPhase = 1;
  const api = async (url) => {
    if (url.startsWith("/api/workers/list")) return {items: [wRun]};
    if (url.includes("worker_id=w_run")) return {worker: wRun, turns: [tRun]};
    if (url.includes("turn_id=turn_run")) {
      if (pollPhase === 1) {
        return {
          turn: tRun,
          entries: Array.from({length: 10}, (_, i) => ({
            seq: i + 1,
            timestamp: "2026-11-21T09:00:00Z",
            kind: "command",
            summary: `step-${i + 1}`,
          })),
        };
      }
      // Incremental after_seq=10
      assert.match(url, /after_seq=10/);
      return {
        turn: tRun,
        entries: [{seq: 11, timestamp: "2026-11-21T09:00:02Z", kind: "command", summary: "step-11"}],
      };
    }
    throw new Error("unexpected url " + url);
  };

  const panel = workersPanel({
    api,
    post: async () => ({}),
    listen: (t, e, cb) => t.addEventListener(e, cb),
    session: () => ({active: true}),
    notice: () => {},
  });

  await panel.open();
  const detailEl = byId.get("workers-detail");
  const logBoxBefore = detailEl.querySelector(".worker-logs-box");
  assert.ok(logBoxBefore, "expected log box rendered");
  assert.equal(logBoxBefore.children.length, 10);

  // User manually scrolls down to read mid-log (not at bottom: scrollHeight=400, clientHeight=200, scrollTop=120).
  logBoxBefore.scrollTop = 120;

  // Next poll arrives with entry 11.
  pollPhase = 2;
  byId.get("workers-refresh").click();
  await new Promise(r => setTimeout(r, 20));

  const logBoxAfter = detailEl.querySelector(".worker-logs-box");
  assert.equal(logBoxAfter, logBoxBefore, "log box DOM node should be preserved in-place");
  assert.equal(logBoxAfter.children.length, 11);
  assert.equal(logBoxAfter.scrollTop, 120, "manual scrollTop must not jump back to 0");

  // Wheel event at scroll boundary (or non-scrollable area) must be prevented so outer page never scrolls;
  // wheel event inside a scrollable pane that still has room must NOT be prevented.
  const dialog = byId.get("coding-workers");
  const wheelHandlers = dialog.listeners.get("wheel") || [];
  assert.equal(wheelHandlers.length, 1);

  let prevented = false;
  wheelHandlers[0]({target: logBoxAfter, deltaY: 60, deltaX: 0, preventDefault: () => { prevented = true; }});
  assert.equal(prevented, false, "scrollable log box with remaining room should scroll normally");

  logBoxAfter.scrollTop = 0;
  prevented = false;
  wheelHandlers[0]({target: logBoxAfter, deltaY: -60, deltaX: 0, preventDefault: () => { prevented = true; }});
  assert.equal(prevented, true, "wheel past top boundary must be intercepted to stop outer page scroll");

  prevented = false;
  wheelHandlers[0]({target: dialog, deltaY: 60, deltaX: 0, preventDefault: () => { prevented = true; }});
  assert.equal(prevented, true, "wheel on non-scrollable dialog chrome must be intercepted");

  panel.stop();
});

test("switching between existing workers updates detail atomically in one swap without rebuilding sidebar", async () => {
  const byId = installFakeDOM();
  const {workersPanel} = await import("../web/workers-panel.mjs");

  const w1 = {worker_id: "w1", alias: "proj-one", harness_id: "codex", access_mode: "read_only", state: "idle", workspace_root: "F:\\p1"};
  const w2 = {worker_id: "w2", alias: "proj-two", harness_id: "codex", access_mode: "read_only", state: "idle", workspace_root: "F:\\p2"};
  const t1 = {turn_id: "t1", worker_id: "w1", state: "completed", answer_available: true};
  const t2 = {turn_id: "t2", worker_id: "w2", state: "completed", answer_available: true};

  const api = async (url) => {
    if (url.startsWith("/api/workers/list")) return {items: [w1, w2]};
    if (url.includes("worker_id=w1")) return {worker: w1, turns: [t1]};
    if (url.includes("worker_id=w2")) return {worker: w2, turns: [t2]};
    if (url.includes("turn_id=t1")) return {turn: t1, entries: [{seq: 1, kind: "command", summary: "log-1"}], result: {text: "ans-1"}};
    if (url.includes("turn_id=t2")) return {turn: t2, entries: [{seq: 1, kind: "command", summary: "log-2"}], result: {text: "ans-2"}};
    throw new Error("unexpected url " + url);
  };

  const panel = workersPanel({
    api,
    post: async () => ({}),
    listen: (t, e, cb) => t.addEventListener(e, cb),
    session: () => ({active: true}),
    notice: () => {},
  });

  await panel.open();
  const detailEl = byId.get("workers-detail");
  const listEl = byId.get("workers-list");
  let detailRebuilds = 0;
  let listRebuilds = 0;
  const origDetailReplace = detailEl.replaceChildren.bind(detailEl);
  const origListReplace = listEl.replaceChildren.bind(listEl);
  detailEl.replaceChildren = (...args) => { detailRebuilds++; return origDetailReplace(...args); };
  listEl.replaceChildren = (...args) => { listRebuilds++; return origListReplace(...args); };

  const buttons = listEl.querySelectorAll(".worker-list-item");
  buttons[1].click(); // switch w1 -> w2
  await new Promise(r => setTimeout(r, 20));

  assert.equal(listRebuilds, 0, "sidebar DOM must update selection in-place without rebuilding");
  assert.equal(detailRebuilds, 1, "detail pane must swap atomically in a single render with no empty intermediate frame");
  assert.equal(buttons[1].dataset.selected, "true");
  assert.equal(buttons[0].dataset.selected, undefined);
  assert.match(detailEl.textContent, /ans-2/);
  assert.doesNotMatch(detailEl.textContent, /ans-1/);
  panel.stop();
});

test("already-open empty Workers panel automatically discovers newly created worker via polling", async () => {
  const byId = installFakeDOM();
  const {workersPanel} = await import("../web/workers-panel.mjs");

  const scheduled = [];
  const origSetTimeout = globalThis.setTimeout;
  const origClearTimeout = globalThis.clearTimeout;
  globalThis.setTimeout = (fn, ms) => {
    const token = {fn, ms, cleared: false};
    scheduled.push(token);
    return token;
  };
  globalThis.clearTimeout = (token) => {
    if (token && typeof token === "object") token.cleared = true;
  };

  try {
    const wNew = {worker_id: "w_created_later", alias: "auto-found", harness_id: "codex", access_mode: "workspace_write", state: "idle", workspace_root: "F:\\proj"};
    let workerList = [];
    const api = async (url) => {
      if (url.startsWith("/api/workers/list")) return {items: workerList};
      if (url.includes("worker_id=w_created_later")) return {worker: wNew, turns: []};
      throw new Error("unexpected url " + url);
    };

    const panel = workersPanel({
      api,
      post: async () => ({}),
      listen: (t, e, cb) => t.addEventListener(e, cb),
      session: () => ({active: true}),
      notice: () => {},
    });

    await panel.open();
    const listEl = byId.get("workers-list");
    assert.equal(listEl.querySelectorAll(".worker-list-item").length, 0);
    assert.ok(scheduled.some(s => !s.cleared), "expected poll scheduled even when panel opens with 0 workers");

    // Caller agent creates a new worker while dialog remains open.
    workerList = [wNew];
    const nextPoll = scheduled.findLast(s => !s.cleared);
    nextPoll.fn();
    await new Promise(r => origSetTimeout(r, 20));

    assert.equal(listEl.querySelectorAll(".worker-list-item").length, 1);
    assert.match(listEl.textContent, /auto-found/);
    assert.match(byId.get("workers-detail").textContent, /auto-found/);
    panel.stop();
  } finally {
    globalThis.setTimeout = origSetTimeout;
    globalThis.clearTimeout = origClearTimeout;
  }
});

test("Workers panel preserves polling after transient selectWorker error and supports refreshIfOpen", async () => {
  const byId = installFakeDOM();
  const {workersPanel} = await import("../web/workers-panel.mjs");

  const scheduled = [];
  const origSetTimeout = globalThis.setTimeout;
  const origClearTimeout = globalThis.clearTimeout;
  globalThis.setTimeout = (fn, ms) => {
    const token = {fn, ms, cleared: false};
    scheduled.push(token);
    return token;
  };
  globalThis.clearTimeout = (token) => {
    if (token && typeof token === "object") token.cleared = true;
  };

  try {
    const w1 = {worker_id: "w1", alias: "alpha", harness_id: "codex", access_mode: "workspace_write", state: "idle", session_auto_approve: false, workspace_root: "F:\\proj"};
    let failGet = false;
    let getCalls = 0;
    const api = async (url) => {
      if (url.startsWith("/api/workers/list")) return {items: [w1], interactions: []};
      if (url.includes("worker_id=w1")) {
        getCalls++;
        if (failGet) throw new Error("transient network glitch");
        return {worker: w1, turns: []};
      }
      throw new Error("unexpected url " + url);
    };

    const panel = workersPanel({
      api,
      post: async () => ({}),
      listen: (t, e, cb) => t.addEventListener(e, cb),
      session: () => ({active: true}),
      notice: () => {},
    });

    await panel.open();
    assert.equal(getCalls, 1);

    // Simulate a transient poll error on /api/workers/get -> poll MUST still be rescheduled!
    failGet = true;
    const poll1 = scheduled.findLast(s => !s.cleared);
    assert.ok(poll1);
    poll1.fn();
    await new Promise(r => origSetTimeout(r, 20));
    assert.equal(getCalls, 2);

    const pollAfterErr = scheduled.findLast(s => !s.cleared);
    assert.ok(pollAfterErr && pollAfterErr !== poll1, "expected schedulePoll() to recover after selectWorker error");

    // Server updates session_auto_approve and SSE fires refreshIfOpen() -> detail immediately reflects badge.
    failGet = false;
    w1.session_auto_approve = true;
    await panel.refreshIfOpen();
    assert.equal(getCalls, 3);
    assert.match(byId.get("workers-detail").textContent, /免审|auto-approve/i);
    panel.stop();
  } finally {
    globalThis.setTimeout = origSetTimeout;
    globalThis.clearTimeout = origClearTimeout;
  }
});

test("Workers panel ticks pending input countdown in place every second without replacing answer textarea", async () => {
  const byId = installFakeDOM();
  const {workersPanel} = await import("../web/workers-panel.mjs");

  const scheduled = [];
  const origSetTimeout = globalThis.setTimeout;
  const origClearTimeout = globalThis.clearTimeout;
  globalThis.setTimeout = (fn, ms) => {
    const token = {fn, ms, cleared: false};
    scheduled.push(token);
    return token;
  };
  globalThis.clearTimeout = (token) => {
    if (token && typeof token === "object") token.cleared = true;
  };

  try {
    let nowMs = Date.parse("2026-11-23T15:00:00.000Z");
    const escalateAt = "2026-11-23T15:00:15.000Z";
    const w1 = {worker_id: "w1", alias: "codex-1", harness_id: "codex", access_mode: "workspace_write", state: "running", workspace_root: "F:\\proj"};
    const pendingInput = {
      request_id: "wir_q1",
      kind: "input",
      worker_id: "w1",
      worker_alias: "codex-1",
      harness_id: "codex",
      workspace_root: "F:\\proj",
      summary: "Pick format",
      questions: [{id: "q1", question: "Pick format"}],
      visible_to_human: false,
      claimed_by_caller: true,
      escalate_at: escalateAt,
      escalation_note: "Checking config.json",
    };

    const panel = workersPanel({
      api: async (url) => {
        if (url.startsWith("/api/workers/list")) return {items: [w1], interactions: [pendingInput]};
        if (url.includes("worker_id=w1")) return {worker: w1, turns: []};
        throw new Error("unexpected url " + url);
      },
      post: async () => ({}),
      listen: (t, e, cb) => t.addEventListener(e, cb),
      session: () => ({active: true}),
      notice: () => {},
      now: () => nowMs,
    });

    await panel.open();
    const intEl = byId.get("workers-interactions");
    assert.match(intEl.textContent, /15s/);
    const taBefore = intEl.querySelectorAll(".workers-int-input")[0];
    assert.ok(taBefore, "expected answer textarea rendered");

    // Advance 1s and fire the 1000ms countdown tick -> badge updates in place without replacing textarea.
    const tick1 = scheduled.find(t => !t.cleared && t.ms === 1000);
    assert.ok(tick1, "expected 1000ms countdown timer scheduled");
    nowMs += 1000;
    tick1.cleared = true;
    tick1.fn();

    assert.match(intEl.textContent, /14s/);
    assert.doesNotMatch(intEl.textContent, /15s/);
    assert.equal(intEl.querySelectorAll(".workers-int-input")[0], taBefore, "textarea DOM ref must stay intact across countdown ticks");

    // Advance another 3s and fire next tick -> shows 11s.
    const tick2 = scheduled.findLast(t => !t.cleared && t.ms === 1000);
    assert.ok(tick2);
    nowMs += 3000;
    tick2.cleared = true;
    tick2.fn();
    assert.match(intEl.textContent, /11s/);

    panel.stop();
    assert.equal(scheduled.filter(t => !t.cleared && t.ms === 1000).length, 0, "stop() must clear countdown timer");
  } finally {
    globalThis.setTimeout = origSetTimeout;
    globalThis.clearTimeout = origClearTimeout;
  }
});
