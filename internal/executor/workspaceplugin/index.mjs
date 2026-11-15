import {registerSandboxBackend} from 'openclaw/plugin-sdk/sandbox';
import {declareAgentWorkspaceAccess, registerAgentWorkspaceAccess} from 'openclaw/plugin-sdk/agent-workspace-runtime';
import {allowedTools, fileClient} from './files.mjs';
import {memoryFiles} from './memory.mjs';
import {createBatchCoordinator} from './batch.mjs';
import {createWorkspaceTools} from './tools.mjs';

export default {
  id: 'asterhand-workspace',
  register(api) {
    const lifetime = new AbortController();
    const client = fileClient(api.pluginConfig);
    const memory = memoryFiles(client, lifetime.signal);
    const coordinator = createBatchCoordinator(client);
    const denied = async () => {
      throw new Error('Process execution is disabled');
    };
    declareAgentWorkspaceAccess(client.workspace);
    const releaseWorkspace = registerAgentWorkspaceAccess(client.workspace, {
      bridge: client.bootstrapBridge,
      memoryFiles: memory,
      loadSkills: async () => ({entries: [], executionEntries: [], runtime: {platform: process.platform, bins: []}}),
      installSkillDependencies: denied,
    });
    const releaseBackend = registerSandboxBackend('asterhand-files', {
      resolveWorkdir: () => client.workspace,
      factory: async () => ({
        id: 'asterhand-files',
        runtimeId: `asterhand-${api.pluginConfig.policy}`,
        runtimeLabel: 'Asterhand workspace files',
        workdir: client.workspace,
        buildExecSpec: denied,
        runShellCommand: denied,
        createFsBridge: () => client.bootstrapBridge,
      }),
    });
    for (const tool of createWorkspaceTools({api, client, memory, coordinator})) {
      api.registerTool(tool);
    }
    api.on('before_prompt_build', () => ({toolsAllow: [...allowedTools]}));
    api.on('before_tool_call', (event, context) => coordinator.beforeToolCall(event, context));
    api.on('after_tool_call', (event, context) => coordinator.afterToolCall(event, context));
    api.on('gateway_stop', () => {
      lifetime.abort();
      releaseBackend();
      releaseWorkspace();
    });
  },
};
