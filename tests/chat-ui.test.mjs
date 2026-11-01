import test from 'node:test';
import assert from 'node:assert/strict';
import {chatPanel} from '../internal/quickinput/assets/chat-ui.mjs';

const target = {node_id:'reviewer-node-0001', role_id:'default'};
const A = 'conversation-aaaa', B = 'conversation-bbbb';
const message = index => ({id:`message-${index}`, role:'assistant', blocks:[{type:'text', text:`row ${index}`} ]});
const flush = async () => { for (let index=0;index<30;index++) await Promise.resolve(); };

class Node {
  constructor(tag='div') {
    this.tagName=tag; this.children=[]; this.listeners=new Map(); this.attributes={}; this.dataset={};
    this.classList={toggle(){}}; this.hidden=false; this.scrollTop=0; this.clientHeight=100;
    this._text='';
  }
  get textContent() { return this._text + this.children.map(child=>child.textContent).join(''); }
  set textContent(value) { this._text=String(value); this.children=[]; }
  append(...children) { for (const child of children) { if (child.fragment) this.append(...[...child.children]); else { child.remove(); child.parent=this; this.children.push(child); } } }
  replaceChildren(...children) { this.children.forEach(child=>child.parent=null); this.children=[]; this._text=''; this.append(...children); }
  insertBefore(child, next) { child.remove(); const index=next?this.children.indexOf(next):-1; child.parent=this; this.children.splice(index<0?this.children.length:index,0,child); }
  remove() { if (this.parent) { const at=this.parent.children.indexOf(this); if(at>=0)this.parent.children.splice(at,1); this.parent=null; } }
  addEventListener(name, listener) { if(!this.listeners.has(name))this.listeners.set(name,[]); this.listeners.get(name).push(listener); }
  removeEventListener(name, listener) { this.listeners.set(name,(this.listeners.get(name)||[]).filter(value=>value!==listener)); }
  setAttribute(name, value) { this.attributes[name]=String(value); }
  getAttribute(name) { return this.attributes[name]??null; }
  focus() {}
  getBoundingClientRect() {
    const index=this.parent?.children.indexOf(this)??0;
    const top=this.dataset.messageId ? index*10-document.getElementById('chat-scroll').scrollTop : 0;
    return {top,bottom:top+10};
  }
  async fire(name) { await Promise.all((this.listeners.get(name)||[]).map(listener=>listener({}))); }
}

