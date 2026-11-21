import {$, localizeError, node} from "./ui.mjs";
import {t} from "./i18n.mjs";

export function harnessesSettings({api, post, listen, session, notice}) {
  const statusEl = $("#harnesses-status");
  const listEl = $("#harnesses-list");
  const turnsSection = $("#harnesses-turns-section");
  const turnsEl = $("#harnesses-turns-list");
  const refreshBtn = $("#harnesses-refresh");
  let state = null;
  let loadGeneration = 0;
  let refreshing = false;
  const pendingSwitches = new Map();
  const pendingIssues = new Set();
  const pendingPaths = new Set();
  const pendingTurns = new Set();
  const cardNodes = new Map();
  let lastTurnsSig = null;
  let mutationChain = Promise.resolve();

  const setTextIfChanged = (el, text) => {
    if (el && el.textContent !== text) el.textContent = text;
  };

  function setStatus(key, isError = false) {
    if (!statusEl) return;
    setTextIfChanged(statusEl, key ? t(key) : "");
    if (isError) statusEl.dataset.tone = "error";
    else delete statusEl.dataset.tone;
  }

  function syncRefreshUI() {
    if (!refreshBtn) return;
    refreshBtn.setAttribute("aria-busy", String(refreshing));
    refreshBtn.disabled = refreshing;
    refreshBtn.title = t(refreshing ? "web.harnesses.loading" : "web.harnesses.refresh");
  }

  function createSwitchEntry(harnessID, field, labelKey, reconcileMCP, extraClass = "") {
    const cls = extraClass ? `harness-switch-row ${extraClass}` : "harness-switch-row";
    const row = node("label", null, cls);
    const span = node("span", t(labelKey));
    const input = document.createElement("input");
    input.type = "checkbox";
    input.className = "switch";
    input.addEventListener("change", () => {
      void toggleSwitch(harnessID, field, input.checked, reconcileMCP, input);
    });
    row.append(span, input);
    return {row, span, input, labelKey};
  }

  function noteKeyForHarness(h) {
    if (!h.capabilities?.supports_worker) return "web.harnesses.antigravity_caller_only";
    if (!h.capabilities?.caller_managed && !h.capabilities?.worker_managed) return "web.harnesses.deepseek_discovery_only";
    if (h.capabilities?.caller_managed && !h.capabilities?.worker_managed) return "web.harnesses.worker_coming_soon";
    return "";
  }

  function createHarnessCard(h) {
    const card = node("div", null, "harness-card");
    const head = node("div", null, "harness-card-head");
    const titleWrap = node("div", null, "harness-title-wrap");
    const titleEl = node("strong", h.id);
    const verEl = node("span", "", "harness-version");
    verEl.hidden = true;
    titleWrap.append(titleEl, verEl);
    const headRight = node("div", null, "harness-head-right");
    const badgeEl = node("span", "", "badge");
    const pathToggleBtn = node("button", t("web.harnesses.edit_path"), "quiet harness-path-btn");
    pathToggleBtn.type = "button";
    headRight.append(badgeEl, pathToggleBtn);
    head.append(titleWrap, headRight);
    card.append(head);

    const pathEl = node("small", "", "harness-path");
    pathEl.hidden = true;
    card.append(pathEl);

    const pathEditor = node("div", null, "harness-path-editor");
    pathEditor.hidden = true;
    const pathInput = document.createElement("input");
    pathInput.type = "text";
    pathInput.placeholder = t("web.harnesses.path_placeholder");
    const pathSaveBtn = node("button", t("web.harnesses.path_save"), "secondary");
    pathSaveBtn.type = "button";
    const pathClearBtn = node("button", t("web.harnesses.path_clear"), "quiet");
    pathClearBtn.type = "button";
    pathEditor.append(pathInput, pathSaveBtn, pathClearBtn);
    card.append(pathEditor);

    const switchesEl = node("div", null, "harness-switches");
    const switches = new Map();
    if (h.capabilities?.caller_managed) {
      switches.set("general_tools_enabled", createSwitchEntry(h.id, "general_tools_enabled", "web.harnesses.switch_general", true));
      switches.set("collab_caller_enabled", createSwitchEntry(h.id, "collab_caller_enabled", "web.harnesses.switch_caller", true));
    }
    if (h.capabilities?.worker_managed) {
      switches.set("collab_worker_enabled", createSwitchEntry(h.id, "collab_worker_enabled", "web.harnesses.switch_worker", false));
      switches.set("allow_workspace_write", createSwitchEntry(h.id, "allow_workspace_write", "web.harnesses.switch_workspace_write", false, "harness-sub-switch"));
    }
    for (const sw of switches.values()) switchesEl.append(sw.row);
    if (switches.size > 0) card.append(switchesEl);

    const noteKey = noteKeyForHarness(h);
    let noteEl = null;
    if (noteKey) {
      noteEl = node("small", t(noteKey), "harness-note");
      card.append(noteEl);
    }

    const issueEl = node("div", null, "harness-issue");
    issueEl.hidden = true;
    const issueText = node("p", "");
    const issueBtn = node("button", "", "secondary");
    issueBtn.type = "button";
    issueEl.append(issueText, issueBtn);
    card.append(issueEl);

    const entry = {
      card,
      verEl,
      badgeEl,
      pathToggleBtn,
      pathEl,
      pathEditor,
      pathInput,
      pathSaveBtn,
      pathClearBtn,
      switches,
      noteEl,
      noteKey,
      issueEl,
      issueText,
      issueBtn,
      issueConflict: false,
      editingPath: false,
    };

    pathToggleBtn.addEventListener("click", () => {
      entry.editingPath = !entry.editingPath;
      pathEditor.hidden = !entry.editingPath;
      if (entry.editingPath) {
        const cur = state?.harnesses?.find(x => x.id === h.id);
        pathInput.value = cur?.manual_path || "";
        pathInput.focus();
      }
    });
    pathSaveBtn.addEventListener("click", () => void saveManualPath(h.id, pathInput.value, entry));
    pathClearBtn.addEventListener("click", () => {
      pathInput.value = "";
      void saveManualPath(h.id, "", entry);
    });
    issueBtn.addEventListener("click", () => void reconcileHarnessIssue(h.id, entry.issueConflict));
    return entry;
  }

  function updateHarnessCard(entry, h) {
    setTextIfChanged(entry.badgeEl, t(h.detected ? "web.harnesses.detected" : "web.harnesses.not_detected"));
    if (h.detected) entry.badgeEl.dataset.tone = "ok";
    else delete entry.badgeEl.dataset.tone;

    const ver = h.version || "";
    setTextIfChanged(entry.verEl, ver);
    entry.verEl.hidden = !ver;

    setTextIfChanged(entry.pathToggleBtn, t("web.harnesses.edit_path"));
    entry.pathInput.placeholder = t("web.harnesses.path_placeholder");
    setTextIfChanged(entry.pathSaveBtn, t("web.harnesses.path_save"));
    setTextIfChanged(entry.pathClearBtn, t("web.harnesses.path_clear"));
    const pathBusy = pendingPaths.has(h.id);
    entry.pathSaveBtn.disabled = pathBusy;
    entry.pathClearBtn.disabled = pathBusy;

    const path = h.executable_path || h.manual_path || "";
    setTextIfChanged(entry.pathEl, path);
    entry.pathEl.hidden = !path;

    for (const [field, sw] of entry.switches.entries()) {
      setTextIfChanged(sw.span, t(sw.labelKey));
      const key = `${h.id}:${field}`;
      if (!pendingSwitches.has(key)) {
        const nextChecked = Boolean(h[field]);
        if (sw.input.checked !== nextChecked) sw.input.checked = nextChecked;
      }
      if (field === "allow_workspace_write") {
        const workerSw = entry.switches.get("collab_worker_enabled");
        const workerOn = workerSw ? workerSw.input.checked : Boolean(h.collab_worker_enabled);
        sw.row.hidden = !workerOn;
      }
    }

    if (entry.noteEl && entry.noteKey) {
      setTextIfChanged(entry.noteEl, t(entry.noteKey));
    }

    const showIssue = Boolean(h.capabilities?.caller_managed);
    const hasConflict = showIssue && h.mcp_observed === "conflict";
    const hasError = showIssue && (h.mcp_observed === "error" || Boolean(h.last_reconcile_error));
    const hasDrift = showIssue && (
      (h.mcp_desired && h.mcp_observed !== "registered")
      || (!h.mcp_desired && h.mcp_observed === "registered")
    );
    if (!hasConflict && !hasError && !hasDrift) {
      entry.issueEl.hidden = true;
      return;
    }
    entry.issueConflict = hasConflict;
    entry.issueEl.hidden = false;
    entry.issueEl.dataset.tone = hasConflict ? "warn" : "error";
    const msg = hasConflict
      ? t("web.harnesses.issue_conflict")
      : (h.last_reconcile_error || t("web.harnesses.issue_drift"));
    setTextIfChanged(entry.issueText, msg);
    setTextIfChanged(entry.issueBtn, t(hasConflict ? "web.harnesses.takeover_config" : "web.harnesses.retry_connect"));
    entry.issueBtn.disabled = pendingIssues.has(h.id);
  }

  function renderHarnesses() {
    if (!listEl) return;
    const items = Array.isArray(state?.harnesses) ? state.harnesses : [];
    const activeIDs = new Set(items.map(h => h.id));

    for (const [id, entry] of cardNodes.entries()) {
      if (!activeIDs.has(id)) {
        entry.card.remove();
        cardNodes.delete(id);
      }
    }

    for (const h of items) {
      let entry = cardNodes.get(h.id);
      if (!entry) {
        entry = createHarnessCard(h);
        cardNodes.set(h.id, entry);
        listEl.append(entry.card);
      }
      updateHarnessCard(entry, h);
    }
  }

  function renderTurns(force = false) {
    if (!turnsEl) return;
    const turns = Array.isArray(state?.active_turns) ? state.active_turns : [];
    if (turnsSection) turnsSection.hidden = turns.length === 0;
    const sig = JSON.stringify({turns, pending: [...pendingTurns]});
    if (!force && sig === lastTurnsSig) return;
    lastTurnsSig = sig;
    turnsEl.replaceChildren();
    if (!turns.length) return;
    for (const turn of turns) {
      const li = node("li", null, "harness-row");
      const info = node("div", null, "harness-row-meta");
      info.append(
        node("strong", `${turn.turn_id} · ${turn.harness_id} · ${turn.state}`),
        node("small", turn.progress || turn.error_message || turn.worker_id),
      );
      const btns = node("div", null, "actions");
      const isPending = pendingTurns.has(turn.turn_id);
      if (turn.state === "running" || turn.state === "prepared") {
        const stopBtn = node("button", t("web.harnesses.turn_stop"), "danger");
        stopBtn.type = "button";
        stopBtn.disabled = isPending;
        stopBtn.addEventListener("click", () => void mutateTurn("cancel", turn.turn_id));
        btns.append(stopBtn);
      }
      if (turn.state === "reconciliation_required") {
        const recBtn = node("button", t("web.harnesses.turn_reconcile"), "secondary");
        recBtn.type = "button";
        recBtn.disabled = isPending;
        recBtn.addEventListener("click", () => void mutateTurn("reconcile", turn.turn_id));
        const resBtn = node("button", t("web.harnesses.turn_resolve"), "secondary");
        resBtn.type = "button";
        resBtn.disabled = isPending;
        resBtn.addEventListener("click", () => void mutateTurn("resolve", turn.turn_id));
        btns.append(recBtn, resBtn);
      }
      li.append(info, btns);
      turnsEl.append(li);
    }
  }

  function paint(force = false) {
    syncRefreshUI();
    if (!state) return;
    renderHarnesses();
    renderTurns(force);
  }

  function enqueueMutation(fn) {
    const next = mutationChain.then(fn, fn);
    mutationChain = next.catch(() => {});
    return next;
  }

  async function open() {
    const scope = session();
    const ver = ++loadGeneration;
    refreshing = true;
    syncRefreshUI();
    try {
      const latest = await api("/api/harnesses/status");
      if (!scope?.active || ver !== loadGeneration) return;
      state = latest;
      setStatus("");
      paint();
    } catch (err) {
      if (!scope?.active || ver !== loadGeneration) return;
      setStatus("web.harnesses.load_failed", true);
      notice?.(localizeError(err));
    } finally {
      if (scope?.active && ver === loadGeneration) {
        refreshing = false;
        syncRefreshUI();
      }
    }
  }

  function toggleSwitch(harnessID, field, value, reconcileMCP, input) {
    const scope = session();
    const key = `${harnessID}:${field}`;
    pendingSwitches.set(key, (pendingSwitches.get(key) || 0) + 1);
    const entryImmediate = cardNodes.get(harnessID);
    if (entryImmediate && field === "collab_worker_enabled") {
      const writeSw = entryImmediate.switches.get("allow_workspace_write");
      if (writeSw) writeSw.row.hidden = !value;
    }
    return enqueueMutation(async () => {
      try {
        const payload = {harness_id: harnessID, [field]: value};
        if (reconcileMCP) payload.reconcile_mcp = true;
        const latest = await post("/api/harnesses/policy", payload);
        if (!scope?.active) return;
        state = latest;
        setStatus("");
      } catch (err) {
        if (!scope?.active) return;
        const current = state?.harnesses?.find(h => h.id === harnessID);
        if (current && (pendingSwitches.get(key) || 0) <= 1) {
          input.checked = Boolean(current[field]);
        }
        notice?.(localizeError(err));
      } finally {
        const remaining = (pendingSwitches.get(key) || 1) - 1;
        if (remaining <= 0) pendingSwitches.delete(key);
        else pendingSwitches.set(key, remaining);
        if (scope?.active) {
          const updated = state?.harnesses?.find(h => h.id === harnessID);
          const entry = cardNodes.get(harnessID);
          if (updated && entry) updateHarnessCard(entry, updated);
        }
      }
    });
  }

  function saveManualPath(harnessID, manualPath, entry) {
    const scope = session();
    pendingPaths.add(harnessID);
    entry.pathSaveBtn.disabled = true;
    entry.pathClearBtn.disabled = true;
    return enqueueMutation(async () => {
      try {
        const latest = await post("/api/harnesses/policy", {
          harness_id: harnessID,
          manual_path: manualPath.trim(),
        });
        if (!scope?.active) return;
        state = latest;
        entry.editingPath = false;
        entry.pathEditor.hidden = true;
      } catch (err) {
        if (!scope?.active) return;
        notice?.(localizeError(err));
      } finally {
        pendingPaths.delete(harnessID);
        if (scope?.active) {
          const updated = state?.harnesses?.find(h => h.id === harnessID);
          if (updated) updateHarnessCard(entry, updated);
        }
      }
    });
  }

  function reconcileHarnessIssue(harnessID, takeOverConflict) {
    const scope = session();
    pendingIssues.add(harnessID);
    const entry = cardNodes.get(harnessID);
    if (entry) entry.issueBtn.disabled = true;
    return enqueueMutation(async () => {
      try {
        const latest = await post("/api/harnesses/policy", {
          harness_id: harnessID,
          reconcile_mcp: true,
          take_over_conflict: takeOverConflict,
        });
        if (!scope?.active) return;
        state = latest;
      } catch (err) {
        if (!scope?.active) return;
        notice?.(localizeError(err));
      } finally {
        pendingIssues.delete(harnessID);
        if (scope?.active) {
          const updated = state?.harnesses?.find(h => h.id === harnessID);
          const curEntry = cardNodes.get(harnessID);
          if (updated && curEntry) updateHarnessCard(curEntry, updated);
        }
      }
    });
  }

  async function mutateTurn(action, turnID) {
    const scope = session();
    pendingTurns.add(turnID);
    renderTurns();
    try {
      const res = await post("/api/harnesses/turn-action", {action, turn_id: turnID});
      if (!scope?.active) return;
      if (state) state = {...state, active_turns: res.active_turns || []};
    } catch (err) {
      if (!scope?.active) return;
      notice?.(localizeError(err));
    } finally {
      pendingTurns.delete(turnID);
      if (scope?.active) renderTurns();
    }
  }

  if (refreshBtn) {
    listen(refreshBtn, "click", () => void open());
  }

  return {open, relocalize: () => paint(true)};
}
