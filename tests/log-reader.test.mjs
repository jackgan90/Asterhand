import test from 'node:test';
import assert from 'node:assert/strict';
import {logReader,logReadQueue} from '../internal/quickinput/assets/log-reader.mjs';
import {logPane} from '../internal/quickinput/assets/log-pane.mjs';
import {setLocale,t} from '../internal/quickinput/assets/i18n.mjs';
import {deferred,tick,domFixture,descendants} from './web-fixture.mjs';

const run={run_id:'run',phase:'running',command:{kind:'graph'},nodes:[]};
const chunk=(node_id,text,stream='stdout')=>({node_id,stream,data:Buffer.from(text).toString('base64')});
const page=(chunks,fields={})=>({log:{run,chunks,start:0,previous:0,next:100,end:true,...fields}});
const makeReader=(nodeID,read,change)=>logReader({read,commandID:'command',runID:'run',nodeID,change});

test('node tail skips bounded empty ranges and preserves the actual tail boundary',async()=>{
  const calls=[],updates=[];
  const reader=makeReader('a',async(action,fields)=>{
    calls.push([action,fields]);
    if(action==='tail_log')return page([],{start:400,next:500});
    if(fields.cursor===400)return page([],{start:200,next:400,end:false});
    return page([chunk('a','old node output')],{start:0,next:200,end:false,run:{...run,nodes:[{node_id:'a',phase:'finished'}]}});
  },update=>updates.push(update));
  reader.setRecord({phase:'finished'});await reader.refresh();
  assert.deepEqual(calls.map(([action,fields])=>[action,fields.cursor,fields.before]),[['tail_log',undefined,undefined],['read_log',400,true],['read_log',200,true]]);
  assert.ok(calls.every(([,fields])=>fields.node_id==='a'&&fields.run_id==='run'));
  assert.equal(reader.state.page.next,500);assert.equal(reader.state.page.end,true);
  assert.equal(updates.flatMap(update=>update.chunks??[]).map(item=>item.text).join(''),'old node output');
  await reader.refresh();assert.equal(calls.length,3);
});

test('node panes independently pause, page history and continue following other output',async()=>{
  const calls=[],updatesA=[],updatesB=[];
  const read=async(action,fields)=>{
    calls.push([action,fields]);
    return page([chunk(fields.node_id,fields.cursor===0?'history':'latest')],{start:fields.cursor??100,next:200,previous:0});
  };
  const a=makeReader('a',read,update=>updatesA.push(update)),b=makeReader('b',read,update=>updatesB.push(update));
  a.setRecord({phase:'running'});b.setRecord({phase:'running'});
  await Promise.all([a.refresh(),b.refresh()]);a.pause();
  const before=calls.length;await Promise.all([a.refresh(),b.refresh()]);
  assert.equal(calls.length,before+1);assert.equal(calls.at(-1)[1].node_id,'b');
  await a.navigate('first');assert.equal(a.state.follow,false);assert.equal(b.state.follow,true);
  assert.equal(updatesA.filter(update=>update.chunks).at(-1).chunks[0].text,'history');
  await a.navigate('latest');assert.equal(a.state.follow,true);
});

test('forward node searches preserve the previous boundary and seed split characters',async()=>{
  const calls=[],updates=[];
  const reader=makeReader('a',async(action,fields)=>{
    calls.push([action,fields]);
    if(fields.cursor===0)return page([],{start:0,next:200,end:false});
    return page([{node_id:'a',stream:'stdout',data:Buffer.from([0xad]).toString('base64')}],{
      start:200,next:300,context:{'utf-8':[{node_id:'a',stream:'stdout',data:Buffer.from([0xe4,0xb8]).toString('base64')}]},
    });
  },update=>updates.push(update));
  reader.setRecord({phase:'finished'});await reader.navigate('first');
  assert.equal(reader.state.page.start,0);assert.equal(reader.state.page.next,300);
  assert.equal(updates.flatMap(update=>update.chunks??[]).map(item=>item.text).join(''),'中');
  assert.deepEqual(calls.map(([,fields])=>fields.cursor),[0,200]);
});

