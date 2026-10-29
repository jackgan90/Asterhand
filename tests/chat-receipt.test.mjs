import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtempSync, readFileSync, writeFileSync, copyFileSync, rmSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {spawn} from 'node:child_process';

const source = new URL('../internal/executor/chatbridge/', import.meta.url);
const run = 'run-0123456789abcdef';
const binding = {runtime: 'openclaw', generation: 'generation-0123456789', key: 'conversation-0123456789',
  session: 'native-session', lifecycle: 'native-lifecycle'};

// Exercise the production command handler with only its private Gateway replaced.
// A successful send is never repeated, even when the follow-up read fails.
test('submit confirms missing-sequence acknowledgements by durable evidence and preserves unknown failures', async () => {
  const root = mkdtempSync(join(tmpdir(), 'aster-receipt-'));
  try {
    for (const name of ['main.mjs', 'projection.mjs', 'prompt-state.mjs']) copyFileSync(new URL(name, source), join(root, name));
    writeFileSync(join(root, 'gateway.mjs'), `
      import {writeFileSync} from 'node:fs';
      export {failure, submissionError} from ${JSON.stringify(new URL('gateway.mjs', source).href)};
      const calls = [];
      function record(method, params) {
        calls.push({method, params}); writeFileSync('calls.json', JSON.stringify(calls));
      }
      export class Gateway {
        async start() {} async ensureConnected() {}
        async rpc(method, params) {
          record(method, params);
          if (method !== 'chat.send') throw Error('unexpected RPC');
          return {runId: params.idempotencyKey, status: 'started',
            ...(process.env.RECEIPT_CASE === 'sequence' ? {messageSeq: 1} : {})};
        }
        async readyRead(method, params) {
          record(method, params);
          const scenario = process.env.RECEIPT_CASE;
          if (params.inputRunIds && scenario === 'read-failure') throw Error('read unavailable');
          return {sessionId: scenario === 'binding-change' && params.inputRunIds ? 'different' : 'native-session',
            sessionInfo: {lifecycleRevision: 'native-lifecycle', hasActiveRun: true}, messages: [],
            inputReceipts: params.inputRunIds && scenario === 'receipt' ? [{runId: params.inputRunIds[0], state: 'queued'}] :
              [{runId: 'unrelated'}]};
        }
      }
    `);
    for (const scenario of ['sequence', 'receipt', 'missing', 'read-failure', 'binding-change']) {
      const child = spawn(process.execPath, [join(root, 'main.mjs')], {cwd: root, windowsHide: true,
        env: {...process.env, RECEIPT_CASE: scenario}, stdio: ['pipe', 'pipe', 'pipe']});
      const exited = new Promise(resolve => child.once('exit', resolve));
      try {
        let buffer = '', stderr = '';
        child.stderr.on('data', chunk => { stderr += chunk; });
        const replies = [], waiters = [];
        child.stdout.on('data', chunk => {
          buffer += chunk;
          for (;;) {
            const end = buffer.indexOf('\n'); if (end < 0) break;
            const reply = JSON.parse(buffer.slice(0, end)); buffer = buffer.slice(end + 1);
            if (waiters.length) waiters.shift()(reply); else replies.push(reply);
          }
        });
        const call = async command => {
          child.stdin.write(JSON.stringify(command) + '\n');
          let timeout;
          try {
            return await Promise.race([replies.length ? Promise.resolve(replies.shift()) : new Promise(resolve => waiters.push(resolve)),
              new Promise((_, reject) => { timeout = setTimeout(() => reject(Error('bridge timeout: ' + stderr)), 5000); })]);
          } finally { clearTimeout(timeout); }
        };
        assert.deepEqual(await call({id: '1', action: 'bootstrap', entry: 'fixture', port: 1234, token: 'fixture',
          prompt: {mode: 'unsupported', state: 'degraded'}}), {id: '1', result: {}});
        const reply = await call({id: '2', action: 'submit', binding, run, text: 'fixture input'});
        assert.equal(reply.error, undefined, scenario);
        assert.equal(reply.result.accepted, ['sequence', 'receipt'].includes(scenario), scenario);
        const calls = JSON.parse(readFileSync(join(root, 'calls.json'), 'utf8'));
        assert.equal(calls.filter(item => item.method === 'chat.send').length, 1, scenario);
        const reads = calls.filter(item => item.method === 'chat.history');
        assert.equal(reads.length, scenario === 'sequence' ? 1 : 2, scenario);
        if (scenario !== 'sequence') assert.deepEqual(reads[1].params.inputRunIds, [run]);
      } finally {
        child.kill(); await exited;
      }
    }
  } finally { rmSync(root, {recursive: true, force: true}); }
});
