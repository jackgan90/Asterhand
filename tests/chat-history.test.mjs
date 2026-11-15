import test from 'node:test';
import assert from 'node:assert/strict';
import {historyPage, historyCursor, visibleHistory, fragmentBytes, byteSize} from '../internal/executor/chatbridge/projection.mjs';
import {readHistoryPage} from '../internal/executor/chatbridge/history.mjs';
import {messageSelection, selectedMessageText} from '../internal/executor/chatbridge/timeline.mjs';
import {summarizeToolCall, validateToolDisplaySpec} from '../internal/executor/chatbridge/tool-display.mjs';

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
    {type: 'toolCall', id: 'call-b', name: 'edit', arguments: {
      path: 'notes/plan.md', edits: [{oldText: 'SECRET-OLD', newText: 'SECRET-NEW'}], secret: 'SECRET-EXTRA',
    }},
    {type: 'text', text: 'after second tool'},
  ]);
  const result = {...native('tool-result-00001', 'SECRET-RESULT', 'toolResult'), toolCallId: 'call-a', isError: true};
  const page = historyPage(await visibleHistory({messages: [source, result]}, async () => source));
  assert.equal(page.messages.length, 1);
  assert.deepEqual(page.messages[0].blocks.map(block => block.type), ['tool', 'text', 'tool', 'text']);
  assert.equal(page.messages[0].blocks[0].tool.state, 'failed');
  assert.equal(page.messages[0].blocks[0].tool.summary, '.');
  assert.equal(page.messages[0].blocks[2].tool.state, 'unknown');
  assert.equal(page.messages[0].blocks[2].tool.summary, 'notes/plan.md');
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

test('tool display specs summarize registered tools and legacy aliases while redacting bodies and outside paths', () => {
  const root = 'F:\\Asterhand\\work';
  assert.equal(summarizeToolCall('list_dir', {}, root), '.');
  assert.equal(summarizeToolCall('ls', '{"path":"docs\\\\sub"}', root), 'docs/sub');
  assert.equal(summarizeToolCall('read_file', {path: 'F:\\Asterhand\\work\\README.md', offset: 10, limit: 25}, root), 'README.md:10-34');
  assert.equal(summarizeToolCall('read', {path: 'README.md', offset: 120}, root), 'README.md:120+');
  assert.equal(summarizeToolCall('read_file', {path: 'README.md', offset: 1}, root), 'README.md');
  assert.equal(summarizeToolCall('write_file', {path: 'a\\b.txt', content: 'SECRET_BODY'}, root), 'a/b.txt');
  assert.equal(summarizeToolCall('write', {file_path: 'legacy.txt', content: 'SECRET_BODY'}, root), 'legacy.txt');
  assert.equal(summarizeToolCall('edit_file', {path: 'src/app.mjs', edits: [{oldText: 'OLD', newText: 'NEW'}]}, root), 'src/app.mjs');
  const patch = [
    '*** Begin Patch',
    '*** Update File: a.txt',
    '@@',
    '-SECRET_PATCH_OLD',
    '+SECRET_PATCH_NEW',
    '*** Add File: b.txt',
    '*** Update File: old.txt',
    '*** Move to: new.txt',
    '*** Delete File: dead.txt',
    '*** End Patch',
  ].join('\n');
  assert.equal(summarizeToolCall('apply_file_patch', {input: patch}, root), 'a.txt, b.txt, old.txt → new.txt (+1)');
  assert.equal(summarizeToolCall('apply_patch', {input: '*** Begin Patch\n*** Update File: only.txt\n*** End Patch'}, root), 'only.txt');
  assert.equal(summarizeToolCall('search_files', {query: 'alpha\nbeta', path: '.'}, root), '"alpha beta"');
  assert.equal(summarizeToolCall('search_files', {query: 'needle', path: 'docs/web'}, root), '"needle" · docs/web');
  assert.equal(summarizeToolCall('memory_search', {query: 'deploy host', corpus: 'memory'}, root), '"deploy host"');
  assert.equal(summarizeToolCall('memory_search', {query: 'deploy host', corpus: 'sessions'}, root), '"deploy host" · sessions');
  assert.equal(summarizeToolCall('memory_get', {path: 'memory/2026-11-15.md', from: 12, lines: 8}, root), 'memory/2026-11-15.md:12-19');
  assert.equal(summarizeToolCall('progress_card', {markdown: 'SECRET_MD', plan: [{step: 'x', status: 'pending'}]}, root), '');
  assert.equal(summarizeToolCall('read_file', {path: 'C:\\Users\\Other\\secret\\token.txt'}, root), 'token.txt');
  assert.equal(summarizeToolCall('read_file', {path: '../outside/secret.env'}, root), 'secret.env');
  const longPath = `${'nested/'.repeat(20)}target-file.md`;
  const clamped = summarizeToolCall('read_file', {path: longPath, offset: 5, limit: 10}, root);
  assert.ok(clamped.includes('…/target-file.md:5-14'));
  assert.ok(clamped.length <= 96);
  assert.equal(validateToolDisplaySpec({name: 'bad', display: {kind: 'path', pathParams: ['nope']}, parameters: {properties: {path: {}}}}).ok, false);
});

test('historyPage projects tool summaries and drops summary metadata before failing page budget', () => {
  const repeatedTools = Array.from({length: 250}, (_, i) => ({
    type: 'toolCall',
    id: `call-${i}`,
    name: 'read_file',
    arguments: {path: `docs/very-long-directory-segment-${'x'.repeat(50)}/file-${i}.md`},
  }));
  const source = native('big-tool-page', [{type: 'text', text: 'x'.repeat(28000)}, ...repeatedTools]);
  const page = historyPage({messages: [source]});
  assert.ok(byteSize(page) <= 64 * 1024);
  assert.ok(page.messages[0].blocks.some(b => b.type === 'tool' && b.tool.summary === undefined));
});
