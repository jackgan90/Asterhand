import { taskViews, submissionState } from "./view-model.mjs";
import { desktopSettings } from "./desktop-settings.mjs";

const $ = (s, root = document) => root.querySelector(s);
const states = {
  queued: "已接收 · 排队中",
  running: "执行中",
  waiting_input: "等待你的回答",
  cancel_requested: "已登记取消",
  cancelled: "已停止",
  succeeded: "完成并验证",
  completed_unverified: "执行完成 · 待验收",
  failed: "执行失败",
  reconciliation_required: "需要核对",
};
let csrf = "",
  info = null,
  peers = [],
  cachedJobs = [],
  cachedOutbox = [],
  refreshing = false,
  submitting = false,
  pollTimer,
  explicitTarget = false;
const answerIDs = new Map();
function node(tag, text, className) {
  const el = document.createElement(tag);
  if (text != null) el.textContent = text;
  if (className) el.className = className;
  return el;
}
function notice(text) {
  const el = $("#notice");
  el.textContent = text;
  el.hidden = false;
  clearTimeout(pollTimer);
  pollTimer = setTimeout(() => (el.hidden = true), 7000);
}
async function api(path, options = {}) {
  const headers = {
    "Content-Type": "application/json",
    "X-Asterhand-CSRF": csrf,
    ...options.headers,
  };
  const response = await fetch(path, { ...options, headers });
  const data = await response.json();
  if (!response.ok) {
    const error = new Error(data.error || `请求失败 (${response.status})`);
    error.status = response.status;
    throw error;
  }
  return data;
}
const post = (path, data = {}) =>
  api(path, { method: "POST", body: JSON.stringify(data) });
const desktop = desktopSettings(api, post);
async function guarded(fn) {
  try {
    await fn();
  } catch (error) {
    notice(error.message);
  }
}
function date(value) {
  return value ? new Date(value).toLocaleString() : "未确认";
}
function dialog(id) {
  if (!$(id).open) $(id).showModal();
}
for (const el of document.querySelectorAll("[data-close]"))
  el.addEventListener("click", () =>
    document.getElementById(el.dataset.close).close(),
  );
$("#login-form").addEventListener("submit", (event) => {
  event.preventDefault();
  guarded(async () => {
    const result = await post("/api/login", {
      token: new FormData(event.target).get("token"),
    });
    csrf = result.csrf;
    event.target.reset();
    await start();
  });
});

