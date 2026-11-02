import {t,localizeError} from './i18n.mjs';
import {node} from './ui.mjs';
import {timeoutSeconds} from './command-run.mjs';
import {copyGraph,newGraph,edgeIssue,graphIssue,removeGraphNode,graphLimits} from './graph-model.mjs';

export function graphEditor({container,onChange,browse,getCommands}) {
  let graph=null,selected='',locked=false,error=null;
  const labels=[],buttons=new Map(),argumentRows=[];
  const text=(tag,key,className)=>{const el=node(tag,t(key),className);labels.push([el,key]);return el;};
  const button=key=>{const el=text('button',key,'quiet');el.type='button';return el;};
  const label=(key,control)=>{const el=node('label',null,'field');el.append(text('span',key),control);return el;};
  const nameOf=n=>n.kind==='start'?t('common.graph.start'):n.kind==='end'?t('common.graph.end'):n.name||t('common.graph.unnamed');
  const controls=node('div',null,'actions compact');
  const add=button('web.graph.add'),remove=button('web.graph.remove'),parallel=node('input');
  parallel.type='number';parallel.min='1';parallel.max=String(graphLimits.parallelism);
  controls.append(add,remove,label('web.graph.parallelism',parallel));
  const viewport=node('div',null,'graph-viewport'),stage=node('div',null,'graph-stage');
  const svg=document.createElementNS('http://www.w3.org/2000/svg','svg');svg.classList.add('graph-edges');svg.setAttribute('aria-hidden','true');
  stage.append(svg);viewport.append(stage);
  const links=node('div',null,'graph-links'),target=node('select'),edgeList=node('select');
  const connect=button('web.graph.connect'),disconnect=button('web.graph.disconnect');
  links.append(label('web.graph.target',target),connect,label('web.graph.edges',edgeList),disconnect);
  const properties=node('fieldset',null,'graph-properties'),title=text('legend','web.graph.properties');
  const name=node('input'),script=node('input'),directory=node('input'),timeout=node('input');
  name.maxLength=128;script.placeholder='C:\\Scripts\\step.ps1';timeout.type='number';timeout.min='1';
  const path=node('div',null,'command-script-path'),pick=button('web.commands.browse');path.append(script,pick);
  const args=node('div'),addArg=button('web.commands.arg_add');
  const templates=node('select'),copy=button('web.graph.copy');
  const templateRow=node('div',null,'graph-copy');templateRow.append(label('web.graph.template',templates),copy);
  properties.append(title,label('web.graph.node_name',name),label('web.commands.script_label',path),label('web.commands.dir_label',directory),
    label('web.graph.node_timeout',timeout),label('web.commands.args_label',args),addArg,templateRow);
  const hint=text('p','web.graph.hint','graph-hint'),message=node('p',null,'graph-error');message.setAttribute('role','status');
  container.append(controls,hint,viewport,links,properties,message);

  function changed(){onChange();error=null;paint();}
  function option(select,value,labelText){const el=node('option',labelText);el.value=value;select.append(el);}
  function replaceOptions(select,entries){
    const value=select.value;select.replaceChildren();
    for(const [id,labelText] of entries)option(select,id,labelText);
    if(entries.some(([id])=>id===value))select.value=value;
  }
  function drawEdges(){
    svg.replaceChildren();
    const definitions=document.createElementNS(svg.namespaceURI,'defs'),marker=document.createElementNS(svg.namespaceURI,'marker');
    for(const [key,value] of Object.entries({id:'graph-arrow',viewBox:'0 0 10 10',refX:'9',refY:'5',markerWidth:'6',markerHeight:'6',orient:'auto-start-reverse'}))marker.setAttribute(key,value);
    const arrow=document.createElementNS(svg.namespaceURI,'path');arrow.setAttribute('d','M 0 0 L 10 5 L 0 10 z');marker.append(arrow);definitions.append(marker);svg.append(definitions);
    for(const edge of graph.edges){
      const from=graph.nodes.find(n=>n.id===edge.from),to=graph.nodes.find(n=>n.id===edge.to);if(!from||!to)continue;
      const path=document.createElementNS(svg.namespaceURI,'path');
      const x=from.x+180,y=from.y+28,tx=to.x,ty=to.y+28,bend=Math.max(60,Math.abs(tx-x)/2);
      path.setAttribute('d',`M ${x} ${y} C ${x+bend} ${y}, ${tx-bend} ${ty}, ${tx} ${ty}`);
      path.setAttribute('marker-end','url(#graph-arrow)');path.classList.add('graph-edge');svg.append(path);
    }
  }
  function nodeButton(n){
    const el=node('button',null,'graph-node');el.type='button';
    el.onclick=()=>{if(!locked)select(n.id);};
    el.onpointerdown=event=>{
      if(locked||event.button!==0)return;
      select(n.id);
      const initial={x:event.clientX,y:event.clientY,nx:n.x,ny:n.y};
      el.setPointerCapture(event.pointerId);
      el.onpointermove=move=>{
        if(locked)return;
        n.x=Math.min(8000,Math.max(0,Math.round(initial.nx+move.clientX-initial.x)));
        n.y=Math.min(8000,Math.max(0,Math.round(initial.ny+move.clientY-initial.y)));
        changed();
      };
      const end=()=>{el.onpointermove=null;el.onpointerup=null;el.onpointercancel=null;};
      el.onpointerup=end;el.onpointercancel=end;
    };
    return el;
  }
  function paint(){
    if(!graph)return;
    const ids=new Set(graph.nodes.map(n=>n.id));
    for(const [id,el] of buttons)if(!ids.has(id)){el.remove();buttons.delete(id);}
    for(const n of graph.nodes){
      if(!buttons.has(n.id)){const el=nodeButton(n);buttons.set(n.id,el);stage.append(el);}
      const el=buttons.get(n.id);el.textContent=nameOf(n);el.style.left=`${n.x}px`;el.style.top=`${n.y}px`;
      el.dataset.kind=n.kind;el.setAttribute('aria-pressed',String(selected===n.id));el.disabled=locked;
    }
    stage.style.width=`${Math.max(780,...graph.nodes.map(n=>n.x+240))}px`;
    stage.style.height=`${Math.max(280,...graph.nodes.map(n=>n.y+100))}px`;
    drawEdges();
    replaceOptions(target,graph.nodes.filter(n=>n.id!==selected&&n.kind!=='start').map(n=>[n.id,nameOf(n)]));
    replaceOptions(edgeList,graph.edges.map((e,i)=>[String(i),t('web.graph.edge_label',{from:nameOf(graph.nodes.find(n=>n.id===e.from)),to:nameOf(graph.nodes.find(n=>n.id===e.to))})]));
    message.textContent=error?localizeError(error):'';
    applyLock();
  }
  function argument(value=''){
    const row=node('div',null,'command-argument'),input=node('input'),remove=node('button',t('web.commands.arg_remove'),'quiet');remove.type='button';
    input.value=value;row.append(input,remove);args.append(row);argumentRows.push({row,input,remove});
    const update=()=>{graph.nodes.find(n=>n.id===selected).arguments=argumentRows.map(a=>a.input.value);changed();};
    input.oninput=update;
    remove.onclick=()=>{if(locked)return;argumentRows.splice(argumentRows.findIndex(a=>a.row===row),1);row.remove();update();};
  }
  function select(id){
    selected=id;const n=graph.nodes.find(n=>n.id===id);
    name.value=n.name;script.value=n.script??'';directory.value=n.directory??'';timeout.value=n.timeout_seconds??'';
    args.replaceChildren();argumentRows.length=0;for(const value of n.arguments??[])argument(value);
    refreshTemplates();paint();
  }
  function refreshTemplates(){
    replaceOptions(templates,getCommands().filter(c=>c.kind!=='graph').map(c=>[c.id,c.name]));
  }
  function applyLock(){
    const n=graph?.nodes.find(n=>n.id===selected);
    for(const el of container.querySelectorAll('button,input,select,textarea'))el.disabled=locked;
    properties.disabled=locked||n?.kind!=='script';
    remove.disabled=locked||n?.kind!=='script';add.disabled=locked||(graph?.nodes.length??0)>=graphLimits.nodes;
    connect.disabled=locked||!target.value||n?.kind==='end';disconnect.disabled=locked||!graph?.edges.length;
    copy.disabled=locked||!templates.value;
  }
  for(const [input,field] of [[name,'name'],[script,'script'],[directory,'directory']])input.oninput=()=>{
    if(locked)return;graph.nodes.find(n=>n.id===selected)[field]=input.value;changed();
  };
  timeout.oninput=()=>{if(!locked){graph.nodes.find(n=>n.id===selected).timeout_seconds=timeout.value.trim()===''?null:Number(timeout.value);changed();}};
  parallel.oninput=()=>{if(!locked){graph.parallelism=Number(parallel.value);changed();}};
  pick.onclick=()=>browse(path=>{if(!graph)return;graph.nodes.find(n=>n.id===selected).script=path;script.value=path;changed();});
  addArg.onclick=()=>{if(!locked){argument();graph.nodes.find(n=>n.id===selected).arguments=argumentRows.map(a=>a.input.value);changed();}};
  add.onclick=()=>{
    if(locked||graph.nodes.length>=graphLimits.nodes)return;
    const n={id:newID(),kind:'script',name:t('web.graph.default_name',{index:graph.nodes.filter(n=>n.kind==='script').length+1}),script:'',arguments:[],directory:'',failure_policy:'abort',x:290,y:Math.max(...graph.nodes.map(n=>n.y))+100};
    n.y=Math.min(n.y,8000);graph.nodes.push(n);select(n.id);changed();
  };
  remove.onclick=()=>{if(!locked&&removeGraphNode(graph,selected)){select(graph.nodes[0].id);changed();}};
  connect.onclick=()=>{
    if(locked)return;error=edgeIssue(graph,selected,target.value);
    if(!error){graph.edges.push({from:selected,to:target.value});onChange();}paint();
  };
  disconnect.onclick=()=>{if(!locked&&edgeList.value!==''){graph.edges.splice(Number(edgeList.value),1);changed();}};
  copy.onclick=()=>{
    const source=getCommands().find(c=>c.id===templates.value);if(locked||!source)return;
    const n=graph.nodes.find(n=>n.id===selected);if(n.kind!=='script')return;
    Object.assign(n,{name:source.name,script:source.script,arguments:[...(source.arguments??[])],directory:source.directory,timeout_seconds:source.timeout_seconds??null});
    select(n.id);changed();
  };
  const newID=()=>globalThis.crypto.randomUUID();
  return {
    load(value){graph=value?copyGraph(value):newGraph();if(!value)graph.nodes[1].name=t('web.graph.default_name',{index:1});error=null;for(const el of buttons.values())el.remove();buttons.clear();parallel.value=graph.parallelism;select(graph.nodes.find(n=>n.kind==='script').id);},
    read(){
      for(const n of graph.nodes)if(n.kind==='script'&&n.timeout_seconds!=null)n.timeout_seconds=timeoutSeconds(n.timeout_seconds,1);
      error=graphIssue(graph);paint();if(error)throw error;return copyGraph(graph);
    },
    setLocked(value){locked=value;applyLock();},
    relocalize(){for(const [el,key] of labels)el.textContent=t(key);for(const row of argumentRows)row.remove.textContent=t('web.commands.arg_remove');refreshTemplates();paint();},
  };
}
