import test from 'node:test';
import assert from 'node:assert/strict';
import {liveEditors} from '../web/live-editors.mjs';
import {nodeController} from '../web/node-controller.mjs';
import {taskController} from '../web/task-controller.mjs';
import {nodeSettings} from '../web/node-settings.mjs';
import {domFixture} from './web-fixture.mjs';
import {connectLiveInput} from '../internal/quickinput/assets/live-input.mjs';

test('window identity, target and revision guard unsaved ordinary edits without UI controls',async()=>{
  const bridge=liveEditors();let object='first',text='unfinished',saves=0;
  const slot=bridge.register({target:()=>({kind:'command',node_id:'node',command_id:object}),read:()=>({text}),apply:v=>{text=v.text;},commit:async()=>{saves++;return 'saved';}});
  const [ref]=await bridge.execute({action:'list'}),first=await bridge.execute({action:'get',editor:ref.draft_id});
  const updated=await bridge.execute({action:'update',editor:ref.draft_id,expected_revision:first.revision,content:{text:'finished'}});
  assert.equal(text,'finished');assert.equal(saves,0);
  await assert.rejects(bridge.execute({action:'commit',editor:ref.draft_id,expected_revision:first.revision}),{code:'conflict'});
  const saved=await bridge.execute({action:'commit',editor:ref.draft_id,expected_revision:updated.revision});
  assert.equal(saved.state,'saved');assert.equal(saves,1);
  object='second';await assert.rejects(bridge.execute({action:'get',editor:ref.draft_id}),{code:'editor_closed'});
  const [second]=await bridge.execute({action:'list'});slot.invalidate();
  await assert.rejects(bridge.execute({action:'get',editor:second.draft_id}),{code:'editor_closed'});
});

test('quick input reads unfinished text before chat is connected and commits without a handoff control',async t=>{
  const dom=domFixture(),previous={document:globalThis.document,window:globalThis.window};
  globalThis.document=dom.document;globalThis.window=new EventTarget();
  let listener,response,text='unfinished chat',sends=0;
  const bridge={subscribe(fn){listener=fn;return()=>{};}};
  const editor={liveState:()=>({text,revision:1}),replaceLive:v=>{text=v;},post:(_,m)=>{response=m.editor_reply;}};
  connectLiveInput(bridge,editor,{liveTarget:()=>({conversation_id:'local-chat',ready:false}),submitLive:async()=>{sends++;return 'queued';}});
  t.after(()=>{window.dispatchEvent(new Event('pagehide'));Object.assign(globalThis,previous);});
  listener({kind:'init'});
  async function ask(action,extra={}){
    const request={id:crypto.randomUUID(),action,...extra};response=null;
    listener({kind:'editor_request',node_id:'local-node',request});
    for(let i=0;!response&&i<100;i++)await new Promise(resolve=>setImmediate(resolve));
    assert.ok(response);assert.equal(response.error,undefined);return response.data;
  }
  const [row]=await ask('list'), before=await ask('get',{editor:row.draft_id});
  assert.equal(before.content.text,'unfinished chat');
  const edit=await ask('update',{editor:row.draft_id,expected_revision:before.revision,content:{text:'edited',mode:'message'}});
  assert.equal(text,'edited');assert.equal(sends,0);
  assert.equal((await ask('commit',{editor:row.draft_id,expected_revision:edit.revision,operation_id:'test-commit'})).state,'queued');
  assert.equal(sends,1);
});

test('ordinary settings cannot read or write the pending key when using the live editor',async t=>{
  const dom=domFixture(),oldDocument=globalThis.document;globalThis.document=dom.document;
  t.after(()=>{globalThis.document=oldDocument;});
  const editors=liveEditors(),writes=[];
  const config={node_id:'node',name:'old',agent_runtime:'openclaw',llm:{provider:'provider',model:'model',credential_ref:'private'},projects:[]};
  const settings=nodeSettings({editors,session:()=>({active:true}),desktop:{open(){}},listen:(el,k,fn)=>el.addEventListener(k,fn),guarded:fn=>fn(),notice(){},refreshPeers(){},
    api:async path=>path==='/api/settings'?{config,revision:'first',providers:[{id:'provider',models:['model']}],runtimes:[{id:'openclaw',providers:['provider']}]}:
      path==='/api/probe'?{state:'not_run'}:{},
    post:async(path,body)=>{writes.push({path,body});return {data:{revision:'next'}};}
  });
  await settings.open();
  const fields=dom.select('#settings-form').elements;let secret='pending';
  Object.defineProperty(fields.key,'value',{get(){throw Error('read pending key');},set(v){secret=v;},configurable:true});
  const [row]=await editors.execute({action:'list'});
  const before=await editors.execute({action:'get',editor:row.draft_id});
  assert.deepEqual(before.content,{name:'old',model:'model'});
  const change=await editors.execute({action:'update',editor:row.draft_id,expected_revision:before.revision,content:{...before.content,name:'new'}});
  await editors.execute({action:'commit',editor:row.draft_id,expected_revision:change.revision,operation_id:'test'});
  assert.deepEqual(writes[0].body.arguments,{settings:'node',expected_revision:'first',name:'new'});
  assert.equal(secret,'pending');
  settings.stop();
});

test('returning to a previous target invalidates its old ID and pending task completion', async t => {
  const dom = domFixture(), previous = globalThis.document;
  globalThis.document = dom.document;
  t.after(() => { globalThis.document = previous; });
  const info = {node_id:'node-a', name:'A', projects:[{id:'project', name:'Project'}]};
  const peers = [{id:'node-b', name:'B', online:true, info:{...info,node_id:'node-b'}}];
  const nodes = nodeController({api:async path => path === '/api/info' ? info : peers,
    session:() => ({active:true}), listen:(el,type,fn) => el.addEventListener(type,fn), isSubmitting:() => false});
  await nodes.refresh();
  const goal = dom.select('#goal'), target = dom.select('#target');
  goal.value = 'same words';
  let resolveWrite, started;
  const writing = new Promise(resolve => { started = resolve; });
  const controller = taskController({view:{submitted:() => { goal.value = ''; }, submissionChanged(){}, editing:() => true},
    api:async () => [], post:() => new Promise(resolve => { resolveWrite = resolve; started(); }),
    selection:nodes.selection, session:() => ({active:true}), storage:{getItem:() => null,setItem(){},removeItem(){}}
  });
  const bridge = liveEditors();
  bridge.register({target:() => ({kind:'task_goal',node_id:target.value,project_id:'project'}),
    targetVersion:nodes.taskTargetVersion, read:() => ({text:goal.value}),
    commit:async (_id,_snapshot,stillCurrent) => { await controller.submit(nodes.content(),stillCurrent); return 'accepted'; }});
  const [row] = await bridge.execute({action:'list'});
  const first = await bridge.execute({action:'get',editor:row.draft_id});
  const saving = bridge.execute({action:'commit',editor:row.draft_id,expected_revision:first.revision});
  await writing;
  target.value = 'node-b'; target.fire('change');
  target.value = 'node-a'; target.fire('change');
  await assert.rejects(bridge.execute({action:'get',editor:row.draft_id}), {code:'editor_closed'});
  resolveWrite();
  assert.equal((await saving).state, 'accepted');
  assert.equal(goal.value, 'same words');
});
