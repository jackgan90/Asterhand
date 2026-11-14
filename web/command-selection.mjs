import {$, node} from './ui.mjs';
import {localizeError, t} from './i18n.mjs';

// Selection and deletion receipts are independent of the command editor draft.
export function commandSelection({post, confirm, locked, lock, beforeDelete, deleted, settled, active}) {
  const chosen = new Set(), pending = new Map();
  const all = $('#commands-select-all'), count = $('#commands-selection-count');
  const remove = $('#commands-delete-selected'), output = $('#commands-delete-results');
  const manage = $('#commands-manage'), toolbar = $('#commands-selection');
  let commands = [], outcomes = [], running = false, managing = false, epoch = 0;
  let renderedResults = '';

  function paint() {
    manage.dataset.i18n = managing ? 'web.commands.manage_done' : 'web.commands.manage';
    manage.textContent = t(manage.dataset.i18n);
    manage.setAttribute('aria-expanded', String(managing));
    manage.className = managing ? '' : 'secondary';
    manage.disabled = locked();
    toolbar.hidden = !managing;
    output.hidden = !managing || !outcomes.length;
    for (const input of $('#registered-commands').querySelectorAll('input')) {
      input.hidden = !managing;
      input.checked = chosen.has(input.dataset.commandSelection);
      input.disabled = !managing || locked();
    }
    all.checked = commands.length > 0 && chosen.size === commands.length;
    all.indeterminate = chosen.size > 0 && !all.checked;
    all.disabled = !managing || locked() || !commands.length;
    remove.disabled = !managing || locked() || !chosen.size;
    count.textContent = t('web.commands.selected_count', {count:chosen.size});
    const messages = outcomes.map(outcome => {
      const message = outcome.state === 'not_sent' ? t('web.commands.delete_not_sent') :
        outcome.error ? localizeError(outcome.error) : t('web.commands.delete_unknown');
      return `${outcome.name}: ${message}`;
    });
    const signature = JSON.stringify(messages);
    if (signature !== renderedResults) {
      renderedResults = signature;
      output.replaceChildren(...messages.map(message => node('p', message)));
    }
  }
  function setManaging(value) {
    if (running || locked()) return;
    managing = value;
    if (!managing) chosen.clear();
    // Keep unresolved receipts so a later explicit retry uses the same identity.
    paint();
  }
  function sync(values) {
    commands = values;
    const ids = new Set(values.map(command => command.id));
    for (const id of chosen) if (!ids.has(id)) chosen.delete(id);
    paint();
  }
  function checkbox(command) {
    const input = node('input'); input.type = 'checkbox';
    input.dataset.commandSelection = command.id;
    input.hidden = !managing;
    input.checked = chosen.has(command.id); input.disabled = !managing || locked();
    input.setAttribute('aria-label', t('web.commands.select_command', {name:command.name}));
    input.addEventListener('change', () => {
      if (!managing || locked()) return;
      if (input.checked) chosen.add(command.id); else chosen.delete(command.id);
      paint();
    });
    return input;
  }
  function selectAll() {
    if (!managing || locked()) return;
    chosen.clear();
    if (all.checked) for (const command of commands) chosen.add(command.id);
    paint();
  }
  async function submit() {
    if (!managing || running || locked() || !chosen.size) return;
    if (!confirm(t('web.commands.delete_selected_confirm', {count:chosen.size})) || !beforeDelete(chosen)) return;
    const version = epoch, valid = () => version === epoch && active();
    const items = commands.filter(command => chosen.has(command.id)).map(command => pending.get(command.id) || {
      command_id:command.id, expected_revision:command.revision,
      operation_id:`${Date.now()}_${crypto.randomUUID().replaceAll('-', '')}`,
    });
    const names = new Map(commands.map(command => [command.id, command.name]));
    outcomes = []; running = true; lock(true); paint();
    try {
      for (let offset = 0; offset < items.length && valid(); offset += 32) {
        const chunk = items.slice(offset, offset + 32);
        for (const item of chunk) pending.set(item.command_id, item);
        let response;
        try {
          response = await post('/api/local-commands/delete-batch', {items:chunk});
          if (!Array.isArray(response?.results) || response.results.length !== chunk.length) throw new Error('invalid_batch_response');
        }
        catch (error) {
          if (valid()) {
            for (const item of chunk) outcomes.push({name:names.get(item.command_id), state:'unknown'});
            for (const item of items.slice(offset + chunk.length)) outcomes.push({name:names.get(item.command_id), state:'not_sent'});
          }
          // Unsent chunks remain selected. Unknown items retain their original IDs.
          break;
        }
        if (!valid()) return;
        const removed = [];
        for (let index = 0; index < chunk.length; index++) {
          const item = chunk[index], result = response.results?.[index];
          const success = result?.receipt?.state === 'completed' && !result.error;
          const rejected = result?.receipt?.state === 'rejected' || result?.error && !result.receipt;
          if (success || rejected) pending.delete(item.command_id);
          if (!success) outcomes.push({name:names.get(item.command_id), state:'unknown', error:result?.error});
          if (success) { chosen.delete(item.command_id); removed.push(item.command_id); }
        }
        if (removed.length) deleted(removed);
        paint();
      }
      if (valid()) await settled();
    } finally {
      if (valid()) { running = false; lock(false); paint(); }
    }
  }
  return {sync, checkbox, paint, selectAll, submit, toggle:()=>setManaging(!managing), leave:()=>setManaging(false),
    stop() { epoch++; running = false; managing = false; chosen.clear(); pending.clear(); outcomes = []; }};
}
