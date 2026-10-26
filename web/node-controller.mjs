import {$, node, dialog} from "./ui.mjs";
import {submissionState} from "./view-model.mjs";
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
          `${p.local ? "本机 · " : ""}${p.online ? (p.info?.ready ? "助手就绪" : "待配置") : "暂不可达"}`,
        ),
      );
      if (p.error) card.append(node("small", p.error, "error-text"));
      if (!p.local) {
        const revoke = node("button", "撤销配对", "quiet");
        revoke.addEventListener("click", () => {
          if (actions.confirm(`撤销与 ${p.name} 的配对？已经执行的任务不会自动取消。`))
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
    const state = submissionState(info, selected, project, $("#goal").value, isSubmitting());
    $("#submit-task").disabled = state.disabled;
    $("#submit-task").setAttribute("aria-busy", String(state.busy));
    $("#submit-task").textContent = state.busy ? "正在提交…" : "发送任务 ↗";
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
    if (scope?.active) notice("已复制，10 分钟内在另一台设备粘贴。");
  }));
  listen($("#pair-form"), "submit", event => {
    event.preventDefault();
    guarded(async () => {
      const scope = session();
      await post("/api/pair", {code:new FormData(event.target).get("code")});
      if (!scope?.active) return;
      event.target.reset(); $("#pair").close();
      notice("节点配对成功，TLS 身份已验证。");
      await refreshPeers();
    });
  });
  return {refresh:refreshPeers, updateSubmission, selection:() => ({info, peers}),
    content:() => ({project_id:$("#project").value, goal:$("#goal").value.trim(), target:$("#target").value, target_explicit:explicitTarget})};
}