test('disposed panes and superseded encoding reads never paint late data',async()=>{
  const pending=deferred(),updates=[];let count=0;
  const reader=makeReader('a',async()=>++count===1?await pending.promise:page([chunk('a','new encoding')]),update=>updates.push(update));
  reader.setRecord({phase:'running'});
  const first=reader.refresh();await reader.setEncoding('gbk');
  pending.resolve(page([chunk('a','late old encoding')]));await first;
  const text=()=>updates.flatMap(update=>update.chunks??[]).map(item=>item.text).join('');
  assert.equal(text(),'new encoding');
  const later=deferred(),removed=[];
  const other=makeReader('b',async()=>await later.promise,update=>removed.push(update));
  const load=other.refresh();other.dispose();const before=removed.length;
  later.resolve(page([chunk('b','removed')]));await load;assert.equal(removed.length,before);
});

test('a finished status overtaking an older tail page cannot discard the final output',async()=>{
  const pending=deferred(),updates=[];let calls=0;
  const reader=makeReader('a',async()=>{
    if(++calls===1)return await pending.promise;
    return page([{node_id:'a',stream:'stdout',data:Buffer.from([0xad]).toString('base64')}],{
      start:100,next:200,run:{...run,nodes:[{node_id:'a',phase:'finished'}]},
    });
  },update=>updates.push(update));
  reader.setRecord({phase:'running'});const first=reader.refresh();
  reader.setRecord({phase:'finished'});
  pending.resolve(page([{node_id:'a',stream:'stdout',data:Buffer.from([0xe4,0xb8]).toString('base64')}],{
    run:{...run,nodes:[{node_id:'a',phase:'running'}]},
  }));
  await first;await reader.refresh();await reader.refresh();
  assert.equal(calls,2);assert.equal(updates.flatMap(update=>update.chunks??[]).map(item=>item.text).join(''),'中');
});

test('the read queue reserves native request slots and drops obsolete queued work',async()=>{
  const pending=[],called=[];let inFlight=0,maximum=0,oldCurrent=true;
  const read=logReadQueue(async(action,fields)=>{
    called.push(fields.node_id);maximum=Math.max(maximum,++inFlight);
    const task=deferred();pending.push(task);await task.promise;inFlight--;return page([]);
  });
  const requests=['a','b','old','c'].map(node_id=>read('read_log',{node_id},()=>node_id!=='old'||oldCurrent).catch(error=>error));
  await tick();assert.equal(called.length,2);oldCurrent=false;
  pending[0].resolve();await tick();assert.deepEqual(called,['a','b','c']);
  pending[1].resolve();pending[2].resolve();const results=await Promise.all(requests);
  assert.equal(maximum,2);assert.equal(results[2].cancelled,true);
});

test('hidden panes stop sparse scans and resume without accepting hidden responses',async()=>{
  const pending=deferred(),updates=[];let calls=0;
  const reader=makeReader('a',async()=>++calls===1?await pending.promise:page([chunk('a','visible')]),update=>updates.push(update));
  const load=reader.refresh();reader.setVisible(false);pending.resolve(page([],{start:300,next:400}));await load;
  await reader.refresh();assert.equal(calls,1);
  reader.setVisible(true);await reader.refresh();assert.equal(calls,2);
  assert.equal(updates.flatMap(update=>update.chunks??[]).map(item=>item.text).join(''),'visible');
});

test('real pane controls preserve output and scroll position while translating status and toolbar',async tcase=>{
  const dom=domFixture();globalThis.document=dom.document;tcase.after(()=>delete globalThis.document);setLocale('en-US');
  const node={node_id:'a',kind:'script',name:'build',phase:'finished',result:'succeeded'};
  const record={...run,nodes:[node]};
  const pane=logPane({read:async()=>page([chunk('a','saved history')]),commandID:'command',record,nodeID:'a',encoding:'utf-8'});
  await pane.refresh();
  const output=descendants(pane.element).find(element=>element.className==='log-pane-output');
  const original=output.children[0];output.scrollTop=40;
  const buttons=descendants(pane.element).filter(element=>element.tagName==='BUTTON' && element.className!=='log-pane-stop');buttons[0].focus();
  setLocale('zh-CN');pane.relocalize();
  assert.equal(output.children[0],original);assert.equal(output.scrollTop,40);assert.equal(dom.document.activeElement,buttons[0]);
  assert.equal(buttons[0].textContent,t('common.command.first'));assert.equal(pane.element.dataset.state,'succeeded');
  pane.metadata({...record,nodes:[{...node,result:'failed'}]});assert.equal(pane.element.dataset.state,'failed');
});

