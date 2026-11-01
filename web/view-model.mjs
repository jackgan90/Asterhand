import {localizeMessage, t} from "./i18n.mjs";

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
  if (!localTasks.available) reason = t("web.vm.local_reason", {detail: localTasks.detail});
  else if (!targetTasks.available) reason = t("web.vm.target_reason", {detail: targetTasks.detail});
  else if (!project) reason = t("web.vm.missing_project");
  // An empty goal only disables sending; the placeholder already explains it.
  return { disabled: !!reason || !goal.trim(), busy: false, reason };
}

export function modelTaskStatus(info) {
  if (info?.capabilities !== undefined) {
    const status = info.capabilities?.model_tasks;
    const detail = localizeMessage(status?.message_key, status?.message_args, status?.detail || "") || t("web.vm.missing_model_status");
    return {available: status?.available === true, detail};
  }
  return {available: info?.ready === true, detail: t("web.vm.legacy_node")};
}
