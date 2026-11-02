import test from 'node:test';
import assert from 'node:assert/strict';
import {newGraph,graphIssue,edgeIssue,removeGraphNode} from '../web/graph-model.mjs';
import {graphEditor} from '../web/graph-editor.mjs';
import {logDecoder} from '../internal/quickinput/assets/log-model.mjs';
import {logGraphView} from '../internal/quickinput/assets/log-graph.mjs';
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
  const name=inputs.find(el=>el.value==='compile');name.value='未保存的新名称';name.setSelectionRange(2,4);name.focus();name.fire('input');
  setLocale('en-US');editor.relocalize();
  assert.equal(name.value,'未保存的新名称');assert.equal(name.selectionStart,2);assert.equal(dom.document.activeElement,name);
  assert.equal(editor.read().nodes[1].name,name.value);
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

test('parallel log panes filter by node and changing run clears old selections and buffers',tcase=>{
  const dom=domFixture();globalThis.document=dom.document;tcase.after(()=>delete globalThis.document);setLogLocale('en-US');
  const byID=id=>dom.select('#'+id),output=byID('log-output');
  byID('log-node-left').tagName='SELECT';byID('log-node-right').tagName='SELECT';
  const view=logGraphView({byID,output,onPause(){}});
  const nodes=['a','b'].map(node_id=>({node_id,name:node_id,kind:'script',phase:'running',elapsed_seconds:0}));
  view.metadata({run_id:'first',command:{kind:'graph'},nodes});
  view.append([{node_id:'a',stream:'stdout',text:'alpha'},{node_id:'b',stream:'stderr',text:'beta'}]);
  const left=byID('log-node-left'),right=byID('log-node-right');left.value='a';right.value='b';left.fire('change');right.fire('change');
  const texts=el=>descendants(el).map(child=>child.textContent).filter(Boolean).join('');
  assert.ok(texts(output).includes('alpha'));assert.ok(!texts(output).includes('beta'));
  assert.ok(texts(byID('log-output-right')).includes('beta'));assert.ok(!texts(byID('log-output-right')).includes('alpha'));
  setLogLocale('zh-CN');view.relocalize();assert.equal(left.value,'a');assert.equal(right.value,'b');
  view.reset();view.metadata({run_id:'second',command:{kind:'graph'},nodes});assert.equal(left.value,'');assert.equal(right.value,'');assert.equal(view.chunkCount,0);
});
