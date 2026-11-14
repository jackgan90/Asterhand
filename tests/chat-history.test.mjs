import test from 'node:test';
import assert from 'node:assert/strict';
import {historyPage, historyCursor, visibleHistory, fragmentBytes, byteSize} from '../internal/executor/chatbridge/projection.mjs';
import {readHistoryPage} from '../internal/executor/chatbridge/history.mjs';
import {messageSelection, selectedMessageText} from '../internal/executor/chatbridge/timeline.mjs';

const native = (id, content, role = 'assistant', run = 'run-0000000000001') => ({role, content,
  __openclaw: {id, runId: run}});
const head = {sessionId: 'session', deltaCursor: 'stable', messages: [native('last-message-0001', 'last')], hasMore: true, nextOffset: 50};
const older = {sessionId: 'session', messages: [native('first-message-001', 'hello', 'user')], hasMore: false};

test('an unchanged offset page without deltaCursor advances to the earliest message', async () => {
  const requests = [];
  const page = await readHistoryPage(async ({offset}) => { requests.push(offset); return offset ? older : head; },
    async value => value, 50, historyCursor(head));
  assert.deepEqual(requests, [0, 50, 0]);
  assert.equal(page.reset, false);
  assert.equal(page.has_more, false);
  assert.equal(page.messages[0].blocks[0].text, 'hello');
  assert.equal(page.cursor, historyCursor(head));
});

test('real appends, rewrites and native resets invalidate the older window', async () => {
  for (const stage of ['before', 'during', 'native']) {
    let reads = 0;
    const changed = {...head, deltaCursor: 'appended'};
    const page = await readHistoryPage(async ({offset}) => {
      reads++;
      if (stage === 'before' || stage === 'during' && reads === 3) return changed;
      return offset ? {...older, windowReset: stage === 'native'} : head;
    }, async value => value, 50, historyCursor(head));
    assert.equal(page.reset, true, stage);
    assert.equal(page.messages[0].id, 'last-message-0001');
    if (stage === 'before') assert.equal(reads, 1);
  }
  assert.notEqual(historyCursor(head), historyCursor({...head, messages: [native('last-message-0001', 'rewritten')]}));
});

test('hidden rows still advance the native offset and never create empty bubbles', async () => {
  const page = historyPage({sessionId: 'session', nextOffset: 100, hasMore: true,
    messages: [native('hidden-message-01', [{type: 'thinking', thinking: 'private'}]), native('result-message-01', 'raw private result', 'toolResult')]});
  assert.deepEqual(page.messages, []);
  assert.equal(page.next_offset, 100);
  assert.equal(page.has_more, true);
});

test('visible text and tools keep content order while secrets and raw outputs stay private', async () => {
  const source = native('mixed-message-0001', [
    {type: 'thinking', thinking: 'SECRET-THOUGHT'},
    {type: 'toolCall', id: 'call-a', name: 'ls', arguments: {secret: 'SECRET-ARGUMENT'}},
    {type: 'text', text: 'after first tool'},
    {type: 'toolCall', id: 'call-b', name: 'read', arguments: {path: 'SECRET-PATH'}},
    {type: 'text', text: 'after second tool'},
  ]);
  const result = {...native('tool-result-00001', 'SECRET-RESULT', 'toolResult'), toolCallId: 'call-a', isError: true};
  const page = historyPage(await visibleHistory({messages: [source, result]}, async () => source));
  assert.equal(page.messages.length, 1);
  assert.deepEqual(page.messages[0].blocks.map(block => block.type), ['tool', 'text', 'tool', 'text']);
  assert.equal(page.messages[0].blocks[0].tool.state, 'failed');
  assert.equal(page.messages[0].blocks[2].tool.state, 'unknown');
  assert.doesNotMatch(JSON.stringify(page), /SECRET|arguments|thinking/);
  assert.equal(selectedMessageText(source, messageSelection(page.messages[0].blocks[1].id)), 'after first tool');
  assert.equal(selectedMessageText(source, messageSelection(page.messages[0].blocks[3].id)), 'after second tool');
  assert.throws(() => selectedMessageText(source, messageSelection('mixed-message-0001_text_1')));
});

test('tool results cannot attach to another turn that reused the same native call ID', () => {
  const call = native('call-message-0001', [{type: 'toolCall', id: 'same-id', name: 'write'}]);
  const result = {...native('tool-result-00001', 'result', 'toolResult', 'other-turn'), toolCallId: 'same-id'};
  assert.equal(historyPage({messages: [call, result]}).messages[0].blocks[0].tool.state, 'unknown');
});

test('long mixed text stays bounded and expands only its original segment in exact UTF-8', () => {
  const text = '中文🙂<>&'.repeat(12000);
  const source = native('mixed-message-0001', [{type: 'text', text}, {type: 'toolCall', id: 'call', name: 'write'}, {type: 'text', text: 'after'}]);
  const page = historyPage({messages: [source]});
  assert.ok(byteSize(page) < 64 * 1024);
  const [before, call, after] = page.messages[0].blocks;
  assert.equal(before.truncated, true);
  assert.equal(call.type, 'tool');
  assert.equal(after.type, 'text');
  assert.equal(after.text, 'after');
  const bytes = new TextEncoder().encode(selectedMessageText(source, messageSelection(before.id)));
  let offset = 0, restored = '';
  do {
    const part = fragmentBytes(bytes, before.id, offset);
    restored += part.text; offset = part.next_offset;
    if (!part.has_more) break;
  } while (true);
  assert.equal(restored, text);
  assert.equal(selectedMessageText(source, messageSelection(after.id)), 'after');
});

test('native commentary and tool projections sharing an ID compose without losing either row', async () => {
  const commentary = {...native('mixed-message-0001', [{type: 'text', text: 'before'}]),
    openclawStreamFallback: {source: 'segment', itemId: 'provider-part'}};
  const tool = native('mixed-message-0001', [{type: 'toolCall', id: 'call', name: 'ls'}]);
  const page = historyPage(await visibleHistory({messages: [commentary, tool]}, async () => null));
  assert.equal(page.messages.length, 1);
  const message = page.messages[0];
  assert.deepEqual(message.blocks.map(block => block.type), ['text', 'tool']);
  assert.equal(message.blocks[0].text, 'before');
  const full = native('mixed-message-0001', [...commentary.content, ...tool.content]);
  assert.equal(selectedMessageText(full, messageSelection(message.blocks[0].id)), 'before');
});

test('a native page boundary that starts on the tool row recovers its omitted commentary', async () => {
  const tool = native('mixed-message-0001', [{type: 'toolCall', id: 'call', name: 'ls'}]);
  const full = {...tool, content: [{type: 'text', text: 'before boundary'}, ...tool.content]};
  let reads = 0;
  const page = historyPage(await visibleHistory({messages: [tool]}, async (id, maxChars) => {
    reads++; assert.equal(id, tool.__openclaw.id); assert.equal(maxChars, 1000); return full;
  }));
  assert.equal(reads, 1);
  assert.deepEqual(page.messages[0].blocks.map(block => block.type), ['text', 'tool']);
  assert.equal(page.messages[0].blocks[0].text, 'before boundary');
  await assert.rejects(visibleHistory({messages: [tool]}, async () => null), {code: 'unavailable'});
});
