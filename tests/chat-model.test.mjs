import test from 'node:test';
import assert from 'node:assert/strict';
import {ChatWindow, MAX_BODY_BYTES, bodyBytes, unsettled} from '../internal/quickinput/assets/chat-model.mjs';
import {conversationClient} from '../internal/quickinput/assets/chat-client.mjs';
import {historyPage, messageFragment, StreamState, observation, byteSize} from '../internal/executor/chatbridge/projection.mjs';

const message = (id, text = '中文🙂') => ({id, role: 'assistant', blocks: [{type: 'text', text}]});

test('history window bounds count and bytes, deduplicates pages, and resets stale cursors', () => {
  const state = new ChatWindow();
  state.apply({messages: Array.from({length: 250}, (_, i) => message(String(i))), cursor: 'one', next_offset: 250, has_more: true});
  assert.equal(state.messages.length, 200);
  assert.equal(state.messages[0].id, '50');
  state.apply({messages: [message('older'), message('50')], cursor: 'one', next_offset: 252, has_more: true}, true);
  assert.equal(state.messages.length, 200);
  assert.equal(state.messages[0].id, 'older');
  assert.equal(state.messages.filter(item => item.id === '50').length, 1);
  state.apply({messages: [message('new')], cursor: 'two', reset: true}, true);
  assert.deepEqual(state.messages.map(item => item.id), ['new']);
  state.apply({messages: Array.from({length: 20}, (_, i) => message(String(i), '中'.repeat(50000)))});
  assert.ok(state.messages.reduce((sum, item) => sum + bodyBytes(item), 0) <= MAX_BODY_BYTES);
  for (let i = 0; i < 100; i++) state.live({...message('live-' + i), transient: true});
  assert.equal(state.messages.filter(item => item.transient).length, 1);
  assert.equal(unsettled({run: 'interrupted', persistence: 'unknown'}), false);
});

test('ten thousand native history rows remain independently reachable through a bounded window', () => {
  const state = new ChatWindow();
  const visited = new Set();
  for (let offset = 0; offset < 10000; offset += 50) {
    const entries = Array.from({length: 50}, (_, index) => message(String(9999 - offset - index))).reverse();
    entries.forEach(entry => visited.add(entry.id));
    state.apply({messages: entries, cursor: 'stable', next_offset: offset + 50, has_more: offset < 9950}, offset !== 0);
    assert.ok(state.messages.length <= 200);
    assert.ok(state.messages.reduce((sum, entry) => sum + bodyBytes(entry), 0) <= MAX_BODY_BYTES);
  }
  assert.equal(visited.size, 10000);
  assert.equal(state.messages[0].id, '0');
  assert.equal(state.hasMore, false);
});

test('native projection is bounded after Go escaping and fragments preserve exact Unicode', () => {
  const text = '<>&🙂中文\u2028\n'.repeat(9000);
  const native = {role: 'assistant', timestamp: 1700000000000, content: [{type: 'text', text}], __openclaw: {id: 'native-1', runId: 'turn'}};
  const page = historyPage({sessionId: 'session', deltaCursor: 'revision', messages: Array.from({length: 24}, (_, i) => ({...native, __openclaw: {id: 'id-' + i}}))}, 0, '');
  assert.ok(byteSize(page) < 64 * 1024);
  assert.ok(page.messages.every(item => item.truncated));
  assert.equal(page.messages[0].timestamp, new Date(native.timestamp).toISOString());
  assert.deepEqual(historyPage({sessionId: 'session', deltaCursor: 'changed'}, 24, page.cursor), {resetRequired: true});
  let restored = '', offset = 0, fragment;
  do {
    fragment = messageFragment(native, 'native-1', offset);
    assert.ok(Buffer.byteLength(fragment.text) <= 8192);
    restored += fragment.text;
    offset = fragment.next_offset;
  } while (fragment.has_more);
  assert.equal(restored, text);
  assert.throws(() => messageFragment(native, 'native-1', 4));
});

