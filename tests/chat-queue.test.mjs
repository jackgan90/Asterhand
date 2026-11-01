import test from 'node:test';
import assert from 'node:assert/strict';
import {ChatOutbox} from '../internal/quickinput/assets/chat-outbox.mjs';
import {operationQueue} from '../internal/quickinput/assets/chat-queue.mjs';
import {newerTurn} from '../internal/quickinput/assets/chat-model.mjs';

const turn = (operation, delivery='queued', run='queued') => ({operation_id:operation, conversation_id:'A', delivery, run});
const item = operation => ({operation_id:operation, conversation_id:'A', kind:'send', state:'queued', turn:turn(operation)});
const state = (revision, entries=[], active=null) => ({revision, entries, active, paused:false, limit:8});

test('queue snapshots are monotonic and queued turns can advance to running', () => {
  const box=new ChatOutbox(), queue=operationQueue({},box);
  queue.observe(state(4,[item('one'),item('two')]));
  queue.observe(state(3,[]));
  assert.equal(queue.count,2);
  const accepted=turn('one','accepted','running');
  assert.equal(newerTurn(turn('one'),accepted),accepted);
  queue.observe(state(5,[item('two')],accepted));
  assert.equal(box.entries.get('one').delivery,'accepted');
  assert.equal(box.entries.get('one').queued,false);
  assert.equal(queue.count,1);
});

test('host reconstruction hydrates each queued body once and never sends text', async () => {
  const box=new ChatOutbox(), calls=[];
  const snapshot=state(1,Array.from({length:8},(_,i)=>item(`op${i}`)));
  const client={async call(action,fields){
    calls.push({action,fields});
    if(action==='queue')return {queue:snapshot};
    assert.equal(action,'queue_item');
    return {queue_item:{...item(fields.operation_id),text:'中'.repeat(5000)}};
  }};
  const queue=operationQueue(client,box);
  await Promise.all([queue.refresh(),queue.refresh()]);
  assert.equal(calls.length,9);
  assert.equal(box.reservedBytes,120000);
  await queue.refresh();
  assert.equal(calls.length,10,'already hydrated bodies should not be downloaded again');
});

test('a dispatch completed between polls recovers its durable identity without replay', async () => {
  const box=new ChatOutbox(), calls=[];
  box.begin('one','A','retained preview');
  const queue=operationQueue({async call(action){
    calls.push(action);
    if(action==='queue')return {queue:state(2)};
    return {queue_item:{...item('one'),state:'settled',turn:{...turn('one','accepted','completed'),id:'native-turn',user_message_id:'native-user'}}};
  }},box);
  queue.observe(state(1,[item('one')]));
  await queue.refresh();
  assert.deepEqual(calls,['queue','queue_item']);
  assert.equal(box.entries.get('one').delivery,'accepted');
  const native={id:'native-user',role:'user',blocks:[{type:'text',text:'retained preview'}]};
  assert.equal(queue.consumeHistoryChange('A'),true);
  assert.equal(queue.consumeHistoryChange('A'),false);
  assert.equal(box.merge([native],'A').length,1);
  await queue.refresh();
  assert.equal(calls.at(-1),'queue');
  assert.equal(calls.length,3);
});

test('late body recovery cannot put a cancelled message back in the queue', async () => {
  const box=new ChatOutbox();let queue;
  const client={async call(action){
    if(action==='queue')return {queue:state(1,[item('one')])};
    queue.observe(state(2));
    return {queue_item:{...item('one'),state:'settled',text:'cancelled text',turn:turn('one','rejected','cancelled')}};
  }};
  queue=operationQueue(client,box);
  await queue.refresh();
  assert.equal(queue.count,0);
  assert.equal(box.entries.get('one').queued,false);
  assert.equal(box.entries.get('one').delivery,'rejected');
});

test('host reconstruction restores terminal failures without occupying the send queue', async () => {
  const box=new ChatOutbox(), calls=[];
  const failed={...item('failed'),state:'failed',turn:turn('failed','rejected','failed')};
  const client={async call(action,fields){
    calls.push(action);
    if(action==='queue')return {queue:{...state(1),failures:[failed]}};
    assert.equal(fields.operation_id,'failed');
    return {queue_item:{...failed,text:'retained undelivered message'}};
  }};
  const queue=operationQueue(client,box);
  await queue.refresh();
  assert.equal(queue.waiting,false);
  assert.equal(queue.count,0);
  assert.equal(queue.failure('A').turn.delivery,'rejected');
  const row=box.merge([],'A')[0];
  assert.equal(row.blocks[0].text,'retained undelivered message');
  assert.equal(row.delivery_status,'消息未送达');
  assert.equal(row.queue_operation,undefined);
  await queue.refresh();
  assert.deepEqual(calls,['queue','queue_item','queue']);
});
