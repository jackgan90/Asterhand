import {createHash} from 'node:crypto';
import {failure} from './gateway.mjs';

export const HISTORY_MAX_CHARS = 1000;
export const FULL_MESSAGE_MAX_CHARS = 8000000;
const encoder = new TextEncoder();
const decoder = new TextDecoder('utf-8', {fatal: true});
export const textOf = message => typeof message?.content === 'string' ? message.content :
  (Array.isArray(message?.content) ? message.content : []).filter(block => block.type === 'text').map(block => block.text ?? '').join('');
// Account for Go's HTML-safe JSON encoding as well as UTF-8 and JSON escapes.
const byteSize = value => encoder.encode(JSON.stringify(value).replace(/[<>&\u2028\u2029]/g,
  character => '\\u' + character.charCodeAt(0).toString(16).padStart(4, '0'))).length;

export function textPrefix(text, maxBytes) {
  const bytes = encoder.encode(text);
  let end = Math.min(maxBytes, bytes.length);
  while (end < bytes.length && end > 0 && (bytes[end] & 0xc0) === 0x80) end--;
  return decoder.decode(bytes.subarray(0, end));
}

// OpenClaw display-cap covers every content field, including hidden reasoning.
// In the locked runtime, truncated visible fields have a suffix beyond maxChars;
// segment commentary instead shares one budget across text blocks.
export function visibleBodyState(native, maxChars) {
  if (!native?.__openclaw?.truncated) return 'complete';
  if (native.__openclaw.reason !== 'display-cap') return 'truncated';
  const texts = typeof native.content === 'string' ? [native.content] :
    (native.content ?? []).filter(block => block?.type === 'text').map(block => block.text ?? '');
  const segment = native.openclawStreamFallback?.source === 'segment';
  let remaining = maxChars;
  for (const text of texts) {
    const limit = segment ? remaining : maxChars;
    if (text.length > limit) return 'truncated';
    if (segment) remaining -= text.length + 1;
  }
  // A segment may omit a whole later block after exhausting the shared budget.
  return segment && remaining <= 0 ? 'ambiguous' : 'complete';
}

export async function visibleHistory(history, readMessage) {
  const messages = [];
  for (const native of history.messages ?? []) {
    let state = visibleBodyState(native, HISTORY_MAX_CHARS);
    if (state === 'ambiguous') {
      try {
        // Segment budgets also consume a separator per text block. Require the
        // probe itself to prove completeness, even when visible text is equal.
        const probeLimit = HISTORY_MAX_CHARS + 2;
        const probe = await readMessage(native.__openclaw.id, probeLimit);
        state = probe && textOf(probe) === textOf(native) && visibleBodyState(probe, probeLimit) === 'complete' ? 'complete' : 'truncated';
      } catch { state = 'truncated'; }
    }
    messages.push({...native, __openclaw: {...native.__openclaw, truncated: state !== 'complete'}});
  }
  return {...history, messages};
}

export function visibleMessage(native, budget = 8192) {
  if (!['user', 'assistant'].includes(native?.role) || !native.__openclaw?.id) return null;
  const original = textOf(native);
  let text = textPrefix(original, Math.max(0, budget - 256));
  const message = {id: native.__openclaw.id, role: native.role, blocks: [{type: 'text', text}],
    truncated: native.__openclaw.truncated === true || text.length < original.length};
  const inputIdentity = native.idempotencyKey ?? native.__openclaw.idempotencyKey;
  const turn = native.__openclaw.runId ?? (typeof inputIdentity === 'string' && inputIdentity.endsWith(':user') ? inputIdentity.slice(0, -5) : '');
  if (turn) message.turn_id = turn;
  if (native.timestamp != null) {
    const date = new Date(native.timestamp);
    if (Number.isFinite(date.getTime())) message.timestamp = date.toISOString();
  }
  while (byteSize(message) > budget && text.length) {
    text = textPrefix(text, Math.max(0, Math.floor(encoder.encode(text).length * 0.75)));
    message.blocks[0].text = text;
    message.truncated = true;
  }
  return message;
}

