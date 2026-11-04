import {localizeError, t} from './i18n.mjs';
import {runKey, runBusy, runStoppable, runTone, timeoutSeconds, summaryParts} from './command-run.mjs';
import {$, node, dialog} from './ui.mjs';
import {graphEditor} from './graph-editor.mjs';

const runLabel = run => t(runKey(run));

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
  const search = $('#commands-search');
  let catalog = {revision:'', commands:[]}, selected = '', dirty = false, busy = false, generation = 0;
  let picker = null, statusState = null, runStates = {}, stateTimer = null, stateLoading = false, managedAvailable=true;
  const field = name => form.elements.namedItem(name);
  const editor = graphEditor({container:$('#command-graph-editor'),onChange:()=>{dirty=true;},browse});

  function paintStatus(next = statusState) {
    statusState = next;
    if (!next) {
      status.textContent = '';
      return;
    }

    if (next.kind === 'error') {
      const detail = localizeError(next.error), args = next.error?.messageArgs || next.error?.message_args;
      const message = args?.graph_node ? t('err.graph.script', {node:args.graph_node, err:detail}) : detail;
      status.textContent = message + (next.conflict ? t('web.commands.conflict_suffix') : '');
      return;
    }
    status.textContent = t(statusKeys[next.kind] || '');
  }

  const canDiscard = () => !dirty || actions.confirm(t('web.commands.discard_confirm'));

  function paintFormTitle() {
    $('#command-form-title').textContent = selected ? t('web.commands.form_edit') : t('web.commands.form_new');
  }
  // keepView: after saving the same command, keep the graph's pan and zoom.
  function populate(command = {}, {keepView = false} = {}) {
    selected = command.id ?? '';
    for (const name of ['name', 'description']) field(name).value = command[name] ?? '';
    field('aliases').value = (command.aliases ?? []).join('\n');
    field('allow_concurrent').checked=command.allow_concurrent===true;
    editor.load(command.graph, {keepView});
    field('timeout').value=command.timeout_seconds ?? '';field('timeout_unit').value='1';
    $('#command-delete').hidden = !selected;
    paintFormTitle();
    dirty = false; paint();
  }
  function lockForm(){
    const locked=busy||runBusy(runStates[selected]);
    for(const control of form.querySelectorAll('button,input,textarea,select'))control.disabled=locked;
    editor.setLocked(locked);
    paintRunBar();
  }
  function paint() {
    const term = search.value.trim().toLocaleLowerCase('en-US');
    list.replaceChildren();
    const commands = [...catalog.commands].sort((a, b) => a.name.localeCompare(b.name, 'en-US'));
    for (const command of commands) {
      if (![command.name, ...(command.aliases ?? [])].some(name => name.toLocaleLowerCase('en-US').includes(term))) continue;
      const button = node('button', null, 'registered-command'); button.type = 'button';
      button.setAttribute('aria-pressed', String(command.id === selected));
      const run = runStates[command.id], summary = summaryParts(run, t).join(' · ');
      const title = node('strong'), dot = node('span', null, 'run-dot');
      dot.dataset.state = runTone(run); dot.hidden = !dot.dataset.state;
      if (summary) { dot.title = summary; dot.setAttribute('aria-label', summary); }
      title.append(dot, node('span', command.name));
      button.append(title, node('span', command.description || (command.aliases ?? []).join(' / ')));
      button.addEventListener('click', () => {if (!busy && canDiscard()) {populate(command); paintStatus(null);}});
      list.append(button);
    }
    if (!list.children.length) {
      list.append(node('p', catalog.commands.length ? t('web.commands.empty_no_match') : t('web.commands.empty_none')));
    }
    lockForm();
  }
  // The selected command's run state and actions sit above its form. Painted
  // after the form lock so running commands keep their log and stop actions.
  function paintRunBar() {
    const run = runStates[selected], bar = $('#command-run-bar');
    bar.hidden = !selected || !run?.run_id;
    if (bar.hidden) return;
    const state = $('#command-run-state');
    state.textContent = summaryParts(run, t).join(' · ');
    state.dataset.state = runTone(run);
    $('#command-logs').disabled = busy;
    $('#command-stop-all').hidden = !runBusy(run);
    $('#command-stop-all').disabled = busy || !runStoppable(run);
  }
  async function runAction(action) {
    const run = runStates[selected], id = selected;
    if (!id || !run?.run_id) return;
    const control = $(action === 'logs' ? '#command-logs' : '#command-stop-all');
    control.disabled = true;
    try { await post('/api/local-commands/'+action, {id, ...(action === 'stop-all' ? {run_ids:[...(run.active_run_ids ?? [])]} : {})}); }
    catch (error) { paintStatus({kind:'error', error}); }
    finally { void refreshStates(); }
  }
  async function refreshStates(){
    clearTimeout(stateTimer);stateTimer=null;
    if(stateLoading||!panel.open||!session()?.active)return;
    stateLoading=true;const scope=session(),version=generation;
    try{const result=await api('/api/local-commands/state');if(scope?.active&&version===generation){runStates=result.runs ?? {};managedAvailable=result.managed_available===true;paint();}}
    catch(error){if(scope?.active&&version===generation)paintStatus({kind:'error',error});}
    finally{stateLoading=false;if(scope?.active&&panel.open)stateTimer=setTimeout(refreshStates,1000);}
  }
  async function refresh(id = selected) {
    const scope = session(), version = ++generation;
    const result = await api('/api/local-commands');
    if (!scope?.active || version !== generation) return;
    catalog = result;
    const command = result.commands.find(value => value.id === id);
    populate(command);void refreshStates();
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
      name:field('name').value.trim(), aliases:field('aliases').value.split('\n').map(value=>value.trim()).filter(Boolean),
      description:field('description').value.trim(), allow_concurrent:field('allow_concurrent').checked,
      timeout_seconds:timeoutSeconds(field('timeout').value,field('timeout_unit').value), graph:editor.read(),
    };
  }
  function setBusy(value) {
    busy = value;
    for (const control of panel.querySelectorAll('button,input,textarea,select')) control.disabled = value;
    lockForm();
  }
  async function mutate(remove = false) {
    if (busy || !catalog.revision || runBusy(runStates[selected])) return;
    if (remove && !actions.confirm(t('web.commands.delete_confirm'))) return;
    const scope = session(), version = generation;
    let command;try{if(!remove)command=readForm();}catch(error){paintStatus({kind:'error',error});return;}
    setBusy(true); paintStatus({kind: 'saving'});
    try {
      const result = await post(remove ? '/api/local-commands/delete' : '/api/local-commands', remove ?
        {id:selected, revision:catalog.revision} : {revision:catalog.revision, command});
      if (!scope?.active || version !== generation) return;
      catalog = remove ? result : result.catalog;
      populate(remove ? undefined : catalog.commands.find(value => value.id === result.id), {keepView: !remove});
      paintStatus(remove ? {kind: 'deleted_done'} : {kind: 'saved_done'});
    } catch (error) {
      if (scope?.active && version === generation) paintStatus({kind: 'error', error, conflict: error.status === 409});
    } finally { if (version === generation) setBusy(false); }
  }
  async function browse(apply) {
    if (busy) return;
    const scope = session(), version = generation, request = new AbortController();
    picker = request; setBusy(true); paintStatus(null);
    try {
      const result = await api('/api/local-commands/browse', {method:'POST', body:'{}', signal:request.signal});
      if (!scope?.active || version !== generation) return;
      if (result.path) { apply(result.path); dirty = true; }
    } catch (error) {
      if (scope?.active && version === generation && !request.signal.aborted) paintStatus({kind: 'error', error, conflict: false});
    } finally {
      if (picker === request) picker = null;
      if (version === generation) setBusy(false);
    }
  }
  function relocalize() {
    paintFormTitle();
    editor.relocalize();
    paint();
    paintStatus();
  }
  listen($('#commands-open'), 'click', () => guarded(() => open()));
  listen($('#commands-refresh'), 'click', () => {if (!busy && canDiscard()) guarded(() => refresh());});
  listen($('#command-new'), 'click', () => {if (!busy && canDiscard()) {populate(); paintStatus(null);}});
  listen(form, 'input', () => {dirty = true;});
  listen(form, 'submit', event => {event.preventDefault(); void mutate();});
  listen($('#command-delete'), 'click', () => mutate(true));
  listen(search, 'input', paint);
  listen($('#command-logs'), 'click', () => runAction('logs'));
  listen($('#command-stop-all'), 'click', () => runAction('stop-all'));
  listen($('#commands-close'), 'click', () => {if (!busy && canDiscard()) panel.close();});
  listen(panel, 'cancel', event => {if (busy || !canDiscard()) event.preventDefault();});
  // Unfinished canvas gestures never outlive the dialog or the page session.
  listen(panel, 'close', () => editor.cancelInteraction());
  function stop() { clearTimeout(stateTimer);generation++; picker?.abort(); picker = null; editor.cancelInteraction(); setBusy(false); }
  return {open, relocalize, stop};
}
