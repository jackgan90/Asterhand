import {createOpenClawCodingTools} from 'openclaw/plugin-sdk/agent-harness';
import {getActiveMemorySearchManager} from 'openclaw/plugin-sdk/memory-host-search';

const codingToolConfig = Object.freeze({
  tools: {
    fs: {workspaceOnly: true},
    exec: {applyPatch: {enabled: true, workspaceOnly: true}},
    deny: ['exec', 'process'],
  },
});

const codingPlan = Object.freeze({
  includeBaseCodingTools: true,
  includeShellTools: true,
  includeChannelTools: false,
  includeOpenClawTools: false,
  includePluginTools: false,
});

const progressPlan = Object.freeze({
  includeBaseCodingTools: false,
  includeShellTools: false,
  includeChannelTools: false,
  includeOpenClawTools: true,
  includePluginTools: false,
});

const delegatedFileMap = Object.freeze([
  {publicName: 'read_file', sdkName: 'read', label: 'Read file'},
  {publicName: 'write_file', sdkName: 'write', label: 'Write file'},
  {publicName: 'edit_file', sdkName: 'edit', label: 'Edit file'},
  {publicName: 'apply_file_patch', sdkName: 'apply_patch', label: 'Apply file patch'},
  {publicName: 'list_dir', sdkName: 'ls', label: 'List directory'},
]);

function buildCodingToolSet(workspace, fsBridge, abortSignal) {
  const list = createOpenClawCodingTools({
    workspaceDir: workspace,
    modelProvider: 'openai',
    abortSignal,
    wrapBeforeToolCallHook: false,
    config: codingToolConfig,
    sandbox: {
      enabled: true,
      workspaceDir: workspace,
      agentWorkspaceDir: workspace,
      containerWorkdir: workspace,
      workspaceAccess: 'rw',
      docker: {},
      fsBridge,
    },
    toolConstructionPlan: codingPlan,
  });
  return new Map(list.map(tool => [tool.name, tool]));
}

function buildProgressTool(workspace, sessionKey, abortSignal) {
  const list = createOpenClawCodingTools({
    workspaceDir: workspace,
    agentSessionKey: sessionKey,
    abortSignal,
    wrapBeforeToolCallHook: false,
    config: {tools: {updatePlan: true, allow: ['progress_card']}},
    toolConstructionPlan: progressPlan,
  });
  const tool = list.find(item => item.name === 'progress_card');
  if (!tool) throw new Error('OpenClaw progress_card delegate is unavailable');
  return tool;
}

function normalizeMemorySearchArgs(raw) {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return raw;
  const next = {...raw};
  if (next.maxResults === undefined && next.max_results !== undefined) next.maxResults = next.max_results;
  if (next.minScore === undefined && next.min_score !== undefined) next.minScore = next.min_score;
  delete next.max_results;
  delete next.min_score;
  return next;
}

function createSearchFilesTool(client, coordinator) {
  return {
    name: 'search_files',
    label: 'Search files',
    description: 'Find files by name or literal text within a directory subtree. Results and scans are bounded; pass next as cursor to continue.',
    parameters: {
      type: 'object',
      properties: {
        path: {type: 'string'},
        query: {type: 'string'},
        content: {type: 'boolean'},
        cursor: {type: 'string'},
      },
      required: ['query'],
      additionalProperties: false,
    },
    execute: (toolCallId, params, signal) =>
      coordinator.runTool('search_files', toolCallId, params, signal, async ctx => {
        const result = await ctx.tracker.track(
          client.request('search', {
            id: ctx.id,
            path: client.relative(params.path ?? '.'),
            query: params.query,
            content: params.content === true,
            cursor: params.cursor ?? '',
          }),
        );
        return {content: [{type: 'text', text: JSON.stringify(result)}]};
      }),
  };
}

