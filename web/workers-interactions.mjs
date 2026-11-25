import {node} from "./ui.mjs";
import {t} from "./i18n.mjs";

const INTERACTION_SWITCH_GUARD_MS = 400;

function folderName(rootPath) {
  if (!rootPath) return "";
  const cleaned = rootPath.replace(/[\\/]+$/, "");
  const parts = cleaned.split(/[\\/]/);
  return parts[parts.length - 1] || rootPath;
}

function questionHasSecret(questions) {
  return Array.isArray(questions) && questions.some(q => Boolean(q?.is_secret));
}

function itemQuestions(item) {
  return Array.isArray(item?.questions) && item.questions.length
    ? item.questions
    : [{id: "q1", question: item?.summary || ""}];
}

export function createWorkersInteractionsController({
  interactionsEl,
  dialog,
  now = () => Date.now(),
  getBusyAction,
  onDecide,
}) {
  let interactions = [];
  let activeInteractionID = "";
  let interactionCmdExpanded = false;
  let lastInteractionsSig = null;
  let lastLocaleTag = "";
  let interactionGuardUntil = 0;
  let interactionGuardTimer = null;
  let countdownEl = null;
  let countdownMeta = null;
  let countdownTimer = null;

  // Ordinary drafts (non-secret fields only). Secret text and secret option
  // selections live strictly for the lifetime of the currently displayed card.
  const interactionDrafts = new Map();
  const secretInputs = new Map(); // question_id -> HTMLInputElement (type=password)
  const secretSelected = new Map(); // question_id -> Set<string>
  let renderedSecretReqID = "";
  let secretHeadEl = null;
  let secretActionButtons = [];
  let secretControlInputs = [];

  function clearSecretState() {
    for (const input of secretInputs.values()) {
      input.value = "";
    }
    secretInputs.clear();
    secretSelected.clear();
    renderedSecretReqID = "";
    secretHeadEl = null;
    secretActionButtons = [];
    secretControlInputs = [];
  }

  function clearInteractionGuard() {
    if (interactionGuardTimer !== null) {
      clearTimeout(interactionGuardTimer);
      interactionGuardTimer = null;
    }
    interactionGuardUntil = 0;
  }

  function isGuarded() {
    return now() < interactionGuardUntil;
  }

  function armInteractionGuard() {
    clearInteractionGuard();
    interactionGuardUntil = now() + INTERACTION_SWITCH_GUARD_MS;
    interactionGuardTimer = setTimeout(() => {
      interactionGuardTimer = null;
      if (dialog?.open && interactions.length) render(true);
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

  function getInteractionDraft(reqID) {
    if (!interactionDrafts.has(reqID)) {
      interactionDrafts.set(reqID, {text: "", answers: {}, selected: {}});
    }
    return interactionDrafts.get(reqID);
  }

  function setItems(nextItems) {
    interactions = Array.isArray(nextItems) ? nextItems : [];
  }

  function hasItems() {
    return interactions.length > 0;
  }

  function invalidateLocale() {
    lastInteractionsSig = null;
    lastLocaleTag = "";
  }

  function stop() {
    clearSecretState();
    clearInteractionGuard();
    clearCountdown();
    activeInteractionID = "";
    lastInteractionsSig = null;
    lastLocaleTag = "";
    if (interactionsEl) {
      interactionsEl.hidden = true;
      interactionsEl.replaceChildren();
    }
  }

  function buildHead(item, idx) {
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
        clearSecretState();
        activeInteractionID = interactions[(idx - 1 + interactions.length) % interactions.length].request_id;
        interactionCmdExpanded = false;
        render(true);
      });
      const next = node("button", "›", "secondary");
      next.type = "button";
      next.setAttribute("aria-label", t("common.worker_action.next"));
      next.addEventListener("click", () => {
        clearSecretState();
        activeInteractionID = interactions[(idx + 1) % interactions.length].request_id;
        interactionCmdExpanded = false;
        render(true);
      });
      pager.append(
        node("span", t("common.worker_action.queue_breakdown", {approvals, inputs})),
        prev,
        node("span", `${idx + 1} / ${interactions.length}`),
        next,
      );
      head.append(pager);
    }
    return head;
  }

  function render(force = false) {
    if (!interactionsEl) return;
    const liveIDs = new Set(interactions.map(x => x.request_id));
    for (const k of interactionDrafts.keys()) {
      if (!liveIDs.has(k)) interactionDrafts.delete(k);
    }
    if (!interactions.length) {
      clearSecretState();
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
      if (renderedSecretReqID && renderedSecretReqID !== interactions[0].request_id) {
        clearSecretState();
      }
      activeInteractionID = interactions[0].request_id;
      interactionCmdExpanded = false;
    }
    if (autoSwitched && activeInteractionID && activeInteractionID !== prevActiveID) {
      armInteractionGuard();
    }

    const idx = Math.max(0, interactions.findIndex(x => x.request_id === activeInteractionID));
    const item = interactions[idx];
    const busyAction = Boolean(getBusyAction?.());
    const guarded = isGuarded();
    const actionDisabled = busyAction || guarded;
    const draft = getInteractionDraft(item.request_id);
    const localeTag = t("common.worker_action.badge_approval");
    const qs = itemQuestions(item);
    const isSecretCard = item.kind !== "approval" && questionHasSecret(qs);

    // Keep live secret input DOM intact when the same secret card stays active
    // across background polls, newly queued interactions from other workers, or
    // busy/guard state transitions. Update header pager & disabled states in place.
    if (isSecretCard && renderedSecretReqID === item.request_id && secretHeadEl && localeTag === lastLocaleTag) {
      const freshHead = buildHead(item, idx);
      secretHeadEl.replaceChildren(...freshHead.children);
      for (const btn of secretActionButtons) {
        btn.disabled = actionDisabled;
      }
      for (const el of secretControlInputs) {
        el.disabled = busyAction;
      }
      paintCountdown();
      return;
    }

    const sig = JSON.stringify({
      locale: localeTag,
      ids: interactions.map(x => x.request_id),
      active: item.request_id,
      visible: Boolean(item.visible_to_human),
      claimed: Boolean(item.claimed_by_caller),
      escalateAt: item.escalate_at || "",
      note: item.escalation_note || "",
      expanded: interactionCmdExpanded,
      busyAction,
      guarded,
      selected: Object.fromEntries(Object.entries(draft.selected).map(([k, v]) => [k, Array.from(v)])),
    });
    if (!force && sig === lastInteractionsSig) {
      paintCountdown();
      return;
    }
    lastInteractionsSig = sig;
    lastLocaleTag = localeTag;
    clearCountdown();
    clearSecretState();

    interactionsEl.hidden = false;
    interactionsEl.replaceChildren();

    const head = buildHead(item, idx);
    interactionsEl.append(head);

    if (item.kind === "approval") {
      renderApprovalBody(item, actionDisabled);
    } else {
      renderInputBody(item, qs, draft, busyAction, actionDisabled, isSecretCard ? head : null);
    }
  }

  function renderApprovalBody(item, actionDisabled) {
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
        toggle.addEventListener("click", () => { interactionCmdExpanded = !interactionCmdExpanded; render(true); });
        interactionsEl.append(toggle);
      }
    }
    const actions = node("div", null, "workers-int-actions");
    const bOnce = node("button", t("common.worker_action.allow_once"));
    bOnce.type = "button";
    bOnce.disabled = actionDisabled;
    bOnce.addEventListener("click", () => void onDecide({request_id: item.request_id, decision: "allow_once"}));
    actions.append(bOnce);

    if (item.allow_session_ok && !item.hard_redline) {
      const bSess = node("button", t("common.worker_action.allow_session"), "secondary");
      bSess.type = "button";
      bSess.disabled = actionDisabled;
      bSess.addEventListener("click", () => void onDecide({request_id: item.request_id, decision: "allow_session"}));
      actions.append(bSess);
    }

    const bDeny = node("button", t("common.worker_action.deny"), "secondary");
    bDeny.type = "button";
    bDeny.disabled = actionDisabled;
    bDeny.addEventListener("click", () => void onDecide({request_id: item.request_id, decision: "deny"}));
    actions.append(bDeny);

    const bCancel = node("button", t("common.worker_action.cancel_turn"), "danger");
    bCancel.type = "button";
    bCancel.disabled = actionDisabled;
    bCancel.addEventListener("click", () => void onDecide({request_id: item.request_id, decision: "cancel_turn"}));
    actions.append(bCancel);
    interactionsEl.append(actions);
  }

  function renderInputBody(item, qs, draft, busyAction, actionDisabled, headEl) {
    if (!item.visible_to_human && item.escalate_at) {
      const waitBadge = node("span", "", "badge");
      waitBadge.dataset.tone = "warn";
      armCountdown(waitBadge, item);
      interactionsEl.append(waitBadge);
    }
    if (item.escalation_note) {
      interactionsEl.append(node("p", t("common.worker_action.escalation_note", {note: item.escalation_note}), "worker-meta-line"));
    }
    const hasSecret = questionHasSecret(qs);
    let focusTarget = null;
    let submitAns = () => {};

    for (const q of qs) {
      const qID = q.id || "q1";
      interactionsEl.append(node("p", q.question || item.summary || "", "worker-meta-line"));
      if (Array.isArray(q.options) && q.options.length) {
        let sel;
        if (q.is_secret) {
          if (!secretSelected.has(qID)) secretSelected.set(qID, new Set());
          sel = secretSelected.get(qID);
        } else {
          if (!draft.selected[qID]) draft.selected[qID] = new Set();
          sel = draft.selected[qID];
        }
        const optRow = node("div", null, "workers-int-opts");
        const chips = [];
        for (const opt of q.options) {
          const chip = node("button", opt.description ? `${opt.label} — ${opt.description}` : opt.label, "workers-int-opt");
          chip.type = "button";
          chip.dataset.optionLabel = opt.label;
          chip.setAttribute("aria-pressed", sel.has(opt.label) ? "true" : "false");
          chip.disabled = busyAction;
          chip.addEventListener("click", () => {
            if (q.multi_select) {
              if (sel.has(opt.label)) sel.delete(opt.label); else sel.add(opt.label);
            } else {
              const had = sel.has(opt.label);
              sel.clear();
              if (!had) sel.add(opt.label);
            }
            if (hasSecret) {
              for (const c of chips) {
                c.setAttribute("aria-pressed", sel.has(c.dataset.optionLabel) ? "true" : "false");
              }
            } else {
              render(true);
            }
          });
          chips.push(chip);
          if (hasSecret) secretControlInputs.push(chip);
          optRow.append(chip);
        }
        interactionsEl.append(optRow);
      }
      if (hasSecret) {
        if (q.is_secret) {
          const input = node("input", null, "workers-int-secret");
          input.type = "password";
          input.autocomplete = "new-password";
          input.spellcheck = false;
          input.placeholder = t("common.worker_action.answer_placeholder");
          input.dataset.questionId = qID;
          input.disabled = busyAction;
          input.addEventListener("keydown", ev => {
            if (ev.key === "Enter" && !ev.isComposing) {
              ev.preventDefault();
              submitAns();
            }
          });
          secretInputs.set(qID, input);
          secretControlInputs.push(input);
          if (!focusTarget) focusTarget = input;
          interactionsEl.append(input);
        } else if (!Array.isArray(q.options) || !q.options.length || q.is_other) {
          const input = node("input", null, "workers-int-input");
          input.type = "text";
          input.placeholder = t("common.worker_action.answer_placeholder");
          input.value = draft.answers[qID] || "";
          input.disabled = busyAction;
          input.addEventListener("input", () => { draft.answers[qID] = input.value; });
          input.addEventListener("keydown", ev => {
            if (ev.key === "Enter" && !ev.isComposing) {
              ev.preventDefault();
              submitAns();
            }
          });
          secretControlInputs.push(input);
          if (!focusTarget) focusTarget = input;
          interactionsEl.append(input);
        }
      }
    }

    if (hasSecret) {
      renderedSecretReqID = item.request_id;
      secretHeadEl = headEl;
      submitAns = () => {
        const answers = {};
        let hasAny = false;
        for (const [qID, set] of Object.entries(draft.selected)) {
          if (set && set.size > 0) {
            answers[qID] = Array.from(set);
            hasAny = true;
          }
        }
        for (const [qID, set] of secretSelected.entries()) {
          if (set && set.size > 0) {
            answers[qID] = Array.from(set);
            hasAny = true;
          }
        }
        for (const [qID, val] of Object.entries(draft.answers)) {
          const trimmed = (val || "").trim();
          if (trimmed) {
            answers[qID] = answers[qID] ? [...answers[qID], trimmed] : [trimmed];
            hasAny = true;
          }
        }
        for (const [qID, input] of secretInputs.entries()) {
          const val = input.value || "";
          if (val !== "") {
            answers[qID] = answers[qID] ? [...answers[qID], val] : [val];
            hasAny = true;
          }
        }
        if (!hasAny) {
          focusTarget?.focus?.();
          return;
        }
        clearSecretState();
        void onDecide({request_id: item.request_id, decision: "answer", answers});
      };
    } else {
      const textarea = node("textarea", null, "workers-int-input");
      textarea.placeholder = t("common.worker_action.answer_placeholder");
      textarea.value = draft.text || "";
      textarea.disabled = busyAction;
      let composing = false;
      textarea.addEventListener("compositionstart", () => { composing = true; });
      textarea.addEventListener("compositionend", () => { composing = false; draft.text = textarea.value; });
      textarea.addEventListener("input", () => { draft.text = textarea.value; });
      submitAns = () => {
        const text = (draft.text || "").trim();
        const answers = {};
        let hasSel = false;
        for (const [qID, set] of Object.entries(draft.selected)) {
          if (set && set.size > 0) { answers[qID] = Array.from(set); hasSel = true; }
        }
        if (!text && !hasSel) { textarea.focus(); return; }
        void onDecide({request_id: item.request_id, decision: "answer", text, answers});
      };
      textarea.addEventListener("keydown", ev => {
        if (ev.key === "Enter" && !ev.shiftKey && !composing && !ev.isComposing) {
          ev.preventDefault();
          submitAns();
        }
      });
      interactionsEl.append(textarea);
    }

    const actions = node("div", null, "workers-int-actions");
    const bSend = node("button", t("common.worker_action.send_answer"));
    bSend.type = "button";
    bSend.disabled = actionDisabled;
    bSend.addEventListener("click", submitAns);
    const bCancel = node("button", t("common.worker_action.cancel_turn"), "danger");
    bCancel.type = "button";
    bCancel.disabled = actionDisabled;
    bCancel.addEventListener("click", () => {
      clearSecretState();
      void onDecide({request_id: item.request_id, decision: "cancel_turn"});
    });
    if (hasSecret) {
      secretActionButtons.push(bSend, bCancel);
    }
    actions.append(bSend, bCancel);
    interactionsEl.append(actions);
  }

  return {
    setItems,
    hasItems,
    isGuarded,
    render,
    invalidateLocale,
    stop,
  };
}
