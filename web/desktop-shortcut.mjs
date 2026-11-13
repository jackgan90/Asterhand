import {localizeError, localizeMessage, t} from "./i18n.mjs";
import {$} from "./ui.mjs";

export function desktopShortcut(api, post, {
  listen = (target, event, callback) => target.addEventListener(event, callback),
  session = () => ({active: true}),
} = {}) {
  let current = null;
  let inflight = false;
  let pending = null;
  let errorState = null;
  let generation = 0;

  function paint() {
    const field = $("#desktop-shortcut-field");
    const input = $("#desktop-shortcut");
    const status = $("#desktop-shortcut-status");
    if (!field || !input || !status) return;
    if (!current || !current.supported) {
      field.hidden = true;
      input.disabled = true;
      return;
    }
    field.hidden = false;
    if (!current.available) {
      input.checked = false;
      input.disabled = true;
      status.textContent = localizeMessage(
        current.message_key,
        current.message_args,
        current.message || t("status.desktop.shortcut_not_installed"),
      );
      delete status.dataset.tone;
      return;
    }
    input.disabled = inflight;
    if (!inflight) {
      input.checked = Boolean(current.enabled);
    }
    if (errorState) {
      status.textContent = localizeError(errorState);
      status.dataset.tone = "error";
      return;
    }
    if (current.conflict) {
      status.textContent = localizeMessage(
        current.message_key,
        current.message_args,
        current.message || t("err.launch.shortcut_conflict"),
      );
      status.dataset.tone = "error";
      return;
    }
    status.textContent = t("web.desktop.shortcut_hint");
    delete status.dataset.tone;
  }

  function applySnapshot(snapshot) {
    current = snapshot;
    if (pending && snapshot?.last_operation && snapshot.last_operation === pending.id) {
      pending = null;
      errorState = null;
    } else if (pending && snapshot?.last_operation && snapshot.last_operation !== pending.id) {
      pending = null;
      errorState = null;
    }
    paint();
  }

  async function open() {
    const scope = session();
    const gen = ++generation;
    try {
      const snapshot = await api("/api/desktop/shortcut");
      if (!scope?.active || gen !== generation) return;
      errorState = null;
      applySnapshot(snapshot);
    } catch (error) {
      if (!scope?.active || gen !== generation) return;
      errorState = error;
      paint();
    }
  }

  const toggle = $("#desktop-shortcut");
  if (toggle?.addEventListener) {
    listen(toggle, "change", async () => {
      if (inflight || !current?.supported || !current?.available) {
        paint();
        return;
      }
      const desired = Boolean(toggle.checked);
      if (!pending || pending.enabled !== desired) {
        pending = {id: crypto.randomUUID(), enabled: desired};
      }
      inflight = true;
      errorState = null;
      paint();
      const scope = session();
      try {
        const snapshot = await post("/api/desktop/shortcut", {
          operation_id: pending.id,
          enabled: desired,
        });
        pending = null;
        inflight = false;
        if (scope?.active) applySnapshot(snapshot);
      } catch (error) {
        inflight = false;
        if (error?.status) {
          pending = null;
          if (error.details?.shortcut) current = error.details.shortcut;
        }
        errorState = error;
        if (scope?.active) paint();
      }
    });
  }

  function relocalize() {
    paint();
  }

  function stop() {
    generation++;
    inflight = false;
  }

  const settingsDialog = $("#settings");
  if (settingsDialog?.addEventListener) {
    listen(settingsDialog, "close", stop);
  }

  return {open, relocalize, stop};
}
