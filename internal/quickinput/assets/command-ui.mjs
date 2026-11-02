import {commandClient} from './command-client.mjs';
import {commandQuery, filterCommands} from './command-model.mjs';
import {runBusy, runStoppable, runLabel, modeLabel} from './command-run.mjs';
import {commandResults} from './command-results.mjs';
import {getLocale, localizeError, localizeMessage, t} from './i18n.mjs';

export function commandPanel(bridge, {input, post, changed, sendText}) {
  const root = document.getElementById('command-panel');
  if (!root) return {changed() {}, key() {return false;}, submit() {return false;}, relocalize() {}, dispose() {}};
  const list = document.getElementById('command-list'), message = document.getElementById('command-message');
  const run = document.getElementById('command-run');
  const empty = document.getElementById('command-empty'), chatSend = document.getElementById('chat-send');
  const client = commandClient(bridge, post), results = commandResults(), errorMeta = new Map(), cleanup = [];
  let managedAvailable=false;
  let entries = [], filtered = [], selected = '', ready = false, visible = false, dismissed = false, composing = false;
  let loading = false, disposed = false, timer = null, lastText = '', painted = '', messageState = null;
  const listen = (target, name, callback) => { target.addEventListener(name, callback); cleanup.push(() => target.removeEventListener(name, callback)); };
  const query = () => commandQuery(input.value);
  const active = () => ready && visible && query() !== null && !dismissed;
  function clearMessage() {
    messageState = null;
    message.textContent = ''; message.hidden = true;
    measure();
  }
  function showError(err, operation = false) {
    messageState = {err, operation};
    const text = localizeError(err);
    message.textContent = text; message.hidden = !text;
    measure();
  }
  function rowErrorText(id) {
    const raw = results.error(id);
    if (!raw) return '';
    const meta = errorMeta.get(id);
    return meta ? localizeError(meta) : raw;
  }
  function measure() {
    if (!root.hidden) {
      const extras = root.querySelector('.command-toolbar').getBoundingClientRect().height + 2;
      root.dataset.desiredHeight = String(extras + (filtered.length ? Math.min(8, filtered.length) * 56 : 56));
    }
    changed();
  }
  function highlight(scroll = false) {
    const entry = filtered.find(entry => entry.id === selected);
    run.disabled = !entry || runBusy(entry) || entry.execution_mode === 'managed' && !managedAvailable;
    for (const row of list.children) row.setAttribute('aria-selected', String(row.dataset.id === selected));
    const row = [...list.children].find(node => node.dataset.id === selected);
    if (row) {
      input.setAttribute('aria-activedescendant', row.id);
      if (scroll) row.scrollIntoView({block:'nearest'});
    } else input.removeAttribute('aria-activedescendant');
  }
  function paint(scroll = false) {
    root.hidden = !active();
    input.setAttribute('aria-expanded', String(!root.hidden));
    chatSend.hidden = query() !== null;
    if (root.hidden) {
      clearTimeout(timer); timer = null; input.removeAttribute('aria-activedescendant');
      delete root.dataset.layoutSettled; list.style.removeProperty('max-height');
      changed(); return;
    }
    filtered = filterCommands(entries, query());
    if (!filtered.some(entry => entry.id === selected)) selected = filtered[0]?.id ?? '';
    const signature = JSON.stringify([getLocale(), filtered.map(entry => [entry, rowErrorText(entry.id)])]);
    if (signature === painted) { highlight(scroll); measure(); return; }
    painted = signature;
    const previousScroll = list.scrollTop;
    list.replaceChildren();
    for (const entry of filtered) {
      const row = document.createElement('div');
      row.type = 'button'; row.className = 'command-option'; row.id = `command-option-${entry.id}`;
      row.dataset.id = entry.id; row.dataset.state = entry.state; row.setAttribute('role', 'option'); row.tabIndex = -1;
      const info = document.createElement('span'), title = document.createElement('span'), description = document.createElement('small');
      info.className = 'command-info';
      const aliases = [...(entry.aliases ?? [])].sort((a, b) => Number(b === entry.matchedName) - Number(a === entry.matchedName));
      title.textContent = modeLabel(entry) + ' ' + entry.name + (aliases.length ? ' · ' + aliases.join(' / ') : '');
      title.title = title.textContent;
      const detailText = localizeMessage(entry.detail_key, entry.detail_args, entry.detail || '');
      const failed = !['ready', 'checking'].includes(entry.state);
      description.textContent = [runLabel(entry), failed ? detailText : entry.description || t(entry.state === 'checking' ? 'quickinput.commands_checking' : 'quickinput.commands_local_script')].filter(Boolean).join(' · ');
      row.title = [title.textContent, entry.description, failed ? detailText : ''].filter(Boolean).join('\n');
      info.append(title, description);
      const error = document.createElement('span');
      const errText = rowErrorText(entry.id);
      error.className = 'command-error'; error.textContent = errText; error.hidden = !errText;
      error.title = errText ? t('quickinput.commands_open_config_hint', {error: errText}) : '';
      error.setAttribute('role', 'status');
      row.append(info, error);
      if(entry.execution_mode === "managed" && entry.run_id){
        const controls=document.createElement("span");controls.className="command-controls";
        for(const [action,key] of [["logs","common.command.logs"],["stop","common.command.stop"]]){
          if(action==="stop"&&!runBusy(entry))continue;
          const button=document.createElement("button");button.type="button";button.textContent=t(key);
          button.disabled=action==="stop"&&!runStoppable(entry);
          button.addEventListener("click",event=>{event.stopPropagation();void control(action,entry,button);});controls.append(button);
        }
        row.append(controls);
      }
      row.addEventListener('pointerdown', event => { if (event.button === 0) event.preventDefault(); });
      row.addEventListener('click', event => {
        selected = entry.id; highlight();
        if (event.target === error) void settings(entry.id); else void execute(entry);
      });
      list.append(row);
    }
    list.scrollTop = previousScroll;
    empty.hidden = Boolean(filtered.length);
    empty.textContent = t(loading && !entries.length ? 'quickinput.commands_loading' : entries.length ? 'quickinput.commands_no_match' : 'quickinput.commands_empty');
    highlight(scroll); measure();
  }
  async function refresh() {
    clearTimeout(timer); timer = null;
    if (!active() || loading || disposed) return;
    loading = true;
    try {
      const all = []; let offset = 0, catalog = '';
      do {
        const response = await client.call('list', {offset, catalog});
        if (disposed || !active()) return;
        managedAvailable=response.managed_available===true;all.push(...(response.entries ?? [])); catalog = response.catalog; offset = response.next ?? 0;
      } while (offset);
      const ids = all.map(entry => entry.id);
      entries = all; results.retain(ids);
      const keep = new Set(ids);
      for (const id of errorMeta.keys()) if (!keep.has(id)) errorMeta.delete(id);
      if (!messageState?.operation) clearMessage(); paint();
    } catch (error) {
      if (!disposed) { entries = []; showError(error); paint(); }
    } finally {
      loading = false;
      if (active() && !disposed) timer = setTimeout(refresh, 1500);
    }
  }
  async function execute(entry) {
    if (composing || !entry || runBusy(entry)) return;
    clearMessage();
    if(entry.execution_mode==='managed'&&!managedAvailable){showError({messageKey:'err.localcmd.managed_unavailable'});return;}
    const version = results.begin(entry.id);
    if (entry.state === 'missing') {
      const msg = t('quickinput.commands_missing_script');
      if (results.finish(entry.id, version, msg)) errorMeta.set(entry.id, {messageKey: 'quickinput.commands_missing_script', rawMessage: msg});
      paint();
      return;
    }
    try {
      const response = await client.call('launch', {command_id:entry.id, revision:entry.revision});
      if (!disposed && response.started && results.finish(entry.id, version)) { errorMeta.delete(entry.id); if(entry.execution_mode==='managed'){entry.phase='starting';void refresh();} paint(); }
    } catch (error) {
      if (disposed || !results.finish(entry.id, version, localizeError(error))) return;
      errorMeta.set(entry.id, error);
      paint();
      if (error.code === 'conflict' || error.code === 'not_found') void refresh();
    }
    input.focus();
  }
  async function control(action,entry,button){
    clearMessage();
    button.disabled=true;
    try{await client.call(action,{command_id:entry.id,...(action==='stop'?{run_id:entry.run_id}:{})});}
    catch(err){showError(err, true);}
    finally{button.disabled=false;void refresh();}
  }
  async function settings(id = '') {
    clearMessage();
    try { await client.call('settings', {command_id:id}); }
    catch (error) { if (!disposed) showError(error); }
  }
  function textChanged() {
    if (composing || disposed) return;
    if (input.value !== lastText) { dismissed = false; selected = ''; lastText = input.value; }
    paint();
    if (active() && !timer && !loading) void refresh();
  }
  function submit() {
    if (query() === null) return false;
    if (dismissed) { dismissed = false; textChanged(); return true; }
    if (!composing) void execute(filtered.find(entry => entry.id === selected));
    return true;
  }
  function key(event) {
    if (composing || event.isComposing || event.keyCode === 229) return false;
    if (event.key === 'Escape' && active()) { dismissed = true; paint(); return true; }
    if (!active() || !['ArrowUp','ArrowDown'].includes(event.key) || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return false;
    const index = filtered.findIndex(entry => entry.id === selected);
    selected = filtered[Math.max(0, Math.min(filtered.length - 1, index + (event.key === 'ArrowDown' ? 1 : -1)))]?.id ?? '';
    highlight(true); return true;
  }
  function relocalize() {
    if (disposed) return;
    if (messageState) {
      const text = localizeError(messageState.err);
      message.textContent = text;
      message.hidden = !text;
    }
    painted = '';
    paint();
  }
  listen(input, 'compositionstart', () => {composing = true;});
  listen(input, 'compositionend', () => {composing = false; textChanged();});
  listen(input, 'input', textChanged);
  listen(run, 'click', submit);
  listen(document.getElementById('command-settings'), 'click', () => settings());
  listen(document.getElementById('command-as-text'), 'click', sendText);
  const unsubscribe = bridge.subscribe(value => {
    if (value.kind === 'init') { ready = true; textChanged(); }
    if (value.kind === 'visibility') { visible = value.visible; textChanged(); }
    if (value.kind === 'focus') { visible = true; dismissed = false; textChanged(); }
  });
  function dispose() {
    disposed = true; clearTimeout(timer); unsubscribe(); client.dispose();
    for (const remove of cleanup) remove();
  }
  return {changed: textChanged, key, submit, relocalize, dispose};
}