function watchButtons(buttons) {
  const writes=[];
  for(const button of buttons)for(const property of ['textContent','disabled']){
    let value=button[property];
    Object.defineProperty(button,property,{get:()=>value,set:next=>{writes.push({button,property,value:next});value=next;}});
  }
  return writes;
}

test('merged and node toolbar controls stay unchanged throughout automatic reads',async tcase=>{
  for(const nodeID of ['', 'a'])await tcase.test(nodeID?'node pane':'merged pane',async subcase=>{
    const dom=domFixture();globalThis.document=dom.document;subcase.after(()=>delete globalThis.document);setLocale('en-US');
    const record={...run,nodes:[{node_id:'a',kind:'script',name:'build',phase:'running'}]};
    let pending,calls=0;
    const pane=logPane({read:async()=>++calls===1?page([chunk('a','initial')],{start:100,next:200}):pending.promise,
      commandID:'command',record,nodeID,encoding:'utf-8'});
    subcase.after(()=>pane.dispose());await pane.refresh();
    const buttons=descendants(pane.element).filter(element=>element.tagName==='BUTTON' && element.className!=='log-pane-stop'),latest=buttons[3];
    const writes=watchButtons(buttons);latest.focus();
    for(let cycle=0;cycle<3;cycle++){
      pending=deferred();const refresh=pane.refresh(),before=calls;
      assert.equal(latest.disabled,false,'automatic reads must not dim the latest button');
      assert.equal(buttons[0].disabled,false,'history controls must remain usable');
      pane.metadata({...record,elapsed_seconds:cycle});await pane.refresh();
      assert.equal(calls,before,'automatic reads must remain serial');
      pending.resolve(page(cycle?[chunk('a',' more output')]:[],{start:200,next:300}));await refresh;
    }
    assert.deepEqual(writes,[],'unchanged toolbar labels and disabled state must not be rewritten');
    assert.equal(dom.document.activeElement,latest);
    setLocale('zh-CN');pane.relocalize();
    assert.equal(latest.textContent,t('common.command.latest'));
    assert.equal(writes.filter(write=>write.property==='textContent').length,4);
    assert.equal(writes.filter(write=>write.property==='disabled').length,0);
    assert.equal(dom.document.activeElement,latest);setLocale('en-US');
  });
});

test('history navigation supersedes background reads and still blocks duplicate user actions',async tcase=>{
  const dom=domFixture();globalThis.document=dom.document;tcase.after(()=>delete globalThis.document);setLocale('en-US');
  const background=deferred(),history=deferred(),calls=[];
  const pane=logPane({read:async(action,fields)=>{
    calls.push({action,fields});
    if(calls.length===1)return page([chunk('a','initial')],{start:100,next:200});
    if(calls.length===2)return background.promise;
    if(calls.length===3)return history.promise;
    return page([chunk('a','latest output')],{start:300,next:400});
  },commandID:'command',record:run,encoding:'utf-8'});
  tcase.after(()=>pane.dispose());
  const buttons=descendants(pane.element).filter(element=>element.tagName==='BUTTON' && element.className!=='log-pane-stop'),latest=buttons[3];
  const initial=pane.refresh();assert.equal(latest.disabled,true,'initial loading must prevent duplicate actions');await initial;
  const refresh=pane.refresh();assert.equal(latest.disabled,false);
  buttons[0].onclick();await tick();
  assert.equal(calls.length,3);assert.equal(calls[2].fields.cursor,0);
  assert.equal(latest.disabled,true,'explicit navigation must prevent duplicate actions');
  latest.onclick();await tick();assert.equal(calls.length,3);
  history.resolve(page([chunk('a','history')],{start:0,next:100,end:false}));await tick();
  const output=descendants(pane.element).find(element=>element.className==='log-pane-output');
  const text=()=>descendants(output).filter(element=>element.dataset.stream).map(element=>element.textContent).join('');
  assert.equal(text(),'history');assert.equal(latest.disabled,false);
  background.resolve(page([chunk('a','stale automatic output')],{start:200,next:300}));await refresh;
  assert.equal(text(),'history');
  latest.onclick();await tick();assert.equal(calls.length,4);assert.equal(calls[3].action,'tail_log');
  assert.equal(text(),'latest output');assert.equal(latest.disabled,false);
});
