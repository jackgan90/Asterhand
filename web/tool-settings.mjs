import {$} from './ui.mjs';
import {t} from './i18n.mjs';

// Permission has its own revision and applies immediately. It never submits
// or clears the model/provider fields being edited in the settings form.
export function toolSettings({api,post,listen,session}) {
  const input=$('#assistant-tools-enabled'),status=$('#assistant-tools-status'),dialog=$('#settings');
  const trustBox=$('#assistant-tools-trust'),modeStandard=$('#assistant-tools-mode-standard'),modeTurbo=$('#assistant-tools-mode-turbo');
  const allowedBox=$('#assistant-tools-allowed'),allowedList=$('#assistant-tools-allowed-list');
  let state=null,generation=0,loading=false,saving=false,toggling=false,desired=false,savingMode='';
  let message='web.settings.tools.hint';
  const rows=new Map();
  const current=(scope,version)=>scope?.active&&version===generation;
  const stateMessage=()=>!state?.supported?'web.settings.tools.unsupported':
    state.authorized&&!state.ready?(state.unavailable_reason==='reconciliation_required'?
      'web.settings.tools.reconciliation':'web.settings.tools.unavailable'):'web.settings.tools.hint';
  function rowNode(item){
    let row=rows.get(item.id);
    if(!row){
      const li=document.createElement('li');
      li.className='tools-allowed-item';
      const label=document.createElement('span');
      const btn=document.createElement('button');
      btn.type='button';
      btn.className='secondary';
      btn.dataset.removeRule=item.id;
      btn.addEventListener('click',()=>void setTrust({remove_rule_id:item.id}));
      li.append(label,btn);
      row={li,label,btn};
      rows.set(item.id,row);
    }
    const labelText=t(item.kind==='folder'?'web.settings.tools.allowed_folder':'web.settings.tools.allowed_command',{label:item.label});
    if(row.label.textContent!==labelText)row.label.textContent=labelText;
    const btnText=t('web.settings.tools.remove_rule');
    if(row.btn.textContent!==btnText)row.btn.textContent=btnText;
    row.btn.disabled=loading||saving;
    return row.li;
  }
  function renderAllowed(authorized){
    if(!allowedBox||!allowedList)return;
    const items=Array.isArray(state?.allowed_items)?state.allowed_items:[];
    const ids=new Set(items.map(item=>item.id));
    for(const id of [...rows.keys()])if(!ids.has(id))rows.delete(id);
    allowedBox.hidden=!authorized||items.length===0;
    const nodes=items.map(rowNode);
    const sameOrder=allowedList.children.length===nodes.length&&nodes.every((el,idx)=>allowedList.children[idx]===el);
    if(!sameOrder)allowedList.replaceChildren(...nodes);
  }
  function paint(){
    const authorized=toggling?desired:state?.authorized===true;
    input.checked=authorized;
    input.indeterminate=!state&&!saving;
    input.disabled=loading||saving||!state?.supported;
    const mode=(savingMode||state?.execution_mode)==='turbo'?'turbo':'standard';
    if(trustBox)trustBox.hidden=!state?.supported||!authorized;
    if(modeStandard){
      modeStandard.checked=mode==='standard';
      modeStandard.disabled=loading||saving||!state?.supported||!authorized;
    }
    if(modeTurbo){
      modeTurbo.checked=mode==='turbo';
      modeTurbo.disabled=loading||saving||!state?.supported||!authorized;
    }
    renderAllowed(authorized);
    const key=saving?'web.settings.tools.saving':loading?'web.settings.tools.loading':message;
    status.textContent=t(key);
    if(!loading&&!saving&&key!=='web.settings.tools.hint')status.dataset.tone='error';
    else delete status.dataset.tone;
  }
  async function open(){
    const scope=session(),version=++generation;
    loading=true;paint();
    try{
      const latest=await api('/api/workspace/permission');
      if(!current(scope,version))return;
      state=latest;message=stateMessage();
    }catch{
      if(!current(scope,version))return;
      state=null;message='web.settings.tools.load_failed';
    }finally{
      if(current(scope,version)){loading=false;paint();}
    }
  }
  function failureMessage(error){
    if(error.code==='busy'||error.messageKey==='err.workspace.busy'||error.messageKey==='err.conv.busy_before_model_settings')return 'web.settings.tools.busy';
    if(error.code==='revision_conflict'||error.messageKey==='err.workspace.conflict')return 'web.settings.tools.conflict';
    return 'web.settings.tools.save_failed';
  }
  async function toggle(){
    const scope=session();
    if(!scope?.active||saving||loading||!state?.supported){paint();return;}
    const version=++generation;
    desired=input.checked;saving=true;toggling=true;paint();
    try{
      const latest=await post('/api/workspace/permission',{enabled:desired,expected_revision:state.policy_revision??''});
      if(current(scope,version)){state=latest;message=stateMessage();}
    }catch(error){
      // A lost response may already have changed permission. Read the current
      // value instead of presenting the old checkbox as confirmed state.
      try{
        const latest=await api('/api/workspace/permission');
        if(current(scope,version)){
          state=latest;
          message=state.authorized===desired?stateMessage():failureMessage(error);
        }
      }catch{
        if(current(scope,version)){state=null;message='web.settings.tools.unconfirmed';}
      }
    }finally{
      saving=false;toggling=false;
      if(current(scope,version))paint();
      else if(session()?.active&&dialog.open)void open();
    }
  }
  async function setTrust(payload, nextMode = ''){
    const scope=session();
    if(!scope?.active||saving||loading||!state?.supported||!state?.authorized){paint();return;}
    const version=++generation;
    saving=true;savingMode=nextMode;paint();
    try{
      const latest=await post('/api/workspace/permission',payload);
      if(current(scope,version)){state=latest;message=stateMessage();}
    }catch(error){
      try{
        const latest=await api('/api/workspace/permission');
        if(current(scope,version)){state=latest;message=failureMessage(error);}
      }catch{
        if(current(scope,version)){message='web.settings.tools.unconfirmed';}
      }
    }finally{
      saving=false;savingMode='';
      if(current(scope,version))paint();
      else if(session()?.active&&dialog.open)void open();
    }
  }
  function stop(){generation++;loading=false;toggling=false;savingMode='';}
  listen(input,'change',()=>void toggle());
  if(modeStandard)listen(modeStandard,'change',()=>{if(modeStandard.checked)void setTrust({execution_mode:'standard'},'standard');});
  if(modeTurbo)listen(modeTurbo,'change',()=>{if(modeTurbo.checked)void setTrust({execution_mode:'turbo'},'turbo');});
  listen(dialog,'close',stop);
  return {open,stop,relocalize:paint};
}