function harness(queued = false) {
  const elements = new Map();
  const ids=['chat-panel','chat-scroll','chat-status','chat-conversations','chat-send','chat-stop','chat-older','chat-messages',
    'chat-draft-decision','chat-draft-cancel','chat-draft-discard','chat-expand','chat-conversation-name','chat-history','chat-new','chat-refresh','chat-latest','chat-pin','chat-connection'];
  for(const id of ids) elements.set(id,new Node());
  Object.defineProperty(elements.get('chat-scroll'),'scrollHeight',{get:()=>elements.get('chat-messages').children.length*10});
  globalThis.document={getElementById:id=>elements.get(id),createElement:tag=>new Node(tag),
    createDocumentFragment(){const node=new Node();node.fragment=true;return node;},createTextNode(text){const node=new Node('#text');node.textContent=text;return node;}};
  globalThis.window=new Node('window');
  let confirmations=0;
  window.confirm=()=>{confirmations++;return false;};
  const timers=new Map(); let timerID=0;
  const originalSetTimeout=globalThis.setTimeout, originalClearTimeout=globalThis.clearTimeout;
  globalThis.setTimeout=(fn,ms)=>{timers.set(++timerID,{fn,ms});return timerID;};
  globalThis.clearTimeout=id=>timers.delete(id);
  const subscribers=new Set();
  const bridge={subscribe(fn){subscribers.add(fn);return()=>subscribers.delete(fn);},emit(value){for(const fn of [...subscribers])fn(value);}};
  const requests=[], held=[];
  const holdActions=new Set(), overrides=new Map();
  let running=true, draft='', revision=1, total=100, reset=false;
  let queueRevision=0;
  const queueState={entries:[],failures:[],paused:false,limit:8};
  const turn=()=>({id:'turn-00000000001',operation_id:'operation-000001',conversation_id:A,delivery:'accepted',
    run:running?'running':'completed',persistence:running?'pending':'saved',assistant_message_id:running?'':'final-message'});
  let staged=null;
  const editor={stageSubmission(operation,snapshot){staged={operation,...snapshot};draft='';return true;},
    restoreSubmission(operation){if(staged?.operation!==operation||staged.revision!==revision)return false;draft=staged.text;staged=null;return true;},
    snapshot:()=>({text:draft,revision}),clearIfRevision:expected=>{if(expected===revision){staged=null;draft='';revision++;return true;}return false;},setExpanded(){},onSubmit(fn){this.submit=fn;},
    post(kind,value){
      if(kind!=='chat')return;
      const request=value.chat;requests.push(request);
      const response={target,can_send:true,busy:running};
      switch(request.action){
        case 'hello': break;
        case 'connect': case 'connection': response.connection='connected'; break;
        case 'list': response.conversations=[{id:A,updated_at:'2026-10-27T00:00:00Z',state:'ready'},{id:B,updated_at:'2026-10-26T00:00:00Z',state:'ready'}];break;
        case 'create': response.conversation={id:B,state:'ready'};break;
        case 'select': response.conversation={id:request.conversation_id,state:'ready'};break;
        case 'prepare':
          response.conversation={id:B,state:'creating'};
          queueState.entries.push({kind:'create',state:'queued',operation_id:request.operation_id,conversation_id:B});break;
        case 'enqueue': {
          response.turn={id:request.operation_id,operation_id:request.operation_id,conversation_id:request.conversation_id,delivery:'queued',run:'queued'};
          queueState.entries.push({kind:'send',state:'queued',operation_id:request.operation_id,conversation_id:request.conversation_id,text:request.text,turn:response.turn});break;
        }
        case 'queue_item': response.queue_item=[...queueState.entries,...queueState.failures].find(item=>item.operation_id===request.operation_id);break;
        case 'cancel':
          response.turn={...queueState.active,run:'cancelled'};queueState.active=null;break;
        case 'history':
          response.conversation={id:request.conversation_id,state:'ready'};
          response.turn=request.conversation_id===A?turn():null;
          { const offset=reset?0:request.offset??0;
            response.page={messages:request.conversation_id===A?Array.from({length:50},(_,i)=>message(total-offset-50+i)):[],cursor:'cursor',next_offset:offset+50,has_more:request.conversation_id===A && offset+50<total,reset}; }
          break;
        case 'reconcile': response.turn=turn();break;
        case 'submit': response.turn={...turn(),conversation_id:request.conversation_id,operation_id:request.operation_id};break;
      }
      if(queued)response.queue={...queueState,entries:queueState.entries.map(({text,...item})=>item),
        failures:queueState.failures.map(({text,...item})=>item),revision:++queueRevision};
      Object.assign(response, overrides.get(request.action)?.(request, response));
      const send = (extra={}, envelope={}) => bridge.emit({kind:'chat_result',id:value.id,reply:{...(queued && request.action==='enqueue' && !response.error ? {clear_sequence:request.draft_revision} : {}),...envelope,response:{...response,...extra}}});
      if(holdActions.has(request.action)) held.push({request,send});
      else queueMicrotask(()=>send());
    }};
  const panel=chatPanel(bridge,editor);
  return {elements,requests,bridge,editor,queueState,
    hold(action){holdActions.add(action);}, override(action,fn){overrides.set(action,fn);},
    reply(action,response={},envelope={}){const index=held.findIndex(item=>item.request.action===action);assert.ok(index>=0);held.splice(index,1)[0].send(response,envelope);},
    get confirmations(){return confirmations;},setDraft(value){draft=value;revision++;},setRunning(value){running=value;},setTotal(value){total=value;},setReset(value){reset=value;},
    async open(){bridge.emit({kind:'focus'});await flush();await elements.get('chat-expand').fire('click');await flush();},
    async tick(){const entry=[...timers].find(([,value])=>value.ms===500);assert.ok(entry,'expected scheduled reconciliation');timers.delete(entry[0]);await entry[1].fn();await flush();},
    async connectionTick(){const entry=[...timers].find(([,value])=>value.ms===1000);assert.ok(entry,'expected connection poll');timers.delete(entry[0]);await entry[1].fn();await flush();},
    get connectionTimers(){return [...timers.values()].filter(value=>value.ms===1000).length;},
    close(){panel.dispose();globalThis.setTimeout=originalSetTimeout;globalThis.clearTimeout=originalClearTimeout;}};
}

test('connection label is independent of queue, reply status and conversation selection', async () => {
  const h = harness(true);
  try {
    h.override('connect', () => ({connection:'connecting'}));
    await h.open();
    const label = h.elements.get('chat-connection');
    assert.equal(label.textContent, '连接中');
    assert.equal(label.dataset.state, 'connecting');
    await h.connectionTick();
    assert.equal(label.textContent, '已连接');
    h.setDraft('hello'); await h.editor.submit(); await flush();
    const reply = h.elements.get('chat-status').textContent;
    h.override('connection', () => ({connection:'unavailable', busy:false}));
    await h.connectionTick();
    assert.equal(label.textContent, '无法连接');
    assert.equal(label.dataset.state, 'unavailable');
    assert.equal(h.elements.get('chat-status').textContent, reply);
    assert.equal(h.elements.get('chat-send').disabled, false);
    await h.elements.get('chat-stop').fire('click'); await flush();
    assert.equal(label.textContent, '无法连接');
    await h.elements.get('chat-new').fire('click'); await flush();
    assert.equal(label.textContent, '无法连接');
  } finally { h.close(); }
});

