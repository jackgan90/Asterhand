import {$, node, dialog} from "./ui.mjs";
export function nodeSettings({api, post, session, desktop, guarded, notice, refreshPeers, listen}) {
  let probeTimer, probeGeneration = 0, openGeneration = 0;
  async function openSettings() {
    clearTimeout(probeTimer);
    probeGeneration++;
    const scope = session(), generation = ++openGeneration;
    void desktop.open();
    const data = await api("/api/settings"),
      c = data.config,
      form = $("#settings-form");
    if (!scope?.active || generation !== openGeneration) return;
    form.elements.name.value = c.name;
    form.elements.endpoint.value = c.endpoint || "";
    form.elements.peer_listen.value = c.peer_listen;
    $("#settings-status").textContent = "";
    const populate = (select, items, selected) => {
      select.replaceChildren();
      for (const item of items) {
        const option = node("option", item.name || item.id);
        option.value = item.id;
        select.append(option);
      }
      select.value = selected;
    };
    populate(form.elements.agent_runtime, data.runtimes, c.agent_runtime);
    const updateModels = (selected) => {
      const provider = data.providers.find(
        (p) => p.id === form.elements.provider.value,
      );
      populate(
        form.elements.model,
        (provider?.models || []).map((id) => ({ id })),
        selected || provider?.models[0],
      );
    };
    const updateProviders = () => {
      const runtime = data.runtimes.find(
        (r) => r.id === form.elements.agent_runtime.value,
      );
      populate(
        form.elements.provider,
        data.providers.filter((p) => runtime?.providers.includes(p.id)),
        c.llm.provider,
      );
      if (!form.elements.provider.value) form.elements.provider.selectedIndex = 0;
      const sameProvider = form.elements.provider.value === c.llm.provider;
      form.elements.credential_ref.value = sameProvider
        ? c.llm.credential_ref
        : form.elements.provider.value;
      updateModels(sameProvider ? c.llm.model : undefined);
    };
    form.elements.agent_runtime.onchange = updateProviders;
    form.elements.provider.onchange = () => {
      form.elements.credential_ref.value = form.elements.provider.value;
      updateModels();
    };
    updateProviders();
    form.elements.credential_ref.value = c.llm.credential_ref;
    form.elements.root.value = c.projects?.[0]?.root || "";
    form.elements.key.value = "";
    form.elements.delete_key.checked = false;
    $("#key-status").textContent = data.has_key
      ? "已保存本机 key，页面不会读回密钥。"
      : "尚未配置 key。";
    $("#runtime-status").textContent =
      `节点 ${c.node_id} · 本机数据 ${data.home}`;
    dialog("#settings");
    $("#settings").scrollTop = 0;
    await updateProbe();
    if (!scope.active || generation !== openGeneration) return;
    const network = await api("/api/network");
    if (!scope.active || generation !== openGeneration) return;
    $("#network-status").textContent =
      `${network.detail}${network.ipv4 ? " 地址：" + network.ipv4 : ""}`;
    $("#use-tailnet").hidden = !network.endpoint;
    $("#use-tailnet").onclick = () => {
      form.elements.endpoint.value = network.endpoint;
      form.elements.peer_listen.value = network.listen;
      $("#settings-status").textContent = "已填写 Tailscale 连接配置。保存后按提示重启节点，使监听地址生效。";
    };
  }
  listen($("#settings-open"), "click", () => guarded(openSettings));
  listen($("#settings-form"), "submit", (event) => {
    event.preventDefault();
    guarded(async () => {
      const scope = session(), generation = openGeneration;
      const f = new FormData(event.target);
      const result = await post("/api/settings", {
        name: f.get("name"),
        endpoint: f.get("endpoint").trim(),
        peer_listen: f.get("peer_listen").trim(),
        agent_runtime: f.get("agent_runtime"),
        llm: {
          provider: f.get("provider"),
          model: f.get("model"),
          credential_ref: f.get("credential_ref"),
        },
        root: f.get("root").trim(),
        key: f.get("key"),
        delete_key: f.get("delete_key") === "on",
      });
      if (!scope?.active || generation !== openGeneration) return;
      event.target.elements.key.value = "";
      const message = ["设置已保存。", result.ready ? "模型验证保持有效。" : "请验证模型与工具。", result.restart_required ? "监听地址已修改：请停止本机节点，再运行 start.ps1。" : ""].join(" ");
      $("#settings-status").textContent = message;
      notice(message);
      await updateProbe();
      if (scope.active) await refreshPeers();
    });
  });
  async function updateProbe() {
    const scope = session(), generation = ++probeGeneration;
    clearTimeout(probeTimer);
    const result = await api("/api/probe");
    if (!scope?.active || generation !== probeGeneration) return;
    $("#probe-status").textContent =
      {
        not_run: "尚未运行检查",
        running: "正在调用所选模型并检查受限工具…",
        passed: "模型与工具检查通过。节点已就绪。",
        failed: "检查失败：",
      }[result.state] + (result.error || "");
    $("#probe").disabled = result.state === "running";
    if (result.state === "running") probeTimer = setTimeout(() => guarded(updateProbe), 1500);
    if (result.state === "passed") await refreshPeers();
  }
  listen($("#probe"), "click", () =>
    guarded(async () => {
      const scope = session();
      await post("/api/probe");
      if (scope?.active) await updateProbe();
    }),
  );

  function stop() {
    openGeneration++;
    probeGeneration++;
    clearTimeout(probeTimer);
    const fields = $("#settings-form").elements;
    fields.agent_runtime.onchange = fields.provider.onchange = null;
    $("#use-tailnet").onclick = null;
  }
  listen($("#settings"), "close", stop);
  return {open:openSettings, stop, resume:() => $("#settings").open ? openSettings() : undefined};
}
