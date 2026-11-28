import {$, localizeError, node, trapDialogWheel} from "./ui.mjs";
import {t} from "./i18n.mjs";
import {createWorkersInteractionsController} from "./workers-interactions.mjs";
import {createTurnTimeline, createWorkerSummaryUpdater, stateTone} from "./workers-timeline.mjs";

const WORKER_STATE_KEYS = {
  idle: "web.workers.state_idle",
  running: "web.workers.state_running",
  reconciliation_required: "web.workers.state_reconciliation_required",
  closed: "web.workers.state_closed",
};

function approvalModeLabel(mode) {
  if (mode === "auto") return t("web.workers.approval_auto");
  if (mode === "ask_human") return t("web.workers.approval_ask_human");
  if (mode === "strict_deny") return t("web.workers.approval_strict_deny");
  return "";
}

function folderName(rootPath) {
  if (!rootPath) return "";
  const cleaned = rootPath.replace(/[\\/]+$/, "");
  const parts = cleaned.split(/[\\/]/);
  return parts[parts.length - 1] || rootPath;
}

function workerStateLabel(state) {
  const key = WORKER_STATE_KEYS[state];
  return key ? t(key) : state;
}

function accessLabel(mode) {
  return t(mode === "workspace_write" ? "web.workers.access_workspace_write" : "web.workers.access_read_only");
}

