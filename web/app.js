import {apiClient} from "./api-client.mjs";
import {appLifecycle} from "./app-lifecycle.mjs";
import {browserActions} from "./browser-actions.mjs";
import {t} from "./i18n.mjs";
import {localeController} from "./locale-controller.mjs";
import {$, localizeError, noticeView, applyTheme, restoreTheme, settingsTabs} from "./ui.mjs";
import {desktopSettings} from "./desktop-settings.mjs";
import {displayNodeName, nodeController} from "./node-controller.mjs";
import {nodeSettings} from "./node-settings.mjs";
import {taskController} from "./task-controller.mjs";
import {taskView} from "./task-view.mjs";
import {localCommands} from "./local-commands.mjs";

export function mountApp() {
  restoreTheme();
  const client = apiClient(), actions = browserActions(), notices = noticeView($("#notice"));
  const listeners = [];
  const listen = (target, event, callback, options) => {
    target.addEventListener(event, callback, options);
    listeners.push(() => target.removeEventListener(event, callback, options));
  };
  let lifecycle, tasks, onlineState = null;
  const session = () => lifecycle?.session();
  const notice = notices.show;
  async function guarded(fn) {
    const scope = session();
    if (!scope?.active) return;
    try { await fn(); } catch (error) { if (scope.active) notice(localizeError(error)); }
  }
  const desktop = desktopSettings(client.api, client.post, {listen, session});
  const commands = localCommands({...client, listen, session, guarded, actions});
  let commandTarget = new URLSearchParams(location.hash.slice(1)).get("commands");
  const nodes = nodeController({...client, session, guarded, notice, actions, listen, isSubmitting:() => tasks?.submitting() || false});
  const settings = nodeSettings({...client, session, desktop, guarded, notice, listen, refreshPeers:nodes.refresh});
  const view = taskView({guarded, submissionChanged:nodes.updateSubmission, callbacks:{
    answer:(...args) => tasks.answer(...args), cancel:(...args) => tasks.cancel(...args),
    resolve:job => tasks.resolve(job), detail:(...args) => tasks.detail(...args), refresh:() => tasks.refresh(),
  }});
  tasks = taskController({...client, view, selection:nodes.selection, session, notice, actions});
  settingsTabs($("#settings"), listen);
  const paintConnection = () => {
    if (onlineState === "unauthenticated") {
      $("#connection").textContent = t("web.topbar.not_logged_in");
      $("#connection").dataset.state = "offline";
      return;
    }
    if (onlineState === null) {
      $("#connection").textContent = t("web.topbar.connecting");
      return;
    }
    const {info} = nodes.selection();
    const label = displayNodeName(info, info?.name || "");
    $("#connection").textContent = onlineState ? label : t("web.topbar.node_offline", {name: label});
    $("#connection").dataset.state = onlineState ? "online" : "offline";
  };
  const connection = online => {
    onlineState = Boolean(online);
    paintConnection();
  };
  const locale = localeController({
    desktop,
    listen,
    notice,
    onRelocalize: () => {
      paintConnection();
      nodes.relocalize();
      view.relocalize();
      desktop.relocalize();
      settings.relocalize();
      commands.relocalize();
    },
  });
  async function syncDesktopStream(scope) {
    const rec = await desktop.reconcileLocale();
    if (scope?.active && rec?.outcome !== "stale" && rec?.status) {
      applyTheme(rec.status?.desired?.theme_id);
      locale.syncDesktopStatus(rec.status);
    }
  }
  async function initialize(scope, token) {
    try {
      await client.authenticate(token, () => scope.active);
      if (!scope.active) return;
      locale.markAuthenticated();
      if (token && $("#login-form").elements.token.value === token) $("#login-form").reset();
      $("#login").hidden = true;
      $("#workspace").hidden = false;
      // Theme and locale sync without blocking main task/peer rendering if unavailable.
      desktop.reconcileLocale().then(rec => {
        if (scope.active && rec?.outcome !== "stale" && rec?.status) {
          applyTheme(rec.status?.desired?.theme_id);
          locale.syncDesktopStatus(rec.status);
        }
      }, () => {});
      await nodes.refresh();
      if (!scope.active) return;
      connection(true);
      $("#version").textContent = `v${nodes.selection().info.version}`;
      await tasks.refresh(scope);
      if (!scope.active) return;
      await settings.resume();
      if (scope.active && commandTarget !== null) {
        const id = commandTarget; commandTarget = null;
        try { await commands.open(id); } catch (error) { notice(localizeError(error)); }
      }
    } catch (error) {
      if (scope.active) {
        $("#login").hidden = false;
        $("#workspace").hidden = true;
        onlineState = "unauthenticated";
        paintConnection();
        void locale.calibrateUnauthenticated(() => scope.active);
      }
      throw error;
    }
  }
  lifecycle = appLifecycle({
    client,
    initialize,
    refreshPeers:nodes.refresh,
    refreshTasks:async scope => {
      void syncDesktopStream(scope);
      await tasks.refresh(scope);
    },
    connection,
    stopped:() => { settings.stop(); desktop.stop(); commands.stop(); notices.stop(); },
    failed:error => {
      if (error?.messageKey === "err.http.login_link_required") return;
      notice(localizeError(error));
    },
  });
  for (const element of document.querySelectorAll("[data-close]"))
    listen(element, "click", () => document.getElementById(element.dataset.close).close());
  listen($("#login-form"), "submit", event => {
    event.preventDefault();
    const token = new FormData(event.target).get("token");
    void lifecycle.start(token);
  });
  listen($("#task-form"), "submit", event => { event.preventDefault(); void guarded(() => tasks.submit(nodes.content())); });
  listen($("#stop-node"), "click", () => {
    if (actions.confirm(t("web.settings.network.stop_confirm")))
      void guarded(async () => {
        const scope = session();
        await client.post("/api/stop");
        if (scope.active) notice(t("web.settings.network.stopping_notice"));
      });
  });
  listen(window, "pagehide", lifecycle.stop);
  listen(window, "pageshow", event => { if (event.persisted && !session()?.active) void lifecycle.start(); });
  const token = new URLSearchParams(location.hash.slice(1)).get("login");
  if (token) history.replaceState(null, "", location.pathname);
  const ready = lifecycle.start(token);
  return {
    ready,
    start:lifecycle.start,
    stop:lifecycle.stop,
    dispose:() => { lifecycle.stop(); locale.dispose(); for (const remove of listeners) remove(); },
  };
}

export const app = mountApp();
