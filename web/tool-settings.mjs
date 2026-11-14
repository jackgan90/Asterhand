import {$} from './ui.mjs';
import {t} from './i18n.mjs';

// Permission has its own revision and applies immediately. It never submits
// or clears the model/provider fields being edited in the settings form.
export function toolSettings({api,post,listen,session}) {
  const input=$('#assistant-tools-enabled'),status=$('#assistant-tools-status'),dialog=$('#settings');
  let state=null,generation=0,loading=false,saving=false,desired=false;
  let message='web.settings.tools.hint';
  const current=(scope,version)=>scope?.active&&version===generation;
  const stateMessage=()=>!state?.supported?'web.settings.tools.unsupported':
    state.authorized&&!state.ready?(state.unavailable_reason==='reconciliation_required'?
      'web.settings.tools.reconciliation':'web.settings.tools.unavailable'):'web.settings.tools.hint';
  function paint(){
    input.checked=saving?desired:state?.authorized===true;
    input.indeterminate=!state&&!saving;
    input.disabled=loading||saving||!state?.supported;
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
    desired=input.checked;saving=true;paint();
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
      saving=false;
      if(current(scope,version))paint();
      else if(session()?.active&&dialog.open)void open();
    }
  }
  function stop(){generation++;loading=false;}
  listen(input,'change',()=>void toggle());
  listen(dialog,'close',stop);
  return {open,stop,relocalize:paint};
}
