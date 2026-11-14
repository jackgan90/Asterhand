import {t} from './i18n.mjs';
const states={started:'common.workspace.state.started',completed:'common.workspace.state.completed',failed:'common.workspace.state.failed',cancelled:'common.workspace.state.cancelled',unknown:'common.workspace.state.unknown',reconciled:'common.workspace.state.reconciled'};
const steps={pending:'common.workspace.step.pending',in_progress:'common.workspace.step.in_progress',completed:'common.workspace.step.completed'};

// The list holds only bounded metadata. Polling after a saved sequence also
// updates a row whose completion arrived after its start was displayed.
export function toolActivities(container, client) {
  let conversation = '', sequence = 0, generation = 0, pending = false;
  let rows = new Map();
  function paint() {
    if (!container) return;
    container.replaceChildren();
    container.hidden = rows.size === 0;
    for (const row of rows.values()) {
      const item = document.createElement('div');
      item.className = 'tool-activity';
      const title = document.createElement('span');
      title.textContent = `${row.tool} · ${t(states[row.state]??states.unknown)}`;
      item.append(title);
      if (row.progress?.length) {
        const progress = document.createElement('ul');
        const label = document.createElement('small');
        label.textContent = t('common.workspace.progress_declared');
        item.append(label,progress);
        for (const step of row.progress) {
          const line = document.createElement('li');
          line.textContent = `${step.step} · ${t(steps[step.status]??steps.pending)}`;
          progress.append(line);
        }
      }
      container.append(item);
    }
  }
  function select(id) { generation++; conversation=id; sequence=0; rows=new Map(); paint(); }
  async function refresh() {
    if (!container || !conversation || pending) return;
    const version=generation;
    pending=true;
    try {
      const result=await client.call('activities',{conversation_id:conversation,offset:sequence});
      if(version!==generation)return;
      const page=result.activities;
      if(!page?.authorized){rows.clear();paint();return;}
      for(const row of page.items??[]){rows.delete(row.id);rows.set(row.id,row);}
      while(rows.size>30)rows.delete(rows.keys().next().value);
      sequence=page.next??sequence;
      paint();
    } catch { /* Optional activity reads never alter send or turn state. */ }
    finally {pending=false;}
  }
  return {select,refresh,relocalize:paint};
}