test('expanding before hello finishes connects with the resolved target and no credential failure detail', async () => {
  const h = harness(true);
  try {
    h.hold('hello');
    h.override('connect', () => ({connection:'unavailable'}));
    await h.open();
    assert.equal(h.elements.get('chat-connection').textContent, '连接中');
    h.reply('hello', {can_send:false}); await flush();
    assert.equal(h.requests.filter(request => request.action === 'connect').length, 1);
    assert.equal(h.elements.get('chat-connection').textContent, '无法连接');
    assert.equal(h.elements.get('chat-connection').getAttribute('title'), null);
  } finally { h.close(); }
});

test('empty expanded chat connects once; visibility stops polling and discards delayed snapshots', async () => {
  const h = harness(true);
  try {
    h.setRunning(false);
    h.override('list', () => ({conversations:[]}));
    await h.open();
    assert.equal(h.elements.get('chat-connection').textContent, '已连接');
    assert.equal(h.requests.filter(request => request.action === 'connect').length, 1);
    assert.ok(!h.requests.some(request => ['create','prepare','submit','enqueue'].includes(request.action)));
    h.hold('connection');
    const pending = h.connectionTick(); await flush();
    h.bridge.emit({kind:'visibility', visible:false});
    assert.equal(h.connectionTimers, 0);
    h.reply('connection', {connection:'unavailable'}); await pending;
    assert.equal(h.elements.get('chat-connection').textContent, '已连接');
    h.bridge.emit({kind:'focus'}); await flush();
    assert.equal(h.elements.get('chat-connection').textContent, '已连接');
    assert.equal(h.connectionTimers, 1);
    await h.elements.get('chat-expand').fire('click');
    assert.equal(h.connectionTimers, 0);
  } finally { h.close(); }
});

test('startup queue accepts successive sends to the prepared conversation without waiting for history',async()=>{
  const h=harness(true);
  try {
    h.setRunning(false);await h.open();
    h.hold('history');
    const refreshing=h.elements.get('chat-refresh').fire('click');await flush();
    await h.elements.get('chat-new').fire('click');await flush();
    assert.equal(h.elements.get('chat-conversation-name').textContent,'对话 '+B.slice(0,8));
    for(const text of ['first queued','second queued']){
      h.setDraft(text);await h.editor.submit();await flush();
      assert.equal(h.elements.get('chat-send').disabled,false);
      assert.equal(h.editor.snapshot().text,'');
    }
    const sends=h.requests.filter(request=>request.action==='enqueue');
    assert.equal(sends.length,2);
    assert.ok(sends.every(request=>request.conversation_id===B));
    assert.notEqual(sends[0].operation_id,sends[1].operation_id);
    assert.match(h.elements.get('chat-messages').textContent,/first queued/);
    assert.match(h.elements.get('chat-messages').textContent,/second queued/);
    assert.equal(h.elements.get('chat-status').textContent,'正在回复...');
    assert.doesNotMatch(h.elements.get('chat-messages').textContent,/待发送|取回编辑|取消发送/);
    h.reply('history');await refreshing;await flush();
    assert.equal(h.elements.get('chat-conversation-name').textContent,'对话 '+B.slice(0,8));
  } finally {h.close();}
});

test('startup, preparation and accepted generation keep one ordinary bubble and reply status',async()=>{
  const h=harness(true);
  try {
    h.setRunning(false);await h.open();h.setDraft('one smooth message');await h.editor.submit();await flush();
    const rows=h.elements.get('chat-messages'),status=h.elements.get('chat-status');
    const bubble=rows.children.find(row=>row.textContent.includes('one smooth message'));
    const entry=h.queueState.entries.find(item=>item.kind==='send');
    const assertWaiting=()=>{
      assert.ok(rows.children.includes(bubble),'stage transition replaced the message bubble');
      assert.equal(status.textContent,'正在回复...');
      assert.equal(status.children[1].hidden,false);
      assert.doesNotMatch(bubble.textContent,/待发送|取回编辑|取消发送|正在准备/);
      assert.equal(h.elements.has('chat-queue-resume'),false);
    };
    for(const phase of ['queued','preparing','sending']) {
      entry.state=phase;
      await h.tick();assertWaiting();
      assert.equal(h.elements.get('chat-stop').hidden,true);
    }
    const accepted={...entry.turn,id:'native-turn',delivery:'accepted',run:'running',user_message_id:'native-user'};
    h.queueState.entries=[];h.queueState.active=accepted;
    h.override('queue_item',()=>({queue_item:{...entry,state:'settled',turn:accepted}}));
    h.override('reconcile',()=>({turn:accepted}));
    h.override('history',()=>({turn:accepted,page:{messages:[{id:'native-user',role:'user',blocks:[{type:'text',text:'one smooth message'}]}]}}));
    await h.tick();assertWaiting();
    assert.equal(h.elements.get('chat-stop').hidden,false);
    assert.equal(rows.children.filter(row=>row.textContent.includes('one smooth message')).length,1);
    assert.equal(h.requests.filter(request=>request.action==='enqueue').length,1);
  } finally {h.close();}
});

