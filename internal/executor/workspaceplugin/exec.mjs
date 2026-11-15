import {coded} from './files.mjs';
import {toolDisplaySpecs} from './tool-display.mjs';

const processActions = ['poll', 'status', 'log', 'list', 'kill'];

function normalizeOptionalInt(value, min, max, label) {
  if (value === undefined || value === null || value === '') return undefined;
  const num = typeof value === 'number' ? value : Number(value);
  if (!Number.isInteger(num) || num < min || num > max) {
    throw coded('invalid_request', `${label} must be an integer between ${min} and ${max}`);
  }
  return num;
}

function normalizeEnv(env) {
  if (env === undefined || env === null) return undefined;
  if (typeof env !== 'object' || Array.isArray(env)) {
    throw coded('invalid_env', 'env must be an object mapping strings to strings');
  }
  const clean = {};
  for (const [key, val] of Object.entries(env)) {
    if (typeof val !== 'string') {
      throw coded('invalid_env', `env.${key} must be a string`);
    }
    clean[key] = val;
  }
  return clean;
}

function normalizeExecCommandArgs(raw) {
  if (!raw || typeof raw !== 'object') return raw;
  const next = {...raw};
  if (next.yieldMs === undefined && next.yield_ms !== undefined) next.yieldMs = next.yield_ms;
  if (next.timeoutSec === undefined && next.timeout_sec !== undefined) next.timeoutSec = next.timeout_sec;
  delete next.yield_ms;
  delete next.timeout_sec;
  return next;
}

function normalizeProcessControlArgs(raw) {
  if (!raw || typeof raw !== 'object') return raw;
  const next = {...raw};
  if (next.sessionId === undefined && next.session_id !== undefined) next.sessionId = next.session_id;
  if (next.waitMs === undefined && next.wait_ms !== undefined) next.waitMs = next.wait_ms;
  delete next.session_id;
  delete next.wait_ms;
  return next;
}

function isAbortError(error, signal) {
  return Boolean(signal?.aborted || error?.name === 'AbortError' || error?.code === 'cancelled');
}

function createExecCommandTool(client, coordinator) {
  return {
    name: 'exec_command',
    label: 'Execute Command',
    description:
      'Execute a shell command or script inside the workspace OS sandbox. Foreground commands run for up to yieldMs (default 10000 ms; 0 starts immediately in the background) before returning a background session_id. Writes are allowed only inside the workspace and managed sandbox temp directory; background sessions end automatically on Stop or turn completion.',
    display: toolDisplaySpecs.exec_command,
    parameters: {
      type: 'object',
      properties: {
        command: {type: 'string', description: 'Command or script to execute inside the workspace sandbox.'},
        workdir: {type: 'string', description: 'Optional relative working directory inside the workspace.'},
        env: {
          type: 'object',
          additionalProperties: {type: 'string'},
          description: 'Optional extra non-sensitive environment variables.',
        },
        yieldMs: {
          type: 'integer',
          minimum: 0,
          maximum: 30000,
          description: 'Milliseconds to wait for completion before detaching into a background session (0-30000).',
        },
        timeoutSec: {
          type: 'integer',
          minimum: 1,
          maximum: 600,
          description: 'Hard runtime limit in seconds for this command session (1-600).',
        },
      },
      required: ['command'],
      additionalProperties: false,
    },
    prepareArguments: normalizeExecCommandArgs,
    execute: (toolCallId, params, signal) =>
      coordinator.runTool('exec_command', toolCallId, params, signal, async ctx => {
        const command = typeof params.command === 'string' ? params.command : '';
        const yieldMs = normalizeOptionalInt(params.yieldMs ?? params.yield_ms, 0, 30000, 'yieldMs');
        const timeoutSec = normalizeOptionalInt(params.timeoutSec ?? params.timeout_sec, 1, 600, 'timeoutSec');
        const workdir =
          typeof params.workdir === 'string' && params.workdir.length > 0 ? client.relative(params.workdir) : '';
        const env = normalizeEnv(params.env);
        const waitBudgetMs = yieldMs === undefined ? 10000 : yieldMs;
        const payload = {
          id: ctx.id,
          command,
          ...(workdir ? {workdir} : {}),
          ...(env ? {env} : {}),
          ...(yieldMs !== undefined ? {yield_ms: yieldMs} : {}),
          ...(timeoutSec !== undefined ? {timeout_sec: timeoutSec} : {}),
        };
        try {
          const result = await ctx.tracker.track(
            client.request('exec/start', payload, waitBudgetMs + 15000, ctx.signal),
          );
          return {content: [{type: 'text', text: JSON.stringify(result)}]};
        } catch (error) {
          if (isAbortError(error, ctx.signal)) {
            await client.request('exec/kill', {id: ctx.id, detail: 'cancelled'}, 5000).catch(() => {});
          }
          throw error;
        }
      }),
  };
}

