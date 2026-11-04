import {t} from './i18n.mjs';
import {node} from './ui.mjs';

// Right-hand panel for the selected script step. Inputs are created once and
// only refilled on selection, so unsaved values, focus and selection survive
// language changes.
export function graphInspector({text, button, label, onEdit, onBrowse, onFinish = () => {}}) {
  const element = node('aside', null, 'graph-inspector');
  const empty = text('p', 'web.graph.empty_selection', 'graph-inspector-empty');
  const properties = node('fieldset', null, 'graph-properties'), title = text('legend', 'web.graph.properties');
  const name = node('input'), script = node('input'), directory = node('input'), timeout = node('input');
  name.maxLength = 128; script.placeholder = 'C:\\Scripts\\step.ps1'; timeout.type = 'number'; timeout.min = '1';
  const path = node('div', null, 'command-script-path'), pick = button('web.commands.browse');
  path.append(script, pick);
  const more = node('details', null, 'graph-more'), summary = text('summary', 'web.graph.more');
  const args = node('div'), addArg = button('web.commands.arg_add'), rows = [];
  more.append(summary, label('web.commands.dir_label', directory), label('web.graph.node_timeout', timeout), label('web.commands.args_label', args), addArg);
  properties.append(title, label('web.graph.node_name', name), label('web.commands.script_label', path), more);
  element.append(empty, properties);
  let current = null, locked = false;
  // "More settings" keeps the state the user chose while switching steps; a
  // step that already uses these settings always shows them.
  let keepMoreOpen = false;
  summary.addEventListener('click', () => { keepMoreOpen = !more.open; });

  const values = () => rows.map(row => row.input.value);
  function argument(value = '') {
    const row = node('div', null, 'command-argument'), input = node('input'), remove = node('button', t('web.commands.arg_remove'), 'quiet');
    remove.type = 'button'; input.value = value; input.disabled = locked; remove.disabled = locked;
    row.append(input, remove); args.append(row);
    const entry = {row, input, remove}; rows.push(entry);
    input.oninput = () => { if (!locked) onEdit('arguments', values(), input); };
    input.addEventListener('blur', onFinish);
    remove.onclick = () => {
      if (locked) return;
      onFinish(); rows.splice(rows.indexOf(entry), 1); row.remove(); onEdit('arguments', values()); onFinish();
    };
  }
  for (const [input, field] of [[name, 'name'], [script, 'script'], [directory, 'directory']]) {
    input.oninput = () => { if (!locked) onEdit(field, input.value, input); };
    input.addEventListener('blur', onFinish);
  }
  timeout.oninput = () => { if (!locked) onEdit('timeout_seconds', timeout.value.trim() === '' ? null : Number(timeout.value), timeout); };
  timeout.addEventListener('blur', onFinish);
  pick.onclick = () => { if (!locked) onBrowse(); };
  addArg.onclick = () => { if (!locked) { onFinish(); argument(); onEdit('arguments', values()); onFinish(); } };

  function applyLock() {
    for (const control of [name, script, directory, timeout, pick, addArg]) control.disabled = locked;
    for (const row of rows) { row.input.disabled = locked; row.remove.disabled = locked; }
  }
  return {
    element,
    // Refills inputs only when a different step is selected.
    show(step, refill = false) {
      const isScript = step?.kind === 'script';
      properties.hidden = !isScript;
      empty.hidden = isScript;
      if (!isScript) { current = null; return; }
      if (current === step.id && !refill) return;
      current = step.id;
      name.value = step.name; script.value = step.script ?? ''; directory.value = step.directory ?? '';
      timeout.value = step.timeout_seconds ?? '';
      more.open = keepMoreOpen || Boolean(step.directory || step.timeout_seconds || step.arguments?.length);
      args.replaceChildren(); rows.length = 0;
      for (const value of step.arguments ?? []) argument(value);
    },
    setScript(value) { script.value = value; },
    focusName() { name.focus?.(); name.select?.(); },
    setLocked(value) { locked = value; applyLock(); },
    relocalize() { for (const row of rows) row.remove.textContent = t('web.commands.arg_remove'); },
  };
}
