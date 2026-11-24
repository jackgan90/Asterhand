import {$, localizeError, node, trapDialogWheel} from "./ui.mjs";
import {t} from "./i18n.mjs";

const TERMINAL_STATES = new Set([
  "completed",
  "failed",
  "cancelled",
  "cancelled_authorization_revoked",
  "interrupted",
  "blocked_unsupported_approval",
  "blocked_unsupported_user_input",
]);

const WORKER_STATE_KEYS = {
  idle: "web.workers.state_idle",
  running: "web.workers.state_running",
  reconciliation_required: "web.workers.state_reconciliation_required",
  closed: "web.workers.state_closed",
};

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

function formatLogTime(raw) {
  if (!raw) return "";
  const d = new Date(raw);
  if (Number.isNaN(d.getTime())) return "";
  return d.toTimeString().slice(0, 8);
}

function workerStateLabel(state) {
  const key = WORKER_STATE_KEYS[state];
  return key ? t(key) : state;
}

function turnStateLabel(state) {
  const key = TURN_STATE_KEYS[state];
  return key ? t(key) : state;
}

function accessLabel(mode) {
  return t(mode === "workspace_write" ? "web.workers.access_workspace_write" : "web.workers.access_read_only");
}

function stateTone(state) {
  if (state === "running" || state === "prepared" || state === "waiting_approval" || state === "waiting_input") return "warn";
  if (state === "completed" || state === "idle") return "ok";
  if (state === "reconciliation_required" || state.startsWith("blocked_")) return "warn";
  if (state === "failed" || state === "interrupted" || state.startsWith("cancelled")) return "error";
  return "";
}

function createLogRow(entry) {
  const row = node("div", null, "worker-log-row");
  const ts = formatLogTime(entry.timestamp);
  const tag = entry.phase && entry.phase !== entry.kind ? `${entry.kind}/${entry.phase}` : entry.kind;
  if (ts) row.append(node("span", ts, "worker-log-time"));
  row.append(node("span", tag, "worker-log-tag"), node("span", entry.summary || "", "worker-log-msg"));
  return row;
}

function isPinnedToBottom(el) {
  if (!el) return false;
  return (el.scrollHeight - el.scrollTop - el.clientHeight) <= 24;
}

