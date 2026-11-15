import test from 'node:test';
import assert from 'node:assert/strict';
import {fileClient, createTracker} from '../internal/executor/workspaceplugin/files.mjs';
import {createBatchCoordinator} from '../internal/executor/workspaceplugin/batch.mjs';
import {memoryFiles} from '../internal/executor/workspaceplugin/memory.mjs';

test('readDirectory maps directory and file metadata and marks bootstrap requests', async () => {
  const originalFetch = globalThis.fetch;
  try {
    globalThis.fetch = async (_url, req) => {
      const body = JSON.parse(req.body);
      assert.equal(body.action, 'list');
      assert.equal(body.bootstrap, true);
      return {
        ok: true,
        json: async () => ({
          value: [
            {path: 'sub/nested', type: 'directory', size: 0, mtimeMs: 1700000000000},
            {path: 'sub/alpha.txt', type: 'file', size: 10, mtimeMs: 1700000000100},
          ],
        }),
      };
    };
    const client = fileClient({
      workspace: 'F:/synthetic-workspace',
      endpoint: 'http://127.0.0.1:19876/',
      token: 'secret-token',
      policy: 'policy-rev-1',
    });
    const bootstrapItems = await client.bootstrapBridge.readDirectory({filePath: 'sub'});
    assert.deepEqual(bootstrapItems, [
      {name: 'nested', type: 'directory', isDirectory: true, isFile: false, size: 0, mtimeMs: 1700000000000},
      {name: 'alpha.txt', type: 'file', isDirectory: false, isFile: true, size: 10, mtimeMs: 1700000000100},
    ]);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('batch coordinator differentiates equal-length write fingerprints and surfaces conflicting_batch_write', async () => {
  const sealedBatches = [];
  const client = {
    relative: p => p.replaceAll('\\', '/'),
    createBridge: () => ({}),
    request: async (action, payload) => {
      if (action === 'register') return {id: payload.id};
      if (action === 'seal') {
        sealedBatches.push(payload);
        return {sealed: true};
      }
      if (action === 'acquire') {
        if (payload.id === sealedBatches[0].items[1].id) {
          return {
            granted: false,
            terminal: true,
            operation: {id: payload.id, state: 'failed', detail: 'conflicting_batch_write'},
          };
        }
        return {granted: true, operation: {id: payload.id, state: 'started'}};
      }
      if (action === 'end') return {state: payload.state};
      throw new Error(`unexpected action: ${action}`);
    },
  };
  const coord = createBatchCoordinator(client);
  const ctx = {runId: 'run-1', sessionKey: 'agent:main:asterhand:conv-1'};
  await coord.beforeToolCall({toolName: 'write_file', toolCallId: 'c0', params: {path: 'race.txt', content: 'WRITER_A'}}, {...ctx, toolCallId: 'c0'});
  await coord.beforeToolCall({toolName: 'write_file', toolCallId: 'c1', params: {path: 'race.txt', content: 'WRITER_B'}}, {...ctx, toolCallId: 'c1'});

  const [r0, r1] = await Promise.allSettled([
    coord.runTool('write_file', 'c0', {path: 'race.txt', content: 'WRITER_A'}, undefined, async () => ({content: [{type: 'text', text: 'ok'}]})),
    coord.runTool('write_file', 'c1', {path: 'race.txt', content: 'WRITER_B'}, undefined, async () => ({content: [{type: 'text', text: 'clobber'}]})),
  ]);

  assert.equal(r0.status, 'fulfilled');
  assert.equal(r1.status, 'rejected');
  assert.equal(r1.reason.code, 'conflicting_batch_write');
  assert.match(r1.reason.message, /conflicting_batch_write/);
  assert.notEqual(sealedBatches[0].items[0].fingerprint, sealedBatches[0].items[1].fingerprint);
});

test('batch coordinator does not prematurely seal valid tools when middle tool is hook-blocked during async prepare', async () => {
  const sealedBatches = [];
  const client = {
    relative: p => p.replaceAll('\\', '/'),
    createBridge: () => ({}),
    request: async (action, payload) => {
      if (action === 'register') {
        await new Promise(resolve => setTimeout(resolve, 15));
        return {id: payload.id};
      }
      if (action === 'seal') {
        sealedBatches.push(payload);
        return {sealed: true};
      }
      if (action === 'acquire') {
        return {granted: true, operation: {id: payload.id, state: 'started'}};
      }
      if (action === 'end') return {state: payload.state};
      throw new Error(`unexpected action: ${action}`);
    },
  };
  const coord = createBatchCoordinator(client);
  const ctx = {runId: 'run-mid-block', sessionKey: 'agent:main:asterhand:conv-mid'};

  await coord.beforeToolCall({toolName: 'read_file', toolCallId: 't0', params: {path: 'a.txt'}}, {...ctx, toolCallId: 't0'});
  await coord.beforeToolCall({toolName: 'read_file', toolCallId: 't1', params: {path: 'b.txt'}}, {...ctx, toolCallId: 't1'});
  coord.afterToolCall({toolName: 'read_file', toolCallId: 't1', error: 'blocked by policy'}, {...ctx, toolCallId: 't1'});
  await coord.beforeToolCall({toolName: 'read_file', toolCallId: 't2', params: {path: 'c.txt'}}, {...ctx, toolCallId: 't2'});

  const [res0, res2] = await Promise.all([
    coord.runTool('read_file', 't0', {path: 'a.txt'}, undefined, async () => ({content: [{type: 'text', text: 'A'}]})),
    coord.runTool('read_file', 't2', {path: 'c.txt'}, undefined, async () => ({content: [{type: 'text', text: 'C'}]})),
  ]);

  assert.equal(res0.content[0].text, 'A');
  assert.equal(res2.content[0].text, 'C');
  assert.equal(sealedBatches.length, 1);
  assert.equal(sealedBatches[0].items.length, 3);
  assert.equal(Boolean(sealedBatches[0].items[0].aborted), false);
  assert.equal(sealedBatches[0].items[1].aborted, true);
  assert.equal(sealedBatches[0].items[1].reason, 'hook_blocked');
  assert.equal(Boolean(sealedBatches[0].items[2].aborted), false);
});

test('memoryFiles requires active call context and tracks supervised calls', async () => {
  const seen = [];
  const client = {
    workspace: 'F:/synthetic-workspace',
    relative: p => p.replaceAll('\\', '/').replace(/^F:\/synthetic-workspace\/?/i, ''),
    request: async (action, payload) => {
      seen.push({action, id: payload.id});
      if (action === 'stat') return {type: 'file', size: 5, mtimeMs: 100};
      if (action === 'search') return {items: [], next: ''};
      if (action === 'read') return Buffer.from('hello').toString('base64');
      throw new Error(`unexpected ${action}`);
    },
  };
  const controller = new AbortController();
  const mem = memoryFiles(client, controller.signal);

  await assert.rejects(() => mem.listFiles('F:/synthetic-workspace'), {code: 'missing_call_identity'});
  await assert.rejects(() => mem.readFile({relPath: 'MEMORY.md'}), {code: 'missing_call_identity'});

  const tracker = createTracker();
  const files = await mem.withCall({id: 'call-mem-1', tracker}, () => mem.listFiles('F:/synthetic-workspace'));
  assert.equal(files.length, 2);
  assert.ok(seen.every(entry => entry.id === 'call-mem-1'));
});