test('a new empty conversation does not inherit another conversation startup status',async()=>{
  const h=harness(true);
  try {
    h.setRunning(false);await h.open();h.setDraft('message for A');await h.editor.submit();await flush();
    await h.elements.get('chat-new').fire('click');await flush();
    assert.doesNotMatch(h.elements.get('chat-status').textContent,/正在回复|待发送|正在准备/);
    assert.equal(h.elements.get('chat-status').children[1].hidden,true);
    assert.doesNotMatch(h.elements.get('chat-messages').textContent,/message for A/);
  } finally {h.close();}
});

test('startup failure stays on the message without recall controls and new sends remain available',async()=>{
  const h=harness(true);
  try {
    h.setRunning(false);await h.open();await h.elements.get('chat-new').fire('click');await flush();
    h.setDraft('retain on actual failure');await h.editor.submit();await flush();
    const rows=h.elements.get('chat-messages'),status=h.elements.get('chat-status');
    assert.doesNotMatch(rows.textContent,/取回编辑|取消发送/);
    const failed=h.queueState.entries.find(item=>item.kind==='send');
    failed.state='failed';failed.turn={...failed.turn,delivery:'rejected',run:'failed'};
    h.queueState.failures=[failed];h.queueState.entries=[];
    await h.tick();
    assert.equal(status.textContent,'消息未送达');
    assert.equal(status.children[1].hidden,true);
    assert.match(rows.textContent,/retain on actual failure[\s\S]*消息未送达/);
    assert.doesNotMatch(rows.textContent,/取回编辑|取消发送|继续发送|移出队列/);
    h.setDraft('new message after failure');await h.editor.submit();await flush();
    assert.equal(status.textContent,'正在回复...');
    assert.doesNotMatch(rows.textContent,/取回编辑|取消发送/);
    assert.equal(h.requests.filter(request=>request.action==='enqueue').length,2);
  } finally {h.close();}
});

test('queued sends have no stop control and preserve delivery after hiding and new sends',async()=>{
  const h=harness(true);
  try {
    h.setRunning(false);await h.open();h.setDraft('keep while hidden');await h.editor.submit();await flush();
    assert.equal(h.elements.get('chat-stop').hidden,true);
    await h.elements.get('chat-stop').fire('click');await flush();
    assert.equal(h.requests.some(request=>request.action==='cancel'),false);
    assert.equal(h.elements.get('chat-status').textContent,'正在回复...');
    assert.doesNotMatch(h.elements.get('chat-messages').textContent,/取回编辑|取消发送/);
    h.bridge.emit({kind:'visibility',visible:false});
    h.bridge.emit({kind:'visibility',visible:true});await h.tick();
    assert.equal(h.queueState.paused,false);
    assert.match(h.elements.get('chat-messages').textContent,/keep while hidden/);
    h.setDraft('new message');await h.editor.submit();await flush();
    assert.equal(h.requests.filter(request=>request.action==='enqueue').length,2);
    assert.equal(h.requests.some(request=>['queue_pause','queue_resume','queue_cancel'].includes(request.action)),false);
  } finally {h.close();}
});

test('queue admission failure restores only the submitted draft',async()=>{
  const h=harness(true);
  try {
    h.setRunning(false);await h.open();
    h.override('enqueue',()=>({turn:null,error:{code:'queue_full',message:'待发队列已满，当前草稿已保留'}}));
    h.setDraft('overflow draft');await h.editor.submit();await flush();
    assert.equal(h.editor.snapshot().text,'overflow draft');
    assert.match(h.elements.get('chat-status').textContent,/队列已满/);
  } finally {h.close();}
});

test('stop reply targets only the accepted turn and leaves subsequent sends active',async()=>{
  const h=harness(true);
  try {
    h.setRunning(false);await h.open();h.setDraft('preserve this user message');await h.editor.submit();await flush();
    const entry=h.queueState.entries[0];
    const active={...entry.turn,delivery:'accepted',run:'running',user_message_id:'stop-user'};
    h.queueState.entries=[];h.queueState.active=active;
    h.override('queue_item',()=>({queue_item:{...entry,state:'settled',turn:active}}));
    h.override('reconcile',()=>({turn:active}));
    await h.tick();
    assert.equal(h.elements.get('chat-stop').hidden,false);
    h.hold('cancel');
    const stopped=h.elements.get('chat-stop').fire('click');await flush();
    assert.equal(h.elements.get('chat-stop').disabled,true);
    h.reply('cancel');await stopped;await flush();
    assert.equal(h.requests.find(request=>request.action==='cancel').operation_id,active.operation_id);
    assert.match(h.elements.get('chat-messages').textContent,/preserve this user message/);
    h.setDraft('next message after stop');await h.editor.submit();await flush();
    assert.equal(h.elements.get('chat-status').textContent,'正在回复...');
    assert.equal(h.elements.get('chat-stop').hidden,true);
    assert.equal(h.requests.filter(request=>request.action==='enqueue').length,2);
    assert.equal(h.requests.some(request=>['queue_pause','queue_resume','queue_cancel'].includes(request.action)),false);
  } finally {h.close();}
});

