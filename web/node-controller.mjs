import {$, node, dialog} from "./ui.mjs";
import {submissionState, modelTaskStatus} from "./view-model.mjs";
export function nodeController({api, post, session, guarded, notice, actions, listen, isSubmitting}) {
  let info, peers = [], explicitTarget = false, refreshGeneration = 0;
  async function refreshPeers() {
    const scope = session(), generation = ++refreshGeneration;
    const nextInfo = await api("/api/info"), nextPeers = await api("/api/peers");
    if (!scope?.active || generation !== refreshGeneration) return;
    info = nextInfo; peers = nextPeers;
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
          `${p.local ? "本机 · " : ""}${p.online ? "在线" : "离线"}`,
        ),
      );
      if (p.info) {
        const status = modelTaskStatus(p.info);
        card.append(node("small", `${p.online ? "" : "上次状态："}${status.available ? "已就绪" : status.detail}`));
        if (p.info.capabilities?.chat?.available && !status.available)
          card.append(node("small", "可以聊天"));
      }
      if (p.error) card.append(node("small", p.error, "error-text"));
      if (!p.local) {
        const revoke = node("button", "解除配对", "quiet");
        revoke.addEventListener("click", () => {
          if (actions.confirm(`解除与 ${p.name} 的配对？已在执行的任务不会被取消。`))
            guarded(async () => {
              const scope = session();
              await api("/api/peers", {
                method: "DELETE",
                body: JSON.stringify({ id: p.id }),
              });
              if (scope?.active) await refreshPeers();
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
      ? `保存到 ${project.root}`
      : "该设备离线或尚未设置工作目录";
    if (id !== info.node_id && project && !peers.find((p) => p.id === id)?.online)
      $("#target-hint").textContent +=
        " · 设备离线，恢复连接后自动送达";
    updateSubmission();
  }
  function updateSubmission() {
    const id = $("#target").value;
    const selected = id === info.node_id ? info : peers.find((p) => p.id === id)?.info;
    const project = selected?.projects?.find((p) => p.id === $("#project").value);
    const state = submissionState(info, selected, project, $("#goal").value, isSubmitting());
    $("#submit-task").disabled = state.disabled;
    $("#submit-task").setAttribute("aria-busy", String(state.busy));
    $("#submit-task").textContent = state.busy ? "正在发送…" : "发送";
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
    if (scope?.active) notice("已复制，请在 10 分钟内粘贴到另一台设备。");
  }));
  listen($("#pair-form"), "submit", event => {
    event.preventDefault();
    guarded(async () => {
      const scope = session();
      await post("/api/pair", {code:new FormData(event.target).get("code")});
      if (!scope?.active) return;
      event.target.reset(); $("#pair").close();
      notice("配对成功");
      await refreshPeers();
    });
  });
  return {refresh:refreshPeers, updateSubmission, selection:() => ({info, peers}),
    content:() => ({project_id:$("#project").value, goal:$("#goal").value.trim(), target:$("#target").value, target_explicit:explicitTarget})};
}