test('stream ignores duplicates, retains bounded runs, and requires stop proof', () => {
  const state = new StreamState();
  const event = (runId, seq, deltaText) => ({event: 'chat', payload: {sessionKey: 'session', runId, seq, state: 'delta', deltaText}});
  state.event(event('one', 1, 'first'));
  state.event(event('one', 1, 'duplicate'));
  state.event(event('one', 0, 'old'));
  assert.equal(state.get('one', 'session').text, 'first');
  state.event(event('one', 2, '中'.repeat(10000)));
  assert.ok(Buffer.byteLength(state.get('one', 'session').text) <= 8192);
  for (let i = 0; i < 20; i++) state.event(event(String(i), 1, 'value'));
  assert.equal(state.runs.size, 16);
  const history = {sessionInfo: {hasActiveRun: false}, messages: []};
  assert.equal(observation(history, 'run', 200, 100, null).stopped, false);
  assert.equal(observation(history, 'run', 100, 200, null).stopped, true);
  history.sessionInfo.hasActiveRun = true;
  assert.equal(observation(history, 'run', 100, 200, {terminal: 'final'}).stopped, false);
});

test('closed client preserves operation identity and processes a late durable receipt', async () => {
  let receive;
  const posts = [], envelopes = [];
  const client = conversationClient({subscribe(fn) { receive = fn; return () => {}; }}, {post(kind, value) { posts.push({kind, ...value}); }}, value => envelopes.push(value));
  const hello = client.call('hello');
  receive({kind: 'chat_result', id: posts[0].id, reply: {response: {target: {node_id: 'node', role_id: 'default'}}}});
  await hello;
  const pending = client.call('submit', {operation_id: 'operation', conversation_id: 'conversation', text: 'private', draft_revision: 7});
  assert.equal(posts[1].chat.operation_id, 'operation');
  receive({kind: 'chat_result', id: posts[1].id, reply: {clear_sequence: 7, response: {turn: {delivery: 'accepted'}}}});
  await pending;
  receive({kind: 'chat_result', id: 'late-id', reply: {clear_sequence: 6, response: {}}});
  assert.equal(envelopes.length, 3);
  assert.equal(posts.length, 2);
  client.dispose();
});

import {ChatOutbox} from '../internal/quickinput/assets/chat-outbox.mjs';
import {visibleHistory, visibleBodyState, inputReceipt} from '../internal/executor/chatbridge/projection.mjs';

test('durable input receipts confirm custody before visible history without guessing by run activity or text', () => {
  for (const receipt of [{runId: 'current', state: 'pending'}, {idempotencyKey: 'current', state: 'consumed'}]) {
    const history = {messages: [], inputReceipts: [receipt]};
    assert.deepEqual(inputReceipt(history, 'current'), {accepted: true, user_message: ''});
    assert.equal(observation(history, 'current', 200, 100, null).accepted, true);
  }
  for (const identity of [{idempotencyKey: 'current:user'}, {__openclaw: {id: 'native', idempotencyKey: 'current:user'}}]) {
    const native = {role: 'user', content: 'same text', __openclaw: {id: 'native'}, ...identity};
    assert.deepEqual(inputReceipt({messages: [native]}, 'current'), {accepted: true, user_message: 'native'});
  }
  for (const history of [{}, {inputReceipts: [{runId: 'previous'}]},
    {sessionInfo: {hasActiveRun: true, activeRunIds: ['current']}},
    {messages: [{role: 'user', content: 'same text', __openclaw: {id: 'previous', runId: 'current'}}]}]) {
    assert.deepEqual(inputReceipt(history, 'current'), {accepted: false, user_message: ''});
  }
});

test('outbox merges by identity, preserves distinct equal text, and separates delivery from generation', () => {
  const box = new ChatOutbox();
  box.begin('first','A','same'); box.begin('second','A','same'); box.begin('third','B','elsewhere');
  assert.equal(box.merge([], 'A').length, 2);
  box.observe({operation_id:'first',conversation_id:'A',id:'turn1',user_message_id:'native1',delivery:'accepted',run:'running'});
  const native={id:'native1',role:'user',turn_id:'turn1',blocks:[{type:'text',text:'same'}]};
  box.reconcile([native], 'A');
  const merged=box.merge([native], 'A');
  assert.equal(merged.length,2); assert.equal(merged[0].ui_id,'pending-first');
  assert.equal(merged[0].delivery_status,'');
  assert.equal(box.entries.get('first').text,'');
  box.observe({operation_id:'first',conversation_id:'A',id:'turn1',user_message_id:'native1',delivery:'accepted',run:'failed'});
  assert.match(box.merge([native],'A')[0].delivery_status,/回复失败/);
  box.fail('second',true,{});
  assert.match(box.merge([native],'A')[1].delivery_status,/送达待确认/);
  box.observe({operation_id:'second',conversation_id:'A',id:'turn2',delivery:'rejected',run:'failed'});
  assert.match(box.merge([native],'A')[1].delivery_status,/消息发送失败/);
  assert.equal(box.merge([], 'B').length,1);
  for(let i=0;i<20;i++)box.begin('op'+i,'A','x'.repeat(16000));
  assert.equal(box.entries.size,8);assert.equal(box.reservedBytes,128000);
  const window=new ChatWindow();window.reserve(box.reservedBytes);
  window.apply({messages:Array.from({length:200},(_,i)=>message('large'+i,'x'.repeat(16000)))});
  assert.ok(window.messages.reduce((n,m)=>n+bodyBytes(m),0)+box.reservedBytes<=MAX_BODY_BYTES);
  assert.ok(box.merge(window.messages,'A').length<=200);
});

