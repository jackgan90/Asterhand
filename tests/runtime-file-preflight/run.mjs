import assert from 'node:assert/strict';
import {createHash, randomUUID} from 'node:crypto';
import {spawn, spawnSync} from 'node:child_process';
import {readFile, readdir, mkdir, mkdtemp, open, writeFile} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath, pathToFileURL} from 'node:url';

// Opt-in P0 probe, not a production adapter or a successful G3 acceptance test.
// Use the pinned package's actual host tools without changing its source or I/O.
const here = path.dirname(fileURLToPath(import.meta.url));
const repository = path.resolve(here, '../..');
const [runtimeArgument, outputArgument] = process.argv.slice(2);
assert(runtimeArgument && outputArgument, 'Usage: <locked-node> run.mjs <runtime-root> <evidence-directory>');
assert.equal(process.platform, 'win32', 'This probe requires Windows sharing semantics');
const runtime = path.resolve(runtimeArgument);
const catalog = JSON.parse(await readFile(path.join(repository, 'packaging/dependencies.json'), 'utf8'));
assert.equal(process.versions.node, catalog.node.version, 'Run with the pinned Node version');
const packageRoot = path.join(runtime, 'node_modules/openclaw');
const packageInfo = JSON.parse(await readFile(path.join(packageRoot, 'package.json'), 'utf8'));
const component = catalog.components.find(entry => entry.package === 'openclaw');
assert.equal(packageInfo.version, component.version);
assert.equal(packageInfo.license, component.license);
const lock = await readFile(path.join(runtime, 'package-lock.json'));
assert.deepEqual(lock, await readFile(path.join(repository, 'packaging/runtime-lock.json')));
await mkdir(path.resolve(outputArgument), {recursive: true});
const evidence = await mkdtemp(path.join(path.resolve(outputArgument), 'native-files-'));
// Imports must not discover a developer's personal OpenClaw configuration.
process.env.OPENCLAW_STATE_DIR = path.join(evidence, 'state');
process.env.OPENCLAW_CONFIG_PATH = path.join(evidence, 'openclaw.json');
process.env.OPENCLAW_NO_RESPAWN = '1';
await writeFile(process.env.OPENCLAW_CONFIG_PATH, '{}');
const dist = path.join(packageRoot, 'dist');
const modules = (await readdir(dist)).filter(name => /^core-coding-tools-.*\.mjs$/.test(name));
assert.equal(modules.length, 1, 'Pinned core tool module must be unambiguous');
const source = await readFile(path.join(dist, modules[0]), 'utf8');
const alias = source.match(/createCoreCodingTools as (\w+)/)?.[1];
assert(alias, 'Pinned core factory export was not found');
const createTools = (await import(pathToFileURL(path.join(dist, modules[0])).href))[alias];
const report = {
  node: process.versions.node, platform: process.platform, osRelease: os.release(), arch: process.arch,
  openclaw: packageInfo.version, license: packageInfo.license,
  factory: modules[0], factorySHA256: createHash('sha256').update(source).digest('hex'),
  scope: 'Direct native host tools, workspaceOnly enabled; no Gateway/model or production configuration',
  cases: [],
};

async function contents(file) {
  try { return await readFile(file, 'utf8'); } catch (error) {
    if (error.code === 'ENOENT') return null;
    throw error;
  }
}

function external(mode, target, content, expectDenied = false) {
  const result = spawnSync(process.execPath, [path.join(here, 'external-write.mjs'), mode, target, content],
    {encoding: 'utf8', windowsHide: true, timeout: 10000});
  assert.ifError(result.error);
  if (expectDenied) assert.notEqual(result.status, 0, 'Sharing lock did not reject the external writer');
  else assert.equal(result.status, 0, result.stderr);
}

async function sharingLock(target) {
  const executable = path.join(process.env.SYSTEMROOT, 'System32/WindowsPowerShell/v1.0/powershell.exe');
  const child = spawn(executable, ['-NoProfile', '-NonInteractive', '-File', path.join(here, 'hold-file.ps1'), '-Path', target],
    {windowsHide: true, stdio: ['pipe', 'pipe', 'pipe']});
  let diagnostic = '';
  child.stderr.on('data', data => { diagnostic += data; });
  // close also fires after spawn errors; exit does not.
  const exited = new Promise(resolve => child.once('close', resolve));
  try {
    await new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error(`Lock helper timed out: ${diagnostic}`)), 15000);
      const finish = callback => value => { clearTimeout(timer); callback(value); };
      let output = '';
      child.once('error', finish(reject));
      child.once('exit', finish(code => reject(new Error(`Lock helper exited ${code}: ${diagnostic}`))));
      child.stdout.on('data', data => {
        output += data;
        if (output.includes('locked')) finish(resolve)();
      });
    });
  } catch (error) {
    child.kill();
    await exited;
    throw error;
  }
  return async () => {
    child.stdin.end();
    const timer = setTimeout(() => child.kill(), 3000);
    await exited;
    clearTimeout(timer);
  };
}

