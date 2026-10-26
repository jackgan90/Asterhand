import {apiClient} from "./api-client.mjs";
import {appLifecycle} from "./app-lifecycle.mjs";
import {browserActions} from "./browser-actions.mjs";
import {$, noticeView} from "./ui.mjs";
import {desktopSettings} from "./desktop-settings.mjs";
import {nodeController} from "./node-controller.mjs";
import {nodeSettings} from "./node-settings.mjs";
import {taskController} from "./task-controller.mjs";
import {taskView} from "./task-view.mjs";

export function mountApp() {
  const client = apiClient(), actions = browserActions(), notices = noticeView($("#notice"));
  const listeners = [];
  const listen = (target, event, callback) => {
    target.addEventListener(event, callback);
    listeners.push(() => target.removeEventListener(event, callback));
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
  const nodes = nodeController({...client, session, guarded, notice, actions, listen, isSubmitting:() => tasks?.submitting() || false});
  const settings = nodeSettings({...client, session, desktop, guarded, notice, listen, refreshPeers:nodes.refresh});
  const view = taskView({guarded, submissionChanged:nodes.updateSubmission, callbacks:{
    answer:(...args) => tasks.answer(...args), cancel:(...args) => tasks.cancel(...args),
    resolve:job => tasks.resolve(job), detail:(...args) => tasks.detail(...args), refresh:() => tasks.refresh(),
  }});
  tasks = taskController({...client, view, selection:nodes.selection, session, notice, actions});
  const connection = online => {
    const {info} = nodes.selection();
    $("#connection").textContent = online ? `当前连接：${info.name}` : `连接中断 · 最后连接 ${info.name}`;
  };
  async function initialize(scope, token) {
    try {
      await client.authenticate(token, () => scope.active);
      if (!scope.active) return;
      if (token && $("#login-form").elements.token.value === token) $("#login-form").reset();
      $("#login").hidden = true;
      $("#workspace").hidden = false;
      await nodes.refresh();
      if (!scope.active) return;
      connection(true);
      $("#version").textContent = `v${nodes.selection().info.version}`;
      await tasks.refresh(scope);
      if (!scope.active) return;
      if (!nodes.selection().info.ready) await settings.open();
      else await settings.resume();
    } catch (error) {
      if (scope.active) {
        $("#login").hidden = false;
        $("#workspace").hidden = true;
        $("#connection").textContent = "等待本机登录";
      }
      throw error;
    }
  }
  lifecycle = appLifecycle({client, initialize, refreshPeers:nodes.refresh, refreshTasks:tasks.refresh, connection,
    stopped:() => { settings.stop(); desktop.stop(); notices.stop(); },
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
    if (actions.confirm("停止本机节点？执行中的任务将停止并在下次启动时核对；其他节点已接收的任务继续运行。"))
      void guarded(async () => {
        const scope = session();
        await client.post("/api/stop");
        if (scope.active) notice("节点正在停止。下次使用启动入口重新打开。");
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