test('a running reply in another conversation does not expose its stop button',async()=>{
  const h=harness(true);
  try {
    h.setRunning(false);await h.open();
    h.queueState.active={operation_id:'other-running',conversation_id:B,delivery:'accepted',run:'running'};
    h.setDraft('message in A');await h.editor.submit();await flush();
    assert.equal(h.elements.get('chat-stop').hidden,true);
    await h.elements.get('chat-stop').fire('click');await flush();
    assert.equal(h.requests.some(request=>request.action==='cancel'),false);
  } finally {h.close();}
});

test('queue completion between polls still loads native reply history',async()=>{
  const h=harness(true);
  try {
    h.setRunning(false);await h.open();h.setDraft('fast queue');await h.editor.submit();await flush();
    const request=h.requests.find(item=>item.action==='enqueue');
    const completed={operation_id:request.operation_id,conversation_id:A,id:'fast-native-turn',delivery:'accepted',run:'completed',persistence:'saved',user_message_id:'fast-user'};
    h.queueState.entries=[];
    h.override('queue_item',()=>({queue_item:{operation_id:request.operation_id,conversation_id:A,kind:'send',state:'settled',turn:completed}}));
    h.override('history',()=>({turn:completed,page:{messages:[{id:'fast-user',role:'user',blocks:[{type:'text',text:'fast queue'}]},{id:'fast-reply',role:'assistant',blocks:[{type:'text',text:'fast reply received'}]}]}}));
    await h.tick();
    assert.match(h.elements.get('chat-messages').textContent,/fast reply received/);
    assert.equal(h.elements.get('chat-messages').children.filter(row=>row.textContent.includes('fast queue')).length,1);
    assert.equal(h.requests.filter(item=>item.action==='enqueue').length,1);
  } finally {h.close();}
});

test('new conversation requires a decision before carrying an unsent draft to its target',async()=>{
  const h=harness();
  try {
    h.setRunning(false); await h.open();h.setDraft('draft intended for conversation A');
    await h.elements.get('chat-new').fire('click');await flush();
    const created=h.requests.filter(request=>request.action==='create');
    assert.equal(created.length,0,'new conversation was created without asking to keep/discard the existing draft');
  } finally {h.close();}
});

test('collapsed active replies reconcile completion without reading history and resume after hiding', async () => {
  const h = harness();
  try {
    await h.open();
    await h.elements.get('chat-expand').fire('click'); await flush();
    const historyReads = h.requests.filter(request => request.action === 'history').length;
    h.bridge.emit({kind:'visibility', visible:false});
    await assert.rejects(h.tick(), /expected scheduled reconciliation/);
    h.setRunning(false);
    h.bridge.emit({kind:'visibility', visible:true});
    await h.tick();
    assert.equal(h.elements.get('chat-status').textContent, '');
    assert.equal(h.elements.get('chat-send').disabled, false);
    assert.equal(h.requests.filter(request => request.action === 'history').length, historyReads);
    await h.elements.get('chat-expand').fire('click'); await flush();
    assert.equal(h.requests.filter(request => request.action === 'history').length, historyReads + 1);
  } finally { h.close(); }
});

test('finishing a reply preserves the old page currently being read',async()=>{
  const h=harness();
  try {
    await h.open();await h.elements.get('chat-older').fire('click');await flush();
    const rows=h.elements.get('chat-messages');
    assert.ok(rows.children.some(child=>child.dataset.messageId==='message-20'),'older page was not loaded');
    h.elements.get('chat-scroll').scrollTop=200;
    h.setRunning(false);await h.tick();
    assert.ok(rows.children.some(child=>child.dataset.messageId==='message-20'),'completion replaced the reader\'s older page with the latest page');
  } finally {h.close();}
});

test('collapsed focus restores metadata without loading native history',async()=>{
  const h=harness();
  try {
    h.bridge.emit({kind:'focus'});await flush();
    assert.equal(h.requests.filter(request=>request.action==='history').length,0);
    await h.elements.get('chat-expand').fire('click');await flush();
    assert.equal(h.requests.filter(request=>request.action==='history').length,1);
  } finally {h.close();}
});