export function historyPage(history, requestedOffset, requestedCursor) {
  const cursor = createHash('sha256').update(history.sessionId + ':' + history.deltaCursor).digest('hex');
  if (requestedOffset && requestedCursor !== cursor) return {resetRequired: true};
  const native = history.messages ?? [];
  const messages = [];
  let remaining = 54 * 1024;
  for (let index = 0; index < native.length; index++) {
    const budget = Math.min(24 * 1024, Math.floor(remaining / (native.length - index)));
    const message = visibleMessage(native[index], budget);
    if (message) { messages.push(message); remaining -= byteSize(message); }
  }
  return {messages, cursor, next_offset: history.nextOffset ?? 0, has_more: history.hasMore === true,
    reset: history.windowReset === true};
}

export function messageFragment(native, id, offset) {
  return fragmentBytes(messageBytes(native), id, offset);
}

export const messageBytes = native => encoder.encode(textOf(native));

export function fragmentBytes(bytes, id, offset) {
  if (!Number.isInteger(offset) || offset < 0 || offset > bytes.length || offset < bytes.length && (bytes[offset] & 0xc0) === 0x80) {
    throw failure('invalid_request', '消息片段位置无效');
  }
  let end = Math.min(bytes.length, offset + 8192);
  while (end < bytes.length && end > offset && (bytes[end] & 0xc0) === 0x80) end--;
  return {message_id: id, text: decoder.decode(bytes.subarray(offset, end)), offset, next_offset: end, has_more: end < bytes.length};
}

// Stream snapshots replace each other; no token queue grows while the UI is
// hidden. At most 16 recent runs and 8 KiB text per run are retained in RAM.
export class StreamState {
  constructor() { this.runs = new Map(); }

  event(frame) {
    const event = frame.payload;
    if (frame.event !== 'chat' || !event?.runId || !event.sessionKey) return;
    let run = this.runs.get(event.runId);
    if (!run) {
      if (this.runs.size >= 16) this.runs.delete(this.runs.keys().next().value);
      run = {seq: -1, text: '', session: event.sessionKey, truncated: false};
      this.runs.set(event.runId, run);
    }
    if (event.seq <= run.seq) return;
    run.seq = event.seq;
    if (event.state === 'delta') {
      const text = typeof event.deltaText === 'string' ? run.text + event.deltaText : textOf(event.message);
      if (!run.truncated) {
        run.text = textPrefix(text, 8192);
        run.truncated = run.text.length < text.length;
      }
    }
    if (['final', 'error', 'aborted'].includes(event.state)) run.terminal = event.state;
  }

  get(run, session) {
    const value = this.runs.get(run);
    return value?.session === session ? value : null;
  }
}

export function observation(history, runID, startedAt, processStarted, stream) {
  const messages = history.messages ?? [];
  const user = messages.find(message => message.role === 'user' &&
    (message.idempotencyKey === runID + ':user' || message.__openclaw?.idempotencyKey === runID + ':user'));
  const assistant = messages.findLast(message => message.role === 'assistant' && message.__openclaw?.runId === runID);
  const receipt = (history.inputReceipts ?? []).find(item => item.runId === runID || item.idempotencyKey === runID);
  const info = history.sessionInfo ?? {};
  const active = info.hasActiveRun === true || (info.activeRunIds ?? []).includes(runID) || (history.pendingInputs?.total ?? 0) > 0;
  const terminal = stream?.terminal;
  const nativeTerminal = info.lastRunId === runID && ['completed', 'done', 'error', 'failed', 'killed', 'cancelled'].includes(info.status);
  const stopped = !active && info.hasActiveRun === false && (Boolean(terminal) || Boolean(assistant) || nativeTerminal || startedAt < processStarted);
  let run = active ? 'running' : 'unknown';
  if (stopped) {
    if (terminal === 'aborted' || assistant?.stopReason === 'aborted' || info.lastRunId === runID && info.abortedLastRun) run = 'cancelled';
    else if (terminal === 'error' || assistant?.stopReason === 'error') run = 'failed';
    else if (assistant) run = 'completed';
    else run = 'interrupted';
  }
  const live = stream?.text && !assistant ? {
    id: 'live-' + runID, role: 'assistant', turn_id: runID, blocks: [{type: 'text', text: stream.text}],
    transient: true, truncated: stream.truncated,
  } : null;
  return {accepted: Boolean(user || receipt), user_message: user?.__openclaw?.id ?? '',
    assistant_message: assistant?.__openclaw?.id ?? '', stopped, run, saved: Boolean(assistant), live};
}

export {byteSize};
