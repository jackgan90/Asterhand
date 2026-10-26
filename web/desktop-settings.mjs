export function desktopSettings(api, post, {listen = (target, event, callback) => target.addEventListener(event, callback), session = () => ({active:true})} = {}) {
  const form = document.querySelector("#desktop-form"), fields = form.elements;
  const state = document.querySelector("#desktop-state");
  const recordInput = document.querySelector("#desktop-record");
  const recordStatus = document.querySelector("#desktop-record-status");
  const recordCancel = document.querySelector("#desktop-record-cancel");
  const page = crypto.randomUUID();
  let current, recording, poll, pending, busy, recordingGeneration = 0, pageGeneration = 0;
  let cancellation = Promise.resolve();
  const key = document.querySelector("#desktop-key");
  for (const name of [..."ABCDEFGHIJKLMNOPQRSTUVWXYZ"].map(c => "Key"+c)
    .concat([..."0123456789"].map(c => "Digit"+c),
      Array.from({length:11}, (_, i) => "F"+(i+1)),
      ["Space", "Enter", "Pause", "Insert", "Delete", "Home", "End", "PageUp", "PageDown"])) {
    const option = document.createElement("option");
    option.value = name; option.textContent = name.replace(/^(Key|Digit)/, ""); key.append(option);
  }
  function binding() {
    return {modifiers:["ctrl","alt","shift"].filter(m => fields[m].checked), key:fields.key.value};
  }
  function label(value) {
    return [...value.modifiers.map(m => m[0].toUpperCase()+m.slice(1)), value.key.replace(/^(Key|Digit)/, "")].join("+");
  }
  function setBinding(value) {
    for (const m of ["ctrl","alt","shift"]) fields[m].checked = value.modifiers.includes(m);
    fields.key.value = value.key; recordInput.value = label(value);
  }
  function render(value, fill = false) {
    current = value;
    const registration = {registered:"已注册", disabled:"已禁用", initializing:"准备中", conflict:"组合已被占用", unavailable:"桌面不可用", unsupported:"当前平台暂不支持", config_invalid:"本机配置无效"};
    state.textContent = `${registration[value.registration] || value.registration} · 面板 ${value.surface === "ready" ? "就绪" : value.surface === "failed" ? "准备失败" : "未就绪"} · 期望 ${label(value.desired.binding)}${value.effective_binding ? " · 生效 "+label(value.effective_binding) : ""}${value.cleanup_pending ? " · 旧注册待清理，请重试；持续失败时从托盘退出并重启" : ""}${value.error ? " · "+value.error : ""}`;
    document.querySelector("#desktop-fields").disabled = !value.supported;
    if (fill) {
      fields.enabled.checked = value.desired.enabled; fields.theme.value = value.desired.theme_id;
      setBinding(value.desired.binding);
    }
  }
  const isCurrent = (generation, scope) => generation === pageGeneration && scope?.active;
  async function refresh(fill = false) {
    const generation = pageGeneration, scope = session();
    const value = await api("/api/desktop/settings");
    if (isCurrent(generation, scope)) render(value, fill);
  }
  async function endRecording() {
    recordingGeneration++;
    const previous = recording; recording = null; clearTimeout(poll);
    recordCancel.hidden = true;
    if (previous) cancellation = cancelLease(previous);
    await cancellation;
  }
  async function cancelLease(lease) {
    if (!lease) return;
    try { await post("/api/desktop/recording/cancel", {page_id:page, recording_id:lease.recording_id}); }
    catch { /* The server lease still expires independently after 30 seconds. */ }
  }
  async function run(fn) {
    if (busy?.generation === pageGeneration) return;
    const generation = pageGeneration, scope = session();
    if (!scope?.active) return;
    const transaction = busy = {generation};
    try { await fn(); }
    catch (error) { if (isCurrent(generation, scope)) state.textContent = error.message; }
    finally { if (busy === transaction) busy = undefined; }
  }
  async function recordPoll() {
    if (!recording) return;
    const lease = recording, generation = recordingGeneration;
    const isCurrent = () => recording === lease && generation === recordingGeneration;
    try {
      const value = await post("/api/desktop/recording/poll", {page_id:page, recording_id:lease.recording_id});
      if (!isCurrent()) return;
      if (value.binding) {
        setBinding(value.binding);
        pending = null;
        const ended = endRecording();
        recordStatus.textContent = "已录制，点击保存并应用。";
        await ended;
        return;
      }
      poll = setTimeout(recordPoll, 250);
    } catch (error) {
      if (isCurrent()) {
        const ended = endRecording();
        recordStatus.textContent = error.message;
        await ended;
      }
    }
  }
  listen(document.querySelector("#desktop-record-start"), "click", () => run(async () => {
    const cancelled = endRecording();
    // Capture before awaiting: blur/close can invalidate this start in flight.
    const generation = recordingGeneration;
    await cancelled;
    if (generation !== recordingGeneration) return;
    pending = null;
    const lease = await post("/api/desktop/recording/start", {page_id:page});
    if (generation !== recordingGeneration) {
      await cancelLease(lease);
      return;
    }
    recording = lease;
    recordCancel.hidden = false;
    recordStatus.textContent = "请按组合键，30 秒内有效。Esc 取消。";
    recordInput.focus();
    void recordPoll();
  }));
  listen(recordInput, "keydown", event => {
    if (!recording || event.isComposing || event.repeat) return;
    event.preventDefault(); event.stopPropagation();
    if (event.key === "Escape") { void endRecording(); recordStatus.textContent = "录制已取消"; return; }
    const identity = /^[a-z]$/i.test(event.key) ? "Key"+event.key.toUpperCase() : /^[0-9]$/.test(event.key) ? "Digit"+event.key : event.code;
    if (/^(Key|Digit)/.test(identity) && !/^[a-z0-9]$/i.test(event.key)) { recordStatus.textContent = "请切换到可输入英文字母的布局，或使用按键选择器。"; return; }
    if (event.metaKey || (!event.ctrlKey && !event.altKey) || ![...key.options].some(o => o.value === identity)) return;
    setBinding({modifiers:[event.ctrlKey && "ctrl", event.altKey && "alt", event.shiftKey && "shift"].filter(Boolean), key:identity});
    pending = null; void endRecording(); recordStatus.textContent = "已录制，点击保存并应用。";
  });
  listen(recordInput, "blur", () => { void endRecording(); });
  listen(recordCancel, "click", () => { void endRecording(); recordStatus.textContent = "录制已取消"; });
  listen(window, "blur", () => { void endRecording(); });
  listen(document.querySelector("#settings"), "close", () => { void endRecording(); });
  listen(form, "input", () => { pending = null; recordInput.value = label(binding()); document.querySelector("#desktop-save").textContent = "保存并应用"; });
  async function saveRequest(reset = false) {
    const generation = pageGeneration, scope = session();
    await endRecording();
    if (!isCurrent(generation, scope)) return;
    pending ||= {operation_id:crypto.randomUUID(), expected_revision:current.desired.revision, enabled:fields.enabled.checked, binding:binding(), theme_id:fields.theme.value, reset};
    try {
      const result = await post("/api/desktop/settings", pending);
      if (!isCurrent(generation, scope)) return;
      pending = null; render(result, true); document.querySelector("#desktop-save").textContent = "保存并应用";
    } catch (error) {
      if (!isCurrent(generation, scope)) return;
      const operation = pending.operation_id;
      await refresh();
      if (!isCurrent(generation, scope)) return;
      if (current.last_operation === operation) { pending = null; return; }
      if (error.status || current.desired.revision !== pending.expected_revision) pending = null;
      document.querySelector("#desktop-save").textContent = pending ? "重试上次保存" : "保存并应用";
      throw error;
    }
  }
  async function save(reset = false) {
    const generation = pageGeneration, scope = session();
    document.querySelector("#desktop-fields").disabled = true;
    try { await saveRequest(reset); }
    finally { if (isCurrent(generation, scope)) document.querySelector("#desktop-fields").disabled = !current?.supported; }
  }
  listen(form, "submit", event => { event.preventDefault(); void run(() => save()); });
  listen(document.querySelector("#desktop-reset"), "click", () => run(() => { pending = null; return save(true); }));
  listen(document.querySelector("#desktop-check"), "click", () => run(async () => {
    const generation = pageGeneration, scope = session();
    await endRecording();
    if (!isCurrent(generation, scope)) return;
    await post("/api/desktop/check", {binding:binding()});
    if (!isCurrent(generation, scope)) return;
    state.textContent = "该组合此刻可用。保存时会再次注册确认。";
  }));
  listen(document.querySelector("#desktop-retry"), "click", () => run(async () => {
    const generation = pageGeneration, scope = session();
    await endRecording();
    if (!isCurrent(generation, scope)) return;
    const value = await post("/api/desktop/retry");
    if (isCurrent(generation, scope)) render(value);
  }));
  return {open:() => run(async () => {
    const generation = pageGeneration, scope = session();
    await endRecording();
    if (!isCurrent(generation, scope)) return;
    const value = await api("/api/desktop/settings");
    if (!isCurrent(generation, scope)) return;
    // A resumed page reconciles the unknown save instead of generating a new ID.
    if (pending && (value.last_operation === pending.operation_id || value.desired.revision !== pending.expected_revision)) pending = null;
    render(value, !pending);
    document.querySelector("#desktop-save").textContent = pending ? "重试上次保存" : "保存并应用";
  }),
    stop:() => { pageGeneration++; void endRecording(); }};
}
