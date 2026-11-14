import {createHash} from 'node:crypto';
import {failure} from './gateway.mjs';

const validID = value => typeof value === 'string' && /^[A-Za-z0-9_-]{16,128}$/.test(value);
const toolCall = block => block?.type === 'toolCall' && typeof block.id === 'string' &&
  typeof block.name === 'string' && /^[A-Za-z0-9_.:-]{1,80}$/.test(block.name);

// A native message can be returned as adjacent commentary and tool rows with
// the same transcript ID. message.get combines those rows in this same order.
export function combineDisplayRows(messages) {
  const combined = [];
  const blocks = row => typeof row.content === 'string' ? [{type: 'text', text: row.content}] : row.content ?? [];
  for (const row of messages) {
    const previous = combined.at(-1);
    if (row.__openclaw?.id && previous?.__openclaw?.id === row.__openclaw.id && previous.role === row.role) {
      const merged = {...row, content: [...blocks(previous), ...blocks(row)],
        __openclaw: {...row.__openclaw, truncated: previous.__openclaw.truncated === true || row.__openclaw.truncated === true}};
      delete merged.openclawStreamFallback;
      combined[combined.length - 1] = merged;
    } else combined.push(row);
  }
  return combined;
}

// Keep the runtime's content order. Hidden reasoning and raw tool arguments are
// deliberately absent from this display projection.
export function contentSections(native) {
  if (typeof native?.content === 'string') return [{type: 'text', index: 0, text: native.content}];
  const sections = [];
  for (const [index, block] of (native?.content ?? []).entries()) {
    if (toolCall(block)) sections.push({type: 'tool', index, call: block});
    else if (block?.type === 'text' && typeof block.text === 'string') {
      const previous = sections.at(-1);
      if (previous?.type === 'text') previous.text += block.text;
      else sections.push({type: 'text', index, text: block.text});
    }
  }
  return sections;
}

export function textSegmentID(nativeID, index) {
  const id = `${nativeID}_text_${index}`;
  if (!validID(id)) throw failure('message_unavailable', 'err.chatbridge.invalid_message', 'Invalid message ID.');
  return id;
}

export function messageSelection(id) {
  const match = /^(.*)_text_(\d+)$/.exec(id);
  if (!match) return {nativeID: id};
  const index = Number(match[2]);
  if (!validID(match[1]) || !Number.isSafeInteger(index)) {
    throw failure('invalid_request', 'err.chatbridge.invalid_message', 'Invalid message ID.');
  }
  return {nativeID: match[1], index};
}

export function selectedMessageText(native, selection) {
  if (!['user', 'assistant'].includes(native?.role)) {
    throw failure('message_unavailable', 'err.chatbridge.message_oversized', 'Message text is unavailable.');
  }
  const sections = contentSections(native);
  if (selection.index === undefined) return sections.filter(section => section.type === 'text').map(section => section.text).join('');
  const section = sections.find(item => item.type === 'text' && item.index === selection.index);
  if (!section || !sections.some(item => item.type === 'tool')) {
    throw failure('message_unavailable', 'err.chatbridge.invalid_message', 'Message text segment is unavailable.');
  }
  return section.text;
}

export function toolResults(history) {
  const results = new Map();
  for (const native of history.messages ?? []) {
    if (!['toolResult', 'tool'].includes(native?.role) || typeof native.toolCallId !== 'string') continue;
    const key = `${native.__openclaw?.runId ?? ''}:${native.toolCallId}`;
    results.set(key, native.isError === true ? 'failed' : 'completed');
  }
  return results;
}

export function toolBlock(section, turn, results) {
  const call = section.call;
  const id = createHash('sha256').update(`${turn}:${call.id}`).digest('hex');
  return {type: 'tool', tool: {id, turn_id: turn, tool: call.name,
    state: results.get(`${turn}:${call.id}`) ?? 'unknown', sequence: 0}};
}