function createMemoryTools(api, memory, coordinator) {
  return [
    {
      name: 'memory_search',
      label: 'Memory Search',
      description: 'Mandatory recall step: search MEMORY.md, USER.md, and memory/*.md before answering questions about prior work, decisions, dates, people, preferences, or todos.',
      parameters: {
        type: 'object',
        properties: {
          query: {type: 'string'},
          maxResults: {type: 'integer', minimum: 1},
          minScore: {type: 'number'},
          corpus: {type: 'string', enum: ['memory', 'wiki', 'all', 'sessions']},
        },
        required: ['query'],
        additionalProperties: false,
      },
      prepareArguments: normalizeMemorySearchArgs,
      execute: (toolCallId, params, signal) =>
        coordinator.runTool('memory_search', toolCallId, params, signal, async ctx => {
          const {manager} = await getActiveMemorySearchManager({cfg: api.config, agentId: 'main', purpose: 'default'});
          if (!manager?.sync || !manager?.search) throw new Error('Native memory index is unavailable');
          const results = await memory.withCall({id: ctx.id, tracker: ctx.tracker}, async () => {
            await manager.sync({reason: 'memory_search', force: true});
            ctx.signal?.throwIfAborted();
            return manager.search(params.query.trim(), {
              maxResults: params.maxResults,
              minScore: params.minScore,
              sessionKey: ctx.sessionKey,
            });
          });
          const status = typeof manager.status === 'function' ? manager.status() : {};
          const payload = {
            results,
            provider: status?.provider ?? 'none',
            model: status?.model,
            fallback: status?.fallback,
            citations: 'auto',
            mode: status?.custom?.searchMode ?? 'fts-only',
          };
          return {content: [{type: 'text', text: JSON.stringify(payload, null, 2)}], details: payload};
        }),
    },
    {
      name: 'memory_get',
      label: 'Memory Get',
      description: 'Safe snippet read from MEMORY.md, USER.md, or memory/*.md with optional from/lines; use after memory_search to pull only the needed lines.',
      parameters: {
        type: 'object',
        properties: {
          path: {type: 'string'},
          from: {type: 'integer', minimum: 1},
          lines: {type: 'integer', minimum: 1},
          corpus: {type: 'string', enum: ['memory', 'wiki', 'all']},
        },
        required: ['path'],
        additionalProperties: false,
      },
      execute: (toolCallId, params, signal) =>
        coordinator.runTool('memory_get', toolCallId, params, signal, async ctx => {
          const excerpt = await memory.withCall({id: ctx.id, tracker: ctx.tracker}, () =>
            memory.readFile({relPath: params.path, from: params.from, lines: params.lines}),
          );
          return {content: [{type: 'text', text: JSON.stringify(excerpt, null, 2)}], details: excerpt};
        }),
    },
  ];
}

function createProgressCardTool(progressProto, coordinator) {
  return {
    name: 'progress_card',
    label: progressProto.label ?? 'Progress Card',
    description: progressProto.description,
    parameters: progressProto.parameters,
    ...(typeof progressProto.prepareArguments === 'function'
      ? {prepareArguments: progressProto.prepareArguments.bind(progressProto)}
      : {}),
    execute: (toolCallId, params, signal) =>
      coordinator.runTool('progress_card', toolCallId, params, signal, async () => {
        const markdown = typeof params?.markdown === 'string' ? params.markdown.trim() : '';
        if (Buffer.byteLength(markdown, 'utf8') > 8192) {
          throw new Error('progress_card markdown exceeds 8 KB limit');
        }
        const plan = Array.isArray(params?.plan) ? params.plan : [];
        const inProgress = plan.filter(item => item?.status === 'in_progress').length;
        if (inProgress > 1) {
          throw new Error('progress_card allows at most one in_progress step');
        }
        const completed = plan.filter(item => item?.status === 'completed').length;
        const total = plan.length;
        const payload = {steps: total > 0 ? {completed, total} : null};
        return {content: [{type: 'text', text: JSON.stringify(payload, null, 2)}], details: payload};
      }),
  };
}

export function createWorkspaceTools({api, client, memory, coordinator}) {
  const prototypes = buildCodingToolSet(client.workspace, client.bootstrapBridge);
  const progressProto = buildProgressTool(client.workspace);
  const tools = [];

  for (const mapping of delegatedFileMap) {
    const proto = prototypes.get(mapping.sdkName);
    if (!proto) throw new Error(`Missing OpenClaw SDK tool prototype: ${mapping.sdkName}`);
    tools.push({
      name: mapping.publicName,
      label: mapping.label,
      description: proto.description,
      parameters: proto.parameters,
      ...(typeof proto.prepareArguments === 'function' ? {prepareArguments: proto.prepareArguments.bind(proto)} : {}),
      execute: (toolCallId, params, signal, onUpdate) =>
        coordinator.runTool(mapping.publicName, toolCallId, params, signal, async ctx => {
          const scopedSet = buildCodingToolSet(client.workspace, ctx.bridge, ctx.signal);
          const impl = scopedSet.get(mapping.sdkName);
          if (!impl) throw new Error(`Missing OpenClaw SDK delegate: ${mapping.sdkName}`);
          return impl.execute(toolCallId, params, ctx.signal, onUpdate);
        }),
    });
  }

  tools.push(createSearchFilesTool(client, coordinator));
  tools.push(...createMemoryTools(api, memory, coordinator));
  tools.push(createProgressCardTool(progressProto, coordinator));
  return tools;
}