const INTERACTION_SWITCH_GUARD_MS = 400;

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
  let interactions = [];
  let activeInteractionID = "";
  let interactionCmdExpanded = false;
  let lastInteractionsSig = null;
  let interactionGuardUntil = 0;
  let interactionGuardTimer = null;
  let countdownEl = null;
  let countdownMeta = null;
  let countdownTimer = null;
  const interactionDrafts = new Map();
  let selectedWorkerID = "";
  let selectedWorker = null;
  let turns = [];
  let selectedTurnID = "";
  let selectedTurn = null;
  let logEntries = [];
  let lastLogSeq = 0;
  let resultPage = null;
  let pollTimer = null;
  let refreshing = false;
  let pendingRefresh = false;
  let busyAction = false;
  let loadSeq = 0;

  // Render cache to avoid rebuilding DOM and resetting scroll positions during polling.
  let lastWorkersSig = null;
  let lastMetaSig = null;
  let renderedTurnID = "";
  let renderedLogBox = null;
  let renderedLogCount = 0;
  const listButtons = new Map();

  function resetTurnState() {
    selectedTurnID = "";
    selectedTurn = null;
    logEntries = [];
    lastLogSeq = 0;
    resultPage = null;
  }

  function invalidateDetailCache() {
    lastMetaSig = null;
    renderedTurnID = "";
    renderedLogBox = null;
    renderedLogCount = 0;
  }

  function setStatus(key, isError = false) {
    if (!statusEl) return;
    statusEl.textContent = key ? t(key) : "";
    if (isError) statusEl.dataset.tone = "error";
    else delete statusEl.dataset.tone;
  }

  function clearInteractionGuard() {
    if (interactionGuardTimer !== null) {
      clearTimeout(interactionGuardTimer);
      interactionGuardTimer = null;
    }
    interactionGuardUntil = 0;
  }

  function isInteractionGuarded() {
    return now() < interactionGuardUntil;
  }

  function armInteractionGuard() {
    clearInteractionGuard();
    interactionGuardUntil = now() + INTERACTION_SWITCH_GUARD_MS;
    interactionGuardTimer = setTimeout(() => {
      interactionGuardTimer = null;
      if (dialog?.open && interactions.length) renderInteractions(true);
    }, INTERACTION_SWITCH_GUARD_MS);
  }

  function clearCountdown() {
    if (countdownTimer !== null) {
      clearTimeout(countdownTimer);
      countdownTimer = null;
    }
    countdownEl = null;
    countdownMeta = null;
  }

  function paintCountdown() {
    if (!countdownEl || !countdownMeta) return;
    const secs = Math.max(0, Math.ceil((countdownMeta.dueMs - now()) / 1000));
    const key = countdownMeta.claimed ? "web.workers.input_caller_verifying" : "web.workers.input_caller_grace";
    countdownEl.textContent = t(key, {seconds: String(secs)});
  }

  function armCountdown(el, item) {
    clearCountdown();
    const dueMs = Date.parse(item.escalate_at);
    if (!el || Number.isNaN(dueMs)) return;
    countdownEl = el;
    countdownMeta = {dueMs, claimed: Boolean(item.claimed_by_caller)};
    paintCountdown();
    const tick = () => {
      countdownTimer = null;
      if (!dialog?.open || !countdownEl || !countdownMeta) {
        clearCountdown();
        return;
      }
      paintCountdown();
      if (countdownMeta.dueMs > now()) {
        countdownTimer = setTimeout(tick, 1000);
      }
    };
    if (dueMs > now()) {
      countdownTimer = setTimeout(tick, 1000);
    }
  }

  function clearPoll() {
    if (pollTimer !== null) {
      clearTimeout(pollTimer);
      pollTimer = null;
    }
    clearInteractionGuard();
  }

  function schedulePoll() {
    clearPoll();
    if (!dialog?.open) return;
    const waitingFirstTurn = Boolean(selectedWorker && selectedWorker.state !== "closed" && turns.length === 0);
    const hasActive = (selectedTurn && !TERMINAL_STATES.has(selectedTurn.state))
      || workers.some(w => w.state === "running")
      || interactions.length > 0
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

  function getInteractionDraft(reqID) {
    if (!interactionDrafts.has(reqID)) interactionDrafts.set(reqID, {text: "", selected: {}});
    return interactionDrafts.get(reqID);
  }

  async function decideInteraction(payload) {
    if (busyAction || isInteractionGuarded()) return;
    const scope = session();
    busyAction = true;
    renderInteractions(true);
    try {
      const res = await post("/api/workers/interactions/decide", payload);
      if (!scope?.active) return;
      interactions = Array.isArray(res.interactions) ? res.interactions : [];
      await refresh();
    } catch (err) {
      if (!scope?.active) return;
      notice?.(localizeError(err));
    } finally {
      busyAction = false;
      if (scope?.active) renderInteractions(true);
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

  function renderInteractions(force = false) {
    if (!interactionsEl) return;
    const liveIDs = new Set(interactions.map(x => x.request_id));
    for (const k of interactionDrafts.keys()) {
      if (!liveIDs.has(k)) interactionDrafts.delete(k);
    }
    if (!interactions.length) {
      interactionsEl.hidden = true;
      interactionsEl.replaceChildren();
      activeInteractionID = "";
      lastInteractionsSig = "";
      clearInteractionGuard();
      clearCountdown();
      return;
    }
    const prevActiveID = activeInteractionID;
    const autoSwitched = Boolean(prevActiveID && !liveIDs.has(prevActiveID));
    if (!liveIDs.has(activeInteractionID)) {
      activeInteractionID = interactions[0].request_id;
      interactionCmdExpanded = false;
    }
    if (autoSwitched && activeInteractionID && activeInteractionID !== prevActiveID) {
      armInteractionGuard();
    }
    const idx = Math.max(0, interactions.findIndex(x => x.request_id === activeInteractionID));
    const item = interactions[idx];
    const actionDisabled = busyAction || isInteractionGuarded();
    const sig = JSON.stringify({
      locale: t("common.worker_action.badge_approval"),
      ids: interactions.map(x => x.request_id),
      active: item.request_id,
      visible: Boolean(item.visible_to_human),
      claimed: Boolean(item.claimed_by_caller),
      escalateAt: item.escalate_at || "",
      note: item.escalation_note || "",
      expanded: interactionCmdExpanded,
      busyAction,
      guarded: isInteractionGuarded(),
      selected: Object.fromEntries(Object.entries(getInteractionDraft(item.request_id).selected).map(([k, v]) => [k, Array.from(v)])),
    });
    if (!force && sig === lastInteractionsSig) {
      paintCountdown();
      return;
    }
    lastInteractionsSig = sig;
    clearCountdown();

    interactionsEl.hidden = false;
    interactionsEl.replaceChildren();

    const head = node("div", null, "workers-int-head");
    const left = node("div", null, "workers-int-head-left");
    const kindBadge = node("span", t(item.kind === "approval" ? "common.worker_action.badge_approval" : "common.worker_action.badge_input"), "badge");
    kindBadge.dataset.tone = item.kind === "approval" ? "warn" : "ok";
    const src = `${item.worker_alias || item.worker_id} · ${item.harness_id}`;
    left.append(kindBadge, node("strong", src));
    if (item.workspace_root) left.append(node("span", folderName(item.workspace_root), "worker-meta-line"));
    head.append(left);

    if (interactions.length > 1) {
      const approvals = interactions.filter(x => x.kind === "approval").length;
      const inputs = interactions.length - approvals;
      const pager = node("div", null, "workers-int-pager");
      const prev = node("button", "‹", "secondary");
      prev.type = "button";
      prev.setAttribute("aria-label", t("common.worker_action.prev"));
      prev.addEventListener("click", () => {
        activeInteractionID = interactions[(idx - 1 + interactions.length) % interactions.length].request_id;
        interactionCmdExpanded = false;
        renderInteractions(true);
      });
      const next = node("button", "›", "secondary");
      next.type = "button";
      next.setAttribute("aria-label", t("common.worker_action.next"));
      next.addEventListener("click", () => {
        activeInteractionID = interactions[(idx + 1) % interactions.length].request_id;
        interactionCmdExpanded = false;
        renderInteractions(true);
      });
      pager.append(
        node("span", t("common.worker_action.queue_breakdown", {approvals, inputs})),
        prev,
        node("span", `${idx + 1} / ${interactions.length}`),
        next,
      );
      head.append(pager);
    }
    interactionsEl.append(head);

    if (item.kind === "approval") {
      const meta = node("div", null, "workers-int-actions");
      if (item.tool_name) meta.append(node("span", item.tool_name, "badge"));
      const riskKey = item.hard_redline ? "common.worker_action.risk_critical" : (item.outside_workspace ? "common.worker_action.risk_outside" : "common.worker_action.risk_workspace");
      const riskBadge = node("span", t(riskKey), "badge");
      if (item.hard_redline) riskBadge.dataset.tone = "error";
      else if (item.outside_workspace) riskBadge.dataset.tone = "warn";
      meta.append(riskBadge);
      interactionsEl.append(meta);

      if (item.summary) interactionsEl.append(node("p", item.summary, "worker-meta-line"));
      if (item.command) {
        const pre = node("pre", item.command, interactionCmdExpanded ? "workers-int-cmd expanded" : "workers-int-cmd");
        interactionsEl.append(pre);
        if (item.command.length > 140 || item.command.includes("\n")) {
          const toggle = node("button", t(interactionCmdExpanded ? "common.worker_action.cmd_collapse" : "common.worker_action.cmd_expand"), "secondary");
          toggle.type = "button";
          toggle.addEventListener("click", () => { interactionCmdExpanded = !interactionCmdExpanded; renderInteractions(true); });
          interactionsEl.append(toggle);
        }
      }
      const actions = node("div", null, "workers-int-actions");
      const bOnce = node("button", t("common.worker_action.allow_once"));
      bOnce.type = "button";
      bOnce.disabled = actionDisabled;
      bOnce.addEventListener("click", () => void decideInteraction({request_id: item.request_id, decision: "allow_once"}));
      actions.append(bOnce);

      if (item.allow_session_ok && !item.hard_redline) {
        const bSess = node("button", t("common.worker_action.allow_session"), "secondary");
        bSess.type = "button";
        bSess.disabled = actionDisabled;
        bSess.addEventListener("click", () => void decideInteraction({request_id: item.request_id, decision: "allow_session"}));
        actions.append(bSess);
      }

      const bDeny = node("button", t("common.worker_action.deny"), "secondary");
      bDeny.type = "button";
      bDeny.disabled = actionDisabled;
      bDeny.addEventListener("click", () => void decideInteraction({request_id: item.request_id, decision: "deny"}));
      actions.append(bDeny);

      const bCancel = node("button", t("common.worker_action.cancel_turn"), "danger");
      bCancel.type = "button";
      bCancel.disabled = actionDisabled;
      bCancel.addEventListener("click", () => void decideInteraction({request_id: item.request_id, decision: "cancel_turn"}));
      actions.append(bCancel);
      interactionsEl.append(actions);
    } else {
      if (!item.visible_to_human && item.escalate_at) {
        const waitBadge = node("span", "", "badge");
        waitBadge.dataset.tone = "warn";
        armCountdown(waitBadge, item);
        interactionsEl.append(waitBadge);
      }
      if (item.escalation_note) {
        interactionsEl.append(node("p", t("common.worker_action.escalation_note", {note: item.escalation_note}), "worker-meta-line"));
      }
      const draft = getInteractionDraft(item.request_id);
      const qs = Array.isArray(item.questions) && item.questions.length ? item.questions : [{id: "q1", question: item.summary}];
      for (const q of qs) {
        interactionsEl.append(node("p", q.question || item.summary || "", "worker-meta-line"));
        if (Array.isArray(q.options) && q.options.length) {
          if (!draft.selected[q.id]) draft.selected[q.id] = new Set();
          const optRow = node("div", null, "workers-int-opts");
          for (const opt of q.options) {
            const chip = node("button", opt.description ? `${opt.label} — ${opt.description}` : opt.label, "workers-int-opt");
            chip.type = "button";
            chip.setAttribute("aria-pressed", draft.selected[q.id].has(opt.label) ? "true" : "false");
            chip.disabled = busyAction;
            chip.addEventListener("click", () => {
              const sel = draft.selected[q.id];
              if (q.multi_select) {
                if (sel.has(opt.label)) sel.delete(opt.label); else sel.add(opt.label);
              } else {
                const had = sel.has(opt.label);
                sel.clear();
                if (!had) sel.add(opt.label);
              }
              renderInteractions(true);
            });
            optRow.append(chip);
          }
          interactionsEl.append(optRow);
        }
      }
      const textarea = node("textarea", null, "workers-int-input");
      textarea.placeholder = t("common.worker_action.answer_placeholder");
      textarea.value = draft.text || "";
      textarea.disabled = busyAction;
      let composing = false;
      textarea.addEventListener("compositionstart", () => { composing = true; });
      textarea.addEventListener("compositionend", () => { composing = false; draft.text = textarea.value; });
      textarea.addEventListener("input", () => { draft.text = textarea.value; });
      const submitAns = () => {
        const text = (draft.text || "").trim();
        const answers = {};
        let hasSel = false;
        for (const [qID, set] of Object.entries(draft.selected)) {
          if (set && set.size > 0) { answers[qID] = Array.from(set); hasSel = true; }
        }
        if (!text && !hasSel) { textarea.focus(); return; }
        void decideInteraction({request_id: item.request_id, decision: "answer", text, answers});
      };
      textarea.addEventListener("keydown", ev => {
        if (ev.key === "Enter" && !ev.shiftKey && !composing && !ev.isComposing) {
          ev.preventDefault();
          submitAns();
        }
      });
      interactionsEl.append(textarea);

      const actions = node("div", null, "workers-int-actions");
      const bSend = node("button", t("common.worker_action.send_answer"));
      bSend.type = "button";
      bSend.disabled = actionDisabled;
      bSend.addEventListener("click", submitAns);
      const bCancel = node("button", t("common.worker_action.cancel_turn"), "danger");
      bCancel.type = "button";
      bCancel.disabled = actionDisabled;
      bCancel.addEventListener("click", () => void decideInteraction({request_id: item.request_id, decision: "cancel_turn"}));
      actions.append(bSend, bCancel);
      interactionsEl.append(actions);
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
      const files = node("div", snap.changed_files.join("\n"), "worker-code-block");
      box.append(files);
    }
    if (snap.external_changes_possible) {
      box.append(node("small", t("web.workers.vcs_external_note"), "harness-note"));
    }
    return box;
  }

  function detailMetaSignature() {
    return JSON.stringify({
      worker: selectedWorker ? {
        worker_id: selectedWorker.worker_id,
        alias: selectedWorker.alias,
        state: selectedWorker.state,
        access_mode: selectedWorker.access_mode,
        effective_approval_mode: selectedWorker.effective_approval_mode,
        session_auto_approve: Boolean(selectedWorker.session_auto_approve),
        workspace_root: selectedWorker.workspace_root,
        harness_id: selectedWorker.harness_id,
        model: selectedWorker.model,
        reasoning_effort: selectedWorker.reasoning_effort,
      } : null,
      turns: turns.map(x => [x.turn_id, x.state]),
      selectedTurnID,
      turn: selectedTurn ? {
        turn_id: selectedTurn.turn_id,
        state: selectedTurn.state,
        error_message: selectedTurn.error_message,
        partial_output: selectedTurn.partial_output,
        baseline_vcs: selectedTurn.baseline_vcs,
        after_vcs: selectedTurn.after_vcs,
      } : null,
      resultText: resultPage?.text || "",
      busyAction,
      hasLogs: logEntries.length > 0,
    });
  }

  function renderDetail(force = false) {
    if (!detailEl) return;
    const metaSig = detailMetaSignature();

    // Fast path: same worker & turn metadata, only new log entries arrived -> append in-place!
    if (!force && metaSig === lastMetaSig && renderedTurnID === selectedTurnID && renderedLogBox) {
      if (logEntries.length > renderedLogCount) {
        const pinned = isPinnedToBottom(renderedLogBox);
        const prevTop = renderedLogBox.scrollTop;
        for (let i = renderedLogCount; i < logEntries.length; i++) {
          renderedLogBox.append(createLogRow(logEntries[i]));
        }
        renderedLogCount = logEntries.length;
        if (pinned) renderedLogBox.scrollTop = renderedLogBox.scrollHeight;
        else renderedLogBox.scrollTop = prevTop;
      }
      return;
    }

    const sameTurn = renderedTurnID !== "" && renderedTurnID === selectedTurnID;
    const prevDetailTop = sameTurn ? detailEl.scrollTop : 0;
    const prevLogBox = sameTurn ? renderedLogBox : null;
    const prevLogPinned = prevLogBox ? isPinnedToBottom(prevLogBox) : false;
    const prevLogTop = prevLogBox ? prevLogBox.scrollTop : 0;
    const prevAnswerEl = sameTurn ? detailEl.querySelector(".worker-answer-block") : null;
    const prevAnswerTop = prevAnswerEl ? prevAnswerEl.scrollTop : 0;

    lastMetaSig = metaSig;
    renderedTurnID = selectedTurnID;
    renderedLogBox = null;
    renderedLogCount = 0;

    detailEl.replaceChildren();
    if (!selectedWorker) {
      detailEl.append(node("div", t("web.workers.empty_select"), "empty"));
      return;
    }
    const w = selectedWorker;
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
    detailEl.append(header);

    const turnsSec = node("section", null, "worker-turns-section");
    turnsSec.append(node("h4", t("web.workers.turns_heading")));
    if (!turns.length) {
      turnsSec.append(node("p", t("web.workers.turns_empty"), "worker-meta-line"));
    } else {
      const turnList = node("div", null, "worker-turns-bar");
      for (const turn of turns) {
        const pill = node("button", null, "worker-turn-pill");
        pill.type = "button";
        if (turn.turn_id === selectedTurnID) pill.dataset.selected = "true";
        pill.append(node("span", turnStateLabel(turn.state)), node("small", turn.turn_id.slice(-6)));
        pill.addEventListener("click", () => void selectTurn(turn.turn_id));
        turnList.append(pill);
      }
      turnsSec.append(turnList);
    }
    detailEl.append(turnsSec);

    if (!selectedTurn || selectedTurn.worker_id !== w.worker_id) return;
    const trn = selectedTurn;
    const turnBox = node("section", null, "worker-turn-detail");
    const turnHead = node("div", null, "worker-turn-head");
    const turnMeta = node("div");
    const stBadge = node("span", turnStateLabel(trn.state), "badge");
    const tone = stateTone(trn.state);
    if (tone) stBadge.dataset.tone = tone;
    turnMeta.append(stBadge, node("small", ` ${trn.turn_id}`, "worker-meta-line"));
    const turnBtns = node("div", null, "actions");
    if (trn.state === "running" || trn.state === "prepared") {
      const stopBtn = node("button", t("web.harnesses.turn_stop"), "danger");
      stopBtn.type = "button";
      stopBtn.disabled = busyAction;
      stopBtn.addEventListener("click", () => void runAction({action: "cancel", turn_id: trn.turn_id}));
      turnBtns.append(stopBtn);
    }
    if (trn.state === "reconciliation_required") {
      const recBtn = node("button", t("web.harnesses.turn_reconcile"), "secondary");
      recBtn.type = "button";
      recBtn.disabled = busyAction;
      recBtn.addEventListener("click", () => void runAction({action: "reconcile", turn_id: trn.turn_id}));
      const resBtn = node("button", t("web.harnesses.turn_resolve"), "secondary");
      resBtn.type = "button";
      resBtn.disabled = busyAction;
      resBtn.addEventListener("click", () => void runAction({action: "resolve", turn_id: trn.turn_id}));
      turnBtns.append(recBtn, resBtn);
    }
    turnHead.append(turnMeta, turnBtns);
    turnBox.append(turnHead);

    if (trn.error_message) {
      const errP = node("p", trn.error_message, "worker-error-line");
      turnBox.append(errP);
    }

    const vcsEl = renderVCSSnapshot(trn);
    if (vcsEl) turnBox.append(vcsEl);

    let answerEl = null;
    if (resultPage?.text) {
      turnBox.append(node("h4", t("web.workers.answer_heading")));
      answerEl = node("pre", resultPage.text, "worker-code-block worker-answer-block");
      turnBox.append(answerEl);
    } else if (trn.partial_output) {
      turnBox.append(node("h4", t("web.workers.partial_heading")));
      answerEl = node("pre", trn.partial_output, "worker-code-block worker-answer-block");
      turnBox.append(answerEl);
    }

    turnBox.append(node("h4", t("web.workers.logs_heading")));
    if (!logEntries.length) {
      turnBox.append(node("p", t("web.workers.logs_empty"), "worker-meta-line"));
    } else {
      const logBox = node("div", null, "worker-logs-box");
      for (const entry of logEntries) {
        logBox.append(createLogRow(entry));
      }
      turnBox.append(logBox);
      renderedLogBox = logBox;
      renderedLogCount = logEntries.length;
    }
    detailEl.append(turnBox);

    if (sameTurn) {
      detailEl.scrollTop = prevDetailTop;
      if (answerEl) answerEl.scrollTop = prevAnswerTop;
      if (renderedLogBox) {
        if (prevLogPinned) renderedLogBox.scrollTop = renderedLogBox.scrollHeight;
        else renderedLogBox.scrollTop = prevLogTop;
      }
    } else {
      detailEl.scrollTop = 0;
    }
  }

  async function fetchTurnSnapshot(turnID, incrementalAfterSeq) {
    const url = incrementalAfterSeq > 0
      ? `/api/workers/logs?turn_id=${encodeURIComponent(turnID)}&after_seq=${incrementalAfterSeq}`
      : `/api/workers/logs?turn_id=${encodeURIComponent(turnID)}`;
    return api(url);
  }

  async function selectTurn(turnID) {
    const scope = session();
    const seq = ++loadSeq;
    const isSame = turnID === selectedTurnID;
    const afterSeq = isSame ? lastLogSeq : 0;
    try {
      const res = await fetchTurnSnapshot(turnID, afterSeq);
      if (!scope?.active || seq !== loadSeq) return;
      selectedTurnID = turnID;
      selectedTurn = res.turn || turns.find(x => x.turn_id === turnID) || null;
      const incoming = Array.isArray(res.entries) ? res.entries : [];
      if (isSame && afterSeq > 0) {
        for (const entry of incoming) {
          if ((entry.seq || 0) > lastLogSeq) {
            logEntries.push(entry);
            lastLogSeq = entry.seq;
          }
        }
      } else {
        logEntries = incoming;
        lastLogSeq = incoming.length ? (incoming[incoming.length - 1].seq || 0) : 0;
        invalidateDetailCache();
      }
      resultPage = res.result || null;
      setStatus("");
      renderDetail();
      schedulePoll();
    } catch (err) {
      if (!scope?.active || seq !== loadSeq) return;
      setStatus("web.workers.load_failed", true);
      notice?.(localizeError(err));
      schedulePoll();
    }
  }

  async function selectWorker(workerID, {isPoll = false} = {}) {
    const scope = session();
    const seq = ++loadSeq;
    const switchedWorker = workerID !== selectedWorkerID;
    selectedWorkerID = workerID;
    syncListSelection();

    if (!workerID) {
      selectedWorker = null;
      turns = [];
      resetTurnState();
      invalidateDetailCache();
      renderDetail();
      schedulePoll();
      return;
    }

    try {
      // Fetch complete worker + turns (+ active turn logs) BEFORE touching detailEl,
      // so switching between workers swaps the right pane atomically in a single paint
      // with zero intermediate empty/collapsed frame or layout shift.
      const wRes = await api(`/api/workers/get?worker_id=${encodeURIComponent(workerID)}`);
      if (!scope?.active || seq !== loadSeq || selectedWorkerID !== workerID) return;

      const nextWorker = wRes.worker || null;
      const nextTurns = Array.isArray(wRes.turns) ? wRes.turns : [];
      const preservedTurnID = switchedWorker ? "" : selectedTurnID;
      const nextTurnObj = nextTurns.find(x => x.turn_id === preservedTurnID) || nextTurns[0] || null;
      const nextTurnID = nextTurnObj ? nextTurnObj.turn_id : "";

      if (!nextTurnID) {
        selectedWorker = nextWorker;
        turns = nextTurns;
        resetTurnState();
        if (switchedWorker) invalidateDetailCache();
        renderDetail();
        schedulePoll();
        return;
      }

      const sameTurn = !switchedWorker && nextTurnID === selectedTurnID;
      const afterSeq = sameTurn ? lastLogSeq : 0;
      const lRes = await fetchTurnSnapshot(nextTurnID, afterSeq);
      if (!scope?.active || seq !== loadSeq || selectedWorkerID !== workerID) return;

      selectedWorker = nextWorker;
      turns = nextTurns;
      selectedTurnID = nextTurnID;
      selectedTurn = lRes.turn || nextTurnObj;
      const incoming = Array.isArray(lRes.entries) ? lRes.entries : [];
      if (sameTurn && afterSeq > 0) {
        for (const entry of incoming) {
          if ((entry.seq || 0) > lastLogSeq) {
            logEntries.push(entry);
            lastLogSeq = entry.seq;
          }
        }
      } else {
        logEntries = incoming;
        lastLogSeq = incoming.length ? (incoming[incoming.length - 1].seq || 0) : 0;
        invalidateDetailCache();
      }
      resultPage = lRes.result || null;
      setStatus("");
      renderDetail();
      schedulePoll();
    } catch (err) {
      if (!scope?.active || seq !== loadSeq) return;
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
      interactions = Array.isArray(res.interactions) ? res.interactions : [];
      setStatus("");
      const nextWorkerID = workers.some(w => w.worker_id === selectedWorkerID)
        ? selectedWorkerID
        : (workers[0]?.worker_id || "");
      renderInteractions();
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
    clearCountdown();
    if (dialog?.open) dialog.close();
  }

  if (openBtn) listen(openBtn, "click", () => void open());
  if (refreshBtn) listen(refreshBtn, "click", () => void refresh());
  if (includeClosedEl) listen(includeClosedEl, "change", () => void refresh());
  if (dialog) {
    listen(dialog, "close", () => { clearPoll(); clearCountdown(); });
    trapDialogWheel(dialog, listen);
  }

  return {
    open,
    stop,
    refreshIfOpen: refreshSelectedTurnAndList,
    relocalize: () => {
      lastInteractionsSig = null;
      lastWorkersSig = null;
      invalidateDetailCache();
      if (!dialog?.open) return;
      renderInteractions(true);
      renderList(true);
      renderDetail(true);
    },
  };
}