test('visible body completeness ignores hidden-field display caps and resolves segment boundaries', async () => {
  const make=(content,extra={})=>({role:'assistant',content,__openclaw:{id:'native',truncated:true,reason:'display-cap'},...extra});
  const short=make([{type:'thinking',thinking:'hidden preview'},{type:'text',text:'short answer'}]);
  const exact=make([{type:'text',text:'x'.repeat(1000)}]);
  const multiple=make([{type:'text',text:'x'.repeat(600)},{type:'text',text:'y'.repeat(600)}]);
  let reads=0;
  const page=await visibleHistory({messages:[short,exact,multiple]},()=>{reads++;throw Error('unexpected full read');});
  assert.ok(page.messages.every(m=>m.__openclaw.truncated===false));assert.equal(reads,0);
  const cut=make([{type:'text',text:'中'.repeat(1000)+'\n...(truncated)...'}]);
  assert.equal(visibleBodyState(cut,1000),'truncated');
  const segment=make([{type:'text',text:'x'.repeat(1000)}],{openclawStreamFallback:{source:'segment'}});
  const complete=await visibleHistory({messages:[segment]},async(id,limit)=>{assert.equal(limit,1002);return segment;});
  assert.equal(complete.messages[0].__openclaw.truncated,false);
  const incomplete=await visibleHistory({messages:[segment]},async()=>make([{type:'text',text:'x'.repeat(1001)}]));
  assert.equal(incomplete.messages[0].__openclaw.truncated,true);
  const ambiguousProbe=make([{type:'text',text:'x'.repeat(1000)},{type:'text',text:''}],{openclawStreamFallback:{source:'segment'}});
  const stillCut=await visibleHistory({messages:[segment]},async()=>ambiguousProbe);
  assert.equal(stillCut.messages[0].__openclaw.truncated,true,'equal text is not proof when the probe still exhausts its segment budget');
  const failed=await visibleHistory({messages:[segment]},async()=>{throw Error('offline');});
  assert.equal(failed.messages[0].__openclaw.truncated,true);
  assert.equal(visibleBodyState({...short,__openclaw:{truncated:true,reason:'oversized'}},1000),'truncated');
});

test('older delivery replies cannot downgrade accepted or terminal presentation',()=>{
  const box=new ChatOutbox();box.begin('op','A','body');box.dispatched('op');
  assert.equal(box.pending('A').operation_id,'op');
  box.observe({operation_id:'op',conversation_id:'A',id:'turn',delivery:'accepted',run:'failed',user_message_id:'native',updated_at:'2026-01-01T10:00:02Z'});
  box.observe({operation_id:'op',conversation_id:'A',id:'turn',delivery:'unknown',run:'running',updated_at:'2026-01-01T10:00:01Z'});
  assert.equal(box.entries.get('op').delivery,'accepted');assert.equal(box.entries.get('op').run,'failed');
  assert.equal(box.pending('A'),null);
  box.begin('preflight','A','not written');box.fail('preflight',true,{notDispatched:true});
  assert.equal(box.entries.get('preflight').delivery,'rejected');
});


import {newerTurn} from '../internal/quickinput/assets/chat-model.mjs';
test('shared turn ordering preserves a completed turn and accepts the next operation',()=>{
  const completed={operation_id:'op',conversation_id:'A',delivery:'accepted',run:'completed',updated_at:'2026-10-27T01:00:02Z'};
  assert.equal(newerTurn(completed,{...completed,run:'running',updated_at:'2026-10-27T01:00:01Z'}),completed);
  assert.equal(newerTurn(completed,{...completed,run:'unknown',updated_at:'2026-10-27T01:00:03Z'}),completed);
  assert.equal(newerTurn(completed,null),completed);
  const next={...completed,operation_id:'next',run:'running',updated_at:'2026-10-27T01:00:04Z'};
  assert.equal(newerTurn(completed,next),next);
});
