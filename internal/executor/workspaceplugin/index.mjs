import {registerSandboxBackend} from 'openclaw/plugin-sdk/sandbox';
import {declareAgentWorkspaceAccess, registerAgentWorkspaceAccess} from 'openclaw/plugin-sdk/agent-workspace-runtime';
import {getActiveMemorySearchManager} from 'openclaw/plugin-sdk/memory-host-search';
import {createHash} from 'node:crypto';
import {allowedTools, fileClient, mutationScope} from './files.mjs';
import {memoryFiles} from './memory.mjs';

export default {
  id: 'asterhand-workspace',
  register(api) {
    const lifetime = new AbortController();
    let active = null;
    let closing = Promise.resolve();
    const client = fileClient(api.pluginConfig, () => active?.id);
    const memory = memoryFiles(client,lifetime.signal);
    const denied = async () => { throw new Error('Process execution is disabled'); };
    declareAgentWorkspaceAccess(client.workspace);
    const releaseWorkspace = registerAgentWorkspaceAccess(client.workspace, {
      bridge: client.bridge, memoryFiles: memory,
      // Only reviewed text sources are loaded in this first capability batch.
      // No writable source can install dependencies or executable plugins.
      loadSkills: async () => ({entries: [], executionEntries: [], runtime: {platform: process.platform, bins: []}}),
      installSkillDependencies: denied,
    });
    const releaseBackend = registerSandboxBackend('asterhand-files', {
      resolveWorkdir: () => client.workspace,
      factory: async () => ({id: 'asterhand-files', runtimeId: `asterhand-${api.pluginConfig.policy}`,
        runtimeLabel: 'Asterhand workspace files', workdir: client.workspace,
        buildExecSpec: denied, runShellCommand: denied, createFsBridge: () => client.bridge}),
    });
    api.registerTool({name: 'workspace_search', label: 'Workspace search',
      description: 'Find workspace files by name or literal text. Results and scans are bounded; pass next as cursor to continue.',
      parameters: {type: 'object', properties: {path: {type: 'string'}, query: {type: 'string'},
        content: {type: 'boolean'}, cursor: {type: 'string'}}, required: ['query'], additionalProperties: false},
      async execute(_id, params) {
        if (!active) throw new Error('Missing authorized tool call');
        const result = await client.request('search', {path: client.relative(params.path ?? '.'),
          query: params.query, content: params.content === true, cursor: params.cursor ?? ''});
        return {content: [{type: 'text', text: JSON.stringify(result)}]};
      },
    });
    api.on('before_prompt_build', () => ({toolsAllow: [...allowedTools]}));
    api.on('before_tool_call', async (event, context) => {
      const run = context.runId ?? event.runId;
      const nativeID = context.toolCallId ?? event.toolCallId;
      if (!allowedTools.includes(event.toolName) || !run || !nativeID || !context.sessionKey?.startsWith('agent:main:asterhand:')) {
        return {block: true, blockReason: 'Workspace tool is not authorized'};
      }
      const identity = {id: createHash('sha256').update(`${run}:${nativeID}`).digest('hex'),run,
        conversation:context.sessionKey.slice('agent:main:asterhand:'.length),tool:event.toolName,
        fingerprint:createHash('sha256').update(JSON.stringify([event.toolName,event.params])).digest('hex')};
      try {
        await closing;
        // Native parallel calls must not share another call's file handles.
        if (active) return {block: true, blockReason: 'Another workspace tool is still running; retry after its result'};
        context.abortSignal?.throwIfAborted();
        if (event.toolName === 'memory_search') {
          const {manager} = await getActiveMemorySearchManager({cfg: api.config, agentId: 'main', purpose: 'default'});
          if (!manager?.sync) throw new Error('Native memory index is unavailable');
          // A sync already in progress can have captured the old file list.
          // Drain it before invalidating, then await the new watch generation.
          // Reindexing stays incremental and starts only for an explicit search.
          await manager.sync({reason: 'watch'});
          context.abortSignal?.throwIfAborted();
          memory.changed();
          await manager.sync({reason: 'watch'});
          context.abortSignal?.throwIfAborted();
        }
        const scope = mutationScope(event.toolName, event.params, client.relative);
        const result = await client.request('begin', {...identity,...scope});
        if (result.state !== 'started') return {block: true, blockReason: 'Tool identity already has a saved result; do not replay'};
        active = {id:identity.id, nativeID};
      } catch (error) {
        const code = typeof error?.code === 'string' && /^[a-z_]+$/.test(error.code) ? error.code : 'invalid_request';
        // A rejected scope has no file handles or body snapshot. Persist only
        // its identity and fixed reason when service admission still permits it.
        try {
          const record=await client.request('begin',{...identity,targets:[],new_bytes:0});
          if(record.state==='started')await client.request('end',{id:identity.id,state:'failed',detail:code});
        } catch {}
        return {block: true, blockReason: `Workspace check failed: ${code}`};
      }
    });
    api.on('after_tool_call', (event, context) => {
      if (!active || active.nativeID !== (context.toolCallId ?? event.toolCallId)) return;
      const call = active;
      closing = client.request('end', {id: call.id,
        state: event.error || event.result?.isError ? 'failed' : 'completed', detail: event.error ? 'native_tool_failed' : ''})
        .then(() => { if (active === call) active = null; });
      // Keep admission closed on uncertain publication. The owner will stop and
      // reconcile the native runtime; swallowed hook errors cannot reopen it.
      void closing.catch(() => {});
      return closing;
    });
    api.on('gateway_stop', () => { lifetime.abort(); releaseBackend(); releaseWorkspace(); });
  },
};