test('draft switch cancellation and stale discard preserve text and target',async()=>{
  const h=harness();
  try {
    h.setRunning(false);await h.open();h.setDraft('original');
    await h.elements.get('chat-new').fire('click');await flush();
    assert.equal(h.elements.get('chat-draft-decision').hidden,false);
    await h.elements.get('chat-draft-cancel').fire('click');await flush();
    assert.equal(h.editor.snapshot().text,'original');
    await h.elements.get('chat-new').fire('click');await flush();h.setDraft('newer');
    await h.elements.get('chat-draft-discard').fire('click');await flush();
    assert.equal(h.editor.snapshot().text,'newer');
    assert.equal(h.requests.filter(request=>request.action==='create').length,0);
    await h.elements.get('chat-new').fire('click');await flush();
    await h.elements.get('chat-draft-discard').fire('click');await flush();
    assert.equal(h.editor.snapshot().text,'');
    assert.equal(h.requests.filter(request=>request.action==='create').length,1);
  } finally {h.close();}
});

test('history selection also requires a draft decision',async()=>{
  const h=harness();
  try {
    h.setRunning(false);await h.open();h.setDraft('keep here');
    await h.elements.get('chat-history').fire('click');await flush();
    await h.elements.get('chat-conversations').children[1].fire('click');await flush();
    assert.equal(h.elements.get('chat-draft-decision').hidden,false);
    assert.equal(h.requests.filter(request=>request.action==='history' && request.conversation_id===B).length,0);
  } finally {h.close();}
});

test('older-page eviction preserves message identity and pixel anchor at 200 rows',async()=>{
  const h=harness();
  try {
    h.setRunning(false);h.setTotal(300);await h.open();
    for(let i=0;i<3;i++){await h.elements.get('chat-older').fire('click');await flush();}
    const rows=h.elements.get('chat-messages'),scroll=h.elements.get('chat-scroll');
    assert.equal(rows.children.length,200);
    scroll.scrollTop=200;
    const anchor=rows.children[20];
    const before=anchor.getBoundingClientRect().top;
    await h.elements.get('chat-older').fire('click');await flush();
    assert.equal(rows.children.length,200);
    assert.ok(rows.children.includes(anchor));
    assert.equal(anchor.getBoundingClientRect().top,before);
    assert.equal(scroll.scrollTop,700);
    h.setReset(true);
    await h.elements.get('chat-older').fire('click');await flush();
    assert.equal(rows.children.length,50);
    assert.equal(rows.children[0].dataset.messageId,'message-250');
    assert.ok(h.elements.get('chat-status').textContent.includes('历史已变化'));
  } finally {h.close();}
});

test('pre-send bubble appears before receipt and merges with native history without duplicate or node replacement',async()=>{
  const h=harness();
  try {
    h.setRunning(false);await h.open();h.setDraft('visible immediately');h.hold('submit');
    const submitted=h.editor.submit();await flush();
    const rows=h.elements.get('chat-messages');
    const preview=rows.children.find(row=>row.textContent.includes('visible immediately'));
    assert.ok(preview,'no local bubble while waiting for receipt');
    assert.equal(h.editor.snapshot().text,'','submitted text is still visible in the composer');
    const operation=h.requests.find(request=>request.action==='submit').operation_id;
    const accepted={id:'sent-turn',operation_id:operation,conversation_id:A,delivery:'accepted',run:'running',user_message_id:'native-user'};
    h.override('history',()=>({turn:accepted,page:{messages:[{id:'native-user',role:'user',turn_id:'sent-turn',blocks:[{type:'text',text:'visible immediately'}]}]}}));
    h.reply('submit',{turn:accepted});await submitted;await flush();
    assert.equal(rows.children.filter(row=>row.textContent.includes('visible immediately')).length,1);
    assert.equal(rows.children[0],preview,'native confirmation replaced the bubble DOM identity');
    assert.doesNotMatch(rows.textContent,/发送成功|发送失败|待确认/);
  } finally {h.close();}
});

test('sending keeps one reply status across old history, receipt, preparation and generation', async () => {
  const h = harness();
  try {
    h.setRunning(false); await h.open(); h.setDraft('one stable wait');
    h.hold('submit'); h.hold('history');
    const submitted = h.editor.submit(); await flush();
    const status = h.elements.get('chat-status');
    assert.equal(status.textContent, '正在回复...');
    const refreshing = h.elements.get('chat-refresh').fire('click'); await flush();
    h.reply('history'); await refreshing; await flush(); // Previous completed turn arrives during this send.
    assert.equal(status.textContent, '正在回复...');
    const operation = h.requests.find(request => request.action === 'submit').operation_id;
    const next = {id: 'next-turn', operation_id: operation, conversation_id: A,
      delivery: 'accepted', run: 'pending', persistence: 'pending'};
    h.reply('submit', {turn: next}); await flush();
    assert.equal(status.textContent, '正在回复...');
    h.reply('history', {turn: next}); await submitted;
    assert.equal(status.textContent, '正在回复...');
    h.override('reconcile', () => ({turn: {...next, run: 'running'}}));
    await h.tick(); assert.equal(status.textContent, '正在回复...');
    h.override('reconcile', () => ({turn: {...next, run: 'completed', persistence: 'saved'}}));
    const finished = h.tick(); await flush();
    h.reply('history', {turn: {...next, run: 'completed', persistence: 'saved'}});
    await finished; assert.equal(status.textContent, '');
  } finally { h.close(); }
});