function request(operation) {
  if (operation === 'write') return {name: 'write', params: {path: 'target.txt', content: 'new\n'}};
  if (operation === 'edit') return {name: 'edit', params: {path: 'target.txt', edits: [{oldText: 'old', newText: 'new'}]}};
  const bodies = {
    update: '*** Update File: target.txt\n@@\n-old\n+new',
    delete: '*** Delete File: target.txt',
    move: '*** Update File: target.txt\n*** Move to: moved.txt\n@@\n-old\n+new',
    add: '*** Add File: target.txt\n+new',
  };
  return {name: 'apply_patch', params: {input: `*** Begin Patch\n${bodies[operation]}\n*** End Patch`}};
}

async function probe(operation, intervention) {
  const workspace = await mkdtemp(path.join(evidence, `${operation}-${intervention}-`));
  const target = path.join(workspace, 'target.txt');
  const missing = intervention === 'create' || operation === 'add';
  if (!missing) await writeFile(target, 'old\n');
  const tools = createTools({codingRoot: workspace, containmentRoot: workspace, workspaceOnly: true,
    includeBaseCodingTools: true, shellTools: 'apply_patch', applyPatchEnabled: true,
    applyPatchWorkspaceOnly: true, applyPatchContainmentSource: 'config'});
  const call = request(operation);
  const tool = tools.find(tool => tool.name === call.name);
  assert(tool, `Missing ${call.name}`);
  const snapshot = await contents(target);
  // Persist before invoking the real tool, like a synchronous before-tool guard.
  const snapshotFile = path.join(evidence, `${path.basename(workspace)}.snapshot.json`);
  const snapshotHandle = await open(snapshotFile, 'wx');
  try {
    await snapshotHandle.writeFile(JSON.stringify({exists: snapshot !== null, content: snapshot}));
    await snapshotHandle.sync();
  } finally { await snapshotHandle.close(); }
  let release;
  if (['write', 'replace', 'create'].includes(intervention)) external(intervention, target, 'old\nexternal\n');
  if (intervention === 'lock') {
    release = await sharingLock(target);
    try { external('write', target, 'external\n', true); } catch (error) { await release(); throw error; }
  }
  const atExecution = await contents(target);
  let result, error;
  try {
    result = await tool.execute(randomUUID(), call.params, AbortSignal.timeout(15000));
  } catch (caught) {
    error = String(caught);
    for (let cause = caught.cause; cause; cause = cause.cause) error += `; caused by ${String(cause)}`;
  } finally { await release?.(); }
  const after = await contents(target);
  const moved = await contents(path.join(workspace, 'moved.txt'));
  const completed = !error && result?.isError !== true;
  const row = {operation, intervention, snapshot, atExecution, after, moved, completed, error: error ?? null,
    result, snapshotCoversOverwrittenVersion: snapshot === atExecution};
  report.cases.push(row);
  if (intervention === 'none') {
    assert(completed, `${operation} control failed: ${error ?? JSON.stringify(result)}`);
    assert.equal(operation === 'move' ? moved : after, operation === 'delete' ? null : 'new\n');
  } else if (intervention === 'lock') {
    assert(!completed, 'Sharing lock unexpectedly allowed a native mutation; investigate before relying on it');
    assert.equal(after, snapshot, 'Denied native mutation changed bytes');
    if (operation === 'move') {
      // Native move creates the destination before removing the locked source.
      // Preserve this partial result; replaying the patch would collide with it.
      assert.equal(moved, 'new\n');
      row.partial = true;
    } else {
      // The same native object must work after the lock is released.
      await tool.execute(randomUUID(), call.params, AbortSignal.timeout(15000));
      assert.equal(await contents(target), operation === 'delete' ? null : 'new\n');
      row.completedAfterRelease = true;
    }
  } else if (operation === 'add') {
    assert(!completed, 'Exclusive patch creation unexpectedly overwrote a competing file');
    assert.equal(after, atExecution);
  } else {
    assert(completed, `Expected reproducible journal conflict, received ${error ?? JSON.stringify(result)}`);
    assert.notEqual(snapshot, atExecution);
    assert.notEqual(after, atExecution);
  }
  console.log(`${operation}/${intervention}: ${completed ? 'native completed' : 'native refused'}`);
}

try {
  for (const operation of ['write', 'edit', 'update', 'delete', 'move', 'add']) await probe(operation, 'none');
  for (const operation of ['write', 'edit', 'update', 'delete', 'move']) {
    await probe(operation, 'write');
    await probe(operation, 'replace');
    await probe(operation, 'lock');
  }
  await probe('write', 'create');
  await probe('add', 'create');
  report.probe = 'passed';
  report.hostStrategy = 'blocked: snapshot-before-call loses external revisions; sharing lock also rejects native writes';
  report.g3 = 'pending: official filesystem bridge and full supervision still require validation';
} catch (error) {
  report.probe = 'failed';
  report.error = String(error.stack ?? error);
  process.exitCode = 1;
} finally {
  await writeFile(path.join(evidence, 'report.json'), JSON.stringify(report, null, 2));
  console.log(`Evidence: ${path.join(evidence, 'report.json')}`);
  if (report.error) console.error(report.error);
}
