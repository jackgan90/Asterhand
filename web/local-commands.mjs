import {$, node, dialog} from './ui.mjs';

export function localCommands({api, post, session, listen, guarded, actions}) {
  const panel = $('#local-commands');
  if (!panel) return {open: async () => {}, stop() {}};
  const form = $('#command-form'), list = $('#registered-commands'), status = $('#commands-status');
  const argumentsList = $('#command-arguments'), search = $('#commands-search');
  let catalog = {revision:'', commands:[]}, selected = '', dirty = false, busy = false, generation = 0;
  let picker = null;
  const field = name => form.elements.namedItem(name);
  const message = text => {status.textContent = text;};
  const canDiscard = () => !dirty || actions.confirm('放弃尚未保存的命令修改？');

  function argumentRow(value = '') {
    const row = node('div', null, 'command-argument');
    const input = node('input'); input.value = value; input.setAttribute('aria-label', `参数 ${argumentsList.children.length + 1}`);
    const remove = node('button', '移除', 'quiet'); remove.type = 'button';
    remove.addEventListener('click', () => {row.remove(); dirty = true;});
    row.append(input, remove); argumentsList.append(row);
  }
  function populate(command = {}) {
    selected = command.id ?? '';
    for (const name of ['name', 'description', 'script', 'directory']) field(name).value = command[name] ?? '';
    field('aliases').value = (command.aliases ?? []).join('\n');
    field('window').value = command.window ?? 'background';
    argumentsList.replaceChildren();
    for (const value of command.arguments ?? []) argumentRow(value);
    $('#command-delete').hidden = !selected;
    $('#command-form-title').textContent = selected ? '编辑命令' : '新建命令';
    dirty = false; paint();
  }
  function paint() {
    const term = search.value.trim().toLocaleLowerCase('en-US');
    list.replaceChildren();
    const commands = [...catalog.commands].sort((a, b) => a.name.localeCompare(b.name, 'en-US'));
    for (const command of commands) {
      if (![command.name, ...(command.aliases ?? [])].some(name => name.toLocaleLowerCase('en-US').includes(term))) continue;
      const button = node('button', null, 'registered-command'); button.type = 'button';
      button.setAttribute('aria-pressed', String(command.id === selected));
      button.append(node('strong', command.name), node('span', (command.aliases ?? []).join(' / ')), node('small', command.script));
      button.addEventListener('click', () => {if (!busy && canDiscard()) {populate(command); message('');}});
      list.append(button);
    }
    if (!list.children.length) list.append(node('p', catalog.commands.length ? '未找到命令' : '尚未注册命令'));
    $('#command-count').textContent = `${catalog.commands.length} 个本地命令`;
  }
  async function refresh(id = selected) {
    const scope = session(), version = ++generation;
    const result = await api('/api/local-commands');
    if (!scope?.active || version !== generation) return;
    catalog = result;
    const command = result.commands.find(value => value.id === id);
    populate(command);
    message(id && !command ? '该命令已删除，可以新建命令。' : '');
  }
  async function open(id = '') {
    if (busy || panel.open && !canDiscard()) return;
    dialog('#local-commands');
    message('正在读取本机命令…');
    try { await refresh(id); } catch (error) { message(error.message); }
  }
  function readForm() {
    return {
      id:selected, revision:catalog.commands.find(value => value.id === selected)?.revision ?? '',
      name:field('name').value.trim(), aliases:field('aliases').value.split(/\r?\n/).map(value => value.trim()).filter(Boolean),
      description:field('description').value.trim(), script:field('script').value.trim(), directory:field('directory').value.trim(),
      arguments:[...argumentsList.querySelectorAll('input')].map(input => input.value), window:field('window').value,
    };
  }
  function setBusy(value) {
    busy = value;
    for (const control of panel.querySelectorAll('button,input,textarea,select')) control.disabled = value;
  }
  async function mutate(remove = false) {
    if (busy || !catalog.revision) return;
    if (remove && !actions.confirm('删除此命令及其所有别名？磁盘上的脚本文件会保留。')) return;
    const scope = session(), version = generation, command = readForm();
    setBusy(true); message('正在保存…');
    try {
      const result = await post(remove ? '/api/local-commands/delete' : '/api/local-commands', remove ?
        {id:selected, revision:catalog.revision} : {revision:catalog.revision, command});
      if (!scope?.active || version !== generation) return;
      catalog = remove ? result : result.catalog;
      populate(remove ? undefined : catalog.commands.find(value => value.id === result.id));
      message(remove ? '已删除注册，脚本文件保留。' : result.merged ? `启动配置相同，已合并为 ${field('name').value} 的别名。` : '已保存，快捷输入中立即可用。');
    } catch (error) {
      if (scope?.active && version === generation) message(error.message + (error.status === 409 ? ' 请先复制需要保留的修改，再点击刷新。' : ''));
    } finally { if (version === generation) setBusy(false); }
  }
  async function browse() {
    if (busy) return;
    const scope = session(), version = generation, request = new AbortController();
    picker = request; setBusy(true); message('');
    try {
      const result = await api('/api/local-commands/browse', {method:'POST', body:'{}', signal:request.signal});
      if (!scope?.active || version !== generation) return;
      if (result.path) { field('script').value = result.path; dirty = true; }
    } catch (error) {
      if (scope?.active && version === generation && !request.signal.aborted) message(error.message);
    } finally {
      if (picker === request) picker = null;
      if (version === generation) setBusy(false);
    }
  }
  listen($('#commands-open'), 'click', () => guarded(() => open()));
  listen($('#commands-refresh'), 'click', () => {if (!busy && canDiscard()) guarded(() => refresh());});
  listen($('#command-new'), 'click', () => {if (!busy && canDiscard()) {populate(); message('');}});
  listen($('#command-add-argument'), 'click', () => {argumentRow(); dirty = true;});
  listen(form, 'input', () => {dirty = true;});
  listen(form, 'change', () => {dirty = true;});
  listen(form, 'submit', event => {event.preventDefault(); void mutate();});
  listen($('#command-delete'), 'click', () => mutate(true));
  listen($('#command-browse'), 'click', browse);
  listen(search, 'input', paint);
  listen($('#commands-close'), 'click', () => {if (!busy && canDiscard()) panel.close();});
  listen(panel, 'cancel', event => {if (busy || !canDiscard()) event.preventDefault();});
  function stop() { generation++; picker?.abort(); picker = null; setBusy(false); }
  return {open, stop};
}
