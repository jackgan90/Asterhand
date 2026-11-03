import test from 'node:test';
import assert from 'node:assert/strict';
import {newGraph,graphIssue,edgeIssue,removeGraphNode} from '../web/graph-model.mjs';
import {graphEditor} from '../web/graph-editor.mjs';
import {logDecoder} from '../internal/quickinput/assets/log-model.mjs';
import {logGraphView} from '../internal/quickinput/assets/log-graph.mjs';
import {nodeState,nodeActive} from '../internal/quickinput/assets/log-node.mjs';
import {setLocale,t} from '../web/i18n.mjs';
import {setLocale as setLogLocale} from '../internal/quickinput/assets/i18n.mjs';
import {domFixture,descendants} from './web-fixture.mjs';

function validGraph(){const graph=newGraph();Object.assign(graph.nodes[1],{name:'compile',script:'C:\\step.cmd'});return graph;}
test('graph structure checks reject cycles, disconnected nodes and duplicate edges',()=>{
  const graph=validGraph();assert.equal(graphIssue(graph),null);
  assert.equal(edgeIssue(graph,graph.nodes[1].id,graph.nodes[0].id).messageKey,'err.graph.edge');
  assert.equal(edgeIssue(graph,graph.nodes[0].id,graph.nodes[1].id).messageKey,'err.graph.edge');
  const extra={...graph.nodes[1],id:crypto.randomUUID(),name:'independent'};graph.nodes.push(extra);
  assert.equal(graphIssue(graph).messageKey,'err.graph.disconnected');
  graph.edges.push({from:graph.nodes[1].id,to:extra.id},{from:extra.id,to:graph.nodes[2].id});
  assert.equal(edgeIssue(graph,extra.id,graph.nodes[1].id).messageKey,'err.graph.cycle');
  assert.equal(graphIssue(graph),null);
  assert.equal(removeGraphNode(graph,graph.nodes[0].id),false);
  assert.equal(removeGraphNode(graph,extra.id),true);assert.equal(graphIssue(graph),null);
});

test('graph editor preserves live inputs across language changes and blocks mutation while locked',tcase=>{
  const dom=domFixture();globalThis.document=dom.document;tcase.after(()=>delete globalThis.document);
  setLocale('zh-CN');const container=dom.document.createElement('section');let changes=0;
  const editor=graphEditor({container,onChange:()=>changes++,browse:apply=>apply('F:\\next.ps1'),getCommands:()=>[]});
  editor.load(validGraph());
  const inputs=descendants(container).filter(el=>el.tagName==='INPUT');
  const properties=descendants(container).find(el=>el.tagName==='FIELDSET');
  assert.equal(properties.hidden,false);
  const name=inputs.find(el=>el.value==='compile');name.value='未保存的新名称';name.setSelectionRange(2,4);name.focus();name.fire('input');
  setLocale('en-US');editor.relocalize();
  assert.equal(name.value,'未保存的新名称');assert.equal(name.selectionStart,2);assert.equal(dom.document.activeElement,name);
  assert.equal(editor.read().nodes[1].name,name.value);
  const start=descendants(container).find(el=>el.dataset.kind==='start');start.fire('click');
  assert.equal(properties.hidden,true);
  const end=descendants(container).find(el=>el.dataset.kind==='end');end.fire('click');assert.equal(properties.hidden,true);
  const script=descendants(container).find(el=>el.dataset.kind==='script');script.fire('click');
  assert.equal(properties.hidden,false);assert.equal(name.value,'未保存的新名称');
  editor.setLocked(true);
  const before=changes,add=descendants(container).find(el=>el.textContent===t('web.graph.add'));add.fire('click');
  assert.equal(changes,before);assert.equal(editor.read().nodes.length,3);
  editor.setLocked(false);add.fire('click');assert.throws(()=>editor.read());
});

test('graph decoders isolate nodes as well as stdout and seed new nodes on incremental pages',()=>{
  const decoder=logDecoder();
  const c=(node,stream,bytes)=>({node_id:node,stream,data:Buffer.from(bytes).toString('base64')});
  const first=new TextEncoder().encode('中'),second=new TextEncoder().encode('文');
  assert.equal(decoder.decode(c('a','stdout',first.slice(0,2))).text,'');
  assert.equal(decoder.decode(c('b','stdout',second)).text,'文');
  assert.equal(decoder.decode(c('a','stderr',second)).text,'文');
  assert.equal(decoder.decode(c('a','stdout',first.slice(2))).text,'中');
  decoder.seed([c('c','stdout',second.slice(0,1)),c('a','stdout',first.slice(0,1))],true);
  assert.equal(decoder.decode(c('c','stdout',second.slice(1))).text,'文');
  assert.equal(decoder.decode(c('a','stdout',first)).text,'中');
});

