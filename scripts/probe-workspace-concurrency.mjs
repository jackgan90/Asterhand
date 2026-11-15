import {createServer} from 'node:http';
import {createServer as createNetServer} from 'node:net';
import {readFile, writeFile, mkdir, copyFile} from 'node:fs/promises';
import {randomUUID} from 'node:crypto';
import {fileURLToPath} from 'node:url';
import path from 'node:path';
import {Gateway} from '../internal/executor/chatbridge/gateway.mjs';
import {memoryResults, ownedExecutionIdentity} from './fixtures/workspace-concurrency-probe/assertions.mjs';

const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const args = process.argv.slice(2);
const argument = name => { const i = args.indexOf(name); return i < 0 ? undefined : args[i + 1]; };
if (!argument('--runtime')) throw new Error('Usage: node scripts/probe-workspace-concurrency.mjs --runtime <openclaw-directory> [--output <directory>]');
const runtime = path.resolve(argument('--runtime'));
// Resolve the repository's build default; do not assume an output directory.
const build = await readFile(path.join(repository, 'scripts/build.ps1'), 'utf8');
const outputDefault = /if \(-not \$OutputDir\) \{ \$OutputDir = Join-Path \$repo '([^']+)'/.exec(build)?.[1];
if (!outputDefault && !argument('--output')) throw new Error('Build output default could not be resolved');
const output = path.resolve(argument('--output') ?? path.join(repository, outputDefault, 'audit-workspace-concurrency-s0'));
const root = path.join(output, 'native-' + randomUUID());
const workspace = path.join(root, 'workspace');
const trace = [], pending = [], waiters = [];
let scenario = '', modelTurn = 0, nativeExited = false;
const record = (event, fields = {}) => {
  const entry = {sequence: trace.length + 1, event, scenario, ...fields};
  if (trace.length >= 10000) throw new Error('Probe trace limit');
  trace.push(entry);
  for (const waiter of [...waiters]) if (waiter.test(entry)) waiter.resolve(entry);
  return entry;
};
const waitFor = (test, timeout = 20000) => {
  const found = trace.find(test);
  if (found) return Promise.resolve(found);
  return new Promise((resolve, reject) => {
    const item = {test, resolve: value => { clearTimeout(timer); waiters.splice(waiters.indexOf(item), 1); resolve(value); }};
    const timer = setTimeout(() => { waiters.splice(waiters.indexOf(item), 1); reject(new Error('Probe checkpoint timed out')); }, timeout);
    waiters.push(item);
  });
};
const release = test => {
  for (let i = pending.length - 1; i >= 0; i--) {
    if (!test(pending[i].entry)) continue;
    pending[i].response.end('{}');
    pending.splice(i, 1);
  }
};
const batches = {
  batch: [['read', {path: 'a.txt'}], ['read', {path: 'b.txt'}], ['ls', {path: '.'}], ['probe_owned', {}]],
  memory: [['memory_search', {query: 'celesta'}], ['memory_get', {path: 'memory/facts.md'}]],
  cancel: [['read', {path: 'slow.txt'}]],
};
const inputs = [];
const server = createServer(async (request, response) => {
  try {
    let body = '';
    for await (const chunk of request) { body += chunk; if (body.length > 2 * 1024 * 1024) throw new Error('Probe input limit'); }
    const value = JSON.parse(body);
    if (request.url === '/event') {
      const entry = record(value.event, value);
      const blockRead = scenario === 'batch' && value.event === 'io_enter' && ['a.txt', 'b.txt'].includes(value.path);
      const blockAfter = scenario === 'batch' && value.event === 'after_enter';
      const blockSlow = scenario === 'cancel' && value.event === 'io_enter' && value.path === 'slow.txt';
      if (blockRead || blockAfter || blockSlow) pending.push({entry, response}); else response.end('{}');
      const reads = trace.filter(item => item.scenario === 'batch' && item.event === 'io_enter' && ['a.txt', 'b.txt'].includes(item.path));
      if (new Set(reads.map(item => item.path)).size === 2) release(item => item.event === 'io_enter' && item.scenario === 'batch');
      return;
    }
    if (request.url !== '/v1/chat/completions') throw new Error('Unexpected probe endpoint');
    inputs.push({scenario, body: value});
    const turn = modelTurn++;
    record('model_input', {turn, tools: (value.tools ?? []).map(tool => tool.function.name)});
    const calls = batches[scenario];
    const delta = turn === 0 ? {role: 'assistant', tool_calls: calls.map(([name, params], index) => ({
      index, id: `${scenario}call${index}`, type: 'function', function: {name, arguments: JSON.stringify(params)},
    }))} : {role: 'assistant', content: 'Probe completed'};
    response.setHeader('Content-Type', 'text/event-stream');
    for (const choice of [{index: 0, delta, finish_reason: null}, {index: 0, delta: {}, finish_reason: turn === 0 ? 'tool_calls' : 'stop'}]) {
      response.write(`data: ${JSON.stringify({id: 'probe', object: 'chat.completion.chunk', model: 'chat', choices: [choice]})}\n\n`);
    }
    response.end('data: [DONE]\n\n');
  } catch (error) { record('controller_error', {message: error.message}); response.writeHead(500).end(); }
});
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
const endpoint = `http://127.0.0.1:${server.address().port}`;
const portServer = createNetServer();
await new Promise(resolve => portServer.listen(0, '127.0.0.1', resolve));
const port = portServer.address().port;
await new Promise(resolve => portServer.close(resolve));
for (const name of ['workspace/memory', 'state', 'plugin', 'profile', 'appdata', 'localappdata', 'temp']) await mkdir(path.join(root, name), {recursive: true});
for (const name of ['a.txt', 'b.txt', 'owned.txt', 'slow.txt']) await writeFile(path.join(workspace, name), `${name} isolated marker\n`);
await writeFile(path.join(workspace, 'memory/facts.md'), '# Facts\nThe probe favorite instrument is celesta.\n');
await copyFile(path.join(repository, 'scripts/fixtures/workspace-concurrency-probe/plugin.mjs'), path.join(root, 'plugin/index.mjs'));
await copyFile(path.join(repository, 'internal/executor/workspaceplugin/memory.mjs'), path.join(root, 'plugin/memory.mjs'));
await writeFile(path.join(root, 'plugin/openclaw.plugin.json'), JSON.stringify({id: 'asterhand-concurrency-probe', name: 'Concurrency SDK probe',
  activation: {onStartup: true}, contracts: {tools: ['probe_owned']},
  configSchema: {type: 'object', properties: {workspace: {type: 'string'}, endpoint: {type: 'string'}}, required: ['workspace', 'endpoint'], additionalProperties: false}}));
