import {localizeError, localizeMessage, t} from "./i18n.mjs";
import {applyTheme} from "./ui.mjs";

const regKeys = {
  initializing: "web.desktop.reg.initializing",
  recovering: "web.desktop.reg.recovering",
  conflict: "web.desktop.reg.conflict",
  unavailable: "web.desktop.reg.unavailable",
  unsupported: "web.desktop.reg.unsupported",
  config_invalid: "web.desktop.reg.config_invalid",
};

const recordStatusKeys = {
  idle: "web.desktop.record_hint",
  waiting: "web.desktop.record_waiting",
  done: "web.desktop.record_done",
  cancelled: "web.desktop.record_cancelled",
  ime: "web.desktop.record_ime_hint",
};

function normalizeBinding(value) {
  return {
    modifiers: [...(value?.modifiers || [])].sort(),
    key: value?.key || "",
  };
}

function sameNonLocale(a, b) {
  if (!a || !b) return false;
  const ba = normalizeBinding(a.binding), bb = normalizeBinding(b.binding);
  return Boolean(a.enabled) === Boolean(b.enabled) &&
    a.theme_id === b.theme_id &&
    ba.key === bb.key &&
    ba.modifiers.length === bb.modifiers.length &&
    ba.modifiers.every((m, idx) => m === bb.modifiers[idx]);
}

function snapshotBaseline(desired) {
  if (!desired) return null;
  return {
    revision: desired.revision,
    enabled: Boolean(desired.enabled),
    binding: structuredClone(desired.binding),
    theme_id: desired.theme_id,
  };
}

