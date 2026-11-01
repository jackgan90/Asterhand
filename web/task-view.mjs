import {$, node, date, dialog} from "./ui.mjs";
const states = {
  queued: "排队中",
  running: "执行中",
  waiting_input: "等你回答",
  cancel_requested: "正在取消",
  cancelled: "已停止",
  succeeded: "已完成",
  completed_unverified: "已完成 · 待确认",
  failed: "失败",
  rejected: "对方未接收",
  reconciliation_required: "状态待确认",
};
const tones = {
  queued: "wait", running: "run", waiting_input: "wait", cancel_requested: "wait", cancelled: "muted",
  succeeded: "ok", completed_unverified: "ok", failed: "error", rejected: "error", reconciliation_required: "error",
};
const outcomes = {
  not_started: "未开始", not_run: "未运行", running: "进行中", succeeded: "成功", failed: "失败", cancelled: "已停止",
  completed_unverified: "完成待确认", unknown_manually_closed: "人工核对后关闭",
  passed: "已验证", not_applicable: "无需验证", pending: "待确认",
};
const size = bytes => bytes < 1024 ? `${bytes} B` : bytes < 1048576 ? `${(bytes / 1024).toFixed(1)} KB` : `${(bytes / 1048576).toFixed(1)} MB`;
const time = value => value ? new Date(value).toLocaleString([], {month:"numeric", day:"numeric", hour:"2-digit", minute:"2-digit"}) : "";

export function taskView({guarded, callbacks, submissionChanged}) {
  let editing = false, context, eventsRoot;
  function render(views, selection) {
    context = selection;
    const root = $("#tasks");
    root.replaceChildren();
    for (const view of views) root.append(taskCard(view.job, view.out, view.goal));
    if (!root.children.length) root.append(node("div", "还没有任务，在上方描述你想完成的事", "empty"));
    $("#last-refresh").textContent = "更新于 " + new Date().toLocaleTimeString([], {hour:"2-digit", minute:"2-digit"});
  }
  function badge(job, out) {
    const rejected = out?.state === "rejected", lost = !rejected && out?.error;
    const element = node("span", rejected ? states.rejected : lost ? "连接中断" : states[job.state] || job.state,
      `badge ${["failed", "rejected", "reconciliation_required"].includes(job.state) ? "error" : ""}`);
    element.dataset.tone = lost ? "wait" : tones[rejected ? "rejected" : job.state] || "muted";
    return element;
  }
  function taskCard(job, out, goal = job.request.goal) {
    const card = node("article", null, "task"),
      head = node("div", null, "task-head");
    head.append(node("div", goal, "task-title"), badge(job, out));
    card.append(head);
    const owner =
      job.owner === context.info.node_id
        ? context.info.name
        : context.peers.find((p) => p.id === job.owner)?.name || job.owner;
    const updated = out?.updated || job.updated;
    card.append(node("div", updated ? `${owner} · ${time(updated)}` : owner, "metadata"));
    if (out && !out.job)
      card.append(node("p", out.state === "rejected" ? "对方设备未接收此任务，已停止发送。请检查该设备的设置后重新发送。" : "正在送达…", "result"));
    if (
      out?.cancel && out.state !== "rejected" &&
      !["succeeded", "failed", "cancelled", "completed_unverified"].includes(
        job.state,
      )
    )
      card.append(node("p", "正在取消，等待对方设备确认。", "result"));
    if (job.result) card.append(node("div", job.result, "result"));
    if (job.error || out?.error)
      card.append(node("div", job.error || out.error, "error-text"));
    for (const a of job.artifacts || []) {
      const block = node("div", null, "artifact"), meta = node("small", size(a.size));
      meta.title = `SHA-256 ${a.sha256}`;
      block.append(node("strong", "已生成文件"), node("div", a.path), node("pre", a.preview), meta);
      card.append(block);
    }
    if (job.interaction?.state === "pending" && job.state === "waiting_input")
      card.append(question(job, out));
    const actions = node("div", null, "task-actions");
    const detail = node("button", "详情", "quiet");
    detail.addEventListener("click", () => guarded(() => callbacks.detail(job, out)));
    actions.append(detail);
    if (
      ![
        "succeeded",
        "failed",
        "rejected",
        "cancelled",
        "completed_unverified",
        "reconciliation_required",
      ].includes(job.state)
    ) {
      const cancel = node("button", "取消任务", "quiet");
      cancel.addEventListener("click", () =>
        guarded(async () => {
          await callbacks.cancel(job, out);
        }),
      );
      actions.append(cancel);
    }
    if (job.state === "reconciliation_required" && !out) {
      const resolve = node("button", "核对后关闭", "secondary");
      resolve.addEventListener("click", () => guarded(() => callbacks.resolve(job)));
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
    input.addEventListener("focus", () => { editing = true; });
    input.addEventListener("blur", () => { editing = false; });
    input.required = true;
    input.maxLength = 4000;
    input.setAttribute("aria-label", "任务问题的回答");
    form.append(input, button);
    form.addEventListener("submit", (event) => {
      event.preventDefault();
      guarded(async () => {
        button.disabled = true;
        try {
          if (await callbacks.answer(job, out, input.value)) {
            input.blur();
            await callbacks.refresh();
          }
        } finally { button.disabled = false; }
      });
    });
    root.append(form);
    return root;
  }

  // Readable outcome first; identifiers and raw events stay in a disclosure.
  function detail(job) {
    const root = $("#detail-content"), summary = node("div", null, "detail-summary");
    const item = (label, value) => {
      const cell = node("div");
      cell.append(node("span", label), node("strong", value));
      return cell;
    };
    summary.append(
      item("状态", states[job.state] || job.state),
      item("执行", outcomes[job.execution_outcome || "not_run"] || job.execution_outcome),
      item("验证", outcomes[job.verification_outcome || "not_run"] || job.verification_outcome),
      item("你的确认", outcomes[job.user_acceptance || "pending"] || job.user_acceptance),
    );
    const technical = node("details", null, "advanced");
    technical.append(
      node("summary", "技术记录"),
      node("div", `任务 ID ${job.id} · 第 ${job.attempt || 0} 次尝试 · 创建于 ${date(job.created)}`, "metadata"),
    );
    eventsRoot = node("div");
    technical.append(eventsRoot);
    root.replaceChildren(node("div", job.request?.goal || "", "task-title"), summary, technical);
    dialog("#detail");
  }
  function events(events) {
    for (const event of events) {
      const row = node("div", null, "event");
      row.append(
        node("strong", `#${event.seq} ${event.type} · ${date(event.time)}`),
        node("pre", JSON.stringify(event.data, null, 2)),
      );
      eventsRoot?.append(row);
    }
  }
  return {render, detail, events, detailOpen:() => $("#detail").open, editing:() => editing, submissionChanged,
    submitted:goal => { if ($("#goal").value.trim() === goal) $("#goal").value = ""; }};
}
