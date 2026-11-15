import test from 'node:test';
import assert from 'node:assert/strict';
import {fileClient, createTracker, callScope} from '../internal/executor/workspaceplugin/files.mjs';
import {createBatchCoordinator} from '../internal/executor/workspaceplugin/batch.mjs';
import {createExecTools} from '../internal/executor/workspaceplugin/exec.mjs';
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
  const registered = [];
  const sealedBatches = [];
  const client = {
    relative: p => p.replaceAll('\\', '/'),
    createBridge: () => ({}),
    request: async (action, payload) => {
      if (action === 'register') {
        registered.push(payload);
        return {id: payload.id};
      }
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
  assert.deepEqual(registered.map(p => p.summary), ['race.txt', 'race.txt']);
  assert.deepEqual(sealedBatches[0].items.map(i => i.summary), ['race.txt', 'race.txt']);
  assert.equal(JSON.stringify({registered, sealedBatches}).includes('WRITER_'), false);
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

test('batch coordinator registers and seals allowlisted display summaries without leaking private bodies', async () => {
  const registered = [];
  const sealedBatches = [];
  const client = {
    workspace: 'F:/synthetic-workspace',
    relative: p => p.replaceAll('\\', '/').replace(/^F:\/synthetic-workspace\/?/i, ''),
    createBridge: () => ({}),
    request: async (action, payload) => {
      if (action === 'register') {
        registered.push(payload);
        return {id: payload.id};
      }
      if (action === 'seal') {
        sealedBatches.push(payload);
        return {sealed: true};
      }
      if (action === 'acquire') return {granted: true, operation: {id: payload.id, state: 'started'}};
      if (action === 'end') return {state: payload.state};
      throw new Error(`unexpected ${action}`);
    },
  };
  const coord = createBatchCoordinator(client);
  const ctx = {runId: 'run-summary', sessionKey: 'agent:main:asterhand:conv-sum'};
  const patchText = '*** Begin Patch\n*** Update File: old.md\n*** Move to: sub/new.md\n@@\n-SECRET_OLD\n+SECRET_NEW\n*** End Patch';

  await coord.beforeToolCall({toolName: 'memory_search', toolCallId: 's0', params: {query: 'deploy plan', corpus: 'sessions'}}, {...ctx, toolCallId: 's0'});
  await coord.beforeToolCall({toolName: 'read_file', toolCallId: 's1', params: {path: 'F:\\synthetic-workspace\\docs\\a.md', offset: 10, limit: 20}}, {...ctx, toolCallId: 's1'});
  await coord.beforeToolCall({toolName: 'apply_file_patch', toolCallId: 's2', params: {input: patchText}}, {...ctx, toolCallId: 's2'});
  await coord.beforeToolCall({toolName: 'progress_card', toolCallId: 's3', params: {markdown: 'SECRET_PROGRESS', plan: [{step: 'one', status: 'pending'}]}}, {...ctx, toolCallId: 's3'});
  await coord.beforeToolCall({toolName: 'exec_command', toolCallId: 's4', params: {command: 'node run.mjs --api-key=SECRET_KEY'}}, {...ctx, toolCallId: 's4'});
  await coord.beforeToolCall({toolName: 'process_control', toolCallId: 's5', params: {action: 'poll', sessionId: 'sess-9'}}, {...ctx, toolCallId: 's5'});

  await Promise.all([
    coord.runTool('memory_search', 's0', {query: 'deploy plan', corpus: 'sessions'}, undefined, async () => ({content: [{type: 'text', text: 'ok'}]})),
    coord.runTool('read_file', 's1', {path: 'F:\\synthetic-workspace\\docs\\a.md', offset: 10, limit: 20}, undefined, async () => ({content: [{type: 'text', text: 'ok'}]})),
    coord.runTool('apply_file_patch', 's2', {input: patchText}, undefined, async () => ({content: [{type: 'text', text: 'ok'}]})),
    coord.runTool('progress_card', 's3', {markdown: 'SECRET_PROGRESS', plan: [{step: 'one', status: 'pending'}]}, undefined, async () => ({content: [{type: 'text', text: 'ok'}]})),
    coord.runTool('exec_command', 's4', {command: 'node run.mjs --api-key=SECRET_KEY'}, undefined, async () => ({content: [{type: 'text', text: 'ok'}]})),
    coord.runTool('process_control', 's5', {action: 'poll', sessionId: 'sess-9'}, undefined, async () => ({content: [{type: 'text', text: 'ok'}]})),
  ]);

  assert.deepEqual(registered.map(r => r.summary ?? ''), ['"deploy plan" · sessions', 'docs/a.md:10-29', 'old.md → sub/new.md', '', 'node run.mjs --api-key=[REDACTED]', 'poll sess-9']);
  assert.equal(registered[3].summary, undefined);
  assert.deepEqual(sealedBatches[0].items.map(i => i.summary ?? ''), ['"deploy plan" · sessions', 'docs/a.md:10-29', 'old.md → sub/new.md', '', 'node run.mjs --api-key=[REDACTED]', 'poll sess-9']);
  assert.equal(sealedBatches[0].items[3].summary, undefined);
  assert.equal(JSON.stringify({registered, sealedBatches}).includes('SECRET_'), false);
});

test('fileClient resolves ASTERHAND_WORKSPACE_TOKEN placeholder and rejects empty env token', async () => {
  const originalFetch = globalThis.fetch;
  const hadEnv = Object.hasOwn(process.env, 'ASTERHAND_WORKSPACE_TOKEN');
  const prevToken = process.env.ASTERHAND_WORKSPACE_TOKEN;
  try {
    delete process.env.ASTERHAND_WORKSPACE_TOKEN;
    assert.throws(
      () => fileClient({workspace: 'F:/synthetic-workspace', endpoint: 'http://127.0.0.1:19876/', token: '${ASTERHAND_WORKSPACE_TOKEN}', policy: 'rev-1'}),
      /Invalid private workspace service/,
    );
    process.env.ASTERHAND_WORKSPACE_TOKEN = 'env-resolved-token';
    globalThis.fetch = async (_url, req) => {
      assert.equal(req.headers.Authorization, 'Bearer env-resolved-token');
      return {ok: true, json: async () => ({value: {ok: true}})};
    };
    const client = fileClient({workspace: 'F:/synthetic-workspace', endpoint: 'http://127.0.0.1:19876/', token: '${ASTERHAND_WORKSPACE_TOKEN}', policy: 'rev-1'});
    assert.deepEqual(await client.request('status', {}), {ok: true});
  } finally {
    globalThis.fetch = originalFetch;
    if (hadEnv) process.env.ASTERHAND_WORKSPACE_TOKEN = prevToken;
    else delete process.env.ASTERHAND_WORKSPACE_TOKEN;
  }
});

test('callScope validates exec_command and process_control arguments', () => {
  const relative = p => p;
  assert.throws(() => callScope('exec_command', {command: '   '}, relative), {code: 'invalid_command'});
  assert.deepEqual(callScope('exec_command', {command: 'echo hi'}, relative), {path: '', targets: [], new_bytes: 0, progress: []});
  assert.throws(() => callScope('process_control', {action: 'restart'}, relative), {code: 'invalid_action'});
  assert.throws(() => callScope('process_control', {action: 'poll'}, relative), {code: 'invalid_session'});
  assert.deepEqual(callScope('process_control', {action: 'list'}, relative), {path: '', targets: [], new_bytes: 0, progress: []});
  assert.deepEqual(callScope('process_control', {action: 'poll', session_id: 's1'}, relative), {path: '', targets: [], new_bytes: 0, progress: []});
});

test('exec_command sends best-effort exec/kill without aborted signal when cancelled', async () => {
  const calls = [];
  let releaseStart;
  const client = {
    workspace: 'F:/synthetic-workspace',
    relative: p => p.replaceAll('\\', '/'),
    createBridge: () => ({}),
    request: async (action, payload, _timeoutMs, signal) => {
      calls.push({action, payload, signalAborted: signal?.aborted, hasSignal: Boolean(signal)});
      if (action === 'register') return {id: payload.id};
      if (action === 'seal') return {sealed: true};
      if (action === 'acquire') return {granted: true, operation: {id: payload.id, state: 'started'}};
      if (action === 'exec/start') {
        return await new Promise((_resolve, reject) => {
          releaseStart = () => reject(Object.assign(new Error('Aborted'), {name: 'AbortError'}));
          signal?.addEventListener('abort', releaseStart, {once: true});
        });
      }
      if (action === 'cancel' || action === 'exec/kill') return {killed: true};
      if (action === 'end') return {state: payload.state};
      throw new Error(`unexpected ${action}`);
    },
  };
  const coord = createBatchCoordinator(client);
  const [execTool] = createExecTools(client, coord);
  const ctx = {runId: 'run-abort', sessionKey: 'agent:main:asterhand:conv-abort', toolCallId: 'a1'};
  await coord.beforeToolCall({toolName: 'exec_command', toolCallId: 'a1', params: {command: 'node slow.mjs'}}, ctx);

  const ac = new AbortController();
  const pending = execTool.execute('a1', {command: 'node slow.mjs'}, ac.signal);
  await new Promise(resolve => setImmediate(resolve));
  await new Promise(resolve => setImmediate(resolve));
  ac.abort();

  await assert.rejects(pending, err => err?.name === 'AbortError');
  const cancelCall = calls.find(c => c.action === 'cancel');
  const killCall = calls.find(c => c.action === 'exec/kill');
  const endCall = calls.find(c => c.action === 'end');
  assert.ok(cancelCall && !cancelCall.hasSignal);
  assert.ok(killCall && !killCall.hasSignal && killCall.payload.detail === 'cancelled');
  assert.equal(endCall?.payload?.state, 'cancelled');
});
