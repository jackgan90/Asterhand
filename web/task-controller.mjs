import {taskViews} from "./view-model.mjs";

export function taskController({api, post, view, selection, session, notice, actions, storage = sessionStorage, uuid = () => crypto.randomUUID()}) {
  let snapshot = {jobs:[], outbox:[]}, refreshing, submitting = false;
  const answerIDs = new Map();
  async function refresh(scope = session()) {
    if (!scope?.active || refreshing === scope || view.editing()) return;
    refreshing = scope;
    try {
      const [jobs, outbox] = await Promise.all([api("/api/jobs"), api("/api/outbox")]);
      if (!scope.active || view.editing()) return;
      snapshot = {jobs, outbox};
      view.render(taskViews(snapshot.jobs, snapshot.outbox, selection().info.node_id), selection());
    } finally { if (refreshing === scope) refreshing = undefined; }
  }
  async function submit(content) {
    if (submitting || !content.goal.trim()) return;
    const scope = session();
    const pendingKey = "asterhand-pending";
    let request;
    try { request = JSON.parse(storage.getItem(pendingKey)); } catch { /* absent */ }
    if (!request || request.goal !== content.goal || request.target !== content.target || request.project_id !== content.project_id)
      request = {...content, request_id:uuid(), start_before:new Date(Date.now()+3600000).toISOString(), mode:"execute"};
    storage.setItem(pendingKey, JSON.stringify(request));
    submitting = true;
    view.submissionChanged();
    try {
      await post("/api/jobs", request);
      // A confirmed write may finish after pagehide. Keep its durable outcome,
      // while protecting a newer draft/session from an old UI completion.
      if (storage.getItem(pendingKey) === JSON.stringify(request)) storage.removeItem(pendingKey);
      if (!scope?.active) return;
      view.submitted(content.goal);
      await refresh(scope);
    } finally { submitting = false; if (session()?.active) view.submissionChanged(); }
  }
  async function answer(job, out, text) {
    const scope = session(), interaction = job.interaction;
    const key = `${interaction.id}:${text}`;
    if (!answerIDs.has(key)) answerIDs.set(key, uuid());
    await post(out ? `/api/remote/${out.id}/answer` : `/api/jobs/${job.id}/answer`, {
      interaction_id:interaction.id, attempt:interaction.attempt, answer_id:answerIDs.get(key), answer:text,
    });
    if (!scope?.active) return false;
    notice("回答已保存，等待执行节点应用");
    return true;
  }
  async function cancel(job, out) {
    const scope = session();
    await post(out ? `/api/remote/${out.id}/cancel` : `/api/jobs/${job.id}/cancel`);
    if (scope?.active) await refresh(scope);
  }
  async function resolve(job) {
    const reason = actions.prompt("请先检查实际文件和运行记录。填写核对证据及关闭理由（不会将任务记为成功）：");
    if (!reason) return;
    const scope = session();
    await post(`/api/jobs/${job.id}/resolve`, {reason});
    if (scope?.active) await refresh(scope);
  }
  let detailGeneration = 0;
  async function detail(job, out) {
    const scope = session(), generation = ++detailGeneration;
    view.detail(job);
    if (out && !out.job) return;
    let after = 0;
    for (let page = 0; page < 21; page++) {
      const data = await api(out ? `/api/remote/${out.id}/events?after=${after}` : `/api/jobs/${job.id}/events?after=${after}`);
      if (!scope?.active || generation !== detailGeneration || !view.detailOpen()) return;
      view.events(data.events);
      for (const event of data.events) after = event.seq;
      if (!data.events.length || after >= data.watermark) break;
    }
  }
  return {refresh, submit, answer, cancel, resolve, detail, submitting:() => submitting};
}
