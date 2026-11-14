import test from 'node:test';
import assert from 'node:assert/strict';
import {domFixture,tick,deferred} from './web-fixture.mjs';
import {toolSettings} from '../web/tool-settings.mjs';
import {toolActivities} from '../internal/quickinput/assets/chat-activities.mjs';
import {setLocale} from '../web/i18n.mjs';
setLocale('en-US');

function fixture(t,{read,write}={}){
 const dom=domFixture(),previous=globalThis.document;
 globalThis.document=dom.document;t.after(()=>{globalThis.document=previous;});
 const calls=[];
 const state={workspace_id:'workspace',authorized:true,supported:true,ready:true,policy_revision:'policy'};
 const scope={active:true};
 const controller=toolSettings({api:read??(async()=>state),post:async(path,body)=>{calls.push({path,body});return write?.(path,body)??{...state,authorized:body.enabled,policy_revision:'saved'};},listen:(el,event,fn)=>el.addEventListener(event,fn),session:()=>scope});
 const input=dom.select('#assistant-tools-enabled'),status=dom.select('#assistant-tools-status'),dialog=dom.select('#settings');
 return {...dom,calls,input,status,controller,scope,open:()=>{dialog.showModal();return controller.open();},close:()=>dialog.close(),toggle:async value=>{input.checked=value;input.fire('change');await tick();}};
}

test('tool settings display effective permission and save only the dedicated policy',async t=>{
 const ui=fixture(t);await ui.open();
 assert.equal(ui.input.checked,true);assert.equal(ui.calls.length,0);
 await ui.toggle(false);
 assert.deepEqual(ui.calls,[{path:'/api/workspace/permission',body:{enabled:false,expected_revision:'policy'}}]);
 assert.equal(ui.input.checked,false);assert.equal(ui.input.disabled,false);
 await ui.toggle(true);
 assert.equal(ui.calls[1].body.expected_revision,'saved');
});

test('tool settings preserve explicit disabled permission without a write',async t=>{
 const ui=fixture(t,{read:async()=>({authorized:false,supported:true,ready:false,policy_revision:'off'})});await ui.open();
 assert.equal(ui.input.checked,false);assert.equal(ui.input.disabled,false);assert.equal(ui.calls.length,0);
});

test('reopening settings ignores an older permission response',async t=>{
 const first=deferred(),second=deferred();let count=0;
 const ui=fixture(t,{read:()=>++count===1?first.promise:second.promise});
 const old=ui.open();ui.close();const reopened=ui.open();
 second.resolve({workspace_id:'new',authorized:true,supported:true,ready:true,policy_revision:'new-policy'});await tick();
 first.resolve({workspace_id:'old',authorized:false,supported:true,ready:false,policy_revision:'old-policy'});await tick();
 await Promise.all([old,reopened]);
 assert.equal(ui.input.checked,true);await ui.toggle(false);
 assert.equal(ui.calls.at(-1).body.expected_revision,'new-policy');
});

test('busy and conflict failures restore server state and use readable messages',async t=>{
 for(const [messageKey,expected] of [['err.conv.busy_before_model_settings',/reply|stop/i],['err.workspace.conflict',/changed|again/i]]){
  const ui=fixture(t,{write:async()=>{throw {messageKey};}});await ui.open();await ui.toggle(false);
  assert.equal(ui.input.checked,true);assert.equal(ui.input.disabled,false);assert.match(ui.status.textContent,expected);
  assert.doesNotMatch(ui.status.textContent,/err\.|not_authorized/);
 }
});

test('lost save response reads authoritative state; failed reconciliation leaves permission unknown',async t=>{
 let changed=false,readFails=false;
 const ui=fixture(t,{read:async()=>{if(readFails)throw Error('offline');return {authorized:!changed,supported:true,ready:true,policy_revision:changed?'changed':'policy'};},write:async()=>{changed=true;throw Error('lost response');}});
 await ui.open();await ui.toggle(false);
 assert.equal(ui.input.checked,false);assert.equal(ui.input.indeterminate,false);assert.equal(ui.status.dataset.tone,undefined);
 readFails=true;await ui.toggle(true);
 assert.equal(ui.input.indeterminate,true);assert.equal(ui.input.disabled,true);assert.match(ui.status.textContent,/confirm/i);
});

test('failed initial read stays unknown and does not accept writes',async t=>{
 const ui=fixture(t,{read:async()=>{throw Error('offline');}});await ui.open();await ui.toggle(true);
 assert.equal(ui.input.indeterminate,true);assert.equal(ui.input.disabled,true);assert.equal(ui.calls.length,0);
});

test('a save finishing after close and reopen refreshes permission and its revision',async t=>{
 const pending=deferred();let saved=false,reads=0;
 const ui=fixture(t,{read:async()=>{reads++;return {authorized:!saved,supported:true,ready:true,policy_revision:saved?'new':'old'};},write:async()=>{await pending.promise;saved=true;return {authorized:false,supported:true,ready:true,policy_revision:'new'};}});
 await ui.open();await ui.toggle(false);assert.equal(ui.input.disabled,true);
 ui.close();await ui.open();assert.equal(ui.input.disabled,true);
 pending.resolve();await tick();await tick();
 assert.equal(reads,3);assert.equal(ui.input.checked,false);assert.equal(ui.input.disabled,false);
 await ui.toggle(true);assert.equal(ui.calls.at(-1).body.expected_revision,'new');
});

test('tool permission localization keeps current value',async t=>{
 const ui=fixture(t);await ui.open();setLocale('zh-CN');ui.controller.relocalize();
 assert.equal(ui.input.checked,true);assert.match(ui.status.textContent,/下一条消息/);
 setLocale('en-US');ui.controller.relocalize();assert.equal(ui.input.checked,true);
});

test('tool activity completion replaces the started row and discards other-conversation reads',async t=>{
 const dom=domFixture(),previous=globalThis.document;globalThis.document=dom.document;t.after(()=>{globalThis.document=previous;});
 const container=dom.select('#tools');let request=0;
 const client={call:async()=>({activities:{authorized:true,next:++request,items:[{id:'call',tool:'write',state:request===1?'started':'completed',sequence:request}]}})};
 const panel=toolActivities(container,client);panel.select('conversation');await panel.refresh();await panel.refresh();
 assert.equal(container.children.length,1);assert.match(container.children[0].children[0].textContent,/Completed|已完成/);
 const pending=deferred();client.call=()=>pending.promise;
 const inFlight=panel.refresh();panel.select('next');pending.resolve({activities:{authorized:true,next:3,items:[{id:'old',tool:'write',state:'completed'}]}});await inFlight;
 assert.equal(container.children.length,0);
});
