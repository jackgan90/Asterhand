export function taskViews(jobs, outbox, localID) {
  const byID = new Map(jobs.map((job) => [job.id, job]));
  const represented = new Set(outbox.map((out) => out.id));
  const views = outbox.map((out) => {
    const route = byID.get(out.id);
    if (out.job && out.peer_id === localID) represented.add(out.job.id);
    const job = (out.peer_id === localID && out.job && byID.get(out.job.id)) || out.job || {
      id: out.id,
      state: "queued",
      request: out.request,
      owner: out.peer_id,
      created: route?.created || out.updated,
      updated: out.updated,
    };
    return {
      id: out.id,
      created: route?.created || job.created || out.updated,
      goal: route?.request.goal || out.request.goal,
      job,
      out: out.peer_id === localID && out.job ? null : out,
    };
  });
  for (const job of jobs) {
    if (!represented.has(job.id))
      views.push({ id: job.id, created: job.created, goal: job.request.goal, job, out: null });
  }
  const time = (value) => Date.parse(value) || 0;
  return views.sort((a, b) => time(b.created) - time(a.created) || a.id.localeCompare(b.id));
}

export function submissionState(local, target, project, goal, submitting) {
  if (submitting) return { disabled: true, busy: true, reason: "正在保存任务提交…" };
  let reason = "";
  if (!local?.ready) reason = "本机尚未就绪，请在节点设置中验证模型与工具。";
  else if (!target?.ready) reason = "执行节点尚未就绪，请在该节点验证模型与工具。";
  else if (!project) reason = "请选择执行节点的授权目录。";
  else if (!goal.trim()) reason = "请输入任务内容，上方灰色文字仅为示例。";
  return { disabled: !!reason, busy: false, reason };
}
