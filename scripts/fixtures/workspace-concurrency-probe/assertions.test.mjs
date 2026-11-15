import test from 'node:test';
import assert from 'node:assert/strict';
import {memoryResults, ownedExecutionIdentity} from './assertions.mjs';

const results = () => [
  {role: 'tool', tool_call_id: 'memorycall0', content: JSON.stringify({results: [{path: 'memory/facts.md', snippet: 'celesta'}]})},
  {role: 'tool', tool_call_id: 'memorycall1', content: JSON.stringify({status: 'ok', path: 'memory/facts.md', text: 'celesta'})},
];
const events = () => ['io_enter', 'io_exit'].map((event, index) => ({event, sequence: index + 1,
  scenario: 'batch', origin: 'owned_execute', path: 'owned.txt', ambient: {id: 'batchcall3', source: 'owned_execute'}}));

test('memory search and get each require their own successful result', () => {
  assert.deepEqual(memoryResults(results()), {search: true, get: true});
  for (const failure of ['error: index search failed', '{"results":[]}', '{"error":"failed","results":[{"path":"memory/facts.md","snippet":"celesta"}]}']) {
    const input = results(); input[0].content = failure;
    assert.deepEqual(memoryResults(input), {search: false, get: true});
  }
  const input = results(); input[1].content = '{"status":"not_found"}';
  assert.deepEqual(memoryResults(input), {search: true, get: false});
});

test('memory results reject wrong IDs, duplicates and unrelated hits', () => {
  const wrong = results(); wrong[0].tool_call_id = 'different-call';
  assert.equal(memoryResults(wrong).search, false);
  const duplicate = results(); duplicate.push({...duplicate[0]});
  assert.equal(memoryResults(duplicate).search, false);
  const unrelated = results(); unrelated[0].content = '{"results":[{"path":"other.md","snippet":"celesta"}]}';
  assert.equal(memoryResults(unrelated).search, false);
});

test('owned execute preserves the exact identity through entry and exit', () => {
  assert.equal(ownedExecutionIdentity(events()), true);
  const wrong = events(); wrong.forEach(event => { event.ambient.id = 'wrong-call'; });
  assert.equal(ownedExecutionIdentity(wrong), false);
  const lost = events(); lost[1].ambient = null;
  assert.equal(ownedExecutionIdentity(lost), false);
  assert.equal(ownedExecutionIdentity(events().slice(0, 1)), false);
  assert.equal(ownedExecutionIdentity(events().reverse()), false);
});