test('log modes keep fixed selections, follow active nodes and preserve mounted panes across state and locale changes',tcase=>{
  const dom=domFixture();globalThis.document=dom.document;tcase.after(()=>delete globalThis.document);setLogLocale('en-US');
  const byID=id=>dom.select('#'+id),created=[];
  const view=logGraphView({byID,createPane:id=>{
    const element=dom.document.createElement('section');element.dataset.node=id;
    const pane={element,metadata(){},refresh(){},setVisible(){},setEncoding(){},relocalize(){},dispose(){element.remove();pane.disposed=true;}};
    created.push(pane);return pane;
  }});
  const nodes=['a','b','c'].map(node_id=>({node_id,name:node_id,kind:'script',phase:'running',elapsed_seconds:0}));
  nodes[2].phase='ready';nodes.unshift({node_id:'start',kind:'start',phase:'finished',result:'succeeded'});
  const record={run_id:'first',command:{kind:'graph'},nodes};
  const publish=record=>view.metadata({round_id:record.run_id,latest_run_id:record.run_id,runs:[record]},new Map([[record.run_id,record]]));
  const ids=()=>byID('log-columns').children.map(el=>el.dataset.node);
  publish(record);assert.deepEqual(ids(),['']);
  assert.equal(byID('log-view-all').getAttribute('aria-pressed'),'true');
  const active=byID('log-view-active');active.fire('click');assert.deepEqual(ids(),['a','b']);
  const b=byID('log-columns').children[1];b.scrollTop=75;
  nodes[1].phase='draining';nodes[2].phase='stopping';publish(record);assert.deepEqual(ids(),['a','b']);
  nodes[1].phase='finished';nodes[1].result='succeeded';nodes[3].phase='starting';publish(record);
  assert.deepEqual(ids(),['b','c']);assert.equal(byID('log-columns').children[0],b);assert.equal(b.scrollTop,75);
  const chips=byID('log-nodes').children;
  assert.equal(chips.length,4,'one merged chip plus the three script steps; start/end are not listed');
  byID('log-view-all').fire('click');assert.deepEqual(ids(),['']);
  chips[1].fire('click');
  assert.equal(byID('log-view-custom').getAttribute('aria-pressed'),'true');assert.deepEqual(ids(),['','a'],'entering custom keeps the visible pane');
  chips[0].fire('click');assert.deepEqual(ids(),['a']);
  chips[2].fire('click');assert.deepEqual(ids(),['a','b']);
  nodes[2].phase='finished';nodes[2].result='interrupted';publish(record);assert.deepEqual(ids(),['a','b']);
  const fixed=byID('log-columns').children[0];chips[1].focus();setLogLocale('zh-CN');view.relocalize();
  assert.equal(byID('log-columns').children[0],fixed);assert.equal(dom.document.activeElement,chips[1]);assert.equal(chips[1].getAttribute('aria-pressed'),'true');
  chips[1].fire('click');chips[2].fire('click');
  assert.deepEqual(ids(),[]);assert.equal(byID('log-empty').hidden,false);assert.equal(byID('log-view-all').getAttribute('aria-pressed'),'false');
  nodes[3].phase='finished';nodes[3].result='failed';active.fire('click');assert.deepEqual(ids(),[]);
  publish({...record,run_id:'second'});assert.deepEqual(ids(),['']);assert.equal(byID('log-view-all').getAttribute('aria-pressed'),'true');
  assert.ok(created.filter(pane=>pane.disposed).length>=4);
});

test('node states distinguish terminal results and retain stopping and draining as active',()=>{
  for(const state of ['waiting','ready','starting','running','stopping','draining'])assert.equal(nodeState({phase:state}),state);
  for(const result of ['succeeded','failed','interrupted','skipped'])assert.equal(nodeState({phase:'finished',result}),result);
  assert.equal(nodeState({phase:'finished',result:'future_state'}),'unknown');
  assert.equal(nodeActive({kind:'script',phase:'draining'}),true);assert.equal(nodeActive({kind:'script',phase:'stopping'}),true);
  assert.equal(nodeActive({kind:'script',phase:'ready'}),false);assert.equal(nodeActive({kind:'end',phase:'running'}),false);
});