const allowed = ['read', 'ls', 'memory_search', 'memory_get', 'probe_owned'];
const settings = {
  logging: {file: path.join(root, 'gateway-native.log'), level: 'debug'},
  gateway: {mode: 'local', bind: 'loopback', port, auth: {mode: 'token', token: '${OPENCLAW_GATEWAY_TOKEN}'}, controlUi: {enabled: false}},
  agents: {defaults: {model: 'fixture/chat', workspace, skipBootstrap: true, heartbeat: {every: '0m'},
    compaction: {memoryFlush: {enabled: false}}, models: {'fixture/chat': {agentRuntime: {id: 'openclaw'}}},
    sandbox: {mode: 'all', backend: 'asterhand-concurrency-probe', scope: 'agent', workspaceAccess: 'rw',
      workspaceRoot: path.join(root, 'sandboxes'), docker: {workdir: workspace}, browser: {enabled: false}, prune: {idleHours: 0, maxAgeDays: 0}}}},
  tools: {allow: allowed, deny: ['exec', 'process', 'code_execution', 'skills_read'], fs: {workspaceOnly: true},
    codeMode: {enabled: false}, toolSearch: false, sandbox: {tools: {allow: allowed}}},
  plugins: {slots: {memory: 'memory-core'}, allow: ['asterhand-concurrency-probe', 'memory-core'],
    load: {paths: [path.join(root, 'plugin')]}, entries: {
      'asterhand-concurrency-probe': {enabled: true, hooks: {allowConversationAccess: true, allowPromptInjection: true}, config: {workspace, endpoint}},
      'memory-core': {enabled: true, config: {dreaming: {enabled: false}}}}},
  memory: {search: {provider: 'none', sources: ['memory'], rememberAcrossConversations: false, extraPaths: [],
    experimental: {sessionMemory: false}, multimodal: {enabled: false}, store: {vector: {enabled: false}}}},
  models: {providers: {fixture: {baseUrl: endpoint + '/v1', api: 'openai-completions', apiKey: 'synthetic-probe',
    models: [{id: 'chat', name: 'Probe', contextWindow: 64000, maxTokens: 8000}]}}},
  commands: {native: false, nativeSkills: false, text: false, bash: false, config: false, debug: false, restart: false, allowFrom: {'*': []}},
  session: {maintenance: {mode: 'warn'}, reset: {mode: 'none'}}, cron: {enabled: false}, browser: {enabled: false},
  discovery: {mdns: {mode: 'off'}}, update: {checkOnStart: false},
};
await writeFile(path.join(root, 'openclaw.json'), JSON.stringify(settings, null, 2));
const token = randomUUID(), originalEnvironment = {...process.env}, originalDirectory = process.cwd();
const environment = {};
for (const key of ['SYSTEMROOT', 'WINDIR', 'COMSPEC', 'PATHEXT']) if (process.env[key]) environment[key] = process.env[key];
Object.assign(environment, {USERPROFILE: path.join(root, 'profile'), HOME: path.join(root, 'profile'),
  APPDATA: path.join(root, 'appdata'), LOCALAPPDATA: path.join(root, 'localappdata'),
  TEMP: path.toNamespacedPath(path.join(root, 'temp')), TMP: path.toNamespacedPath(path.join(root, 'temp')),
  PATH: path.dirname(process.execPath) + path.delimiter + path.join(process.env.SYSTEMROOT, 'System32'),
  OPENCLAW_STATE_DIR: path.join(root, 'state'), OPENCLAW_CONFIG_PATH: path.join(root, 'openclaw.json'),
  OPENCLAW_GATEWAY_TOKEN: token, OPENCLAW_NO_RESPAWN: '1', NODE_DISABLE_COMPILE_CACHE: '1', NO_COLOR: '1'});
