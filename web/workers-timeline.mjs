import {node} from "./ui.mjs";
import {t} from "./i18n.mjs";

export const TERMINAL_STATES = new Set([
  "completed",
  "failed",
  "cancelled",
  "cancelled_authorization_revoked",
  "interrupted",
  "blocked_unsupported_approval",
  "blocked_unsupported_user_input",
]);

const TURN_STATE_KEYS = {
  prepared: "web.workers.turn_state_prepared",
  running: "web.workers.turn_state_running",
  waiting_approval: "web.workers.turn_state_waiting_approval",
  waiting_input: "web.workers.turn_state_waiting_input",
  completed: "web.workers.turn_state_completed",
  failed: "web.workers.turn_state_failed",
  cancelled: "web.workers.turn_state_cancelled",
  cancelled_authorization_revoked: "web.workers.turn_state_cancelled_authorization_revoked",
  interrupted: "web.workers.turn_state_interrupted",
  reconciliation_required: "web.workers.turn_state_reconciliation_required",
  blocked_unsupported_approval: "web.workers.turn_state_blocked_unsupported_approval",
  blocked_unsupported_user_input: "web.workers.turn_state_blocked_unsupported_user_input",
};

const PAGE_SIZE = 20;
const RESULT_PAGE_BYTES = 32 << 10;

export function turnStateLabel(state) {
  const key = TURN_STATE_KEYS[state];
  return key ? t(key) : state;
}

export function stateTone(state) {
  if (state === "running" || state === "prepared" || state === "waiting_approval" || state === "waiting_input") return "warn";
  if (state === "completed" || state === "idle") return "ok";
  if (state === "reconciliation_required" || state.startsWith("blocked_")) return "warn";
  if (state === "failed" || state === "interrupted" || state.startsWith("cancelled")) return "error";
  return "";
}

const pad2 = n => String(n).padStart(2, "0");

