import {readFile, readdir, mkdir, writeFile} from 'node:fs/promises';
import {createHash} from 'node:crypto';
import path from 'node:path';

const [runtimeArg, outputArg] = process.argv.slice(2);
if (!runtimeArg || !outputArg) throw new Error('Usage: node sdk-inventory.mjs <openclaw-directory> <evidence-directory>');
const runtime = path.resolve(runtimeArg), output = path.resolve(outputArg);
const pkg = JSON.parse(await readFile(path.join(runtime, 'package.json'), 'utf8'));
const distFiles = await readdir(path.join(runtime, 'dist'));
const declarations = distFiles.find(name => /^config-contracts-.*\.d\.ts$/.test(name));
if (!declarations) throw new Error('Pinned SDK contract declarations missing');
const files = [path.join('dist', declarations), 'package.json'];
const sdk = await readdir(path.join(runtime, 'dist/plugin-sdk'));
const publicContextReferences = [];
for (const name of sdk.filter(name => name.endsWith('.js'))) {
  const source = await readFile(path.join(runtime, 'dist/plugin-sdk', name), 'utf8');
  if (/getAgentToolExecutionContext|runWithAgentToolExecutionContext/.test(source)) publicContextReferences.push(name);
}
for (const prefix of ['agent-core-', 'agent-tools.before-tool-call-', 'tool-execution-context-']) {
  files.push(...distFiles.filter(name => name.startsWith(prefix) && name.endsWith('.mjs')).map(name => path.join('dist', name)));
}
for (const name of distFiles.filter(name => /^tools-.*\.mjs$/.test(name))) {
  if ((await readFile(path.join(runtime, 'dist', name), 'utf8')).includes('plugin tool name conflict (')) files.push(path.join('dist', name));
}
files.push('dist/plugin-sdk/agent-core.js', 'dist/plugin-sdk/agent-harness-tool-runtime.js');
const evidence = [];
const pattern = /type (?:SandboxFsBridge|PluginHookBeforeToolCallResult|PluginHookToolContext|OpenClawPluginApi)|getAgentToolExecutionContext|runWithAgentToolExecutionContext|prepareToolCallEntry|prepareControl.pause|tool\.execute|plugin tool name conflict|registerAgentToolResultMiddleware|registerTrustedToolPolicy/;
for (const file of files) {
  const bytes = await readFile(path.join(runtime, file));
  const lines = bytes.toString('utf8').split(/\r?\n/);
  evidence.push({file: file.replaceAll('\\', '/'), sha256: createHash('sha256').update(bytes).digest('hex'),
    locations: lines.flatMap((line, index) => pattern.test(line) && line.length < 700 ? [{line: index + 1, text: line.trim()}] : [])});
}
await mkdir(output, {recursive: true});
const report = {version: pkg.version, node: process.version, publicContextReferences, evidence};
await writeFile(path.join(output, 'sdk-inventory.json'), JSON.stringify(report, null, 2));
console.log(JSON.stringify({version: pkg.version, publicContextReferences, files: evidence.length}));