export function desktopSettings(api, post, {listen = (target, event, callback) => target.addEventListener(event, callback), session = () => ({active:true})} = {}) {
  const form = document.querySelector("#desktop-form"), fields = form.elements;
  const state = document.querySelector("#desktop-state");
  const recordInput = document.querySelector("#desktop-record");
  const recordStatus = document.querySelector("#desktop-record-status");
  const recordCancel = document.querySelector("#desktop-record-cancel");
  const themeStatus = document.querySelector("#theme-status");
  const themeRadios = [...(document.querySelectorAll?.('input[name="ui-theme"]') ?? [])];
  const page = crypto.randomUUID();
  let current, formBaseline = null, recording, poll, pending, pendingLocale = null, busy, activeSave = null, themeError = null, pendingTheme = null;
  let recordingGeneration = 0, pageGeneration = 0, localeGeneration = 0;
  let requestClock = 0, latestAppliedClock = 0;
  let stateOverride = null, recordMode = "idle";
  let cancellation = Promise.resolve();
  const key = document.querySelector("#desktop-key");
  for (const name of [..."ABCDEFGHIJKLMNOPQRSTUVWXYZ"].map(c => "Key"+c)
    .concat([..."0123456789"].map(c => "Digit"+c),
      Array.from({length:11}, (_, i) => "F"+(i+1)),
      ["Space", "Enter", "Pause", "Insert", "Delete", "Home", "End", "PageUp", "PageDown"])) {
    const option = document.createElement("option");
    option.value = name; option.textContent = name.replace(/^(Key|Digit)/, ""); key.append(option);
  }
  function binding() {
    return {modifiers:["ctrl","alt","shift"].filter(m => fields[m].checked), key:fields.key.value};
  }
  function label(value) {
    return [...value.modifiers.map(m => m[0].toUpperCase()+m.slice(1)), value.key.replace(/^(Key|Digit)/, "")].join("+");
  }
  function setBinding(value) {
    for (const m of ["ctrl","alt","shift"]) fields[m].checked = value.modifiers.includes(m);
    fields.key.value = value.key; recordInput.value = label(value);
  }
  function paintSaveButton() {
    document.querySelector("#desktop-save").textContent = pending ? t("web.desktop.save_retry") : t("web.desktop.save");
  }
  function paintTheme() {
    const id = current?.desired?.theme_id;
    for (const radio of themeRadios) radio.checked = radio.value === id;
    if (!themeStatus) return;
    themeStatus.textContent = themeError ? localizeError(themeError) : t("web.desktop.theme_hint");
    if (themeStatus.dataset) themeStatus.dataset.tone = themeError ? "error" : "";
  }
  function paintRecordStatus(mode = recordMode) {
    recordMode = mode;
    if (typeof mode === "object" && mode?.error) {
      recordStatus.textContent = localizeError(mode.error);
      return;
    }
    recordStatus.textContent = t(recordStatusKeys[mode] || recordStatusKeys.idle);
  }
  function paintState() {
    if (stateOverride?.kind === "check_available") {
      state.textContent = t("web.desktop.check_available");
      if (state.dataset) state.dataset.tone = "ok";
      return;
    }
    if (stateOverride?.kind === "error") {
      state.textContent = localizeError(stateOverride.error);
      if (state.dataset) state.dataset.tone = "error";
      return;
    }
    if (!current) {
      state.textContent = t("web.desktop.loading");
      return;
    }
    const value = current;
    const usable = value.registration === "registered" && value.surface === "ready" && value.effective_binding;
    const desired = label(value.desired.binding);
    let text, tone = "warn";
    if (!value.supported) [text, tone] = [t("web.desktop.state.unsupported_full"), "muted"];
    else if (usable && label(value.effective_binding) === desired) [text, tone] = [t("web.desktop.state.active", {binding: desired}), "ok"];
    else if (usable) text = t("web.desktop.state.pending_new", {effective: label(value.effective_binding), desired});
    else if (value.registration === "disabled") [text, tone] = [t("web.desktop.state.disabled"), "muted"];
    else if (value.surface === "failed") [text, tone] = [t("web.desktop.state.surface_failed"), "error"];
    else {
      const reason = t(regKeys[value.registration] || "web.desktop.reg.pending");
      [text, tone] = [t("web.desktop.state.inactive_with_reason", {reason, desired}), value.registration === "conflict" ? "error" : "warn"];
    }
    if (value.cleanup_pending) text += t("web.desktop.state.cleanup_pending");
    const detailError = localizeMessage(value.error_key, value.error_args, value.error || "");
    if (detailError) text += ` · ${detailError}`;
    state.textContent = text;
    if (state.dataset) state.dataset.tone = tone;
  }
  function render(value, fill = false) {
    current = value;
    stateOverride = null;
    paintState();
    paintTheme();
    const usable = value.registration === "registered" && value.surface === "ready" && value.effective_binding;
    // Retry is only meaningful when an enabled binding is not in effect.
    document.querySelector("#desktop-retry").hidden = !value.supported || (!value.cleanup_pending && (usable || !value.desired.enabled));
    applyTheme(value.desired.theme_id);
    if (!activeSave) document.querySelector("#desktop-fields").disabled = !value.supported;
    if (fill) {
      formBaseline = snapshotBaseline(value.desired);
      fields.enabled.checked = value.desired.enabled; fields.theme.value = value.desired.theme_id;
      setBinding(value.desired.binding);
    } else if (!pending && formBaseline && sameNonLocale(formBaseline, value.desired)) {
      formBaseline.revision = value.desired.revision;
    }
    reconcileTheme(value, fill);
  }
  // A theme save whose outcome is unknown stays pending until any later valid
  // snapshot settles it. Only this operation's own record confirms success;
  // a later write by anyone else ends the wait and keeps the old baseline.
  // A confirmation that arrives while a shortcut save is still unresolved is
  // kept and applied once that request has ended, so the baseline cannot stay
  // behind the theme revision.
  function reconcileTheme(value, fill) {
    if (!pendingTheme || !value?.desired) return;
    if (fill) { pendingTheme = null; return; }
    if (!pendingTheme.confirmed) {
      if (value.last_operation === pendingTheme.operation) {
        pendingTheme.confirmed = structuredClone(value.desired);
        themeError = null; paintTheme();
      } else if (value.desired.revision !== pendingTheme.base.revision) {
        pendingTheme = null;
        return;
      }
    }
    settleTheme();
  }
  function settleTheme() {
    if (!pendingTheme?.confirmed || pending) return;
    themeSaved(pendingTheme.base, pendingTheme.confirmed);
    pendingTheme = null;
  }
  const isCurrent = (generation, scope) => generation === pageGeneration && scope?.active;
  async function fetchSettings({allowError = false, generation = pageGeneration, scope = session(), valid = () => true} = {}) {
    const stillValid = () => isCurrent(generation, scope) && valid();
    if (!stillValid()) return {stale:true, value:current};
    const stamp = ++requestClock;
    try {
      const value = await api("/api/desktop/settings");
      if (!stillValid() || stamp < latestAppliedClock) return {stale:true, value:current};
      latestAppliedClock = stamp;
      return {stale:false, value};
    } catch (error) {
      if (allowError) throw error;
      return {stale:!stillValid() || stamp < latestAppliedClock, value:null};
    }
  }
  function commitWriteSnapshot(value, fill = false) {
    latestAppliedClock = ++requestClock;
    render(value, fill);
  }
  async function refresh(fill = false) {
    const generation = pageGeneration, scope = session();
    const {stale, value} = await fetchSettings({allowError:true, generation, scope});
    if (!stale && value) render(value, fill);
    return stale ? current : value;
  }
  async function endRecording() {
    recordingGeneration++;
    const previous = recording; recording = null; clearTimeout(poll);
    recordCancel.hidden = true;
    if (previous) cancellation = cancelLease(previous);
    await cancellation;
  }
  async function cancelLease(lease) {
    if (!lease) return;
    try { await post("/api/desktop/recording/cancel", {page_id:page, recording_id:lease.recording_id}); }
    catch { /* The server lease still expires independently after 30 seconds. */ }
  }
  async function run(fn) {
    if (busy?.generation === pageGeneration) return;
    const generation = pageGeneration, scope = session();
    if (!scope?.active) return;
    const transaction = busy = {generation};
    try { await fn(); }
    catch (error) { if (isCurrent(generation, scope)) { stateOverride = {kind:"error", error}; paintState(); } }
    finally { if (busy === transaction) busy = undefined; }
  }
  async function recordPoll() {
    if (!recording) return;
    const lease = recording, generation = recordingGeneration;
    const active = () => recording === lease && generation === recordingGeneration;
    try {
      const value = await post("/api/desktop/recording/poll", {page_id:page, recording_id:lease.recording_id});
      if (!active()) return;
      if (value.binding) {
        setBinding(value.binding);
        pending = null;
        const ended = endRecording();
        paintRecordStatus("done");
        await ended;
        return;
      }
      poll = setTimeout(recordPoll, 250);
    } catch (error) {
      if (active()) {
        const ended = endRecording();
        paintRecordStatus({error});
        await ended;
      }
    }
  }
  listen(document.querySelector("#desktop-record-start"), "click", () => run(async () => {
    const cancelled = endRecording();
    // Capture before awaiting: blur/close can invalidate this start in flight.
    const generation = recordingGeneration;
    await cancelled;
    if (generation !== recordingGeneration) return;
    pending = null;
    const lease = await post("/api/desktop/recording/start", {page_id:page});
    if (generation !== recordingGeneration) {
      await cancelLease(lease);
      return;
    }
    recording = lease;
    recordCancel.hidden = false;
    paintRecordStatus("waiting");
    recordInput.focus();
    void recordPoll();
  }));
  listen(recordInput, "keydown", event => {
    if (!recording || event.isComposing || event.repeat) return;
    event.preventDefault(); event.stopPropagation();
    if (event.key === "Escape") { void endRecording(); paintRecordStatus("cancelled"); return; }
    const identity = /^[a-z]$/i.test(event.key) ? "Key"+event.key.toUpperCase() : /^[0-9]$/.test(event.key) ? "Digit"+event.key : event.code;
    if (/^(Key|Digit)/.test(identity) && !/^[a-z0-9]$/i.test(event.key)) { paintRecordStatus("ime"); return; }
    if (event.metaKey || (!event.ctrlKey && !event.altKey) || ![...key.options].some(o => o.value === identity)) return;
    setBinding({modifiers:[event.ctrlKey && "ctrl", event.altKey && "alt", event.shiftKey && "shift"].filter(Boolean), key:identity});
    pending = null; void endRecording(); paintRecordStatus("done");
  });
  listen(recordInput, "blur", () => { void endRecording(); });
  listen(recordCancel, "click", () => { void endRecording(); paintRecordStatus("cancelled"); });
  listen(window, "blur", () => { void endRecording(); });
  listen(document.querySelector("#settings"), "close", () => { void endRecording(); });
  listen(form, "input", () => { pending = null; recordInput.value = label(binding()); paintSaveButton(); });
  async function saveRequest(reset = false) {
    const generation = pageGeneration, scope = session();
    await endRecording();
    if (!isCurrent(generation, scope)) return;
    settleTheme();
    const expectedRevision = formBaseline ? formBaseline.revision : current.desired.revision;
    pending ||= {operation_id:crypto.randomUUID(), expected_revision:expectedRevision, enabled:fields.enabled.checked, binding:binding(), theme_id:fields.theme.value, reset};
    try {
      const result = await post("/api/desktop/settings", pending);
      if (!isCurrent(generation, scope)) return;
      pending = null; commitWriteSnapshot(result, true); paintSaveButton();
    } catch (error) {
      if (!isCurrent(generation, scope)) return;
      const operation = pending.operation_id;
      await refresh();
      if (!isCurrent(generation, scope)) return;
      if (current.last_operation === operation) { pending = null; formBaseline = snapshotBaseline(current.desired); settleTheme(); return; }
      if (error.status || current.desired.revision !== pending.expected_revision) pending = null;
      // A theme confirmation held back by this request now updates the baseline.
      settleTheme();
      paintSaveButton();
      throw error;
    }
  }
  async function save(reset = false) {
    const generation = pageGeneration, scope = session(), saveToken = activeSave = {};
    document.querySelector("#desktop-fields").disabled = true;
    try { await saveRequest(reset); }
    finally {
      if (activeSave === saveToken) {
        activeSave = null;
        if (isCurrent(generation, scope)) document.querySelector("#desktop-fields").disabled = !current?.supported;
      }
    }
  }
  function inspectLocaleSnapshot(snap) {
    if (!snap?.desired?.binding) return {outcome:"offline"};
    render(snap, false);
    if (!pendingLocale) return {outcome:"synced", status:snap};
    if (snap.last_locale_operation === pendingLocale.operation_id) {
      pendingLocale = null;
      return {outcome:"confirmed", status:snap};
    }
    if (snap.desired?.revision !== pendingLocale.expected_revision) {
      pendingLocale = null;
      return {outcome:"superseded", status:snap};
    }
    return {outcome:"pending", status:snap, pending:pendingLocale};
  }
  async function reconcileLocale(snap = null) {
    const pGen = pageGeneration, lGen = localeGeneration, scope = session();
    if (snap) {
      if (!isCurrent(pGen, scope) || lGen !== localeGeneration) return {outcome:"stale", status:current};
      latestAppliedClock = ++requestClock;
      return inspectLocaleSnapshot(snap);
    }
    const {stale, value} = await fetchSettings({generation:pGen, scope, valid:() => lGen === localeGeneration});
    if (!isCurrent(pGen, scope) || lGen !== localeGeneration || stale) return {outcome:"stale", status:current};
    return inspectLocaleSnapshot(value);
  }
  async function setLocalePreference(locale) {
    const pGen = pageGeneration, lGen = ++localeGeneration, scope = session();
    const sameLocaleGen = () => lGen === localeGeneration;
    if (!scope?.active) return {ok:false, stale:true};
    if (!current?.desired?.revision) {
      const initial = await fetchSettings({allowError:true, generation:pGen, scope, valid:sameLocaleGen});
      if (!isCurrent(pGen, scope) || !sameLocaleGen()) return {ok:false, stale:true};
      if (!initial.stale && initial.value) render(initial.value, false);
    }
    const payload = pendingLocale && pendingLocale.locale === locale && pendingLocale.expected_revision === current.desired.revision
      ? pendingLocale
      : {operation_id:crypto.randomUUID(), expected_revision:current.desired.revision, locale};
    pendingLocale = payload;
    try {
      const result = await post("/api/desktop/locale", payload);
      if (!isCurrent(pGen, scope) || !sameLocaleGen()) return {ok:false, stale:true};
      if (pendingLocale !== payload) return {ok:true, status:current || result};
      pendingLocale = null;
      commitWriteSnapshot(result, false);
      return {ok:true, status:result};
    } catch (error) {
      if (!isCurrent(pGen, scope) || !sameLocaleGen()) return {ok:false, stale:true};
      const hadPending = pendingLocale === payload;
      if (error?.status) {
        if (hadPending) pendingLocale = null;
        if (error.details?.desktop && hadPending) {
          commitWriteSnapshot(error.details.desktop, false);
        } else if (!error.details?.desktop) {
          const fetched = await fetchSettings({generation:pGen, scope, valid:sameLocaleGen});
          if (!isCurrent(pGen, scope) || !sameLocaleGen()) return {ok:false, stale:true};
          if (!fetched.stale && fetched.value) render(fetched.value, false);
        }
        return {ok:false, rejected:true, status:current || error.details?.desktop, error};
      }
      const fetched = await fetchSettings({generation:pGen, scope, valid:sameLocaleGen});
      if (!isCurrent(pGen, scope) || !sameLocaleGen()) return {ok:false, stale:true};
      if (fetched.stale) {
        return current?.last_locale_operation === payload.operation_id
          ? {ok:true, reconciled:true, status:current}
          : {ok:false, unknown:Boolean(pendingLocale), superseded:!pendingLocale, status:current, pending:pendingLocale, error};
      }
      const rec = inspectLocaleSnapshot(fetched.value);
      if (rec.outcome === "confirmed") return {ok:true, reconciled:true, status:rec.status};
      return {ok:false, unknown:rec.outcome !== "superseded", superseded:rec.outcome === "superseded", status:rec.status || current, pending:pendingLocale, error};
    }
  }
  function relocalize() {
    paintState();
    paintTheme();
    paintRecordStatus();
    paintSaveButton();
  }
  // Theme applies on selection. It keeps the saved enabled state and binding,
  // so unsaved shortcut edits in the hotkey pane are never submitted with it.
  // A confirmed save, whether its response arrived or was reconciled later,
  // moves the hidden theme field and (if the form still started from the
  // pre-save state) the baseline revision, keeping unsaved shortcut edits.
  function themeSaved(base, desired) {
    if (formBaseline && !pending && formBaseline.revision === base.revision) {
      formBaseline.revision = desired.revision;
      formBaseline.theme_id = desired.theme_id;
    }
    fields.theme.value = desired.theme_id;
  }
  async function saveTheme(id) {
    const generation = pageGeneration, scope = session();
    if (busy?.generation === pageGeneration || !scope?.active) { paintTheme(); return; }
    const transaction = busy = {generation};
    themeError = null;
    try {
      // An earlier unknown theme save is settled before taking a new baseline.
      if (!current?.desired || pendingTheme) await refresh();
      // A confirmed theme still waiting for an unresolved shortcut save must
      // not be replaced; its baseline update would otherwise be lost.
      if (pendingTheme?.confirmed) return;
      if (!isCurrent(generation, scope) || !current?.desired || current.desired.theme_id === id) return;
      const base = structuredClone(current.desired), operation = crypto.randomUUID();
      pendingTheme = {operation, base};
      applyTheme(id);
      const result = await post("/api/desktop/settings", {operation_id:operation, expected_revision:base.revision,
        enabled:base.enabled, binding:structuredClone(base.binding), theme_id:id, reset:false});
      if (!isCurrent(generation, scope)) return;
      if (pendingTheme?.operation === operation) pendingTheme = null;
      commitWriteSnapshot(result, false);
      themeSaved(base, result.desired);
    } catch (error) {
      if (!isCurrent(generation, scope)) return;
      themeError = error;
      // A server answer means the save was not applied; otherwise the outcome
      // stays pending and later snapshots (refresh, reconcile, reopen) settle it.
      if (error?.status) pendingTheme = null;
      try { await refresh(); } catch { /* keep the error visible */ }
      if (!isCurrent(generation, scope)) return;
      applyTheme(current?.desired?.theme_id);
    } finally {
      if (busy === transaction) busy = undefined;
      if (isCurrent(generation, scope)) paintTheme();
    }
  }
  for (const radio of themeRadios) listen(radio, "change", () => { if (radio.checked) void saveTheme(radio.value); });
  listen(form, "submit", event => { event.preventDefault(); void run(() => save()); });
  listen(document.querySelector("#desktop-reset"), "click", () => run(() => { pending = null; return save(true); }));
  listen(document.querySelector("#desktop-check"), "click", () => run(async () => {
    const generation = pageGeneration, scope = session();
    await endRecording();
    if (!isCurrent(generation, scope)) return;
    await post("/api/desktop/check", {binding:binding()});
    if (!isCurrent(generation, scope)) return;
    stateOverride = {kind:"check_available"}; paintState();
  }));
  listen(document.querySelector("#desktop-retry"), "click", () => run(async () => {
    const generation = pageGeneration, scope = session();
    await endRecording();
    if (!isCurrent(generation, scope)) return;
    const value = await post("/api/desktop/retry");
    if (isCurrent(generation, scope)) commitWriteSnapshot(value);
  }));
  return {
    open:() => run(async () => {
      const generation = pageGeneration, scope = session();
      await endRecording();
      if (!isCurrent(generation, scope)) return;
      const fetched = await fetchSettings({allowError:true, generation, scope});
      if (!isCurrent(generation, scope)) return;
      const value = fetched.stale ? current : fetched.value;
      // A resumed page reconciles the unknown save instead of generating a new ID.
      if (pending && (value.last_operation === pending.operation_id || value.desired.revision !== pending.expected_revision)) pending = null;
      if (!fetched.stale) inspectLocaleSnapshot(value);
      render(value, !pending);
      paintSaveButton();
    }),
    setLocale:setLocalePreference,
    reconcileLocale,
    relocalize,
    status:() => current,
    pendingLocale:() => pendingLocale,
    stop:() => { pageGeneration++; localeGeneration++; latestAppliedClock = ++requestClock; activeSave = null; void endRecording(); },
  };
}