// Compact duration: 42s, 18m42s, 1h05m.
export function formatDuration(totalSeconds) {
  const s = Math.max(0, Math.floor(Number(totalSeconds) || 0));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m${pad2(s % 60)}s`;
  return `${Math.floor(s / 3600)}h${pad2(Math.floor((s % 3600) / 60))}m`;
}

// Same-day timestamps show the clock time; older ones also show the date.
export function formatTurnTime(raw, nowMs = Date.now()) {
  if (!raw) return "";
  const d = new Date(raw);
  if (Number.isNaN(d.getTime())) return "";
  const now = new Date(nowMs);
  const clock = `${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
  if (d.toDateString() === now.toDateString()) return `${clock}:${pad2(d.getSeconds())}`;
  return `${pad2(d.getMonth() + 1)}-${pad2(d.getDate())} ${clock}`;
}

function formatLogTime(raw) {
  if (!raw) return "";
  const d = new Date(raw);
  if (Number.isNaN(d.getTime())) return "";
  return d.toTimeString().slice(0, 8);
}

export function createLogRow(entry) {
  const row = node("div", null, "worker-log-row");
  const ts = formatLogTime(entry.timestamp);
  const tag = entry.phase && entry.phase !== entry.kind ? `${entry.kind}/${entry.phase}` : entry.kind;
  if (ts) row.append(node("span", ts, "worker-log-time"));
  row.append(node("span", tag, "worker-log-tag"), node("span", entry.summary || "", "worker-log-msg"));
  return row;
}

export function isPinnedToBottom(el) {
  if (!el) return false;
  return (el.scrollHeight - el.scrollTop - el.clientHeight) <= 24;
}

function removeChildNode(parent, child) {
  if (!child) return;
  if (typeof child.remove === "function") {
    child.remove();
    return;
  }
  const arr = parent?.children;
  if (Array.isArray(arr)) {
    const i = arr.indexOf(child);
    if (i >= 0) arr.splice(i, 1);
  }
}

function renderVCSSnapshot(turn) {
  const snap = turn.after_vcs || turn.baseline_vcs;
  if (!snap) return null;
  const box = node("section", null, "worker-vcs-box");
  box.append(node("h4", t("web.workers.vcs_heading")));
  if (snap.vcs === "none") {
    box.append(node("p", t("web.workers.vcs_none"), "worker-meta-line"));
    return box;
  }
  const baseDirty = Boolean(turn.baseline_vcs?.dirty_baseline);
  const meta = [
    snap.branch || "",
    snap.head_commit || "",
    t(baseDirty ? "web.workers.vcs_dirty_baseline" : "web.workers.vcs_clean_baseline"),
  ].filter(Boolean).join(" · ");
  box.append(
    node("p", meta, "worker-meta-line"),
    node("p", t("web.workers.vcs_counts", {
      staged: String(snap.staged_count || 0),
      unstaged: String(snap.unstaged_count || 0),
      untracked: String(snap.untracked_count || 0),
    }), "worker-meta-line"),
  );
  if (Array.isArray(snap.changed_files) && snap.changed_files.length) {
    box.append(node("div", snap.changed_files.join("\n"), "worker-code-block"));
  }
  if (snap.external_changes_possible) {
    box.append(node("small", t("web.workers.vcs_external_note"), "harness-note"));
  }
  return box;
}

function buildRoleHintElement(hint, expanded, onToggle) {
  const wrap = node("div", null, "worker-role-hint");
  const head = node("div", null, "worker-role-hint-head");
  head.append(node("span", t("web.workers.role_hint_heading"), "worker-role-hint-title"));
  const long = hint.length > 160 || hint.split("\n").length > 2;
  if (long) {
    const btn = node("button", t(expanded ? "web.workers.collapse" : "web.workers.expand"), "link-button");
    btn.type = "button";
    btn.addEventListener("click", () => onToggle?.());
    head.append(btn);
  }
  const body = node("div", hint, "worker-role-hint-body");
  if (long && !expanded) body.dataset.clamped = "true";
  wrap.append(head, body);
  return wrap;
}

// In-place Worker summary updater: updates duration counters without replacing
// role-hint controls or surrounding header buttons.
export function createWorkerSummaryUpdater(onToggleRoleHint) {
  const el = node("section", null, "worker-summary");
  let roleEl = null;
  let lastRoleSig = null;
  let statsEl = null;
  let turnsSpan = null;
  let turnsDurSpan = null;
  let wallDurSpan = null;
  let attSpan = null;
  let partialEl = null;
  let lastStatsSig = null;

  function update(worker, summary, {roleHintExpanded = false, localeRev = 0, force = false} = {}) {
    const langMark = `${localeRev}:${t("web.workers.turns_heading")}`;
    const hint = String(worker?.role_hint || "").trim();
    const roleSig = hint ? `${hint}|${roleHintExpanded ? 1 : 0}|${langMark}` : "";
    let layoutDirty = false;

    if (force || roleSig !== lastRoleSig) {
      lastRoleSig = roleSig;
      roleEl = hint ? buildRoleHintElement(hint, roleHintExpanded, onToggleRoleHint) : null;
      layoutDirty = true;
    }

    const statsSig = summary
      ? `${summary.turn_count || 0}|${summary.turns_seconds || 0}|${summary.wall_seconds || 0}|${summary.attention_count || 0}|${summary.partial ? 1 : 0}|${langMark}`
      : "";
    if (force || statsSig !== lastStatsSig) {
      lastStatsSig = statsSig;
      if (!summary) {
        if (statsEl || partialEl) layoutDirty = true;
        statsEl = null;
        partialEl = null;
      } else {
        if (!statsEl) {
          statsEl = node("div", null, "worker-summary-stats");
          turnsSpan = node("span", "");
          turnsDurSpan = node("span", "");
          wallDurSpan = node("span", "");
          statsEl.append(turnsSpan, turnsDurSpan, wallDurSpan);
          attSpan = null;
          layoutDirty = true;
        }
        turnsSpan.textContent = t("web.workers.summary_turns", {count: String(summary.turn_count || 0)});
        turnsDurSpan.textContent = t("web.workers.summary_turns_seconds", {duration: formatDuration(summary.turns_seconds)});
        wallDurSpan.textContent = t("web.workers.summary_wall_seconds", {duration: formatDuration(summary.wall_seconds)});
        if (summary.attention_count > 0) {
          const attText = t("web.workers.summary_attention", {count: String(summary.attention_count)});
          if (!attSpan) {
            attSpan = node("span", attText, "worker-summary-attention");
            statsEl.append(attSpan);
          } else {
            attSpan.textContent = attText;
          }
        } else if (attSpan) {
          removeChildNode(statsEl, attSpan);
          attSpan = null;
        }
        if (summary.partial) {
          const pText = t("web.workers.summary_partial");
          if (!partialEl) {
            partialEl = node("small", pText, "harness-note");
            layoutDirty = true;
          } else {
            partialEl.textContent = pText;
          }
        } else if (partialEl) {
          partialEl = null;
          layoutDirty = true;
        }
      }
    }

    const children = [];
    if (roleEl) children.push(roleEl);
    if (statsEl) children.push(statsEl);
    if (partialEl) children.push(partialEl);
    if (layoutDirty) el.replaceChildren(...children);
    return children.length > 0;
  }

  return {el, update};
}

// Stateless helper retained for standalone callers.
export function renderWorkerSummary(worker, summary, {roleHintExpanded = false, onToggleRoleHint} = {}) {
  const u = createWorkerSummaryUpdater(onToggleRoleHint);
  return u.update(worker, summary, {roleHintExpanded, force: true}) ? u.el : null;
}

function pageState(page) {
  if (!page) return null;
  const expired = Boolean(page.expired);
  const complete = page.complete !== false;
  return {
    text: page.text || "",
    complete: expired || complete,
    reachedEnd: !expired && complete,
    nextCursor: expired ? "" : (page.next_cursor || ""),
    expired,
    isFallbackPreview: false,
    partial: Boolean(page.partial),
    partialOutput: page.partial_output || "",
    loading: false,
  };
}

function markPageExpired(page) {
  page.expired = true;
  page.complete = true;
  page.nextCursor = "";
}

function samePageContent(a, b) {
  if (a === b) return true;
  if (!a || !b) return false;
  return a.text === b.text
    && a.complete === b.complete
    && a.nextCursor === b.nextCursor
    && a.expired === b.expired
    && a.partial === b.partial
    && a.partialOutput === b.partialOutput;
}

function sameVCS(a, b) {
  if (a === b) return true;
  if (!a || !b) return false;
  return JSON.stringify(a) === JSON.stringify(b);
}

function turnBodyChanged(prev, next) {
  if (prev === next) return false;
  if (!prev || !next) return true;
  return prev.state !== next.state
    || (prev.error_message || "") !== (next.error_message || "")
    || (prev.partial_output || "") !== (next.partial_output || "")
    || (prev.prompt_bytes || 0) !== (next.prompt_bytes || 0)
    || !sameVCS(prev.baseline_vcs, next.baseline_vcs)
    || !sameVCS(prev.after_vcs, next.after_vcs);
}

function startMs(item) {
  const ms = item?.started_at ? Date.parse(item.started_at) : NaN;
  return Number.isNaN(ms) ? 0 : ms;
}

function isOlder(a, b) {
  const ma = startMs(a);
  const mb = startMs(b);
  if (ma !== mb) return ma < mb;
  const qa = a.turn_seq || 0;
  const qb = b.turn_seq || 0;
  if (qa !== qb) return qa < qb;
  return (a.turn_id || "") < (b.turn_id || "");
}

function formatLogsHeading(d) {
  const suffix = d.logsHasMore ? `${d.logs.length}+` : String(d.logs.length);
  return `${t("web.workers.logs_heading")} · ${suffix}`;
}

// Stateful per-worker timeline: owns the loaded turn list, which turns are
// expanded, and cached per-turn details, and patches its DOM in place so live
// polling never resets scroll positions or open sections.
export function createTurnTimeline({api, workerID, onAction, isBusy = () => false, notice, now = () => Date.now()}) {
  const root = node("section", null, "worker-turns-section");
  const headEl = node("div", null, "worker-turns-head");
  const listEl = node("div", null, "worker-timeline");
  const footEl = node("div", null, "worker-timeline-foot");
  root.append(headEl, listEl, footEl);

  let filter = "all";
  let itemsFilter = "all";
  let queryEpoch = 0;
  let nextHeadSeq = 0;
  let appliedHeadSeq = 0;
  let localeRev = 0;
  let items = [];
  let hasMore = false;
  let nextCursor = "";
  let initialized = false;
  let disposed = false;
  let loadingOlder = false;
  let orderSig = "";
  let headSig = "";
  let footSig = "";
  const seen = new Set();
  const expanded = new Set();
  const details = new Map();
  const views = new Map();

  const enc = encodeURIComponent;

  function detailFor(id) {
    let d = details.get(id);
    if (!d) {
      d = {
        turn: null,
        logs: [],
        lastSeq: 0,
        logsHasMore: false,
        logsLoading: false,
        logsPromise: null,
        result: null,
        prompt: null,
        loaded: false,
        logsOpen: null,
        bodyRev: 0,
      };
      details.set(id, d);
    }
    return d;
  }

  function appendLogs(d, incoming, hasMoreLogs) {
    const wasEmpty = d.logs.length === 0;
    for (const entry of Array.isArray(incoming) ? incoming : []) {
      if ((entry.seq || 0) > d.lastSeq) {
        d.logs.push(entry);
        d.lastSeq = entry.seq;
      }
    }
    d.logsHasMore = Boolean(hasMoreLogs);
    if (wasEmpty && d.logs.length > 0) d.bodyRev++;
  }

  function fetchIncrementalLogs(id, d) {
    if (d.logsPromise) return d.logsPromise;
    d.logsLoading = true;
    d.logsPromise = (async () => {
      try {
        const res = await api(`/api/workers/logs?turn_id=${enc(id)}&after_seq=${d.lastSeq}`);
        if (res.turn) {
          if (turnBodyChanged(d.turn, res.turn)) d.bodyRev++;
          d.turn = res.turn;
        }
        appendLogs(d, res.entries, res.has_more);
        if (res.result) {
          const nextRes = pageState(res.result);
          if (!d.result) {
            d.result = nextRes;
            d.bodyRev++;
          } else if (nextRes.expired && !d.result.expired) {
            markPageExpired(d.result);
            d.bodyRev++;
          } else if (!d.result.loading && !d.result.expired && d.result.text.length < nextRes.text.length && !samePageContent(d.result, nextRes)) {
            d.result = nextRes;
            d.bodyRev++;
          }
        }
      } finally {
        d.logsLoading = false;
        d.logsPromise = null;
      }
    })();
    return d.logsPromise;
  }

  async function loadDetail(item) {
    const id = item.turn_id;
    const d = detailFor(id);
    const promptBytes = item.prompt_bytes || d.turn?.prompt_bytes || 0;
    if (!d.loaded) {
      if (d.logsPromise) return d.logsPromise;
      d.logsLoading = true;
      d.logsPromise = (async () => {
        try {
          const [res, prompt] = await Promise.all([
            api(`/api/workers/logs?turn_id=${enc(id)}`),
            promptBytes > 0 && !d.prompt
              ? api(`/api/workers/result?turn_id=${enc(id)}&part=prompt&max_bytes=${RESULT_PAGE_BYTES}`)
              : Promise.resolve(null),
          ]);
          d.turn = res.turn || item;
          d.logs = Array.isArray(res.entries) ? res.entries : [];
          d.lastSeq = d.logs.length ? (d.logs[d.logs.length - 1].seq || 0) : 0;
          d.logsHasMore = Boolean(res.has_more);
          d.result = pageState(res.result);
          if (prompt) d.prompt = pageState(prompt);
          d.loaded = true;
          d.bodyRev++;
        } finally {
          d.logsLoading = false;
          d.logsPromise = null;
        }
      })();
      return d.logsPromise;
    }
    const stale = !TERMINAL_STATES.has(d.turn?.state || "") || d.turn?.state !== item.state || d.logsHasMore;
    if (!stale) return;
    await fetchIncrementalLogs(id, d);
  }

  async function loadExpanded(expectedEpoch = queryEpoch) {
    if (disposed || expectedEpoch !== queryEpoch) return;
    const targets = items.filter(x => expanded.has(x.turn_id));
    await Promise.all(targets.map(loadDetail));
  }

  // Merges the newest page into the loaded list only when it belongs to the
  // same filter, is not older than an already-applied head snapshot, and
  // overlaps with cached older pages; otherwise resets pagination to the new
  // head page cursor.
  async function applyFirstPage(res, token) {
    if (disposed) return;
    if (token && (token.epoch !== queryEpoch || token.filter !== filter || (token.headSeq && token.headSeq < appliedHeadSeq))) return;
    if (token?.headSeq) appliedHeadSeq = token.headSeq;
    const activeFilter = token?.filter || filter;
    const rawFirst = Array.isArray(res?.turns) ? res.turns : [];
    const first = rawFirst.filter(x => !x.worker_id || x.worker_id === workerID);
    const resHasMore = Boolean(res?.turns_has_more);
    const resNextCursor = res?.turns_next_cursor || "";

    let nextItems;
    let nextHasMore;
    let nextCur;
    if (itemsFilter !== activeFilter || !resHasMore || !first.length || !items.length) {
      nextItems = first;
      nextHasMore = resHasMore && first.length > 0;
      nextCur = nextHasMore ? resNextCursor : "";
    } else {
      const firstIDs = new Set(first.map(x => x.turn_id));
      const hasOverlap = items.some(x => firstIDs.has(x.turn_id));
      if (hasOverlap) {
        const oldestFirst = first[first.length - 1];
        const tail = items.filter(x => !firstIDs.has(x.turn_id) && isOlder(x, oldestFirst));
        nextItems = first.concat(tail);
        nextHasMore = tail.length > 0 ? hasMore : resHasMore;
        nextCur = tail.length > 0 ? nextCursor : resNextCursor;
      } else {
        nextItems = first;
        nextHasMore = resHasMore;
        nextCur = resNextCursor;
      }
    }

    if (token && (token.epoch !== queryEpoch || token.filter !== filter)) return;
    items = nextItems;
    itemsFilter = activeFilter;
    hasMore = nextHasMore;
    nextCursor = nextCur;
    if (!initialized) {
      if (items.length > 0) {
        initialized = true;
        const target = items.find(x => !TERMINAL_STATES.has(x.state)) || items[0];
        if (target) expanded.add(target.turn_id);
      }
    } else {
      for (const x of first) {
        if (!seen.has(x.turn_id) && !TERMINAL_STATES.has(x.state)) expanded.add(x.turn_id);
      }
    }
    for (const x of items) seen.add(x.turn_id);
    await loadExpanded(token?.epoch ?? queryEpoch);
  }

  async function guarded(fn) {
    try {
      await fn();
    } catch (err) {
      if (!disposed) notice?.(err);
    }
    if (!disposed) render();
  }

  function toggle(id) {
    if (expanded.has(id)) {
      expanded.delete(id);
      render();
      return;
    }
    expanded.add(id);
    const item = items.find(x => x.turn_id === id);
    if (item) void guarded(() => loadDetail(item));
    else render();
  }

  function setFilter(next) {
    if (next === filter || disposed) return;
    filter = next;
    const myEpoch = ++queryEpoch;
    const token = {filter: next, epoch: myEpoch, headSeq: ++nextHeadSeq};
    loadingOlder = false;
    if (itemsFilter !== next) {
      items = [];
      hasMore = false;
      nextCursor = "";
      itemsFilter = next;
    }
    render();
    void guarded(async () => {
      const q = next !== "all" ? `&filter=${enc(next)}` : "";
      const res = await api(`/api/workers/turns?worker_id=${enc(workerID)}&limit=${PAGE_SIZE}${q}`);
      await applyFirstPage({
        turns: res?.items,
        turns_has_more: res?.has_more,
        turns_next_cursor: res?.next_cursor,
      }, token);
    });
  }

  function loadOlder() {
    if (loadingOlder || !nextCursor) return;
    loadingOlder = true;
    const myEpoch = queryEpoch;
    const activeFilter = filter;
    const cursorUsed = nextCursor;
    render();
    void guarded(async () => {
      try {
        const q = activeFilter !== "all" ? `&filter=${enc(activeFilter)}` : "";
        const res = await api(`/api/workers/turns?worker_id=${enc(workerID)}&limit=${PAGE_SIZE}&cursor=${enc(cursorUsed)}${q}`);
        if (disposed || myEpoch !== queryEpoch || filter !== activeFilter || itemsFilter !== activeFilter || nextCursor !== cursorUsed) return;
        const known = new Set(items.map(x => x.turn_id));
        for (const x of Array.isArray(res.items) ? res.items : []) {
          if (x.worker_id && x.worker_id !== workerID) continue;
          if (!known.has(x.turn_id)) items.push(x);
          seen.add(x.turn_id);
        }
        hasMore = Boolean(res.has_more);
        nextCursor = res.next_cursor || "";
      } finally {
        if (myEpoch === queryEpoch) loadingOlder = false;
      }
    });
  }

  function loadMoreLogs(id) {
    const d = details.get(id);
    if (!d || !d.logsHasMore || d.logsLoading) return;
    void guarded(async () => {
      const p = fetchIncrementalLogs(id, d);
      render();
      await p;
    });
  }

  function loadRest(id, part) {
    const d = details.get(id);
    const page = part === "prompt" ? d?.prompt : d?.result;
    if (!page || page.complete || !page.nextCursor || page.loading) return;
    const cursor = page.nextCursor;
    page.loading = true;
    d.bodyRev++;
    render();
    void guarded(async () => {
      try {
        const q = part === "prompt" ? "&part=prompt" : "";
        const res = await api(`/api/workers/result?turn_id=${enc(id)}${q}&cursor=${enc(cursor)}&max_bytes=${RESULT_PAGE_BYTES}`);
        if (res.text) page.text += res.text;
        if (!res.expired && res.complete !== false) page.reachedEnd = true;
        if (res.expired || page.expired) {
          markPageExpired(page);
        } else {
          page.complete = res.complete !== false;
          page.nextCursor = res.next_cursor || "";
        }
      } finally {
        page.loading = false;
        d.bodyRev++;
      }
    });
  }

  let headTitleEl = null;
  let filterBtns = null;

  function renderHead() {
    const title = t("web.workers.turns_heading");
    const allText = t("web.workers.filter_all");
    const attText = t("web.workers.filter_attention");
    const sig = `${filter}|${title}|${allText}|${attText}`;
    if (sig === headSig) return;
    headSig = sig;
    if (!filterBtns) {
      headTitleEl = node("h4", title);
      const group = node("div", null, "worker-filter-group");
      filterBtns = [
        ["all", node("button", allText, "worker-filter-btn")],
        ["attention", node("button", attText, "worker-filter-btn")],
      ];
      for (const [value, btn] of filterBtns) {
        btn.type = "button";
        btn.addEventListener("click", () => setFilter(value));
        group.append(btn);
      }
      headEl.replaceChildren(headTitleEl, group);
    } else {
      if (headTitleEl.textContent !== title) headTitleEl.textContent = title;
      if (filterBtns[0][1].textContent !== allText) filterBtns[0][1].textContent = allText;
      if (filterBtns[1][1].textContent !== attText) filterBtns[1][1].textContent = attText;
    }
    for (const [value, btn] of filterBtns) {
      btn.setAttribute("aria-pressed", String(filter === value));
    }
  }

  function rowContent(item, open) {
    const nowMs = now();
    const seq = node("span", item.turn_seq ? `#${item.turn_seq}` : "#", "worker-turn-seq");
    if (item.turn_seq_derived) seq.title = t("web.workers.turn_seq_derived");
    const st = node("span", null, "worker-turn-state");
    const tone = stateTone(item.state);
    if (tone) st.dataset.tone = tone;
    st.append(node("span", null, "worker-turn-dot"));
    if (item.state !== "completed") st.append(node("span", turnStateLabel(item.state), "worker-turn-state-text"));
    else st.title = turnStateLabel(item.state);
    const preview = item.prompt_preview || "";
    const summary = node("span", preview || t("web.workers.turn_no_prompt_preview"), "worker-turn-summary");
    if (!preview) summary.dataset.empty = "true";
    else summary.title = preview;
    return [
      node("span", open ? "▾" : "▸", "worker-turn-caret"),
      seq,
      st,
      summary,
      node("span", formatDuration(item.elapsed_seconds), "worker-turn-num"),
      node("span", formatTurnTime(item.started_at, nowMs), "worker-turn-num worker-turn-time"),
    ];
  }

  function textSection(heading, page, id, part, emptyKey) {
    const frag = [node("h4", heading)];
    if (!page) {
      if (emptyKey) frag.push(node("p", t(emptyKey), "worker-meta-line"));
      return frag;
    }
    if (page.expired) {
      const missingRest = Boolean(page.text) && !page.isFallbackPreview && !page.reachedEnd;
      const expKey = missingRest
        ? "web.workers.continuation_expired"
        : ((page.isFallbackPreview || !page.text) && part === "prompt"
          ? "web.workers.prompt_expired"
          : ((page.isFallbackPreview || !page.text) && page.partial ? "web.workers.partial_expired" : "web.workers.result_expired"));
      frag.push(node("p", t(expKey), "harness-note"));
    }
    if (page.text) frag.push(node("pre", page.text, `worker-code-block ${part === "prompt" ? "worker-prompt-block" : "worker-answer-block"}`));
    if (!page.complete && page.nextCursor) {
      const more = node("button", t("web.workers.load_rest"), "secondary worker-load-rest");
      more.type = "button";
      more.disabled = Boolean(page.loading);
      more.addEventListener("click", () => loadRest(id, part));
      frag.push(more);
    }
    return frag;
  }

  function makeLogMoreButton(id, d) {
    const btn = node("button", t("web.workers.load_more_logs"), "secondary worker-load-more-logs");
    btn.type = "button";
    btn.disabled = Boolean(d.logsLoading);
    btn.addEventListener("click", () => loadMoreLogs(id));
    return btn;
  }

  function bodyContent(item, id, d) {
    const trn = d.turn || item;
    const busy = isBusy();
    const head = node("div", null, "worker-turn-head");
    const meta = node("div", null, "worker-turn-meta");
    const badge = node("span", turnStateLabel(trn.state), "badge");
    const tone = stateTone(trn.state);
    if (tone) badge.dataset.tone = tone;
    meta.append(badge, node("small", trn.turn_id, "worker-meta-line worker-turn-id"));
    const btns = node("div", null, "actions");
    const action = (key, cls, payload) => {
      const b = node("button", t(key), cls);
      b.type = "button";
      b.disabled = busy;
      b.addEventListener("click", () => onAction?.(payload));
      btns.append(b);
    };
    if (trn.state === "running" || trn.state === "prepared") action("web.harnesses.turn_stop", "danger", {action: "cancel", turn_id: trn.turn_id});
    if (trn.state === "reconciliation_required") {
      action("web.harnesses.turn_reconcile", "secondary", {action: "reconcile", turn_id: trn.turn_id});
      action("web.harnesses.turn_resolve", "secondary", {action: "resolve", turn_id: trn.turn_id});
    }
    head.append(meta, btns);
    const out = [head];
    if (trn.error_message) out.push(node("p", trn.error_message, "worker-error-line"));

    const promptBytes = trn.prompt_bytes || item.prompt_bytes || 0;
    if (promptBytes > 0) {
      let page = d.prompt || (item.prompt_preview ? {text: item.prompt_preview, complete: true, expired: true, isFallbackPreview: true} : null);
      if (page?.expired && !page.text && item.prompt_preview) page = {...page, text: item.prompt_preview, isFallbackPreview: true};
      out.push(...textSection(t("web.workers.prompt_heading"), page, id, "prompt"));
    } else {
      out.push(node("h4", t("web.workers.prompt_heading")), node("p", t("web.workers.prompt_missing"), "worker-meta-line"));
    }

    const vcs = renderVCSSnapshot(trn);
    if (vcs) out.push(vcs);

    if (d.result?.partial) {
      let page = d.result;
      const fallbackPreview = page.partialOutput || trn.partial_output || "";
      if (page.expired && !page.text && fallbackPreview) page = {...page, text: fallbackPreview, isFallbackPreview: true};
      out.push(...textSection(t("web.workers.partial_heading"), page, id, "answer"));
    } else if (d.result?.text || d.result?.expired) {
      out.push(...textSection(t("web.workers.answer_heading"), d.result, id, "answer"));
    } else if (trn.partial_output) {
      out.push(node("h4", t("web.workers.partial_heading")), node("pre", trn.partial_output, "worker-code-block worker-answer-block"));
    }

    const logs = node("details", null, "worker-turn-logs");
    logs.open = d.logsOpen ?? !TERMINAL_STATES.has(trn.state);
    logs.addEventListener("toggle", () => { d.logsOpen = logs.open; });
    const logSummary = node("summary", formatLogsHeading(d));
    logs.append(logSummary);
    let logBox = null;
    if (!d.logs.length) {
      logs.append(node("p", t("web.workers.logs_empty"), "worker-meta-line"));
    } else {
      logBox = node("div", null, "worker-logs-box");
      for (const entry of d.logs) logBox.append(createLogRow(entry));
      logs.append(logBox);
    }
    let logMoreBtn = null;
    if (d.logsHasMore) {
      logMoreBtn = makeLogMoreButton(id, d);
      logs.append(logMoreBtn);
    }
    out.push(logs);
    return {nodes: out, logsDetails: logs, logBox, logSummary, logMoreBtn};
  }

  function bodySignature(item, d, langKey) {
    const trn = d.turn || item;
    return `${d.bodyRev}|${isBusy() ? 1 : 0}|${trn.state}|${item.prompt_bytes || 0}|${d.prompt ? "" : (item.prompt_preview || "")}|${langKey}`;
  }

  function syncLogsInPlace(id, v, d) {
    if (v.logBox && d.logs.length > v.logCount) {
      const pinned = isPinnedToBottom(v.logBox);
      const prevTop = v.logBox.scrollTop;
      for (let i = v.logCount; i < d.logs.length; i++) v.logBox.append(createLogRow(d.logs[i]));
      v.logCount = d.logs.length;
      if (pinned) v.logBox.scrollTop = v.logBox.scrollHeight;
      else v.logBox.scrollTop = prevTop;
    }
    if (v.logSummary) {
      const heading = formatLogsHeading(d);
      if (v.logSummary.textContent !== heading) v.logSummary.textContent = heading;
    }
    if (d.logsHasMore) {
      if (!v.logMoreBtn && v.logsDetails) {
        v.logMoreBtn = makeLogMoreButton(id, d);
        v.logsDetails.append(v.logMoreBtn);
      } else if (v.logMoreBtn) {
        v.logMoreBtn.disabled = Boolean(d.logsLoading);
      }
    } else if (v.logMoreBtn) {
      removeChildNode(v.logsDetails, v.logMoreBtn);
      v.logMoreBtn = null;
    }
  }

  function renderItem(item, id, dayKey, langKey, force) {
    let v = views.get(id);
    if (!v) {
      const el = node("div", null, "worker-turn-item");
      const row = node("button", null, "worker-turn-row");
      row.type = "button";
      row.addEventListener("click", () => toggle(id));
      el.append(row);
      v = {el, row, body: null, rowSig: "", bodySig: "", logsDetails: null, logBox: null, logCount: 0, logSummary: null, logMoreBtn: null};
      views.set(id, v);
    }
    const open = expanded.has(id);
    const d = details.get(id);
    const rowSig = `${item.turn_seq || 0}|${item.turn_seq_derived ? 1 : 0}|${item.state}|${item.prompt_preview || ""}|${item.elapsed_seconds || 0}|${item.started_at || ""}|${open ? 1 : 0}|${dayKey}|${langKey}`;
    if (force || rowSig !== v.rowSig) {
      v.rowSig = rowSig;
      v.row.replaceChildren(...rowContent(item, open));
      v.row.setAttribute("aria-expanded", String(open));
      if (open) v.el.dataset.open = "true";
      else delete v.el.dataset.open;
    }
    if (!open || !d?.loaded) {
      if (v.body) {
        v.body = null;
        v.bodySig = "";
        v.logsDetails = null;
        v.logBox = null;
        v.logSummary = null;
        v.logMoreBtn = null;
        v.el.replaceChildren(v.row);
      }
      return;
    }
    const bodySig = bodySignature(item, d, langKey);
    if (!force && v.body && bodySig === v.bodySig) {
      syncLogsInPlace(id, v, d);
      return;
    }
    const prevLog = v.logBox;
    const prevPinned = prevLog ? isPinnedToBottom(prevLog) : true;
    const prevLogTop = prevLog ? prevLog.scrollTop : 0;
    const prevPromptTop = v.body?.querySelector?.(".worker-prompt-block")?.scrollTop || 0;
    const prevAnswerTop = v.body?.querySelector?.(".worker-answer-block")?.scrollTop || 0;
    const built = bodyContent(item, id, d);
    const body = v.body || node("div", null, "worker-turn-detail");
    body.replaceChildren(...built.nodes);
    if (!v.body) {
      v.body = body;
      v.el.replaceChildren(v.row, body);
    }
    v.bodySig = bodySig;
    v.logsDetails = built.logsDetails;
    v.logBox = built.logBox;
    v.logSummary = built.logSummary;
    v.logMoreBtn = built.logMoreBtn;
    v.logCount = d.logs.length;
    const promptPre = body.querySelector?.(".worker-prompt-block");
    if (promptPre) promptPre.scrollTop = prevPromptTop;
    const answerPre = body.querySelector?.(".worker-answer-block");
    if (answerPre) answerPre.scrollTop = prevAnswerTop;
    if (v.logBox) v.logBox.scrollTop = prevLog && !prevPinned ? prevLogTop : v.logBox.scrollHeight;
  }

  function renderFoot() {
    const sig = `${items.length === 0 ? 1 : 0}|${filter}|${hasMore && Boolean(nextCursor) ? 1 : 0}|${loadingOlder ? 1 : 0}|${t("web.workers.load_older")}`;
    if (sig === footSig) return;
    footSig = sig;
    const parts = [];
    if (!items.length) parts.push(node("p", t(filter === "attention" ? "web.workers.turns_empty_attention" : "web.workers.turns_empty"), "worker-meta-line"));
    if (hasMore && nextCursor) {
      const btn = node("button", t("web.workers.load_older"), "secondary worker-load-older");
      btn.type = "button";
      btn.disabled = loadingOlder;
      btn.addEventListener("click", loadOlder);
      parts.push(btn);
    }
    footEl.replaceChildren(...parts);
  }

  function render(force = false) {
    if (disposed) return;
    if (force) {
      headSig = "";
      footSig = "";
    }
    renderHead();
    const dayKey = new Date(now()).toDateString();
    const langKey = `${localeRev}:${t("web.workers.turns_heading")}`;
    const n = items.length;
    const ids = new Array(n);
    for (let i = 0; i < n; i++) {
      const item = items[i];
      const id = item.turn_id;
      ids[i] = id;
      renderItem(item, id, dayKey, langKey, force);
    }
    const sig = ids.join("|");
    if (force || sig !== orderSig) {
      orderSig = sig;
      const els = new Array(n);
      for (let i = 0; i < n; i++) els[i] = views.get(ids[i]).el;
      listEl.replaceChildren(...els);
    }
    if (views.size > n) {
      const keep = new Set(ids);
      for (const id of views.keys()) {
        if (!keep.has(id)) views.delete(id);
      }
    }
    renderFoot();
  }

  return {
    root,
    workerID,
    filter: () => filter,
    queryToken: () => ({filter, epoch: queryEpoch, headSeq: ++nextHeadSeq}),
    invalidateLocale: () => {
      localeRev++;
      headSig = "";
      footSig = "";
    },
    applyFirstPage,
    render,
    hasActive: () => items.some(x => !TERMINAL_STATES.has(x.state)),
    turnCount: () => items.length,
    dispose: () => { disposed = true; },
  };
}
