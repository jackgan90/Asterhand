import test from "node:test";
import assert from "node:assert/strict";
import {readFileSync} from "node:fs";

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
    this.scrollHeight = 1000;
    this.clientHeight = 200;
    this.attrs = new Map();
  }
  setAttribute(k, v) { this.attrs.set(k, String(v)); }
  getAttribute(k) { return this.attrs.get(k) ?? null; }
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
      if (typeof n === "string") this._text += n;
      else if (n) {
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
    this.scrollHeight = 1000;
    this.append(...nodes);
  }
  remove() {
    const p = this.parentElement;
    if (!p) return;
    const i = p.children.indexOf(this);
    if (i >= 0) p.children.splice(i, 1);
    this.parentElement = null;
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
    if (this.disabled) return;
    for (const fn of this.listeners.get("click") || []) fn({target: this});
  }
  showModal() { this.open = true; }
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
    const tag = cls ? "" : selector.toUpperCase();
    const out = [];
    const walk = (el) => {
      for (const c of el.children) {
        if (cls && c.className.split(/\s+/).includes(cls)) out.push(c);
        else if (tag && c.tagName === tag) out.push(c);
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

const defer = () => {
  let resolve, reject;
  const promise = new Promise((res, rej) => { resolve = res; reject = rej; });
  return {promise, resolve, reject};
};

test("P1: switching workers never mixes turns or filter targets on failed retry or overlapping poll", async () => {
  const byId = installFakeDOM();
  const {workersPanel} = await import("../web/workers-panel.mjs");

  const wA = {worker_id: "w_A", alias: "alpha", harness_id: "codex", access_mode: "read_only", state: "idle", workspace_root: "F:\\A"};
  const wB = {worker_id: "w_B", alias: "beta", harness_id: "codex", access_mode: "read_only", state: "idle", workspace_root: "F:\\B"};
  const wC = {worker_id: "w_C", alias: "gamma", harness_id: "codex", access_mode: "read_only", state: "idle", workspace_root: "F:\\C"};
  const tA1 = {turn_id: "turn_A1", worker_id: "w_A", turn_seq: 1, state: "completed", started_at: "2026-11-21T08:00:00Z"};
  const tB1 = {turn_id: "turn_B1", worker_id: "w_B", turn_seq: 1, state: "completed", started_at: "2026-11-21T09:00:00Z"};
  const tC1 = {turn_id: "turn_C1", worker_id: "w_C", turn_seq: 1, state: "completed", started_at: "2026-11-21T09:30:00Z"};

  let failNextB = true;
  let holdFirstC = null;
  const requestedURLs = [];
  const api = async (url) => {
    requestedURLs.push(url);
    if (url.startsWith("/api/workers/list")) return {items: [wA, wB, wC]};
    if (url.startsWith("/api/workers/get?worker_id=w_A")) return {worker: wA, turns: [tA1]};
    if (url.startsWith("/api/workers/get?worker_id=w_B")) {
      if (failNextB) {
        failNextB = false;
        throw new Error("transient B failure");
      }
      return {worker: wB, turns: [tB1]};
    }
    if (url.startsWith("/api/workers/get?worker_id=w_C")) {
      if (holdFirstC) {
        const g = holdFirstC;
        holdFirstC = null;
        await g.promise;
      }
      return {worker: wC, turns: [tC1]};
    }
    if (url.startsWith("/api/workers/logs?turn_id=turn_A1")) return {turn: tA1, entries: [], result: {text: "ANS_A1", complete: true}};
    if (url.startsWith("/api/workers/logs?turn_id=turn_B1")) return {turn: tB1, entries: [], result: {text: "ANS_B1", complete: true}};
    if (url.startsWith("/api/workers/logs?turn_id=turn_C1")) return {turn: tC1, entries: [], result: {text: "ANS_C1", complete: true}};
    if (url.startsWith("/api/workers/turns")) return {items: [], has_more: false};
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
  assert.match(detailEl.textContent, /ANS_A1/);

  // 1) Switch A -> B where first attempt fails: A's detail must clear immediately; retry must load ONLY B!
  const sidebarBtns = byId.get("workers-list").querySelectorAll(".worker-list-item");
  sidebarBtns[1].click(); // w_B (fails first time)
  await new Promise(r => setTimeout(r, 20));
  assert.doesNotMatch(detailEl.textContent, /ANS_A1/, "stale Worker A turns must not stay interactive when Worker B fails");

  byId.get("workers-refresh").click(); // retry w_B succeeds
  await new Promise(r => setTimeout(r, 20));
  assert.match(detailEl.textContent, /beta/);
  assert.match(detailEl.textContent, /ANS_B1/);
  assert.doesNotMatch(detailEl.textContent, /ANS_A1/);
  assert.doesNotMatch(detailEl.textContent, /turn_A1/);

  // Clicking "只看异常" on B must query worker_id=w_B, never w_A!
  requestedURLs.length = 0;
  detailEl.querySelectorAll(".worker-filter-btn")[1].click();
  await new Promise(r => setTimeout(r, 20));
  assert.ok(requestedURLs.some(u => u.includes("/api/workers/turns?worker_id=w_B") && u.includes("filter=attention")));
  assert.ok(!requestedURLs.some(u => u.includes("worker_id=w_A")));

  // 2) Switch B -> C with overlapping poll before first C request finishes: must not reuse B's timeline!
  holdFirstC = defer();
  const pendingC = holdFirstC;
  sidebarBtns[2].click(); // starts first selectWorker("w_C") which hangs on pendingC
  await new Promise(r => setTimeout(r, 5));
  await panel.refreshIfOpen(); // overlapping refresh selects w_C and resolves first
  pendingC.resolve();
  await new Promise(r => setTimeout(r, 20));

  assert.match(detailEl.textContent, /gamma/);
  assert.match(detailEl.textContent, /ANS_C1/);
  assert.doesNotMatch(detailEl.textContent, /ANS_B1/);
  assert.doesNotMatch(detailEl.textContent, /turn_B1/);
  panel.stop();
});

test("P2: reopening after >20 new turns resets cursor instead of leaving a silent gap", async () => {
  const byId = installFakeDOM();
  const {workersPanel} = await import("../web/workers-panel.mjs");

  const w1 = {worker_id: "w1", alias: "gap-worker", harness_id: "codex", access_mode: "read_only", state: "idle", workspace_root: "F:\\proj"};
  const mkTurn = (seq) => ({
    turn_id: `turn_${String(seq).padStart(3, "0")}`,
    worker_id: "w1",
    turn_seq: seq,
    state: "completed",
    started_at: new Date(Date.UTC(2026, 10, 21, 8, 0, seq)).toISOString(),
  });
  const initial20 = Array.from({length: 20}, (_, i) => mkTurn(20 - i)); // #20..#1
  const burstHead20 = Array.from({length: 20}, (_, i) => mkTurn(45 - i)); // #45..#26
  const middle20 = Array.from({length: 20}, (_, i) => mkTurn(25 - i)); // #25..#6

  let phase = 1;
  const api = async (url) => {
    if (url.startsWith("/api/workers/list")) return {items: [w1]};
    if (url.startsWith("/api/workers/get")) {
      if (phase === 1) return {worker: w1, turns: initial20, turns_has_more: false, turns_next_cursor: ""};
      return {worker: w1, turns: burstHead20, turns_has_more: true, turns_next_cursor: "cur_after_26"};
    }
    if (url.startsWith("/api/workers/turns") && url.includes("cursor=cur_after_26")) {
      return {items: middle20, has_more: true, next_cursor: "cur_after_6"};
    }
    if (url.startsWith("/api/workers/logs")) return {turn: mkTurn(45), entries: []};
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
  assert.equal(detailEl.querySelectorAll(".worker-turn-row").length, 20);
  assert.equal(detailEl.querySelector(".worker-load-older"), null);

  // Close panel, 25 new turns (#45..#21) arrive, reopen panel -> must show #45..#26 AND "Load earlier turns"!
  panel.stop();
  phase = 2;
  await panel.open();

  const rowsAfterReopen = detailEl.querySelectorAll(".worker-turn-row");
  assert.equal(rowsAfterReopen.length, 20, "disconnected older cache must not be concatenated across a gap");
  assert.match(rowsAfterReopen[0].textContent, /#45/);
  assert.match(rowsAfterReopen[19].textContent, /#26/);
  const loadOlderBtn = detailEl.querySelector(".worker-load-older");
  assert.ok(loadOlderBtn, "expected Load earlier turns button from new head page cursor");

  loadOlderBtn.click();
  await new Promise(r => setTimeout(r, 20));
  const rowsAfterFill = detailEl.querySelectorAll(".worker-turn-row");
  assert.equal(rowsAfterFill.length, 40);
  assert.match(rowsAfterFill[20].textContent, /#25/, "middle turns #25..#21 must be contiguous without gap");
  panel.stop();
});

test("P2: completed turn with >100 logs continues paginating until has_more=false and then stops", async () => {
  const byId = installFakeDOM();
  const {workersPanel} = await import("../web/workers-panel.mjs");

  const w1 = {worker_id: "w1", alias: "log-heavy", harness_id: "codex", access_mode: "read_only", state: "idle", workspace_root: "F:\\proj"};
  const t1 = {turn_id: "turn_logs", worker_id: "w1", turn_seq: 1, state: "completed", started_at: "2026-11-21T09:00:00Z"};
  const allLogs = Array.from({length: 220}, (_, i) => ({seq: i + 1, kind: "tool", summary: `L${i + 1}`}));
  let logCalls = 0;

  const api = async (url) => {
    if (url.startsWith("/api/workers/list")) return {items: [w1]};
    if (url.startsWith("/api/workers/get")) return {worker: w1, turns: [t1]};
    if (url.startsWith("/api/workers/logs")) {
      logCalls++;
      const m = /after_seq=(\d+)/.exec(url);
      const after = m ? Number(m[1]) : 0;
      const slice = allLogs.filter(e => e.seq > after).slice(0, 100);
      const hasMore = after + slice.length < allLogs.length;
      return {turn: t1, entries: slice, has_more: hasMore};
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
  const logBox = detailEl.querySelector(".worker-logs-box");
  logBox.scrollTop = 140;
  assert.equal(logBox.children.length, 100);
  assert.ok(detailEl.querySelector(".worker-load-more-logs"), "expected Load more logs button when has_more=true");

  await panel.refreshIfOpen(); // fetches 101..200
  assert.equal(logBox.children.length, 200);
  assert.equal(detailEl.querySelector(".worker-logs-box"), logBox, "log box node must stay in-place");
  assert.equal(logBox.scrollTop, 140);

  await panel.refreshIfOpen(); // fetches 201..220 (has_more=false)
  assert.equal(logBox.children.length, 220);
  assert.equal(detailEl.querySelector(".worker-load-more-logs"), null, "button removed once all logs loaded");
  assert.equal(logCalls, 3);

  await panel.refreshIfOpen(); // terminal & has_more=false -> zero extra requests!
  assert.equal(logCalls, 3, "completed turn with all logs loaded must stop polling logs");
  panel.stop();
});

test("P2: late unfiltered poll response does not overwrite active attention filter", async () => {
  const byId = installFakeDOM();
  const {workersPanel} = await import("../web/workers-panel.mjs");

  const w1 = {worker_id: "w1", alias: "race-worker", harness_id: "codex", access_mode: "read_only", state: "idle", workspace_root: "F:\\proj"};
  const tOk = {turn_id: "t_ok", worker_id: "w1", turn_seq: 2, state: "completed", started_at: "2026-11-21T09:02:00Z"};
  const tFail = {turn_id: "t_fail", worker_id: "w1", turn_seq: 1, state: "failed", started_at: "2026-11-21T09:01:00Z"};

  let holdGet = null;
  const api = async (url) => {
    if (url.startsWith("/api/workers/list")) return {items: [w1]};
    if (url.startsWith("/api/workers/get")) {
      if (holdGet && !url.includes("filter=attention")) {
        const g = holdGet;
        holdGet = null;
        await g.promise;
        return {worker: w1, summary: {turn_count: 99, turns_seconds: 10, wall_seconds: 20, attention_count: 1}, turns: [tOk, tFail]};
      }
      return {worker: w1, summary: {turn_count: 2, turns_seconds: 10, wall_seconds: 20, attention_count: 1}, turns: [tOk, tFail]};
    }
    if (url.startsWith("/api/workers/turns") && url.includes("filter=attention")) {
      return {items: [tFail], has_more: false};
    }
    if (url.startsWith("/api/workers/logs")) return {turn: tOk, entries: []};
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
  assert.equal(detailEl.querySelectorAll(".worker-turn-row").length, 2);

  // Start an unfiltered background refresh and hold its /api/workers/get response.
  holdGet = defer();
  const pendingPoll = holdGet;
  const pollDone = panel.refreshIfOpen();
  await new Promise(r => setTimeout(r, 5));

  // User switches to "只看异常" before the held unfiltered poll returns.
  const attBtn = detailEl.querySelectorAll(".worker-filter-btn")[1];
  attBtn.click();
  await new Promise(r => setTimeout(r, 20));
  assert.equal(attBtn.getAttribute("aria-pressed"), "true");
  assert.equal(detailEl.querySelectorAll(".worker-turn-row").length, 1);

  // Now release the stale unfiltered poll -> must keep attention list while still updating summary!
  pendingPoll.resolve();
  await pollDone;
  await new Promise(r => setTimeout(r, 20));

  assert.equal(attBtn.getAttribute("aria-pressed"), "true");
  const rows = detailEl.querySelectorAll(".worker-turn-row");
  assert.equal(rows.length, 1, "stale unfiltered poll must not put completed turns back into attention view");
  assert.match(rows[0].textContent, /#1/);
  assert.match(detailEl.textContent, /99/, "worker summary from poll still updates");
  panel.stop();
});

test("P2/P3: failed turn paginates partial output, prompt loadRest preserves scrollTop, and prompt CSS max-height wins", async () => {
  const byId = installFakeDOM();
  const {workersPanel} = await import("../web/workers-panel.mjs");

  const w1 = {worker_id: "w1", alias: "partial-worker", harness_id: "codex", access_mode: "read_only", state: "idle", workspace_root: "F:\\proj"};
  const t1 = {turn_id: "t_part", worker_id: "w1", turn_seq: 1, state: "failed", prompt_bytes: 40, started_at: "2026-11-21T09:00:00Z"};

  const api = async (url) => {
    if (url.startsWith("/api/workers/list")) return {items: [w1]};
    if (url.startsWith("/api/workers/get")) return {worker: w1, turns: [t1]};
    if (url.startsWith("/api/workers/logs")) {
      return {
        turn: t1,
        entries: [],
        result: {text: "PARTIAL_HEAD_", partial: true, complete: false, next_cursor: "13"},
      };
    }
    if (url.startsWith("/api/workers/result") && url.includes("part=prompt")) {
      if (url.includes("cursor=12")) return {text: "PROMPT_TAIL", part: "prompt", complete: true};
      return {text: "PROMPT_HEAD_", part: "prompt", complete: false, next_cursor: "12"};
    }
    if (url.startsWith("/api/workers/result") && url.includes("cursor=13")) {
      return {text: "PARTIAL_TAIL", partial: true, complete: true};
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
  assert.match(detailEl.textContent, /部分输出|Partial output/);
  assert.doesNotMatch(detailEl.textContent, /结果正文/);

  const promptPre = detailEl.querySelector(".worker-prompt-block");
  const answerPre = detailEl.querySelector(".worker-answer-block");
  promptPre.scrollTop = 420;
  answerPre.scrollTop = 310;

  const restBtns = detailEl.querySelectorAll(".worker-load-rest");
  assert.equal(restBtns.length, 2, "expected load-rest buttons for both prompt and partial output");
  restBtns[0].click();
  restBtns[1].click();
  await new Promise(r => setTimeout(r, 20));

  const promptAfter = detailEl.querySelector(".worker-prompt-block");
  const answerAfter = detailEl.querySelector(".worker-answer-block");
  assert.equal(promptAfter.textContent, "PROMPT_HEAD_PROMPT_TAIL");
  assert.equal(answerAfter.textContent, "PARTIAL_HEAD_PARTIAL_TAIL");
  assert.equal(promptAfter.scrollTop, 420, "prompt scrollTop must not reset to 0 on loadRest");
  assert.equal(answerAfter.scrollTop, 310, "partial output scrollTop must not reset to 0 on loadRest");

  const css = readFileSync(new URL("../web/workers.css", import.meta.url), "utf8");
  const codeIdx = css.indexOf(".worker-code-block {");
  const promptIdx = css.indexOf(".worker-code-block.worker-prompt-block { max-height: 160px; }");
  assert.ok(codeIdx >= 0 && promptIdx > codeIdx, "prompt max-height rule must have higher specificity and follow .worker-code-block");
  panel.stop();
});

test("P2 perf: 500 loaded turns and 20 expanded large turns avoid O(N^2) ID scans and JSON re-serialization on idle render", async () => {
  installFakeDOM();
  const {createTurnTimeline} = await import("../web/workers-timeline.mjs");

  let idReads = 0;
  const makeItem = (seq) => {
    const rawId = `turn_${String(seq).padStart(4, "0")}`;
    const obj = {
      worker_id: "w_perf",
      turn_seq: seq,
      state: "completed",
      prompt_preview: "preview " + seq,
      prompt_bytes: 32768,
      elapsed_seconds: 12,
      started_at: "2026-11-21T09:00:00Z",
    };
    Object.defineProperty(obj, "turn_id", {
      enumerable: true,
      get() { idReads++; return rawId; },
    });
    return obj;
  };
  const items500 = Array.from({length: 500}, (_, i) => makeItem(500 - i));
  const bigPrompt = "P".repeat(32768);
  const bigAnswer = "A".repeat(16384);

  const tl = createTurnTimeline({
    api: async (url) => {
      if (url.startsWith("/api/workers/logs")) {
        return {turn: {turn_id: "t", state: "completed", prompt_bytes: 32768}, entries: [], result: {text: bigAnswer, complete: true}};
      }
      if (url.startsWith("/api/workers/result")) {
        return {text: bigPrompt, part: "prompt", complete: true};
      }
      throw new Error("unexpected " + url);
    },
    workerID: "w_perf",
  });

  await tl.applyFirstPage({turns: items500, turns_has_more: false});
  tl.render();
  // Expand 19 additional turns so 20 large completed turns are open simultaneously.
  const rows = tl.root.querySelectorAll(".worker-turn-row");
  for (let i = 1; i < 20; i++) rows[i].click();
  await new Promise(r => setTimeout(r, 30));

  const origStringify = JSON.stringify;
  let stringifyBytes = 0;
  JSON.stringify = (...args) => {
    const s = origStringify(...args);
    if (typeof s === "string") stringifyBytes += s.length;
    return s;
  };
  idReads = 0;
  try {
    tl.render();
  } finally {
    JSON.stringify = origStringify;
  }

  assert.ok(idReads <= 600, `expected linear turn_id reads (~500), got ${idReads}`);
  assert.equal(stringifyBytes, 0, `expected 0 JSON.stringify bytes on unchanged timeline render, got ${stringifyBytes}`);
  tl.dispose();
});

test("R1 & R4: incremental log refresh preserves in-flight answer continuation, and mid-pagination expiry warns clearly", async () => {
  const byId = installFakeDOM();
  const {workersPanel} = await import("../web/workers-panel.mjs");

  const w1 = {worker_id: "w1", alias: "r1-r4", harness_id: "codex", access_mode: "read_only", state: "idle", workspace_root: "F:\\proj"};
  const t1 = {turn_id: "turn_r1", worker_id: "w1", turn_seq: 1, state: "completed", prompt_bytes: 40000, started_at: "2026-11-21T09:00:00Z", answer_available: true};
  const firstAns = "A".repeat(16384);
  const restAns = "B".repeat(8192);
  const firstPrompt = "P".repeat(32768);

  let holdAnswerRest = null;
  let logCalls = 0;
  const api = async (url) => {
    if (url.startsWith("/api/workers/list")) return {items: [w1]};
    if (url.startsWith("/api/workers/get")) return {worker: w1, turns: [t1]};
    if (url.startsWith("/api/workers/logs")) {
      logCalls++;
      if (logCalls === 1) {
        return {
          turn: t1,
          entries: Array.from({length: 100}, (_, i) => ({seq: i + 1, kind: "tool", summary: `L${i + 1}`})),
          has_more: true,
          result: {text: firstAns, complete: false, next_cursor: "16384"},
        };
      }
      return {
        turn: t1,
        entries: [{seq: 101, kind: "tool", summary: "L101"}],
        has_more: false,
        result: {text: firstAns, complete: false, next_cursor: "16384"},
      };
    }
    if (url.startsWith("/api/workers/result") && url.includes("part=prompt")) {
      if (url.includes("cursor=32768")) {
        // R4: detail expired before second page was fetched!
        return {text: "", part: "prompt", expired: true, complete: true};
      }
      return {text: firstPrompt, part: "prompt", complete: false, next_cursor: "32768"};
    }
    if (url.startsWith("/api/workers/result") && url.includes("cursor=16384")) {
      await holdAnswerRest.promise;
      return {text: restAns, complete: true};
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
  holdAnswerRest = defer();

  // Start answer continuation (held in flight), then let incremental log poll finish first!
  const restBtns = detailEl.querySelectorAll(".worker-load-rest");
  assert.equal(restBtns.length, 2);
  restBtns[1].click(); // answer loadRest
  await new Promise(r => setTimeout(r, 5));

  await panel.refreshIfOpen(); // fetches log #101 + identical 16 KiB result first page
  holdAnswerRest.resolve();
  await new Promise(r => setTimeout(r, 20));

  const answerPre = detailEl.querySelector(".worker-answer-block");
  assert.equal(answerPre.textContent.length, firstAns.length + restAns.length, "R1: log poll must not clobber in-flight answer continuation");

  // R4: click prompt loadRest -> receives expired=true, must retain 32 KiB prefix AND show expiry warning!
  const promptRestBtn = detailEl.querySelector(".worker-load-rest");
  assert.ok(promptRestBtn);
  promptRestBtn.click();
  await new Promise(r => setTimeout(r, 20));

  assert.equal(detailEl.querySelector(".worker-load-rest"), null);
  assert.equal(detailEl.querySelector(".worker-prompt-block").textContent.length, 32768);
  assert.match(detailEl.textContent, /剩余内容已按保留策略清理|Remaining content was removed/);
  panel.stop();
});

test("R2: failed switch to attention filter never leaks cached completed turns or drops cursor on subsequent polls", async () => {
  const byId = installFakeDOM();
  const {workersPanel} = await import("../web/workers-panel.mjs");

  const w1 = {worker_id: "w1", alias: "r2-filter", harness_id: "codex", access_mode: "read_only", state: "idle", workspace_root: "F:\\proj"};
  const all40 = Array.from({length: 40}, (_, i) => {
    const seq = 40 - i;
    const completed = seq % 4 === 0; // #40, #36, ..., #12, #8, #4 are completed (10 completed, 30 failed)
    return {
      turn_id: `t_${seq}`,
      worker_id: "w1",
      turn_seq: seq,
      state: completed ? "completed" : "failed",
      started_at: new Date(Date.UTC(2026, 10, 21, 8, 0, seq)).toISOString(),
    };
  });
  const attAll30 = all40.filter(x => x.state === "failed");

  let failFirstAtt = true;
  const api = async (url) => {
    if (url.startsWith("/api/workers/list")) return {items: [w1]};
    if (url.startsWith("/api/workers/get")) {
      if (url.includes("filter=attention")) {
        return {worker: w1, turns: attAll30.slice(0, 20), turns_has_more: true, turns_next_cursor: "cur_att_20"};
      }
      return {worker: w1, turns: all40.slice(0, 20), turns_has_more: true, turns_next_cursor: "cur_all_20"};
    }
    if (url.startsWith("/api/workers/turns") && url.includes("cursor=cur_all_20")) {
      return {items: all40.slice(20), has_more: false};
    }
    if (url.startsWith("/api/workers/turns") && url.includes("filter=attention")) {
      if (failFirstAtt) {
        failFirstAtt = false;
        throw new Error("transient filter failure");
      }
      return {items: attAll30.slice(20), has_more: false};
    }
    if (url.startsWith("/api/workers/logs")) return {turn: all40[0], entries: []};
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
  detailEl.querySelector(".worker-load-older").click(); // load all 40 in "all"
  await new Promise(r => setTimeout(r, 20));
  assert.equal(detailEl.querySelectorAll(".worker-turn-row").length, 40);

  // Switch to "只看异常" where initial request fails, then run two attention polls.
  detailEl.querySelectorAll(".worker-filter-btn")[1].click();
  await new Promise(r => setTimeout(r, 20));
  await panel.refreshIfOpen();
  await panel.refreshIfOpen();

  const rows = detailEl.querySelectorAll(".worker-turn-row");
  assert.equal(rows.length, 20, "must show the 20 newest attention turns without merging completed tail");
  for (const r of rows) {
    assert.doesNotMatch(r.textContent, /#(40|36|32|28|24|20|16|12|8|4)\b/, "completed turns must never appear in attention filter");
  }
  assert.ok(detailEl.querySelector(".worker-load-older"), "attention pagination cursor must stay available");
  panel.stop();
});

test("R6, R7 & R8: closed-dialog relocalize updates timeline, wall_seconds tick preserves close button, and 0-turn worker auto-expands first completed turn", async () => {
  const byId = installFakeDOM();
  const {setLocale} = await import("../web/i18n.mjs");
  const {workersPanel} = await import("../web/workers-panel.mjs");

  const w1 = {worker_id: "w1", alias: "ux-worker", harness_id: "codex", access_mode: "read_only", state: "idle", workspace_root: "F:\\proj", role_hint: "Line1\nLine2\nLine3"};
  const t1 = {turn_id: "t_first", worker_id: "w1", turn_seq: 1, state: "failed", started_at: "2026-11-21T09:00:00Z"};

  let turns = [];
  let wallSec = 10;
  let logFetches = 0;

  const api = async (url) => {
    if (url.startsWith("/api/workers/list")) return {items: [w1]};
    if (url.startsWith("/api/workers/get")) {
      return {worker: w1, summary: {turn_count: turns.length, turns_seconds: 5, wall_seconds: wallSec, attention_count: turns.length}, turns};
    }
    if (url.startsWith("/api/workers/logs")) {
      logFetches++;
      return {turn: t1, entries: []};
    }
    throw new Error("unexpected url " + url);
  };

  setLocale("en-US");
  try {
    const panel = workersPanel({
      api,
      post: async () => ({}),
      listen: (t, e, cb) => t.addEventListener(e, cb),
      session: () => ({active: true}),
      notice: () => {},
    });

    // R8: open when worker has 0 turns; first turn completes before next poll -> must auto-expand!
    await panel.open();
    const detailEl = byId.get("workers-detail");
    const headActions = detailEl.querySelector(".worker-detail-head").querySelector(".actions");
    const closeBtnBefore = headActions.querySelectorAll("button")[0];
    assert.ok(closeBtnBefore);

    // R7: idle poll advances only wall_seconds -> close button DOM node MUST remain identical!
    wallSec = 13;
    await panel.refreshIfOpen();
    const closeBtnAfter = detailEl.querySelector(".worker-detail-head").querySelector(".actions").querySelectorAll("button")[0];
    assert.equal(closeBtnAfter, closeBtnBefore, "R7: wall_seconds tick must not replace close worker button");
    assert.match(detailEl.textContent, /13s/);

    // R8 continued: first already-terminal turn arrives on poll -> auto-expanded and logs fetched!
    turns = [t1];
    await panel.refreshIfOpen();
    const rowEn = detailEl.querySelector(".worker-turn-row");
    assert.equal(rowEn.getAttribute("aria-expanded"), "true", "R8: first arriving turn must auto-expand even if already terminal");
    assert.equal(logFetches, 1);
    assert.match(detailEl.textContent, /Failed/);

    // R6: close panel, switch locale to zh-CN while closed, reopen -> turn row and detail badge must localize!
    panel.stop();
    setLocale("zh-CN");
    panel.relocalize();
    await panel.open();

    assert.match(detailEl.textContent, /失败/);
    assert.doesNotMatch(detailEl.textContent, /Failed/);
    panel.stop();
  } finally {
    setLocale("zh-CN");
  }
});

test("third-review regressions: late filter head never reverts loaded older pages and complete answer is never mislabeled as prefix on expiry", async () => {
  const byId = installFakeDOM();
  const {workersPanel} = await import("../web/workers-panel.mjs");

  const w1 = {worker_id: "w1", alias: "r3-worker", harness_id: "codex", access_mode: "read_only", state: "idle", workspace_root: "F:\\proj"};
  const att40 = Array.from({length: 40}, (_, i) => {
    const seq = 40 - i;
    return {
      turn_id: `t_att_${seq}`,
      worker_id: "w1",
      turn_seq: seq,
      state: seq === 40 ? "completed" : "failed",
      answer_available: seq === 40,
      result_bytes: seq === 40 ? 12 : 0,
      started_at: new Date(Date.UTC(2026, 10, 22, 8, 0, seq)).toISOString(),
    };
  });
  const onlyAtt40 = Array.from({length: 40}, (_, i) => {
    const seq = 40 - i;
    return {
      turn_id: `t_only_att_${seq}`,
      worker_id: "w1",
      turn_seq: seq,
      state: "failed",
      started_at: new Date(Date.UTC(2026, 10, 22, 9, 0, seq)).toISOString(),
    };
  });

  let holdInitialFilterTurns = null;
  let serverResultExpired = false;

  const api = async (url) => {
    if (url.startsWith("/api/workers/list")) return {items: [w1]};
    if (url.startsWith("/api/workers/get")) {
      if (url.includes("filter=attention")) {
        return {worker: w1, turns: onlyAtt40.slice(0, 20), turns_has_more: true, turns_next_cursor: "cur_att_20"};
      }
      return {worker: w1, turns: att40.slice(0, 20), turns_has_more: true, turns_next_cursor: "cur_all_20"};
    }
    if (url.startsWith("/api/workers/turns") && url.includes("filter=attention")) {
      if (url.includes("cursor=cur_att_20")) {
        return {items: onlyAtt40.slice(20), has_more: false, next_cursor: ""};
      }
      if (holdInitialFilterTurns) await holdInitialFilterTurns.promise;
      return {items: onlyAtt40.slice(0, 20), has_more: true, next_cursor: "cur_att_20"};
    }
    if (url.startsWith("/api/workers/logs")) {
      if (url.includes("t_att_40")) {
        const afterZero = !url.includes("after_seq=") || url.includes("after_seq=0");
        if (afterZero) {
          return {
            turn: att40[0],
            entries: [{seq: 1, kind: "tool", summary: "page-1", timestamp: "2026-11-22T08:00:40Z"}],
            has_more: true,
            result: {text: "12 bytes ans", complete: true, expired: false, total_bytes: 12},
          };
        }
        return {
          turn: att40[0],
          entries: [{seq: 2, kind: "tool", summary: "page-2", timestamp: "2026-11-22T08:00:41Z"}],
          has_more: false,
          result: serverResultExpired
            ? {text: "", complete: true, expired: true, total_bytes: 12}
            : {text: "12 bytes ans", complete: true, expired: false, total_bytes: 12},
        };
      }
      return {turn: onlyAtt40[0], entries: [], has_more: false};
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

  // 1. Complete 12-byte answer already displayed; server expires detail before log page 2 returns.
  assert.equal(detailEl.querySelector(".worker-answer-block").textContent, "12 bytes ans");
  serverResultExpired = true;
  const logMoreBtn = detailEl.querySelector(".worker-load-more-logs");
  assert.ok(logMoreBtn);
  logMoreBtn.click();
  await new Promise(r => setTimeout(r, 20));

  assert.equal(detailEl.querySelector(".worker-answer-block").textContent, "12 bytes ans");
  assert.match(detailEl.textContent, /完整结果正文已按保留策略清理|full result was removed/);
  assert.doesNotMatch(detailEl.textContent, /仅保留已加载前缀|loaded prefix only/, "already-complete answer must not claim trailing text is missing");

  // 2. Click "只看异常" (held) -> same-filter poll completes first -> load older (40 rows) -> release held initial filter head.
  holdInitialFilterTurns = defer();
  detailEl.querySelectorAll(".worker-filter-btn")[1].click();
  await new Promise(r => setTimeout(r, 5));

  await panel.refreshIfOpen();
  assert.equal(detailEl.querySelectorAll(".worker-turn-row").length, 20);
  detailEl.querySelector(".worker-load-older").click();
  await new Promise(r => setTimeout(r, 20));
  assert.equal(detailEl.querySelectorAll(".worker-turn-row").length, 40, "loaded all 40 attention turns");
  assert.equal(detailEl.querySelector(".worker-load-older"), null);

  holdInitialFilterTurns.resolve();
  holdInitialFilterTurns = null;
  await new Promise(r => setTimeout(r, 20));

  assert.equal(detailEl.querySelectorAll(".worker-turn-row").length, 40, "late initial filter head response must not revert 40 loaded turns back to 20");
  assert.equal(detailEl.querySelector(".worker-load-older"), null);

  await panel.refreshIfOpen();
  assert.equal(detailEl.querySelectorAll(".worker-turn-row").length, 40, "subsequent poll must also preserve all 40 turns");
  panel.stop();
});

test("fourth-review regression: concurrent loadRest arriving after log expiry reconciles final vs middle page cleanly", async () => {
  const {workersPanel} = await import("../web/workers-panel.mjs");
  const w1 = {worker_id: "w1", alias: "r4-expiry", harness_id: "codex", access_mode: "read_only", state: "idle", workspace_root: "F:\\proj"};
  const prefix = "A".repeat(16 * 1024);

  for (const lastPage of [true, false]) {
    const byId = installFakeDOM();
    const holdResult = defer();
    const requested = defer();
    const suffix = "B".repeat(lastPage ? 8192 : 32768);
    const totalBytes = prefix.length + suffix.length + (lastPage ? 0 : 1024);
    const turn = {
      turn_id: "t_conc",
      worker_id: "w1",
      turn_seq: 1,
      state: "completed",
      answer_available: true,
      result_bytes: totalBytes,
      started_at: "2026-11-21T09:00:00Z",
    };

    const api = async (url) => {
      if (url.startsWith("/api/workers/list")) return {items: [w1]};
      if (url.startsWith("/api/workers/get")) return {worker: w1, turns: [turn]};
      if (url.startsWith("/api/workers/logs")) {
        if (!url.includes("after_seq=")) {
          return {
            turn,
            entries: Array.from({length: 100}, (_, i) => ({seq: i + 1, kind: "tool", summary: "ok"})),
            has_more: true,
            result: {text: prefix, complete: false, total_bytes: totalBytes, next_cursor: String(prefix.length)},
          };
        }
        return {
          turn,
          entries: [{seq: 101, kind: "tool", summary: "done"}],
          has_more: false,
          result: {text: "", complete: true, expired: true, total_bytes: totalBytes},
        };
      }
      if (url.startsWith("/api/workers/result")) {
        requested.resolve();
        await holdResult.promise;
        return {
          text: suffix,
          complete: lastPage,
          total_bytes: totalBytes,
          next_cursor: lastPage ? "" : String(prefix.length + suffix.length),
        };
      }
      throw new Error("unexpected url " + url);
    };

    const panel = workersPanel({
      api,
      post: async () => ({}),
      listen: (t, e, cb) => t.addEventListener(e, cb),
      session: () => ({active: true}),
      notice: err => { throw new Error(String(err)); },
    });

    await panel.open();
    const detailEl = byId.get("workers-detail");
    detailEl.querySelector(".worker-load-rest").click();
    await requested.promise;

    // Incremental log poll arrives first and marks result expired while loadRest is in flight.
    await panel.refreshIfOpen();

    // Now deliver the in-flight continuation page.
    holdResult.resolve();
    await new Promise(r => setTimeout(r, 20));

    assert.equal(detailEl.querySelector(".worker-answer-block").textContent, prefix + suffix);
    assert.equal(detailEl.querySelector(".worker-load-rest"), null, "loadRest button must stay hidden once server reported expiry");
    if (lastPage) {
      assert.match(detailEl.textContent, /完整结果正文已按保留策略清理|full result was removed/);
      assert.doesNotMatch(detailEl.textContent, /仅保留已加载前缀|loaded prefix only/, "late final page delivered all bytes and must clear prefix warning");
    } else {
      assert.match(detailEl.textContent, /仅保留已加载前缀|loaded prefix only/, "late middle page still lacks trailing bytes and must keep prefix warning");
    }
    panel.stop();
  }
});