function createProcessControlTool(client, coordinator) {
  return {
    name: 'process_control',
    label: 'Process Control',
    description:
      'Poll, inspect, read paged output from, list, or kill background command sessions created during the current turn.',
    display: toolDisplaySpecs.process_control,
    parameters: {
      type: 'object',
      properties: {
        action: {
          type: 'string',
          enum: processActions,
          description: 'Session operation: poll, status, log, list, or kill.',
        },
        sessionId: {type: 'string', description: 'Background session ID (required except for list).'},
        offset: {type: 'integer', minimum: 0, description: 'Byte offset into the combined UTF-8 output buffer.'},
        limit: {type: 'integer', minimum: 1, maximum: 65536, description: 'Maximum UTF-8 bytes to return.'},
        waitMs: {
          type: 'integer',
          minimum: 0,
          maximum: 30000,
          description: 'Long-poll wait duration in milliseconds for poll (0-30000).',
        },
      },
      required: ['action'],
      additionalProperties: false,
    },
    prepareArguments: normalizeProcessControlArgs,
    execute: (toolCallId, params, signal) =>
      coordinator.runTool('process_control', toolCallId, params, signal, async ctx => {
        const action = typeof params.action === 'string' ? params.action.trim() : '';
        const sessionId = String(params.sessionId ?? params.session_id ?? '').trim();
        const offset = normalizeOptionalInt(params.offset, 0, Number.MAX_SAFE_INTEGER, 'offset');
        const limit = normalizeOptionalInt(params.limit, 1, 65536, 'limit');
        if (action === 'poll') {
          const waitMs = normalizeOptionalInt(params.waitMs ?? params.wait_ms, 0, 30000, 'waitMs');
          const waitBudgetMs = waitMs === undefined ? 0 : waitMs;
          const payload = {
            id: ctx.id,
            session_id: sessionId,
            ...(offset !== undefined ? {offset} : {}),
            ...(limit !== undefined ? {limit} : {}),
            ...(waitMs !== undefined ? {wait_ms: waitMs} : {}),
          };
          const result = await ctx.tracker.track(
            client.request('exec/poll', payload, waitBudgetMs + 15000, ctx.signal),
          );
          return {content: [{type: 'text', text: JSON.stringify(result)}]};
        }
        if (action === 'kill') {
          const result = await ctx.tracker.track(
            client.request('exec/kill', {id: ctx.id, session_id: sessionId, detail: 'model_kill'}, 12000),
          );
          return {content: [{type: 'text', text: JSON.stringify(result)}]};
        }
        const payload = {
          id: ctx.id,
          subaction: action,
          ...(sessionId ? {session_id: sessionId} : {}),
          ...(offset !== undefined ? {offset} : {}),
          ...(limit !== undefined ? {limit} : {}),
        };
        const result = await ctx.tracker.track(client.request('exec/control', payload, 12000));
        return {content: [{type: 'text', text: JSON.stringify(result)}]};
      }),
  };
}

export function createExecTools(client, coordinator) {
  return [createExecCommandTool(client, coordinator), createProcessControlTool(client, coordinator)];
}
