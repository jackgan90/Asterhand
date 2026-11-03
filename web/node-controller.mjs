import {localizeMessage, t} from "./i18n.mjs";
import {$, node, dialog} from "./ui.mjs";
import {submissionState, modelTaskStatus} from "./view-model.mjs";

export function displayNodeName(info, fallback = "") {
  if (info?.name_default) return t("common.default_node_name");
  return fallback || info?.name || "";
}

function displayProjectName(project) {
  if (project?.id === "workspace" && !project.name_custom) {
    return t("common.default_workspace_name");
  }
  return project?.name || "";
}

export function nodeController({api, post, session, guarded, notice, actions, listen, isSubmitting}) {
  let info, peers = [], explicitTarget = false, refreshGeneration = 0;

  function paint() {
    if (!info) return;
    const warning = localizeMessage(info.network_warning_key, null, info.network_warning || "");
    $("#node-warning").textContent = warning;
    $("#node-warning").hidden = !warning;
    const root = $("#nodes");
    root.replaceChildren();
    const all = [
      { id: info.node_id, name: displayNodeName(info, info.name), online: true, info, local: true },
      ...peers.filter((p) => !p.revoked).map((p) => ({
        ...p,
        name: displayNodeName(p.info, p.name),
      })),
    ];
    for (const p of all) {
      const card = node("div", null, "node"),
        name = node("strong"),
        online = p.online ? t("web.node.online") : t("web.node.offline"),
        dot = node("span", null, `dot${p.online ? "" : " off"}`);
      dot.title = online;
      dot.setAttribute("aria-label", online);
      name.append(dot, node("span", p.name, "node-name"));
      if (p.local) name.append(node("span", t("web.node.local_tag"), "node-tag"));
      card.append(name);
      // Healthy devices need no text; only offline or unready states explain themselves.
      const lines = [];
      if (!p.online) lines.push(online);
      if (p.info) {
        const status = modelTaskStatus(p.info);
        if (!status.available) {
          lines.push(`${p.online ? "" : t("web.node.last_state_prefix")}${status.detail}`);
          if (p.info.capabilities?.chat?.available) lines.push(t("web.node.chat_available"));
        }
      }
      for (const line of lines) card.append(node("small", line));
      const peerError = localizeMessage(p.error_key, p.error_args, p.error || "");
      if (peerError) card.append(node("small", peerError, "error-text"));
      if (!p.local) {
        const menu = node("details", null, "node-menu"), toggle = node("summary");
        toggle.setAttribute("aria-label", t("web.node.more"));
        toggle.title = t("web.node.more");
        const revoke = node("button", t("web.node.revoke"), "quiet");
        revoke.type = "button";
        revoke.addEventListener("click", () => {
          menu.open = false;
          if (actions.confirm(t("web.node.revoke_confirm", {name: p.name})))
            guarded(async () => {
              const scope = session();
              await api("/api/peers", {
                method: "DELETE",
                body: JSON.stringify({ id: p.id }),
              });
              if (scope?.active) await refreshPeers();
            });
        });
        menu.append(toggle, revoke);
        card.append(menu);
      }
      root.append(card);
    }
    const select = $("#target"),
      previous = select.value;
    select.replaceChildren();
    for (const p of all) {
      const option = node("option", `${p.name}${p.local ? t("web.node.local_suffix") : ""}`);
      option.value = p.id;
      select.append(option);
    }
    if (all.some((p) => p.id === previous)) select.value = previous;
    updateProject();
  }

  async function refreshPeers() {
    const scope = session(), generation = ++refreshGeneration;
    const nextInfo = await api("/api/info"), nextPeers = await api("/api/peers");
    if (!scope?.active || generation !== refreshGeneration) return;
    info = nextInfo; peers = nextPeers;
    paint();
  }
  function updateProject() {
    if (!info) return;
    const id = $("#target").value;
    const selected =
      id === info.node_id ? info : peers.find((p) => p.id === id)?.info;
    const select = $("#project"),
      previous = select.value;
    select.replaceChildren();
    for (const p of selected?.projects || []) {
      const option = node("option", displayProjectName(p));
      option.value = p.id;
      select.append(option);
    }
    if ([...select.options].some((o) => o.value === previous))
      select.value = previous;
    const project = selected?.projects?.find((p) => p.id === select.value);
    $("#target-hint").textContent = project
      ? t("web.node.save_to_root", {root: project.root})
      : t("web.node.no_workspace_or_offline");
    if (id !== info.node_id && project && !peers.find((p) => p.id === id)?.online)
      $("#target-hint").textContent += t("web.node.offline_queued_hint");
    updateSubmission();
  }
  function updateSubmission() {
    if (!info) return;
    const id = $("#target").value;
    const selected = id === info.node_id ? info : peers.find((p) => p.id === id)?.info;
    const project = selected?.projects?.find((p) => p.id === $("#project").value);
    const state = submissionState(info, selected, project, $("#goal").value, isSubmitting());
    $("#submit-task").disabled = state.disabled;
    $("#submit-task").setAttribute("aria-busy", String(state.busy));
    $("#submit-task").textContent = state.busy ? t("web.composer.submitting") : t("web.composer.submit");
    $("#submit-status").textContent = state.reason;
  }
  listen($("#goal"), "input", updateSubmission);
  listen($("#target"), "change", () => {
    explicitTarget = true;
    updateProject();
  });
  listen($("#project"), "change", updateProject);

  listen($("#pair-open"), "click", () => dialog("#pair"));
  listen($("#make-invite"), "click", () => guarded(async () => {
    const scope = session(), result = await post("/api/invite");
    if (scope?.active) $("#invite").value = result.code;
  }));
  listen($("#copy-invite"), "click", () => guarded(async () => {
    const scope = session();
    await actions.copy($("#invite").value);
    if (scope?.active) notice(t("web.node.invite_copied"));
  }));
  listen($("#pair-form"), "submit", event => {
    event.preventDefault();
    guarded(async () => {
      const scope = session();
      await post("/api/pair", {code:new FormData(event.target).get("code")});
      if (!scope?.active) return;
      event.target.reset(); $("#pair").close();
      notice(t("web.node.pair_success"));
      await refreshPeers();
    });
  });
  return {
    refresh:refreshPeers,
    relocalize:paint,
    updateSubmission,
    selection:() => ({info, peers}),
    content:() => ({project_id:$("#project").value, goal:$("#goal").value.trim(), target:$("#target").value, target_explicit:explicitTarget}),
  };
}
