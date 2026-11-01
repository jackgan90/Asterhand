export function taskViews(jobs, outbox, localID) {
  const byID = new Map(jobs.map((job) => [job.id, job]));
  const represented = new Set(outbox.map((out) => out.id));
  const views = outbox.map((out) => {
    const route = byID.get(out.id);
    if (out.job && out.peer_id === localID) represented.add(out.job.id);
    const job = (out.peer_id === localID && out.job && byID.get(out.job.id)) || out.job || {
      id: out.id,
      state: out.state === "rejected" ? "rejected" : "queued",
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
  if (submitting) return { disabled: true, busy: true, reason: "" };
  let reason = "";
  const localTasks = modelTaskStatus(local), targetTasks = modelTaskStatus(target);
  if (!localTasks.available) reason = `本机：${localTasks.detail}`;
  else if (!targetTasks.available) reason = `所选设备：${targetTasks.detail}`;
  else if (!project) reason = "所选设备还没有工作目录，请先在设置中添加。";
  // An empty goal only disables sending; the placeholder already explains it.
  return { disabled: !!reason || !goal.trim(), busy: false, reason };
}

export function modelTaskStatus(info) {
  if (info?.capabilities !== undefined) {
    const status = info.capabilities?.model_tasks;
    return {available:status?.available === true, detail:status?.detail || "未获取到模型配置，请在该设备的设置中检查。"};
  }
  return {available:info?.ready === true, detail:"该设备版本较旧（旧版验证规则），请升级后使用。"};
}
