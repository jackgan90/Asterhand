import {t,formatDateTime} from './i18n.mjs';
import {runLabel,reasonLabel} from './command-run.mjs';

export function logGraphView({byID,output,onPause}) {
  const panel=byID('log-graph'),nodes=byID('log-nodes'),left=byID('log-node-left'),right=byID('log-node-right');
  const second=byID('log-output-right'),layout=byID('log-columns');
  let record=null,chunks=[],identity='',nodeButtons=new Map();
  const nodeName=n=>n.kind==='start'?t('common.graph.start'):n.kind==='end'?t('common.graph.end'):n.name;
  function label(n){
    if(n.phase==='waiting')return t('common.graph.waiting');
    if(n.phase==='ready')return t('common.graph.ready');
    if(n.result==='skipped')return t('common.graph.skipped');
    return runLabel(n);
  }
  function options(select,first,firstKey){
    const selected=select.value,existing=new Map([...select.options].map(o=>[o.value,o]));
    const values=[[first,t(firstKey)],...(record?.nodes??[]).filter(n=>n.kind==='script').map(n=>[n.node_id,nodeName(n)])];
    const ids=new Set(values.map(([id])=>id));
    for(const [id,option] of existing)if(!ids.has(id))option.remove();
    for(const [id,name] of values){
      let option=existing.get(id);if(!option){option=document.createElement('option');option.value=id;select.append(option);}option.textContent=name;
    }
    select.value=ids.has(selected)?selected:first;
  }
  function renderMetadata(next){
    record=next;panel.hidden=record?.command?.kind!=='graph';
    if(identity!==record?.run_id){identity=record?.run_id??'';chunks=[];nodes.replaceChildren();nodeButtons=new Map();left.value='';right.value='';}
    options(left,'','common.graph.all');options(right,'','common.graph.none');
    for(const n of record?.nodes??[]){
      let button=nodeButtons.get(n.node_id);
      if(!button){button=document.createElement('button');button.type='button';button.onclick=()=>{if(n.kind==='script'){left.value=n.node_id;render();}};nodes.append(button);nodeButtons.set(n.node_id,button);}
      button.textContent=t('common.graph.node_line',{name:nodeName(n),state:label(n)});
      button.dataset.result=n.result??'';button.dataset.failed=String(n.node_id===record.failed_node_id);
      const details=[reasonLabel(n)];
      if(n.started_at)details.push(t('common.command.started',{time:formatDateTime(n.started_at)}));
      if(n.ended_at)details.push(t('common.command.ended',{time:formatDateTime(n.ended_at)}));
      details.push(t('common.command.elapsed',{seconds:n.elapsed_seconds.toFixed(1)}));
      if(n.exit?.code!==undefined)details.push(t('common.command.exit_code',{code:n.exit.code}));
      button.title=details.filter(Boolean).join(' · ');
    }
    second.hidden=!right.value;layout.dataset.split=String(Boolean(right.value));
  }
  function appendTo(target,items,filter){
    const fragment=document.createDocumentFragment();
    for(const chunk of items){
      if(!chunk.text||filter&&chunk.node_id!==filter)continue;
      if(record?.command?.kind==='graph'){
        const n=record.nodes.find(n=>n.node_id===chunk.node_id);
        const source=document.createElement('span');source.className='log-source';source.textContent=t('common.graph.log_prefix',{name:n?nodeName(n):chunk.node_id});fragment.append(source);
      }
      const span=document.createElement('span');span.dataset.stream=chunk.stream;span.textContent=chunk.text;fragment.append(span);
    }
    target.append(fragment);
  }
  function render(){
    const top=output.scrollTop,other=second.scrollTop;
    output.replaceChildren();second.replaceChildren();appendTo(output,chunks,left.value);
    if(right.value)appendTo(second,chunks,right.value);
    second.hidden=!right.value;layout.dataset.split=String(Boolean(right.value));
    output.scrollTop=top;second.scrollTop=other;
  }
  left.onchange=render;right.onchange=render;
  second.addEventListener('wheel',event=>{if(event.deltaY<0)onPause();},{passive:true});
  second.addEventListener('scroll',()=>{if(second.scrollHeight-second.scrollTop-second.clientHeight>40)onPause();},{passive:true});
  return {
    metadata:renderMetadata,
    reset(){chunks=[];output.replaceChildren();second.replaceChildren();},
    append(items){chunks.push(...items);appendTo(output,items,left.value);if(right.value)appendTo(second,items,right.value);},
    follow(){output.scrollTop=output.scrollHeight;second.scrollTop=second.scrollHeight;},
    relocalize(){renderMetadata(record);render();},
    get chunkCount(){return chunks.length;},
  };
}
