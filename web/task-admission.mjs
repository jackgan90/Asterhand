// Matches the Go model.Request JSON contract after /api/jobs normalization.
// Keep its fixed field order/HTML escaping covered by the shared Go/JS fixture.
export async function taskRequestDigest(request, localNodeID) {
  const text = value => String(value ?? "").toWellFormed();
  const normalized = {
    request_id:text(request.request_id), project_id:text(request.project_id),
    goal:text(request.goal), start_before:text(request.start_before), mode:"route",
  };
  const target = request.target || localNodeID;
  if (target) normalized.target = text(target);
  if (request.target_explicit === true) normalized.target_explicit = true;
  const body = JSON.stringify(normalized).replace(/[<>&\u2028\u2029]/g,
    value => `\\u${value.charCodeAt(0).toString(16).padStart(4,"0")}`);
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(body));
  return [...new Uint8Array(digest)].map(value => value.toString(16).padStart(2,"0")).join("");
}

export async function definitiveTaskRejection(error, request, localNodeID) {
  const receipt = error.details;
  if (error.status !== 409 || receipt?.code !== "task_configuration_unavailable" ||
      receipt.request_id !== request.request_id || typeof receipt.error !== "string" || !receipt.error) return false;
  try {
    return receipt.request_digest === await taskRequestDigest(request, localNodeID);
  } catch { return false; } // An uncheckable response keeps the original identity.
}