const gateway = new Gateway(event => {
  if (event.event === 'chat') record('chat_event', {state: event.payload?.state, run: event.payload?.runId});
}, () => { nativeExited = true; });
const checks = [];
const check = (name, condition, detail) => { checks.push({name, passed: Boolean(condition), detail}); };
try {
  process.env = environment;
  process.chdir(root);
  await gateway.start({entry: path.join(runtime, 'openclaw.mjs'), port, token, startup_timeout_ms: 150000});
  for (const name of ['batch', 'memory', 'cancel']) {
    scenario = name; modelTurn = 0;
    const key = `agent:main:asterhand:${randomUUID().replaceAll('-', '')}`, run = randomUUID();
    await gateway.readyRead('sessions.create', {key});
    const history = await gateway.readyRead('chat.history', {sessionKey: key, limit: 10});
    await gateway.rpc('chat.send', {sessionKey: key, sessionId: history.sessionId, message: `Run isolated ${name} probe`,
      idempotencyKey: run, deliver: false, timeoutMs: 60000, expectedLeafEntryId: history.sessionInfo?.activeLeafEntryId ?? null}, 30000);
    if (name === 'cancel') {
      await waitFor(e => e.scenario === name && e.event === 'io_enter' && e.path === 'slow.txt');
      await gateway.rpc('chat.abort', {sessionKey: key, runId: run}, 10000);
      record('abort_ack');
      let terminal;
      try { terminal = await waitFor(e => e.scenario === name && e.event === 'chat_event' && ['aborted', 'final', 'error'].includes(e.state), 3000); }
      catch { record('no_terminal_before_release'); }
      check('cancel_terminal_before_io_exit', terminal && !trace.some(e => e.scenario === name && e.event === 'io_exit' && e.path === 'slow.txt'), terminal?.state);
      release(e => e.scenario === name);
      await waitFor(e => e.scenario === name && e.event === 'io_exit' && e.path === 'slow.txt');
    } else {
      await waitFor(e => e.scenario === name && e.event === 'model_input' && e.turn === 1);
      if (name === 'batch') {
        check('model_continues_while_after_hooks_pending', pending.some(e => e.entry.event === 'after_enter'));
        release(e => e.scenario === name && e.event === 'after_enter');
        await waitFor(e => e.scenario === name && e.event === 'after_exit');
      }
      await waitFor(e => e.scenario === name && e.event === 'chat_event' && ['final', 'error'].includes(e.state));
    }
    await writeFile(path.join(root, name + '-history.json'), JSON.stringify(await gateway.readyRead('chat.history', {sessionKey: key, limit: 30}), null, 2));
  }
  const before = trace.filter(e => e.scenario === 'batch' && e.event === 'before');
  const reads = trace.filter(e => e.scenario === 'batch' && e.event === 'io_enter' && ['a.txt', 'b.txt'].includes(e.path));
  check('all_batch_hooks_before_native_file_execution', before.length === 4 && reads.length === 2 && Math.max(...before.map(e => e.sequence)) < Math.min(...reads.map(e => e.sequence)));
  check('different_native_file_invocations_overlap', reads.length === 2 && Math.max(...reads.map(e => e.sequence)) < trace.find(e => e.scenario === 'batch' && e.event === 'io_exit' && ['a.txt', 'b.txt'].includes(e.path))?.sequence);
  check('native_file_bridge_has_no_hook_ambient_identity', reads.length === 2 && reads.every(e => e.ambient === null));
  check('native_file_bridge_has_no_call_identity_parameter', reads.length === 2 && reads.every(e => !e.keys.some(key => /call|run|context/i.test(key))));
  check('owned_execute_wrapper_propagates_identity', ownedExecutionIdentity(trace));
  check('memory_tool_execution_loses_before_hook_context', trace.some(e => e.scenario === 'memory' && e.event === 'memory_io' && e.ambient === null));
  const finalMemory = inputs.find(e => e.scenario === 'memory' && e.body.messages.some(m => m.role === 'tool'));
  const memoryChecks = memoryResults(finalMemory?.body.messages);
  check('native_memory_search_returns_seeded_fact', memoryChecks.search);
  check('native_memory_get_returns_seeded_fact', memoryChecks.get);
  check('batch_registration_matches_original_order', JSON.stringify(before.map(e => e.call)) ===
    JSON.stringify(batches.batch.map((_call, index) => `batchcall${index}`)));
  const finalBatch = inputs.find(e => e.scenario === 'batch' && e.body.messages.some(m => m.role === 'tool'));
  check('native_read_results_keep_call_pairing', ['a.txt', 'b.txt'].every((name, index) =>
    finalBatch?.body.messages.some(m => m.role === 'tool' && m.tool_call_id === `batchcall${index}` && String(m.content).includes(`${name} isolated marker`))));
  check('model_schema_is_exact_probe_allowlist', inputs.length > 0 && inputs.every(e =>
    JSON.stringify((e.body.tools ?? []).map(t => t.function.name).sort()) === JSON.stringify([...allowed].sort())));
} catch (error) {
  record('probe_error', {message: error.message, native: error.native});
  process.exitCode = 1;
} finally {
  release(() => true);
  gateway.disconnect();
  if (gateway.child && !nativeExited) {
    const exited = new Promise(resolve => gateway.child.once('exit', resolve));
    gateway.child.kill();
    await exited;
  }
  process.env = originalEnvironment;
  process.chdir(originalDirectory);
  server.closeAllConnections();
  await new Promise(resolve => server.close(resolve));
  const pkg = JSON.parse(await readFile(path.join(runtime, 'package.json'), 'utf8'));
  const report = {runtime: {version: pkg.version, node: process.version}, checks,
    verdict: 'S0 is blocked unless a supported wrapper and invocation-context API is demonstrated; negative observations are probe assertions, not capability passes.',
    errors: trace.filter(e => e.event === 'probe_error' || e.event === 'controller_error')};
  await writeFile(path.join(root, 'trace.json'), JSON.stringify(trace, null, 2));
  await writeFile(path.join(root, 'model-inputs.json'), JSON.stringify(inputs, null, 2));
  await writeFile(path.join(root, 'report.json'), JSON.stringify(report, null, 2));
  await writeFile(path.join(output, 'latest.json'), JSON.stringify({root, report}, null, 2));
  console.log(JSON.stringify({root, checks, errors: report.errors}));
  if (checks.some(item => !item.passed) || report.errors.length) process.exitCode = 1;
}