test('durable receipt before transcript insertion keeps the same bubble without a delivery warning', async () => {
  const h = harness();
  try {
    h.setRunning(false); await h.open(); h.setDraft('pending native transcript'); h.hold('submit');
    const submitted = h.editor.submit(); await flush();
    const rows = h.elements.get('chat-messages'), status = h.elements.get('chat-status');
    const preview = rows.children.at(-1);
    const operation = h.requests.find(request => request.action === 'submit').operation_id;
    const accepted = {id: 'queued-turn', operation_id: operation, conversation_id: A,
      delivery: 'accepted', run: 'running', persistence: 'pending'};
    h.override('history', () => ({turn: accepted, page: {messages: []}}));
    h.reply('submit', {turn: accepted}); await submitted;
    assert.equal(status.textContent, '正在回复...');
    assert.deepEqual(rows.children, [preview]);
    assert.equal(preview.children.length, 2, 'receipt inserted a temporary delivery-status row');
    assert.doesNotMatch(rows.textContent, /核对|待确认|失败/);
    const confirmed = {...accepted, user_message_id: 'native-user'};
    h.override('history', () => ({turn: confirmed, page: {messages: [{id: 'native-user', role: 'user',
      turn_id: accepted.id, blocks: [{type: 'text', text: 'pending native transcript'}]}]}}));
    await h.elements.get('chat-refresh').fire('click');
    assert.equal(status.textContent, '正在回复...');
    assert.deepEqual(rows.children, [preview]);
    assert.equal(preview.children.length, 2);
  } finally { h.close(); }
});

test('cold connection and new conversation never flash internal stages after send', async () => {
  const h = harness();
  try {
    h.setRunning(false); h.setDraft('first send');
    h.override('list', () => ({conversations: []}));
    h.hold('hello'); h.hold('create'); h.hold('submit');
    h.bridge.emit({kind: 'focus'}); await flush();
    const status = h.elements.get('chat-status'), label = status.children[0], changes = [];
    Object.defineProperty(label, 'textContent', {
      get() { return this._text; },
      set(value) { this._text = String(value); this.children = []; changes.push(this._text); },
    });
    const submitted = h.editor.submit(); await flush();
    h.reply('hello'); await flush();
    assert.equal(status.textContent, '正在回复...');
    h.reply('create'); await flush();
    assert.equal(status.textContent, '正在回复...');
    const operation = h.requests.find(request => request.action === 'submit').operation_id;
    const completed = {id: 'new-turn', operation_id: operation, conversation_id: B,
      delivery: 'accepted', run: 'completed', persistence: 'saved'};
    h.override('history', () => ({turn: completed, page: {messages: []}}));
    h.reply('submit', {turn: completed}); await submitted;
    assert.deepEqual(changes, ['正在回复', '']);
    assert.equal(status.textContent, '');
  } finally { h.close(); }
});

test('rejected sends attach an error to the local bubble and keep the draft',async()=>{
  const h=harness();
  try {
    h.setRunning(false);await h.open();h.setDraft('keep my text');h.hold('submit');
    const submitted=h.editor.submit();await flush();
    h.reply('submit',{error:{code:'rejected',message:'request rejected'},turn:undefined});await submitted;
    assert.match(h.elements.get('chat-messages').textContent,/keep my text[\s\S]*消息未送达/);
    assert.equal(h.editor.snapshot().text,'keep my text');
  } finally {h.close();}
});

test('cold connection shows a preview before hello and waits for the existing connection',async()=>{
  const h=harness();
  try {
    h.setRunning(false);h.hold('hello');h.hold('submit');h.setDraft('cold startup');
    h.bridge.emit({kind:'focus'});
    const submitted=h.editor.submit();await flush();
    assert.match(h.elements.get('chat-messages').textContent,/cold startup/);
    assert.equal(h.editor.snapshot().text,'','cold startup left the message in both places');
    assert.equal(h.requests.filter(r=>r.action==='hello').length,1);
    assert.equal(h.requests.filter(r=>r.action==='submit').length,0);
    h.reply('hello');await flush();
    assert.equal(h.requests.filter(r=>r.action==='submit').length,1);
    h.reply('submit');await submitted;
  } finally {h.close();}
});

