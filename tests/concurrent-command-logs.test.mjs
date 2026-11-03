import test from 'node:test';
import assert from 'node:assert/strict';
import {logGraphView, runLogSelection, paneKey} from '../internal/quickinput/assets/log-graph.mjs';
import {logPane} from '../internal/quickinput/assets/log-pane.mjs';
import {canLaunch, summaryParts} from '../web/command-run.mjs';
import {domFixture, descendants, deferred, tick} from './web-fixture.mjs';

const record = (id, sequence = 1) => ({run_id:id, round_id:'round', sequence, started_at:'2026-11-03T00:00:00Z', phase:'running', elapsed_seconds:1,
  command:{name:'compile'}, nodes:[{node_id:'shared-node',kind:'script',name:'build',phase:'running'}]});
const runList = records => ({round_id:'round',latest_run_id:records.at(-1).run_id,runs:records});

test('fixed run/node panes keep their identity and reading position when another run joins', t => {
  const dom=domFixture(); globalThis.document=dom.document; t.after(()=>delete globalThis.document);
  const byID=id=>dom.select('#'+id), created=[];
  const view=logGraphView({byID, createPane:(nodeID,run)=>{
    const element=dom.document.createElement('section');element.dataset.identity=paneKey(run.run_id,nodeID);
    const pane={element,metadata(){},refresh(){},setVisible(){},setEncoding(){},relocalize(){},dispose(){element.remove();pane.disposed=true;}};
    created.push(pane);return pane;
  }});
  const a=record('a'),b=record('b',2),records=new Map([['a',a],['b',b]]);
  let list=runList([a,b]);view.metadata(list,records);
  const choices=()=>byID('log-columns').children.map(el=>el.dataset.identity);
  assert.deepEqual(choices(),['b:']);
  const newest=byID('log-columns').children[0];newest.scrollTop=120;
  byID('log-view-custom').fire('click');
  assert.equal(byID('log-view-custom').getAttribute('aria-pressed'),'true');
  const picker=byID('log-run-picker');picker.value='a';picker.fire('change');view.metadata(list,records);
  assert.equal(byID('log-run-field').hidden,false);
  const [merged,node]=byID('log-nodes').children;merged.fire('click');
  assert.deepEqual(choices(),['b:','a:']);
  node.fire('click');
  assert.deepEqual(choices(),['b:','a:','a:shared-node']);
  assert.equal(node.getAttribute('aria-pressed'),'true');
  const c=record('c',3);records.set('c',c);list=runList([a,b,c]);view.metadata(list,records);
  assert.deepEqual(choices(),['b:','a:','a:shared-node']);assert.equal(byID('log-columns').children[0],newest);assert.equal(newest.scrollTop,120);
  node.focus();view.relocalize();assert.equal(dom.document.activeElement,node);
  assert.equal(picker.value,'a','new admission must not replace the inspected run');
  const d={...record('d',4),round_id:'next'};
  view.metadata({round_id:'next',latest_run_id:'d',runs:[d]},new Map([['d',d]]));
  assert.deepEqual(choices(),['d:']);assert.equal(created.filter(p=>p.disposed).length,3);
});

test('automatic view is ordered and capped; fixed selection has a separate identity for each run',()=>{
  const runs=Array.from({length:8},(_,i)=>({...record('run'+i,i+1),nodes:Array.from({length:3},(_,n)=>({node_id:'node'+n,kind:'script',phase:'running'}))}));
  const records=new Map(runs.map(run=>[run.run_id,run])),list=runList(runs),selection=runLogSelection();
  selection.reset(list);selection.active();
  const choices=selection.choices(list,records);
  assert.equal(choices.length,24);assert.deepEqual(choices.slice(0,4).map(c=>paneKey(c.runID,c.nodeID)),['run0:node0','run0:node1','run0:node2','run1:node0']);
  for(const run of runs)selection.toggle(run.run_id,'node0',true);
  selection.toggle('run0','node1',true);
  assert.equal(selection.choices(list,records).length,8);
  for(const run of runs)assert.equal(selection.has(run.run_id,'node0'),true);
  selection.toggle('run0','node0',false);selection.toggle('run0','node1',true);
  assert.equal(selection.has('run0','node1'),true);assert.equal(selection.has('run1','node0'),true);
});

test('each pane reads and stops its own run and encoding changes leave its sibling untouched',async t=>{
  const dom=domFixture();globalThis.document=dom.document;t.after(()=>delete globalThis.document);
  const pending=deferred(),calls=[],stopped=[];
  const read=async(action,fields)=>{
    calls.push({action,...fields});
    if(fields.run_id==='a'&&calls.filter(c=>c.run_id==='a').length===1)return pending.promise;
    return {log:{run:{...record(fields.run_id),phase:'finished'},start:0,next:5,end:true,chunks:[{node_id:'shared-node',stream:'stdout',data:Buffer.from(fields.run_id).toString('base64')}]}};
  };
  const panes=['a','b'].map(id=>logPane({read,commandID:'command',record:record(id),nodeID:'shared-node',encoding:'utf-8',stop:async run=>stopped.push(run)}));
  t.after(()=>panes.forEach(p=>p.dispose()));
  const first=panes[0].refresh();await panes[1].refresh();
  pending.resolve({log:{run:{...record('a'),phase:'finished'},start:0,next:5,end:true,chunks:[{node_id:'shared-node',stream:'stdout',data:Buffer.from('a').toString('base64')}]}});await first;
  const text=p=>descendants(p.element).filter(el=>el.dataset.stream).map(el=>el.textContent).join('');
  assert.equal(text(panes[0]),'a');assert.equal(text(panes[1]),'b');
  const siblingOutput=descendants(panes[1].element).find(el=>el.className==='log-pane-output');siblingOutput.scrollTop=45;
  await panes[0].setEncoding('gbk');await tick();
  assert.equal(calls.filter(c=>c.run_id==='b').length,1);assert.equal(siblingOutput.scrollTop,45);
  const stop=descendants(panes[0].element).find(el=>el.className==='log-pane-stop');await stop.onclick();
  assert.deepEqual(stopped,['a']);
});

test('launch availability and summary distinguish concurrent runs from the newest result',()=>{
  const command={phase:'finished',result:'succeeded',active_count:1,failed_count:2,interrupted_count:1};
  assert.equal(canLaunch(command),false);assert.equal(canLaunch({...command,allow_concurrent:true}),true);
  assert.equal(canLaunch({...command,allow_concurrent:true,active_count:8}),false);
  const parts=summaryParts(command,(key,args)=>[key,args?.count].join(':'));
  assert.deepEqual(parts,['common.command.active_count:1','common.command.failed_count:2','common.command.interrupted_count:1']);
});
