import {localizeError, t} from './i18n.mjs';
import {$, node, dialog} from './ui.mjs';

const statusKeys = {
  deleted_notice: 'web.commands.deleted_notice',
  loading: 'web.commands.loading',
  saving: 'web.commands.saving',
  deleted_done: 'web.commands.deleted_done',
  saved_done: 'web.commands.saved_done',
};

export function localCommands({api, post, session, listen, guarded, actions}) {
  const panel = $('#local-commands');
  if (!panel) return {open: async () => {}, relocalize() {}, stop() {}};
  const form = $('#command-form'), list = $('#registered-commands'), status = $('#commands-status');
  const argumentsList = $('#command-arguments'), search = $('#commands-search');
  let catalog = {revision:'', commands:[]}, selected = '', dirty = false, busy = false, generation = 0;
  let picker = null, statusState = null;
  const argRows = [];
  const field = name => form.elements.namedItem(name);

  function paintStatus(next = statusState) {
    statusState = next;
    if (!next) {
      status.textContent = '';
      return;
    }
    if (next.kind === 'merged_done') {
      status.textContent = t('web.commands.merged_done', {name: next.name});
      return;
    }
    if (next.kind === 'error') {
      status.textContent = localizeError(next.error) + (next.conflict ? t('web.commands.conflict_suffix') : '');
      return;
    }
    status.textContent = t(statusKeys[next.kind] || '');
  }

  const canDiscard = () => !dirty || actions.confirm(t('web.commands.discard_confirm'));

  function syncArgumentLabels() {
    argRows.forEach((entry, idx) => {
      entry.input.setAttribute('aria-label', t('web.commands.arg_aria', {index: idx + 1}));
      entry.remove.textContent = t('web.commands.arg_remove');
    });
  }

  function argumentRow(value = '') {
    const row = node('div', null, 'command-argument');
    const input = node('input'); input.value = value;
    const remove = node('button', t('web.commands.arg_remove'), 'quiet'); remove.type = 'button';
    const entry = {row, input, remove};
    argRows.push(entry);
    syncArgumentLabels();
    remove.addEventListener('click', () => {
      row.remove();
      const idx = argRows.indexOf(entry);
      if (idx >= 0) argRows.splice(idx, 1);
      syncArgumentLabels();
      dirty = true;
    });
    row.append(input, remove); argumentsList.append(row);
  }
  function paintFormTitle() {
    $('#command-form-title').textContent = selected ? t('web.commands.form_edit') : t('web.commands.form_new');
  }
  function populate(command = {}) {
    selected = command.id ?? '';
    for (const name of ['name', 'description', 'script', 'directory']) field(name).value = command[name] ?? '';
    field('aliases').value = (command.aliases ?? []).join('\n');
    field('window').value = command.window ?? 'background';
    argumentsList.replaceChildren();
    argRows.length = 0;
    for (const value of command.arguments ?? []) argumentRow(value);
    $('#command-delete').hidden = !selected;
    paintFormTitle();
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
      button.addEventListener('click', () => {if (!busy && canDiscard()) {populate(command); paintStatus(null);}});
      list.append(button);
    }
    if (!list.children.length) {
      list.append(node('p', catalog.commands.length ? t('web.commands.empty_no_match') : t('web.commands.empty_none')));
    }
    $('#command-count').textContent = t('web.commands.count', {count: catalog.commands.length});
  }
  async function refresh(id = selected) {
    const scope = session(), version = ++generation;
    const result = await api('/api/local-commands');
    if (!scope?.active || version !== generation) return;
    catalog = result;
    const command = result.commands.find(value => value.id === id);
    populate(command);
    paintStatus(id && !command ? {kind: 'deleted_notice'} : null);
  }
  async function open(id = '') {
    if (busy || panel.open && !canDiscard()) return;
    dialog('#local-commands');
    paintStatus({kind: 'loading'});
    try { await refresh(id); } catch (error) { paintStatus({kind: 'error', error, conflict: false}); }
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
    if (remove && !actions.confirm(t('web.commands.delete_confirm'))) return;
    const scope = session(), version = generation, command = readForm();
    setBusy(true); paintStatus({kind: 'saving'});
    try {
      const result = await post(remove ? '/api/local-commands/delete' : '/api/local-commands', remove ?
        {id:selected, revision:catalog.revision} : {revision:catalog.revision, command});
      if (!scope?.active || version !== generation) return;
      catalog = remove ? result : result.catalog;
      populate(remove ? undefined : catalog.commands.find(value => value.id === result.id));
      paintStatus(remove ? {kind: 'deleted_done'} : result.merged ? {kind: 'merged_done', name: field('name').value} : {kind: 'saved_done'});
    } catch (error) {
      if (scope?.active && version === generation) paintStatus({kind: 'error', error, conflict: error.status === 409});
    } finally { if (version === generation) setBusy(false); }
  }
  async function browse() {
    if (busy) return;
    const scope = session(), version = generation, request = new AbortController();
    picker = request; setBusy(true); paintStatus(null);
    try {
      const result = await api('/api/local-commands/browse', {method:'POST', body:'{}', signal:request.signal});
      if (!scope?.active || version !== generation) return;
      if (result.path) { field('script').value = result.path; dirty = true; }
    } catch (error) {
      if (scope?.active && version === generation && !request.signal.aborted) paintStatus({kind: 'error', error, conflict: false});
    } finally {
      if (picker === request) picker = null;
      if (version === generation) setBusy(false);
    }
  }
  function relocalize() {
    paintFormTitle();
    syncArgumentLabels();
    paint();
    paintStatus();
  }
  listen($('#commands-open'), 'click', () => guarded(() => open()));
  listen($('#commands-refresh'), 'click', () => {if (!busy && canDiscard()) guarded(() => refresh());});
  listen($('#command-new'), 'click', () => {if (!busy && canDiscard()) {populate(); paintStatus(null);}});
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
  return {open, relocalize, stop};
}
