import {coded} from './files.mjs';
import {toolDisplaySpecs} from './tool-display.mjs';

const processActions = ['poll', 'status', 'log', 'list', 'kill'];
const MAX_EXEC_COMMAND_BYTES = 256 * 1024;
const MAX_ESCALATION_REASON_BYTES = 64 * 1024;

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

function normalizeExternalPaths(raw) {
  if (raw === undefined || raw === null) return undefined;
  if (!Array.isArray(raw) || raw.length > 8) {
    throw coded('invalid_request', 'externalPaths must be an array of at most 8 strings');
  }
  const clean = [];
  for (const item of raw) {
    if (typeof item !== 'string' || item.trim() === '') {
      throw coded('invalid_request', 'externalPaths items must be non-empty strings');
    }
    clean.push(item.trim());
  }
  return clean;
}

function resolveExecWorkdir(rawWorkdir, client) {
  if (typeof rawWorkdir !== 'string') return '';
  const trimmed = rawWorkdir.trim();
  if (!trimmed) return '';
  try {
    return client.relative(trimmed);
  } catch (error) {
    if (error?.code === 'outside_workspace') {
      return trimmed;
    }
    throw error;
  }
}

function normalizeExecCommandArgs(raw) {
  if (!raw || typeof raw !== 'object') return raw;
  const next = {...raw};
  if (next.yieldMs === undefined && next.yield_ms !== undefined) next.yieldMs = next.yield_ms;
  if (next.timeoutSec === undefined && next.timeout_sec !== undefined) next.timeoutSec = next.timeout_sec;
  if (next.escalationReason === undefined && next.escalation_reason !== undefined) {
    next.escalationReason = next.escalation_reason;
  }
  if (next.externalPaths === undefined && next.external_paths !== undefined) {
    next.externalPaths = next.external_paths;
  }
  delete next.yield_ms;
  delete next.timeout_sec;
  delete next.escalation_reason;
  delete next.external_paths;
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

function buildExecStartPayload(id, params, client) {
  const command = typeof params.command === 'string' ? params.command : '';
  if (Buffer.byteLength(command, 'utf8') > MAX_EXEC_COMMAND_BYTES) {
    throw coded('invalid_request', 'command exceeds maximum byte length');
  }
  const yieldMs = normalizeOptionalInt(params.yieldMs ?? params.yield_ms, 0, 30000, 'yieldMs');
  const timeoutSec = normalizeOptionalInt(params.timeoutSec ?? params.timeout_sec, 1, 600, 'timeoutSec');
  const escalated = Boolean(params.escalated);
  const rawReason = params.escalationReason ?? params.escalation_reason;
  const escalationReason = typeof rawReason === 'string' ? rawReason.trim() : '';
  if (Buffer.byteLength(escalationReason, 'utf8') > MAX_ESCALATION_REASON_BYTES) {
    throw coded('invalid_request', 'escalationReason exceeds maximum byte length');
  }
  const externalPaths = normalizeExternalPaths(params.externalPaths ?? params.external_paths);
  const workdir = resolveExecWorkdir(params.workdir, client);
  const env = normalizeEnv(params.env);
  const waitBudgetMs = yieldMs === undefined ? 10000 : yieldMs;
  const payload = {
    id,
    command,
    ...(workdir ? {workdir} : {}),
    ...(env ? {env} : {}),
    ...(yieldMs !== undefined ? {yield_ms: yieldMs} : {}),
    ...(timeoutSec !== undefined ? {timeout_sec: timeoutSec} : {}),
    ...(escalated ? {escalated: true} : {}),
    ...(escalationReason ? {escalation_reason: escalationReason} : {}),
    ...(externalPaths?.length ? {external_paths: externalPaths} : {}),
  };
  return {payload, waitBudgetMs};
}

function createExecCommandTool(client, coordinator) {
  return {
    name: 'exec_command',
    label: 'Execute Command',
    description:
      'Execute a shell command or script inside the workspace OS sandbox, or request controlled ordinary-user escalated execution when access outside the workspace or native OS HTTPS/Git/package tools is required. Foreground commands run for up to yieldMs (default 10000 ms; 0 starts immediately in the background) before returning a background session_id; background sessions end automatically on Stop or turn completion. Results include escalated, approval_mode (once/session_allow/rule_allow/turbo_auto/denied/timeout), user_approved, approval_decision, and approval_wait_ms; duration_ms measures only child process runtime after any confirmation wait.',
    display: toolDisplaySpecs.exec_command,
    parameters: {
      type: 'object',
      properties: {
        command: {type: 'string', description: 'Command or script to execute inside the workspace sandbox.'},
        workdir: {
          type: 'string',
          description: 'Optional relative working directory inside the workspace (or an external directory declared in externalPaths when escalated is true).',
        },
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
        escalated: {
          type: 'boolean',
          description: 'Request controlled ordinary-user execution outside the workspace write sandbox or with native Windows HTTPS/Git/package access.',
        },
        escalationReason: {
          type: 'string',
          description: 'Short plain-language reason shown in the user confirmation card (required when escalated is true).',
        },
        externalPaths: {
          type: 'array',
          items: {type: 'string'},
          maxItems: 8,
          description: 'Minimal list of external directories or file paths needed when escalated is true.',
        },
      },
      required: ['command'],
      additionalProperties: false,
    },
    prepareArguments: normalizeExecCommandArgs,
    execute: (toolCallId, params, signal) =>
      coordinator.runTool('exec_command', toolCallId, params, signal, async ctx => {
        const {payload, waitBudgetMs} = buildExecStartPayload(ctx.id, params, client);
        try {
          let result = await ctx.tracker.track(
            client.request('exec/start', payload, waitBudgetMs + 15000, ctx.signal),
          );
          while (result?.status === 'approval_required' && result?.approval_id) {
            if (ctx.signal?.aborted) {
              await client.request('exec/kill', {id: ctx.id, detail: 'cancelled'}, 5000).catch(() => {});
              throw coded('cancelled', 'Operation cancelled');
            }
            result = await ctx.tracker.track(
              client.request(
                'exec/approval_wait',
                {id: ctx.id, approval_id: result.approval_id, wait_ms: 2500},
                Math.max(15000, waitBudgetMs + 15000),
                ctx.signal,
              ),
            );
          }
          const out = {content: [{type: 'text', text: JSON.stringify(result)}]};
          if (result?.status === 'denied_by_user' || result?.status === 'policy_denied' || result?.status === 'approval_timeout') {
            Object.defineProperty(out, 'endDetail', {value: result.status, enumerable: false});
          }
          return out;
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