async function start() {
  $("#login").hidden = true;
  $("#workspace").hidden = false;
  info = await api("/api/info");
  $("#connection").textContent = `当前连接：${info.name}`;
  $("#version").textContent = `v${info.version}`;
  await refreshPeers();
  await refreshTasks();
  const source = new EventSource("/api/stream");
  source.addEventListener("refresh", () => guarded(refreshTasks));
  source.onerror = () => {
    $("#connection").textContent = `连接中断 · 最后连接 ${info.name}`;
  };
  source.onopen = () => {
    $("#connection").textContent = `当前连接：${info.name}`;
  };
  setInterval(() => guarded(refreshPeers), 20000);
  if (!info.ready) await openSettings();
}
async function refreshPeers() {
  info = await api("/api/info");
  peers = await api("/api/peers");
  $("#node-warning").textContent = info.network_warning || "";
  $("#node-warning").hidden = !info.network_warning;
  const root = $("#nodes");
  root.replaceChildren();
  const all = [
    { id: info.node_id, name: info.name, online: true, info, local: true },
    ...peers.filter((p) => !p.revoked),
  ];
  for (const p of all) {
    const card = node("div", null, "node"),
      name = node("strong");
    name.append(
      node("span", null, `dot${p.online ? "" : " off"}`),
      document.createTextNode(p.name),
    );
    card.append(
      name,
      node(
        "small",
        `${p.local ? "本机 · " : ""}${p.online ? (p.info?.ready ? "助手就绪" : "待配置") : "暂不可达"}`,
      ),
    );
    if (p.error) card.append(node("small", p.error, "error-text"));
    if (!p.local) {
      const revoke = node("button", "撤销配对", "quiet");
      revoke.addEventListener("click", () => {
        if (confirm(`撤销与 ${p.name} 的配对？已经执行的任务不会自动取消。`))
          guarded(async () => {
            await api("/api/peers", {
              method: "DELETE",
              body: JSON.stringify({ id: p.id }),
            });
            await refreshPeers();
          });
      });
      card.append(revoke);
    }
    root.append(card);
  }
  const select = $("#target"),
    previous = select.value;
  select.replaceChildren();
  for (const p of all) {
    const option = node("option", `${p.name}${p.local ? "（本机）" : ""}`);
    option.value = p.id;
    select.append(option);
  }
  if (all.some((p) => p.id === previous)) select.value = previous;
  updateProject();
}
function updateProject() {
  const id = $("#target").value;
  const selected =
    id === info.node_id ? info : peers.find((p) => p.id === id)?.info;
  const select = $("#project"),
    previous = select.value;
  select.replaceChildren();
  for (const p of selected?.projects || []) {
    const option = node("option", p.name);
    option.value = p.id;
    select.append(option);
  }
  if ([...select.options].some((o) => o.value === previous))
    select.value = previous;
  const project = selected?.projects?.find((p) => p.id === select.value);
  $("#target-hint").textContent = project
    ? `保存到 ${selected.name}：${project.root}`
    : "节点离线或尚未配置授权目录";
  if (id !== info.node_id && project && !peers.find((p) => p.id === id)?.online)
    $("#target-hint").textContent +=
      "（暂不可达，按最后确认配置排队，恢复连接后送达）";
  updateSubmission();
}
function updateSubmission() {
  const id = $("#target").value;
  const selected = id === info.node_id ? info : peers.find((p) => p.id === id)?.info;
  const project = selected?.projects?.find((p) => p.id === $("#project").value);
  const state = submissionState(info, selected, project, $("#goal").value, submitting);
  $("#submit-task").disabled = state.disabled;
  $("#submit-task").setAttribute("aria-busy", String(state.busy));
  $("#submit-task").textContent = state.busy ? "正在提交…" : "发送任务 ↗";
  $("#submit-status").textContent = state.reason;
}
$("#goal").addEventListener("input", updateSubmission);
$("#target").addEventListener("change", () => {
  explicitTarget = true;
  updateProject();
});
$("#project").addEventListener("change", updateProject);
$("#task-form").addEventListener("submit", (event) => {
  event.preventDefault();
  guarded(async () => {
    if (submitting) return;
    const goal = $("#goal").value.trim();
    if (!goal) return;
    const pendingKey = "asterhand-pending";
    const content = {
      project_id: $("#project").value,
      goal,
      target: $("#target").value,
      target_explicit: explicitTarget,
    };
    let request;
    try {
      request = JSON.parse(sessionStorage.getItem(pendingKey));
    } catch {
      /* absent */
    }
    if (
      !request ||
      request.goal !== content.goal ||
      request.target !== content.target ||
      request.project_id !== content.project_id
    )
      request = {
        ...content,
        request_id: crypto.randomUUID(),
        start_before: new Date(Date.now() + 3600000).toISOString(),
        mode: "execute",
      };
    sessionStorage.setItem(pendingKey, JSON.stringify(request));
    submitting = true;
    updateSubmission();
    try {
      await post("/api/jobs", request);
      sessionStorage.removeItem(pendingKey);
      $("#goal").value = "";
      await refreshTasks();
    } finally {
      submitting = false;
      updateProject();
    }
  });
});
async function refreshTasks() {
  if (refreshing || document.querySelector(".question input:focus")) return;
  refreshing = true;
  try {
    [cachedJobs, cachedOutbox] = await Promise.all([
      api("/api/jobs"),
      api("/api/outbox"),
    ]);
    const root = $("#tasks");
    root.replaceChildren();
    for (const view of taskViews(cachedJobs, cachedOutbox, info.node_id))
      root.append(taskCard(view.job, view.out, view.goal));
    if (!root.children.length)
      root.append(
        node("div", "还没有任务。从创建一个 hello-world.txt 开始。", "empty"),
      );
    $("#last-refresh").textContent =
      `已同步 ${new Date().toLocaleTimeString()}`;
  } finally {
    refreshing = false;
  }
}
function taskCard(job, out, goal = job.request.goal) {
  const card = node("article", null, "task"),
    head = node("div", null, "task-head");
  head.append(
    node("div", goal, "task-title"),
    node(
      "span",
      out?.error ? "连接中断 · 保留最后状态" : states[job.state] || job.state,
      `badge ${["failed", "reconciliation_required"].includes(job.state) ? "error" : ""}`,
    ),
  );
  card.append(head);
  const owner =
    job.owner === info.node_id
      ? info.name
      : peers.find((p) => p.id === job.owner)?.name || job.owner;
  card.append(
    node(
      "div",
      `执行节点：${owner} · 最后确认：${date(out?.updated || job.updated)}`,
      "metadata",
    ),
  );
  if (out && !out.job)
    card.append(node("p", "送达待确认。重试保持原目标和请求 ID。", "result"));
  if (
    out?.cancel &&
    !["succeeded", "failed", "cancelled", "completed_unverified"].includes(
      job.state,
    )
  )
    card.append(node("p", "取消已登记，正在等待执行节点确认。", "result"));
  if (job.result) card.append(node("div", job.result, "result"));
  if (job.error || out?.error)
    card.append(node("div", job.error || out.error, "error-text"));
  for (const a of job.artifacts || []) {
    const block = node("div", null, "artifact");
    block.append(
      node("strong", "文件已创建并读取验证"),
      node("div", a.path),
      node("pre", a.preview),
      node("small", `${a.size} bytes · SHA-256 ${a.sha256}`),
    );
    card.append(block);
  }
  if (job.interaction?.state === "pending" && job.state === "waiting_input")
    card.append(question(job, out));
  const actions = node("div", null, "task-actions");
  const detail = node("button", "查看执行记录", "quiet");
  detail.addEventListener("click", () => guarded(() => showDetail(job, out)));
  actions.append(detail);
  if (
    ![
      "succeeded",
      "failed",
      "cancelled",
      "completed_unverified",
      "reconciliation_required",
    ].includes(job.state)
  ) {
    const cancel = node("button", "取消任务", "quiet");
    cancel.addEventListener("click", () =>
      guarded(async () => {
        await post(
          out ? `/api/remote/${out.id}/cancel` : `/api/jobs/${job.id}/cancel`,
        );
        await refreshTasks();
      }),
    );
    actions.append(cancel);
  }
  if (job.state === "reconciliation_required" && !out) {
    const resolve = node("button", "核对后关闭", "secondary");
    resolve.addEventListener("click", () => {
      const reason = prompt(
        "请先检查实际文件和运行记录。填写核对证据及关闭理由（不会将任务记为成功）：",
      );
      if (reason)
        guarded(async () => {
          await post(`/api/jobs/${job.id}/resolve`, { reason });
          await refreshTasks();
        });
    });
    actions.append(resolve);
  }
  card.append(actions);
  return card;
}
function question(job, out) {
  const root = node("div", null, "question");
  root.append(node("strong", job.interaction.question));
  const form = node("form"),
    input = node("input"),
    button = node("button", "发送回答");
  input.required = true;
  input.maxLength = 4000;
  input.setAttribute("aria-label", "任务问题的回答");
  form.append(input, button);
  form.addEventListener("submit", (event) => {
    event.preventDefault();
    guarded(async () => {
      const i = job.interaction;
      const key = `${i.id}:${input.value}`;
      if (!answerIDs.has(key)) answerIDs.set(key, crypto.randomUUID());
      button.disabled = true;
      try {
        await post(
          out ? `/api/remote/${out.id}/answer` : `/api/jobs/${job.id}/answer`,
          {
            interaction_id: i.id,
            attempt: i.attempt,
            answer_id: answerIDs.get(key),
            answer: input.value,
          },
        );
        input.blur();
        notice("回答已保存，等待执行节点应用");
        await refreshTasks();
      } finally {
        button.disabled = false;
      }
    });
  });
  root.append(form);
  return root;
}
async function showDetail(job, out) {
  const root = $("#detail-content");
  root.replaceChildren(
    node("div", `Job ${job.id}`, "metadata"),
    node("div", `Attempt ${job.attempt || "尚未开始"}`, "metadata"),
    node(
      "p",
      `执行：${job.execution_outcome || "not_run"}；验证：${job.verification_outcome || "not_run"}；用户验收：${job.user_acceptance || "pending"}`,
    ),
  );
  dialog("#detail");
  if (out && !out.job) return;
  let after = 0;
  for (let page = 0; page < 21; page++) {
    const data = await api(
      out
        ? `/api/remote/${out.id}/events?after=${after}`
        : `/api/jobs/${job.id}/events?after=${after}`,
    );
    for (const event of data.events) {
      const row = node("div", null, "event");
      row.append(
        node("strong", `#${event.seq} ${event.type} · ${date(event.time)}`),
        node("pre", JSON.stringify(event.data, null, 2)),
      );
      root.append(row);
      after = event.seq;
    }
    if (!data.events.length || after >= data.watermark) break;
  }
}
async function openSettings() {
  void desktop.open();
  const data = await api("/api/settings"),
    c = data.config,
    form = $("#settings-form");
  form.elements.name.value = c.name;
  form.elements.endpoint.value = c.endpoint || "";
  form.elements.peer_listen.value = c.peer_listen;
  $("#settings-status").textContent = "";
  const populate = (select, items, selected) => {
    select.replaceChildren();
    for (const item of items) {
      const option = node("option", item.name || item.id);
      option.value = item.id;
      select.append(option);
    }
    select.value = selected;
  };
  populate(form.elements.agent_runtime, data.runtimes, c.agent_runtime);
  const updateModels = (selected) => {
    const provider = data.providers.find(
      (p) => p.id === form.elements.provider.value,
    );
    populate(
      form.elements.model,
      (provider?.models || []).map((id) => ({ id })),
      selected || provider?.models[0],
    );
  };
  const updateProviders = () => {
    const runtime = data.runtimes.find(
      (r) => r.id === form.elements.agent_runtime.value,
    );
    populate(
      form.elements.provider,
      data.providers.filter((p) => runtime?.providers.includes(p.id)),
      c.llm.provider,
    );
    if (!form.elements.provider.value) form.elements.provider.selectedIndex = 0;
    const sameProvider = form.elements.provider.value === c.llm.provider;
    form.elements.credential_ref.value = sameProvider
      ? c.llm.credential_ref
      : form.elements.provider.value;
    updateModels(sameProvider ? c.llm.model : undefined);
  };
  form.elements.agent_runtime.onchange = updateProviders;
  form.elements.provider.onchange = () => {
    form.elements.credential_ref.value = form.elements.provider.value;
    updateModels();
  };
  updateProviders();
  form.elements.credential_ref.value = c.llm.credential_ref;
  form.elements.root.value = c.projects?.[0]?.root || "";
  form.elements.key.value = "";
  form.elements.delete_key.checked = false;
  $("#key-status").textContent = data.has_key
    ? "已保存本机 key，页面不会读回密钥。"
    : "尚未配置 key。";
  $("#runtime-status").textContent =
    `节点 ${c.node_id} · 本机数据 ${data.home}`;
  dialog("#settings");
  $("#settings").scrollTop = 0;
  await updateProbe();
  const network = await api("/api/network");
  $("#network-status").textContent =
    `${network.detail}${network.ipv4 ? " 地址：" + network.ipv4 : ""}`;
  $("#use-tailnet").hidden = !network.endpoint;
  $("#use-tailnet").onclick = () => {
    form.elements.endpoint.value = network.endpoint;
    form.elements.peer_listen.value = network.listen;
    $("#settings-status").textContent = "已填写 Tailscale 连接配置。保存后按提示重启节点，使监听地址生效。";
  };
}
$("#settings-open").addEventListener("click", () => guarded(openSettings));
$("#settings-form").addEventListener("submit", (event) => {
  event.preventDefault();
  guarded(async () => {
    const f = new FormData(event.target);
    const result = await post("/api/settings", {
      name: f.get("name"),
      endpoint: f.get("endpoint").trim(),
      peer_listen: f.get("peer_listen").trim(),
      agent_runtime: f.get("agent_runtime"),
      llm: {
        provider: f.get("provider"),
        model: f.get("model"),
        credential_ref: f.get("credential_ref"),
      },
      root: f.get("root").trim(),
      key: f.get("key"),
      delete_key: f.get("delete_key") === "on",
    });
    event.target.elements.key.value = "";
    const message = ["设置已保存。", result.ready ? "模型验证保持有效。" : "请验证模型与工具。", result.restart_required ? "监听地址已修改：请停止本机节点，再运行 start.ps1。" : ""].join(" ");
    $("#settings-status").textContent = message;
    notice(message);
    await updateProbe();
    await refreshPeers();
  });
});
async function updateProbe() {
  const result = await api("/api/probe");
  $("#probe-status").textContent =
    {
      not_run: "尚未运行检查",
      running: "正在调用所选模型并检查受限工具…",
      passed: "模型与工具检查通过。节点已就绪。",
      failed: "检查失败：",
    }[result.state] + (result.error || "");
  $("#probe").disabled = result.state === "running";
  if (result.state === "running") setTimeout(() => guarded(updateProbe), 1500);
  if (result.state === "passed") await refreshPeers();
}
$("#probe").addEventListener("click", () =>
  guarded(async () => {
    await post("/api/probe");
    await updateProbe();
  }),
);
$("#pair-open").addEventListener("click", () => dialog("#pair"));
$("#make-invite").addEventListener("click", () =>
  guarded(async () => {
    $("#invite").value = (await post("/api/invite")).code;
  }),
);
$("#copy-invite").addEventListener("click", () =>
  guarded(async () => {
    await navigator.clipboard.writeText($("#invite").value);
    notice("已复制，10 分钟内在另一台设备粘贴。");
  }),
);
$("#pair-form").addEventListener("submit", (event) => {
  event.preventDefault();
  guarded(async () => {
    await post("/api/pair", { code: new FormData(event.target).get("code") });
    event.target.reset();
    $("#pair").close();
    notice("节点配对成功，TLS 身份已验证。");
    await refreshPeers();
  });
});
$("#stop-node").addEventListener("click", () => {
  if (
    confirm(
      "停止本机节点？执行中的任务将停止并在下次启动时核对；其他节点已接收的任务继续运行。",
    )
  )
    guarded(async () => {
      await post("/api/stop");
      notice("节点正在停止。下次使用启动入口重新打开。");
    });
});

try {
  const hash = new URLSearchParams(location.hash.slice(1)),
    token = hash.get("login");
  if (token) {
    history.replaceState(null, "", location.pathname);
    csrf = (await post("/api/login", { token })).csrf;
  } else csrf = (await api("/api/session")).csrf;
  await start();
} catch (error) {
  $("#login").hidden = false;
  $("#workspace").hidden = true;
  $("#connection").textContent = "等待本机登录";
  if (!error.message.includes("登录链接")) notice(error.message);
}
