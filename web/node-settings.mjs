import {localizeMessage, t} from "./i18n.mjs";
import {$, node, dialog} from "./ui.mjs";

const probeKeys = {
  not_run: "web.settings.probe.not_run",
  running: "web.settings.probe.running",
  passed: "web.settings.probe.passed",
  failed: "web.settings.probe.failed",
  stale: "web.settings.probe.stale",
};

export function nodeSettings({api, post, session, desktop, guarded, notice, refreshPeers, listen}) {
  let probeTimer, probeGeneration = 0, openGeneration = 0;
  let loadedData = null, lastProbe = null, lastNetwork = null, statusState = null;
  let nameDirty = false, renderedDefaultName = "";
  const form = $("#settings-form");
  // The form spans several panes; tell the user that saving submits all of them.
  const markDirty = dirty => { $("#settings-dirty").hidden = !dirty; };
  // Language and theme apply immediately through the desktop settings API; they
  // sit inside this form's DOM but are not submitted with it.
  const edited = event => {
    if (event.target?.id !== "settings-locale" && event.target?.name !== "ui-theme") markDirty(true);
  };
  listen(form, "input", edited);
  listen(form, "change", edited);

  if (form?.elements?.name?.addEventListener) {
    listen(form.elements.name, "input", () => { nameDirty = true; });
  }

  function paintKeyAndRuntime() {
    if (!loadedData) return;
    $("#key-status").textContent = loadedData.has_key
      ? t("web.settings.model.key_saved")
      : t("web.settings.model.key_empty");
    $("#runtime-status").textContent = t("web.settings.network.runtime_info", {
      node_id: loadedData.config.node_id,
      home: loadedData.home,
    });
  }

  function formatSavedMessage(state) {
    const configuration = state.chatAvailable
      ? t("web.settings.saved_chat_ready")
      : t("web.settings.saved_chat_optional");
    return [
      t("web.settings.saved_prefix"),
      configuration,
      state.restartRequired ? t("web.settings.saved_restart_required") : "",
    ].filter(Boolean).join(" ");
  }

  function paintStatusLine() {
    if (!statusState) {
      $("#settings-status").textContent = "";
      return;
    }
    if (statusState.kind === "tailnet_filled") {
      $("#settings-status").textContent = t("web.settings.network.tailnet_filled");
      return;
    }
    if (statusState.kind === "saved") {
      $("#settings-status").textContent = formatSavedMessage(statusState);
    }
  }

  function paintProbe() {
    if (!lastProbe) return;
    const base = t(probeKeys[lastProbe.state] || probeKeys.not_run);
    const errText = localizeMessage(lastProbe.message_key, lastProbe.message_args, lastProbe.error || "");
    $("#probe-status").textContent = base + (errText ? ` ${errText}` : "");
    $("#probe-status").dataset.tone = {passed:"ok", failed:"error", running:"warn"}[lastProbe.state] || "muted";
    $("#probe").disabled = lastProbe.state === "running";
  }

  function paintNetwork() {
    if (!lastNetwork) return;
    const detail = localizeMessage(lastNetwork.message_key, lastNetwork.message_args, lastNetwork.detail || "");
    $("#network-status").textContent = `${detail}${lastNetwork.ipv4 ? " · " + lastNetwork.ipv4 : ""}`;
    $("#use-tailnet").hidden = !lastNetwork.endpoint;
  }

  async function openSettings() {
    clearTimeout(probeTimer);
    probeGeneration++;
    const scope = session(), generation = ++openGeneration;
    void desktop.open();
    const data = await api("/api/settings"),
      c = data.config;
    if (!scope?.active || generation !== openGeneration) return;
    loadedData = data;
    lastNetwork = null;
    statusState = null;
    nameDirty = false;
    if (c.name_default) {
      renderedDefaultName = t("common.default_node_name");
      form.elements.name.value = renderedDefaultName;
    } else {
      renderedDefaultName = "";
      form.elements.name.value = c.name;
    }
    form.elements.endpoint.value = c.endpoint || "";
    form.elements.peer_listen.value = c.peer_listen;
    paintStatusLine();
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
    paintKeyAndRuntime();
    markDirty(false);
    dialog("#settings");
    $("#settings").scrollTop = 0;
    await updateProbe();
    if (!scope.active || generation !== openGeneration) return;
    const network = await api("/api/network");
    if (!scope.active || generation !== openGeneration) return;
    lastNetwork = network;
    paintNetwork();
    $("#use-tailnet").onclick = () => {
      form.elements.endpoint.value = network.endpoint;
      form.elements.peer_listen.value = network.listen;
      markDirty(true);
      statusState = {kind: "tailnet_filled"};
      paintStatusLine();
    };
  }
  listen($("#settings-open"), "click", () => guarded(openSettings));
  listen(form, "submit", (event) => {
    event.preventDefault();
    guarded(async () => {
      const scope = session(), generation = openGeneration;
      const f = new FormData(event.target);
      const rawInputName = f.get("name");
      const keepDefaultName = Boolean(loadedData?.config?.name_default) && !nameDirty && rawInputName === renderedDefaultName;
      const submittedName = keepDefaultName ? loadedData.config.name : rawInputName;
      const result = await post("/api/settings", {
        name: submittedName,
        name_edited: !keepDefaultName,
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
      if (loadedData && !keepDefaultName) {
        loadedData.config.name = submittedName;
        loadedData.config.name_default = false;
      }
      if (loadedData) {
        if (f.get("delete_key") === "on") loadedData.has_key = false;
        else if (f.get("key")) loadedData.has_key = true;
        paintKeyAndRuntime();
      }
      event.target.elements.key.value = "";
      markDirty(false);
      statusState = {
        kind: "saved",
        chatAvailable: Boolean(result.capabilities?.chat?.available),
        restartRequired: Boolean(result.restart_required),
      };
      const message = formatSavedMessage(statusState);
      paintStatusLine();
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
    lastProbe = result;
    paintProbe();
    if (result.state === "running") probeTimer = setTimeout(() => guarded(updateProbe), 1500);
  }
  listen($("#probe"), "click", () =>
    guarded(async () => {
      const scope = session();
      await post("/api/probe");
      if (scope?.active) await updateProbe();
    }),
  );

  function relocalize() {
    if (loadedData?.config?.name_default && !nameDirty && form.elements.name.value === renderedDefaultName) {
      renderedDefaultName = t("common.default_node_name");
      form.elements.name.value = renderedDefaultName;
    }
    paintKeyAndRuntime();
    paintStatusLine();
    paintProbe();
    paintNetwork();
  }

  function stop() {
    openGeneration++;
    probeGeneration++;
    clearTimeout(probeTimer);
    const fields = form.elements;
    fields.agent_runtime.onchange = fields.provider.onchange = null;
    $("#use-tailnet").onclick = null;
  }
  listen($("#settings"), "close", stop);
  return {open:openSettings, relocalize, stop, resume:() => $("#settings").open ? openSettings() : undefined};
}