export function workersPanel({api, post, listen, session, notice, now = () => Date.now()}) {
  const dialog = $("#coding-workers");
  const openBtn = $("#workers-open");
  const refreshBtn = $("#workers-refresh");
  const includeClosedEl = $("#workers-include-closed");
  const statusEl = $("#workers-status");
  const interactionsEl = $("#workers-interactions");
  const listEl = $("#workers-list");
  const detailEl = $("#workers-detail");

  let workers = [];
  let selectedWorkerID = "";
  let selectedWorker = null;
  let summary = null;
  let timeline = null;
  let roleHintExpanded = false;
  let pollTimer = null;
  let refreshing = false;
  let pendingRefresh = false;
  let busyAction = false;
  let loadSeq = 0;
  let localeRev = 0;

  // Render cache so polling patches the detail pane in place instead of
  // rebuilding header controls when only summary durations advance.
  let lastWorkersSig = null;
  let attachedWorkerID = "";
  let headHost = null;
  let headBarEl = null;
  let lastWorkerBarSig = null;
  let summaryUpdater = null;
  const listButtons = new Map();

  const interactionsCtrl = createWorkersInteractionsController({
    interactionsEl,
    dialog,
    now,
    getBusyAction: () => busyAction,
    onDecide: payload => decideInteraction(payload),
  });

  function invalidateDetailCache() {
    attachedWorkerID = "";
    headHost = null;
    headBarEl = null;
    lastWorkerBarSig = null;
    summaryUpdater = null;
  }

  function setStatus(key, isError = false) {
    if (!statusEl) return;
    statusEl.textContent = key ? t(key) : "";
    if (isError) statusEl.dataset.tone = "error";
    else delete statusEl.dataset.tone;
  }

  function clearPoll() {
    if (pollTimer !== null) {
      clearTimeout(pollTimer);
      pollTimer = null;
    }
  }

  function schedulePoll() {
    clearPoll();
    if (!dialog?.open) return;
    const waitingFirstTurn = Boolean(selectedWorker && selectedWorker.state !== "closed" && timeline && timeline.turnCount() === 0);
    const hasActive = Boolean(timeline?.hasActive())
      || workers.some(w => w.state === "running")
      || interactionsCtrl.hasItems()
      || waitingFirstTurn;
    const delay = hasActive ? 2000 : 3000;
    pollTimer = setTimeout(() => {
      pollTimer = null;
      void refreshSelectedTurnAndList();
    }, delay);
  }

  function syncListSelection() {
    for (const [id, btn] of listButtons.entries()) {
      if (id === selectedWorkerID) btn.dataset.selected = "true";
      else delete btn.dataset.selected;
    }
  }

  async function decideInteraction(payload) {
    if (busyAction || interactionsCtrl.isGuarded()) return;
    const scope = session();
    busyAction = true;
    interactionsCtrl.render(true);
    try {
      const res = await post("/api/workers/interactions/decide", payload);
      if (!scope?.active) return;
      interactionsCtrl.setItems(res.interactions);
      interactionsCtrl.render(true);
      await refresh();
    } catch (err) {
      if (!scope?.active) return;
      notice?.(localizeError(err));
    } finally {
      busyAction = false;
      if (scope?.active) interactionsCtrl.render(true);
    }
  }

  async function setSessionAutoApprove(workerID, enabled) {
    const scope = session();
    busyAction = true;
    renderDetail(true);
    try {
      await post("/api/workers/session-auto-approve", {worker_id: workerID, enabled});
      if (!scope?.active) return;
      await refresh();
    } catch (err) {
      if (!scope?.active) return;
      notice?.(localizeError(err));
    } finally {
      busyAction = false;
      if (scope?.active) renderDetail(true);
    }
  }

  function renderList(force = false) {
    if (!listEl) return;
    const workersSig = JSON.stringify(workers);
    if (!force && workersSig === lastWorkersSig) {
      syncListSelection();
      return;
    }
    lastWorkersSig = workersSig;
    listButtons.clear();
    const prevScrollTop = listEl.scrollTop;
    listEl.replaceChildren();
    if (!workers.length) {
      listEl.append(node("div", t("web.workers.empty_list"), "empty"));
      return;
    }
    for (const w of workers) {
      const btn = node("button", null, "worker-list-item");
      btn.type = "button";
      if (w.worker_id === selectedWorkerID) btn.dataset.selected = "true";

      const top = node("div", null, "worker-item-top");
      top.append(node("strong", w.alias || w.worker_id));
      const stBadge = node("span", workerStateLabel(w.state), "badge");
      const tone = stateTone(w.state);
      if (tone) stBadge.dataset.tone = tone;
      top.append(stBadge);

      const sub = node("div", null, "worker-item-sub");
      sub.append(
        node("span", folderName(w.workspace_root)),
        node("span", `${w.harness_id} · ${accessLabel(w.access_mode)}`),
      );
      btn.append(top, sub);
      btn.addEventListener("click", () => void selectWorker(w.worker_id));
      listButtons.set(w.worker_id, btn);
      listEl.append(btn);
    }
    listEl.scrollTop = prevScrollTop;
  }

  function buildWorkerHead(w) {
    const header = node("div", null, "worker-detail-head");
    const info = node("div", null, "worker-detail-meta");
    const titleRow = node("div", null, "worker-detail-title");
    titleRow.append(node("h3", w.alias || w.worker_id));
    const modeBadge = node("span", accessLabel(w.access_mode), "badge");
    if (w.access_mode === "workspace_write") modeBadge.dataset.tone = "warn";
    titleRow.append(modeBadge);
    if (w.access_mode === "workspace_write") {
      const apprText = approvalModeLabel(w.effective_approval_mode || w.approval_mode);
      if (apprText) titleRow.append(node("span", apprText, "badge"));
      if (w.session_auto_approve) {
        const sessBadge = node("span", t("web.workers.session_auto_badge"), "badge");
        sessBadge.dataset.tone = "ok";
        titleRow.append(sessBadge);
      }
    }
    info.append(
      titleRow,
      node("small", w.workspace_root, "harness-path"),
      node("small", [w.harness_id, w.model, w.reasoning_effort].filter(Boolean).join(" · "), "worker-meta-line"),
    );
    const headActions = node("div", null, "actions");
    if (w.state !== "closed" && w.session_auto_approve) {
      const revokeAutoBtn = node("button", t("web.workers.revoke_session_auto"), "secondary");
      revokeAutoBtn.type = "button";
      revokeAutoBtn.disabled = busyAction;
      revokeAutoBtn.addEventListener("click", () => void setSessionAutoApprove(w.worker_id, false));
      headActions.append(revokeAutoBtn);
    }
    if (w.state !== "closed") {
      const closeBtn = node("button", t("web.workers.close_worker"), "secondary");
      closeBtn.type = "button";
      closeBtn.disabled = busyAction || w.state === "running";
      closeBtn.addEventListener("click", () => void runAction({action: "close", worker_id: w.worker_id}));
      headActions.append(closeBtn);
    }
    header.append(info, headActions);
    return header;
  }

  function workerBarSignature(w) {
    return JSON.stringify([
      w.worker_id, w.alias, w.state, w.access_mode, w.approval_mode,
      w.effective_approval_mode, Boolean(w.session_auto_approve),
      w.workspace_root, w.harness_id, w.model, w.reasoning_effort,
      busyAction, localeRev, t("web.workers.close_worker"),
    ]);
  }

  function syncHeadHost(force = false) {
    const barSig = workerBarSignature(selectedWorker);
    let barChanged = false;
    if (force || !headBarEl || barSig !== lastWorkerBarSig) {
      headBarEl = buildWorkerHead(selectedWorker);
      lastWorkerBarSig = barSig;
      barChanged = true;
    }
    if (!summaryUpdater) {
      summaryUpdater = createWorkerSummaryUpdater(() => {
        roleHintExpanded = !roleHintExpanded;
        renderDetail();
      });
    }
    const hasSummaryContent = summaryUpdater.update(selectedWorker, summary, {roleHintExpanded, localeRev, force});
    const wantedChildren = hasSummaryContent ? [headBarEl, summaryUpdater.el] : [headBarEl];
    const cur = headHost.children || [];
    const layoutChanged = barChanged
      || cur.length !== wantedChildren.length
      || cur[0] !== wantedChildren[0]
      || cur[1] !== wantedChildren[1];
    if (layoutChanged) headHost.replaceChildren(...wantedChildren);
  }

  // Swaps the detail pane atomically only when the selected worker changes;
  // otherwise patches header controls and summary counters independently in place.
  function renderDetail(force = false) {
    if (!detailEl) return;
    if (!selectedWorker || !timeline) {
      if (attachedWorkerID !== "" || !detailEl.children?.length || force) {
        detailEl.replaceChildren(node("div", t("web.workers.empty_select"), "empty"));
      }
      invalidateDetailCache();
      return;
    }
    if (attachedWorkerID !== selectedWorker.worker_id) {
      headHost = node("div", null, "worker-detail-top");
      headBarEl = null;
      lastWorkerBarSig = null;
      summaryUpdater = null;
      syncHeadHost(true);
      timeline.render(true);
      detailEl.replaceChildren(headHost, timeline.root);
      detailEl.scrollTop = 0;
      attachedWorkerID = selectedWorker.worker_id;
      return;
    }
    syncHeadHost(force);
    timeline.render(force);
  }

  function newTimeline(workerID) {
    return createTurnTimeline({
      api,
      workerID,
      now,
      isBusy: () => busyAction,
      onAction: payload => void runAction(payload),
      notice: err => notice?.(localizeError(err)),
    });
  }

  async function selectWorker(workerID, {isPoll = false} = {}) {
    const scope = session();
    const seq = ++loadSeq;
    selectedWorkerID = workerID;
    syncListSelection();

    if (!workerID) {
      timeline?.dispose();
      timeline = null;
      selectedWorker = null;
      summary = null;
      renderDetail();
      schedulePoll();
      return;
    }

    // Decide reuse strictly against the Worker ID actually bound to the live
    // timeline instance, never selectedWorkerID (which updates optimistically
    // before the async fetch settles and may outpace timeline after an error or
    // overlapping refresh).
    const reuseTimeline = Boolean(timeline && timeline.workerID === workerID);
    const tl = reuseTimeline ? timeline : newTimeline(workerID);
    const token = tl.queryToken();
    const filterQuery = token.filter !== "all" ? `&filter=${encodeURIComponent(token.filter)}` : "";

    try {
      // Fetch the worker, summary, newest timeline page, and expanded turn
      // details BEFORE touching detailEl, so switching workers swaps the right
      // pane atomically in a single paint with no intermediate empty frame.
      const res = await api(`/api/workers/get?worker_id=${encodeURIComponent(workerID)}${filterQuery}`);
      if (!scope?.active || seq !== loadSeq || selectedWorkerID !== workerID) {
        if (tl !== timeline) tl.dispose();
        return;
      }
      if (res?.worker?.worker_id && res.worker.worker_id !== workerID) {
        throw new Error("worker_mismatch");
      }
      await tl.applyFirstPage(res, token);
      if (!scope?.active || seq !== loadSeq || selectedWorkerID !== workerID) {
        if (tl !== timeline) tl.dispose();
        return;
      }
      if (tl !== timeline) {
        timeline?.dispose();
        timeline = tl;
        roleHintExpanded = false;
        invalidateDetailCache();
      }
      selectedWorker = res.worker || null;
      summary = res.summary || null;
      setStatus("");
      renderDetail();
      schedulePoll();
    } catch (err) {
      if (tl !== timeline) tl.dispose();
      if (!scope?.active || seq !== loadSeq) return;
      if (attachedWorkerID && attachedWorkerID !== workerID) {
        timeline?.dispose();
        timeline = null;
        selectedWorker = null;
        summary = null;
        renderDetail();
      }
      if (!isPoll) {
        setStatus("web.workers.load_failed", true);
        notice?.(localizeError(err));
      }
      schedulePoll();
    }
  }

  async function refresh({isPoll = false} = {}) {
    const scope = session();
    if (!scope?.active) return;
    refreshing = true;
    if (refreshBtn && !isPoll) {
      refreshBtn.disabled = true;
      refreshBtn.title = t("web.workers.loading");
    }
    try {
      const inc = includeClosedEl?.checked ? "true" : "false";
      const res = await api(`/api/workers/list?include_closed=${inc}`);
      if (!scope?.active) return;
      workers = Array.isArray(res.items) ? res.items : [];
      interactionsCtrl.setItems(res.interactions);
      setStatus("");
      const nextWorkerID = workers.some(w => w.worker_id === selectedWorkerID)
        ? selectedWorkerID
        : (workers[0]?.worker_id || "");
      interactionsCtrl.render();
      renderList();
      await selectWorker(nextWorkerID, {isPoll});
    } catch (err) {
      if (!scope?.active) return;
      if (!isPoll) {
        setStatus("web.workers.load_failed", true);
        notice?.(localizeError(err));
      }
      schedulePoll();
    } finally {
      refreshing = false;
      if (refreshBtn) {
        refreshBtn.disabled = false;
        refreshBtn.title = t("web.workers.refresh");
      }
      if (pendingRefresh && dialog?.open && scope?.active) {
        pendingRefresh = false;
        void refresh({isPoll: true});
      }
    }
  }

  async function refreshSelectedTurnAndList() {
    if (!dialog?.open) return;
    if (refreshing) {
      pendingRefresh = true;
      return;
    }
    await refresh({isPoll: true});
  }

  async function runAction(payload) {
    const scope = session();
    busyAction = true;
    renderDetail();
    try {
      await post("/api/workers/action", payload);
      if (!scope?.active) return;
      await refresh();
    } catch (err) {
      if (!scope?.active) return;
      notice?.(localizeError(err));
    } finally {
      busyAction = false;
      if (scope?.active) renderDetail();
    }
  }

  async function open() {
    if (!dialog) return;
    if (!dialog.open) dialog.showModal();
    await refresh();
  }

  function stop() {
    clearPoll();
    interactionsCtrl.stop();
    if (dialog?.open) dialog.close();
  }

  if (openBtn) listen(openBtn, "click", () => void open());
  if (refreshBtn) listen(refreshBtn, "click", () => void refresh());
  if (includeClosedEl) listen(includeClosedEl, "change", () => void refresh());
  if (dialog) {
    listen(dialog, "close", () => { clearPoll(); interactionsCtrl.stop(); });
    trapDialogWheel(dialog, listen);
  }

  return {
    open,
    stop,
    refreshIfOpen: refreshSelectedTurnAndList,
    relocalize: () => {
      localeRev++;
      interactionsCtrl.invalidateLocale();
      timeline?.invalidateLocale();
      lastWorkersSig = null;
      lastWorkerBarSig = null;
      if (!dialog?.open) return;
      interactionsCtrl.render(true);
      renderList(true);
      renderDetail(true);
    },
  };
}