test('unknown delivery keeps its original identity through reconciliation and does not permit duplicate send',async()=>{
  const h=harness();
  try {
    h.setRunning(false);await h.open();h.setDraft('confirm original');h.hold('submit');
    const submitted=h.editor.submit();await flush();
    const op=h.requests.find(r=>r.action==='submit').operation_id;
    const unknown={id:'unknown-turn',operation_id:op,conversation_id:A,delivery:'unknown',run:'unknown'};
    h.reply('submit',{error:{code:'unavailable',message:'confirmation lost'},turn:unknown});await submitted;
    assert.match(h.elements.get('chat-messages').textContent,/送达待确认/);
    assert.equal(h.editor.snapshot().text,'');
    await h.editor.submit();
    assert.equal(h.requests.filter(r=>r.action==='submit').length,1);
    const accepted={...unknown,delivery:'accepted',run:'completed',user_message_id:'confirmed-user',assistant_message_id:'confirmed-assistant'};
    const page={messages:[{id:'confirmed-user',role:'user',turn_id:'unknown-turn',blocks:[{type:'text',text:'confirm original'}]}]};
    h.override('reconcile',()=>({turn:accepted}));h.override('history',()=>({turn:accepted,page}));
    await h.tick();
    assert.equal(h.requests.find(r=>r.action==='reconcile').operation_id,op);
    assert.equal(h.elements.get('chat-messages').children.length,1);
    assert.doesNotMatch(h.elements.get('chat-messages').textContent,/待确认|失败/);
  } finally {h.close();}
});


test('delayed history cannot undo completion or replace its page, and refreshes once released',async()=>{
  const h=harness();
  try {
    await h.open(); h.hold('history');
    const refresh=h.elements.get('chat-refresh').fire('click');await flush();
    h.setRunning(false);await h.tick();
    assert.equal(h.elements.get('chat-status').textContent,'');
    h.reply('history');await refresh;await flush();
    assert.equal(h.elements.get('chat-status').textContent,'');
    assert.equal(h.elements.get('chat-stop').hidden,true);
    assert.equal(h.elements.get('chat-send').disabled,false);
    const next=h.tick();await flush();
    h.reply('history',{page:{messages:[{...message(101),id:'final-message'}],has_more:false}});
    await next;
    assert.ok(h.elements.get('chat-messages').children.some(row=>row.dataset.messageId==='final-message'));
  } finally {h.close();}
});


test('first conversation creation consumes the composer and restores it on explicit creation failure',async()=>{
  const h=harness();
  try {
    h.setRunning(false);h.override('list',()=>({conversations:[]}));h.hold('create');
    h.bridge.emit({kind:'focus'});await flush();h.setDraft('first conversation');
    const submitted=h.editor.submit();await flush();
    assert.equal(h.editor.snapshot().text,'');
    assert.match(h.elements.get('chat-messages').textContent,/first conversation/);
    assert.equal(h.requests.filter(r=>r.action==='submit').length,0);
    h.reply('create',{error:{code:'rejected',message:'creation failed'}});await submitted;
    assert.equal(h.editor.snapshot().text,'first conversation');
    assert.match(h.elements.get('chat-messages').textContent,/消息未送达/);
  } finally {h.close();}
});

test('rejected sends and late accepted receipts preserve a newer composer draft',async()=>{
  for(const accepted of [false,true]) {
    const h=harness();
    try {
      h.setRunning(false);await h.open();h.setDraft('original');h.hold('submit');
      const revision=h.editor.snapshot().revision;
      const submitted=h.editor.submit();await flush();
      assert.equal(h.editor.snapshot().text,'');h.setDraft('new draft');
      h.reply('submit',accepted?{}:{error:{code:'rejected',message:'rejected'},turn:undefined},accepted?{clear_sequence:revision}:{});
      await submitted;
      assert.equal(h.editor.snapshot().text,'new draft');
    } finally {h.close();}
  }
});

test('unknown delivery restores the untouched composer only after authoritative rejection',async()=>{
  const h=harness();
  try {
    h.setRunning(false);await h.open();h.setDraft('recover on rejection');h.hold('submit');
    const submitted=h.editor.submit();await flush();
    const op=h.requests.find(r=>r.action==='submit').operation_id;
    const unknown={id:'unknown-turn',operation_id:op,conversation_id:A,delivery:'unknown',run:'unknown'};
    h.reply('submit',{error:{code:'unavailable',message:'confirmation lost'},turn:unknown});await submitted;
    assert.equal(h.editor.snapshot().text,'');
    h.override('reconcile',()=>({turn:{...unknown,delivery:'rejected',run:'failed'}}));
    await h.tick();
    assert.equal(h.editor.snapshot().text,'recover on rejection');
    assert.equal(h.requests.filter(r=>r.action==='submit').length,1);
  } finally {h.close();}
});
