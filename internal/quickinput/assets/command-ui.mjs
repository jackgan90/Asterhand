import {commandClient} from './command-client.mjs';
import {commandQuery, filterCommands} from './command-model.mjs';
import {commandResults} from './command-results.mjs';

export function commandPanel(bridge, {input, post, changed, sendText}) {
  const root = document.getElementById('command-panel');
  if (!root) return {changed() {}, key() {return false;}, submit() {return false;}, dispose() {}};
  const list = document.getElementById('command-list'), message = document.getElementById('command-message');
  const run = document.getElementById('command-run');
  const empty = document.getElementById('command-empty'), chatSend = document.getElementById('chat-send');
  const client = commandClient(bridge, post), results = commandResults(), cleanup = [];
  let entries = [], filtered = [], selected = '', ready = false, visible = false, dismissed = false, composing = false;
  let loading = false, disposed = false, timer = null, lastText = '', painted = '';
  const listen = (target, name, callback) => { target.addEventListener(name, callback); cleanup.push(() => target.removeEventListener(name, callback)); };
  const query = () => commandQuery(input.value);
  const active = () => ready && visible && query() !== null && !dismissed;
  function say(text) {
    message.textContent = text; message.hidden = !text;
    measure();
  }
  function measure() {
    if (!root.hidden) {
      const extras = root.querySelector('.command-toolbar').getBoundingClientRect().height + 2;
      root.dataset.desiredHeight = String(extras + (filtered.length ? Math.min(8, filtered.length) * 56 : 56));
    }
    changed();
  }
  function highlight(scroll = false) {
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
    const signature = JSON.stringify(filtered.map(entry => [entry, results.error(entry.id)]));
    if (signature === painted) { highlight(scroll); measure(); return; }
    painted = signature;
    const previousScroll = list.scrollTop;
    list.replaceChildren();
    for (const entry of filtered) {
      const row = document.createElement('button');
      row.type = 'button'; row.className = 'command-option'; row.id = `command-option-${entry.id}`;
      row.dataset.id = entry.id; row.dataset.state = entry.state; row.setAttribute('role', 'option'); row.tabIndex = -1;
      const info = document.createElement('span'), title = document.createElement('span'), description = document.createElement('small');
      info.className = 'command-info';
      const aliases = [...(entry.aliases ?? [])].sort((a, b) => Number(b === entry.matchedName) - Number(a === entry.matchedName));
      title.textContent = entry.name + (aliases.length ? ' · ' + aliases.join(' / ') : '');
      title.title = title.textContent;
      const failed = !['ready', 'checking'].includes(entry.state);
      description.textContent = failed ? entry.detail : entry.description || (entry.state === 'checking' ? '正在检查脚本' : '本地脚本');
      row.title = [title.textContent, entry.description, failed ? entry.detail : ''].filter(Boolean).join('\n');
      info.append(title, description);
      const error = document.createElement('span');
      error.className = 'command-error'; error.textContent = results.error(entry.id); error.hidden = !error.textContent;
      error.title = error.textContent ? error.textContent + '\n点击打开配置' : '';
      error.setAttribute('role', 'status');
      row.append(info, error);
      row.addEventListener('pointerdown', event => { if (event.button === 0) event.preventDefault(); });
      row.addEventListener('click', event => {
        selected = entry.id; highlight();
        if (event.target === error) void settings(entry.id); else void execute(entry);
      });
      list.append(row);
    }
    list.scrollTop = previousScroll;
    empty.hidden = Boolean(filtered.length);
    empty.textContent = loading && !entries.length ? '正在读取命令…' : entries.length ? '未找到命令' : '尚未注册命令，点击“管理命令”添加';
    run.disabled = !selected;
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
        all.push(...(response.entries ?? [])); catalog = response.catalog; offset = response.next ?? 0;
      } while (offset);
      entries = all; results.retain(all.map(entry => entry.id)); say(''); paint();
    } catch (error) {
      if (!disposed) { entries = []; say(error.message); paint(); }
    } finally {
      loading = false;
      if (active() && !disposed) timer = setTimeout(refresh, 1500);
    }
  }
  async function execute(entry) {
    if (composing || !entry) return;
    const version = results.begin(entry.id);
    if (entry.state === 'missing') { results.finish(entry.id, version, '命令脚本缺失，请更新配置'); paint(); return; }
    try {
      const response = await client.call('launch', {command_id:entry.id, revision:entry.revision});
      if (!disposed && response.started && results.finish(entry.id, version)) paint();
    } catch (error) {
      if (disposed || !results.finish(entry.id, version, error.message)) return;
      paint();
      if (error.code === 'conflict' || error.code === 'not_found') void refresh();
    }
    input.focus();
  }
  async function settings(id = '') {
    try { await client.call('settings', {command_id:id}); }
    catch (error) { if (!disposed) say(error.message); }
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
  return {changed: textChanged, key, submit, dispose};
}
