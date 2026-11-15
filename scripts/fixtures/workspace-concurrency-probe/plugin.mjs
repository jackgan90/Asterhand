import {AsyncLocalStorage} from 'node:async_hooks';
import {readFile, readdir, stat} from 'node:fs/promises';
import path from 'node:path';
import {registerSandboxBackend} from 'openclaw/plugin-sdk/sandbox';
import {declareAgentWorkspaceAccess, registerAgentWorkspaceAccess} from 'openclaw/plugin-sdk/agent-workspace-runtime';
import {getActiveMemorySearchManager} from 'openclaw/plugin-sdk/memory-host-search';
import {memoryFiles} from './memory.mjs';

// Isolated SDK probe only. This bridge is not the product's Windows file service.
const context = new AsyncLocalStorage();
export default {
  id: 'asterhand-concurrency-probe',
  register(api) {
    const {workspace, endpoint} = api.pluginConfig;
    const lifetime = new AbortController();
    const allowed = ['read', 'ls', 'memory_search', 'memory_get', 'probe_owned'];
    const emit = async (event, fields = {}) => {
      const response = await fetch(endpoint + '/event', {method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({event, ambient: context.getStore() ?? null, ...fields}),
        signal: AbortSignal.timeout(30000)});
      if (!response.ok) throw new Error('Probe controller rejected event');
    };
    const relative = input => {
      const file = path.relative(workspace, path.resolve(workspace, input)).replaceAll('\\', '/');
      if (file === '..' || file.startsWith('../') || path.isAbsolute(file)) throw new Error('Probe path escape');
      return file;
    };
    const location = params => relative(path.resolve(params.cwd ?? workspace, params.filePath));
    const inspect = async name => {
      try {
        const info = await stat(path.join(workspace, relative(name)));
        return {type: info.isDirectory() ? 'directory' : 'file', size: info.size,
          mtimeMs: info.mtimeMs, modified_unix_ms: info.mtimeMs};
      } catch (error) { if (error.code === 'ENOENT') return null; throw error; }
    };
    const read = async (name, fields) => {
      await emit('io_enter', {path: name, ...fields});
      await Promise.resolve();
      const bytes = await readFile(path.join(workspace, relative(name)));
      await emit('io_exit', {path: name, ...fields});
      return bytes;
    };
    const denied = async () => { throw new Error('Probe has no write or process tools'); };
    const bridge = {
      pathMappings: [{hostRoot: workspace, containerRoot: workspace}],
      resolvePath(params) {
        const name = location(params), full = path.join(workspace, name);
        return {hostPath: full, containerPath: full, relativePath: name};
      },
      readFile: params => read(location(params), {origin: 'fs_bridge', keys: Object.keys(params).sort()}),
      readFileWithSource: async params => ({data: await bridge.readFile(params),
        canonicalPath: path.join(workspace, location(params)), workspaceRelativePath: location(params)}),
      stat: async params => {
        await emit('stat', {path: location(params), keys: Object.keys(params).sort()});
        return inspect(location(params));
      },
      readDirectory: async params => {
        const name = location(params);
        await emit('list', {path: name, keys: Object.keys(params).sort()});
        return (await readdir(path.join(workspace, name), {withFileTypes: true}))
          .map(item => ({name: item.name, type: item.isDirectory() ? 'directory' : 'file'}));
      },
      writeFile: denied, createFileExclusive: denied, remove: denied, mkdirp: denied,
    };
    const memory = memoryFiles({workspace, relative, async request(action, payload) {
      await emit('memory_io', {action, path: payload.path ?? '', keys: Object.keys(payload).sort()});
      if (action === 'read') return (await read(payload.path, {origin: 'memory'})).toString('base64');
      if (action === 'stat') return inspect(payload.path);
      if (action === 'search') return {items: [{path: 'memory/facts.md', type: 'file'}], next: ''};
      throw new Error('Unexpected memory operation');
    }}, lifetime.signal);
    declareAgentWorkspaceAccess(workspace);
    const releaseWorkspace = registerAgentWorkspaceAccess(workspace, {
      bridge, memoryFiles: memory,
      loadSkills: async () => ({entries: [], executionEntries: [], runtime: {platform: process.platform, bins: []}}),
      installSkillDependencies: denied,
    });
    const releaseBackend = registerSandboxBackend('asterhand-concurrency-probe', {
      resolveWorkdir: () => workspace,
      factory: async () => ({id: 'asterhand-concurrency-probe', runtimeId: 'concurrency-probe',
        runtimeLabel: 'Isolated concurrency probe', workdir: workspace,
        buildExecSpec: denied, runShellCommand: denied, createFsBridge: () => bridge}),
    });
    api.registerTool({name: 'probe_owned', label: 'Probe owned execution', description: 'SDK probe control.',
      parameters: {type: 'object', properties: {}, additionalProperties: false},
      async execute(id) {
        return context.run({id, source: 'owned_execute'}, async () => {
          await Promise.resolve();
          await read('owned.txt', {origin: 'owned_execute'});
          return {content: [{type: 'text', text: 'owned wrapper completed'}]};
        });
      },
    });
    api.on('before_prompt_build', () => ({toolsAllow: allowed}));
    api.on('before_tool_call', (event, ctx) => context.run({id: ctx.toolCallId, source: 'before_hook'}, async () => {
      await Promise.resolve();
      await emit('before', {tool: event.toolName, call: ctx.toolCallId, run: ctx.runId,
        contextKeys: Object.keys(ctx).sort()});
      if (!allowed.includes(event.toolName)) return {block: true, blockReason: 'Not a probe tool'};
      if (event.toolName === 'memory_search') {
        const {manager} = await getActiveMemorySearchManager({cfg: api.config, agentId: 'main', purpose: 'default'});
        await manager.sync({reason: 'watch'});
        memory.changed();
        await manager.sync({reason: 'watch'});
      }
    }));
    api.on('after_tool_call', async (event, ctx) => {
      await emit('after_enter', {tool: event.toolName, call: ctx.toolCallId, run: ctx.runId});
      await emit('after_exit', {tool: event.toolName, call: ctx.toolCallId});
    });
    api.on('gateway_stop', () => { lifetime.abort(); releaseBackend(); releaseWorkspace(); });
  },
};
