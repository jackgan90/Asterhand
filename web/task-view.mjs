import {$, node, date, dialog} from "./ui.mjs";
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

export function taskView({guarded, callbacks, submissionChanged}) {
  let editing = false, context;
  function render(views, selection) {
    context = selection;
    const root = $("#tasks");
    root.replaceChildren();
    for (const view of views) root.append(taskCard(view.job, view.out, view.goal));
    if (!root.children.length) root.append(node("div", "还没有任务。从创建一个 hello-world.txt 开始。", "empty"));
    $("#last-refresh").textContent = "已同步 " + new Date().toLocaleTimeString();
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
      job.owner === context.info.node_id
        ? context.info.name
        : context.peers.find((p) => p.id === job.owner)?.name || job.owner;
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
    detail.addEventListener("click", () => guarded(() => callbacks.detail(job, out)));
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

  function detail(job) {
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

  }
  function events(events) {
    const root = $("#detail-content"), data = {events};
    for (const event of data.events) {
      const row = node("div", null, "event");
      row.append(
        node("strong", `#${event.seq} ${event.type} · ${date(event.time)}`),
        node("pre", JSON.stringify(event.data, null, 2)),
      );
      root.append(row);
    }

  }
  return {render, detail, events, detailOpen:() => $("#detail").open, editing:() => editing, submissionChanged,
    submitted:goal => { if ($("#goal").value.trim() === goal) $("#goal").value = ""; }};
}
