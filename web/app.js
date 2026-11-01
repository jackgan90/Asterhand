import {apiClient} from "./api-client.mjs";
import {appLifecycle} from "./app-lifecycle.mjs";
import {browserActions} from "./browser-actions.mjs";
import {$, noticeView, applyTheme, restoreTheme, settingsTabs} from "./ui.mjs";
import {desktopSettings} from "./desktop-settings.mjs";
import {nodeController} from "./node-controller.mjs";
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
  let lifecycle, tasks;
  const session = () => lifecycle?.session();
  const notice = notices.show;
  async function guarded(fn) {
    const scope = session();
    if (!scope?.active) return;
    try { await fn(); } catch (error) { if (scope.active) notice(error.message); }
  }
  const desktop = desktopSettings(client.api, client.post, {listen, session});
  const commands = localCommands({...client, listen, session, guarded, actions});
  let commandTarget = new URLSearchParams(location.hash.slice(1)).get('commands');
  const nodes = nodeController({...client, session, guarded, notice, actions, listen, isSubmitting:() => tasks?.submitting() || false});
  const settings = nodeSettings({...client, session, desktop, guarded, notice, listen, refreshPeers:nodes.refresh});
  const view = taskView({guarded, submissionChanged:nodes.updateSubmission, callbacks:{
    answer:(...args) => tasks.answer(...args), cancel:(...args) => tasks.cancel(...args),
    resolve:job => tasks.resolve(job), detail:(...args) => tasks.detail(...args), refresh:() => tasks.refresh(),
  }});
  tasks = taskController({...client, view, selection:nodes.selection, session, notice, actions});
  settingsTabs($("#settings"), listen);
  const connection = online => {
    const {info} = nodes.selection();
    $("#connection").textContent = online ? info.name : `${info.name} · 连接中断`;
    $("#connection").dataset.state = online ? "online" : "offline";
  };
  async function initialize(scope, token) {
    try {
      await client.authenticate(token, () => scope.active);
      if (!scope.active) return;
      if (token && $("#login-form").elements.token.value === token) $("#login-form").reset();
      $("#login").hidden = true;
      $("#workspace").hidden = false;
      // Theme is cosmetic; a failed read keeps the last applied theme.
      client.api("/api/desktop/settings").then(value => { if (scope.active) applyTheme(value?.desired?.theme_id); }, () => {});
      await nodes.refresh();
      if (!scope.active) return;
      connection(true);
      $("#version").textContent = `v${nodes.selection().info.version}`;
      await tasks.refresh(scope);
      if (!scope.active) return;
      await settings.resume();
      if (scope.active && commandTarget !== null) {
        const id = commandTarget; commandTarget = null;
        try { await commands.open(id); } catch (error) { notice(error.message); }
      }
    } catch (error) {
      if (scope.active) {
        $("#login").hidden = false;
        $("#workspace").hidden = true;
        $("#connection").textContent = "未登录";
        $("#connection").dataset.state = "offline";
      }
      throw error;
    }
  }
  lifecycle = appLifecycle({client, initialize, refreshPeers:nodes.refresh, refreshTasks:tasks.refresh, connection,
    stopped:() => { settings.stop(); desktop.stop(); commands.stop(); notices.stop(); },
    failed:error => { if (!error.message.includes("登录链接")) notice(error.message); }});
  for (const element of document.querySelectorAll("[data-close]"))
    listen(element, "click", () => document.getElementById(element.dataset.close).close());
  listen($("#login-form"), "submit", event => {
    event.preventDefault();
    const token = new FormData(event.target).get("token");
    void lifecycle.start(token);
  });
  listen($("#task-form"), "submit", event => { event.preventDefault(); void guarded(() => tasks.submit(nodes.content())); });
  listen($("#stop-node"), "click", () => {
    if (actions.confirm("停止本机服务？\n\n执行中的任务会停止，下次启动时自动核对；尚未发出的消息将丢弃。其他设备已接收的任务不受影响。"))
      void guarded(async () => {
        const scope = session();
        await client.post("/api/stop");
        if (scope.active) notice("服务正在停止，可从启动入口重新打开。");
      });
  });
  listen(window, "pagehide", lifecycle.stop);
  listen(window, "pageshow", event => { if (event.persisted && !session()?.active) void lifecycle.start(); });
  const token = new URLSearchParams(location.hash.slice(1)).get("login");
  if (token) history.replaceState(null, "", location.pathname);
  const ready = lifecycle.start(token);
  return {ready, start:lifecycle.start, stop:lifecycle.stop,
    dispose:() => { lifecycle.stop(); for (const remove of listeners) remove(); }};
}

export const app = mountApp();
