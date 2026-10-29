export const MAX_MESSAGES = 200;
export const MAX_BODY_BYTES = 2 * 1024 * 1024;
const encoder = new TextEncoder();

export const messageText = message => (message.blocks ?? []).filter(block => block.type === 'text').map(block => block.text).join('');
export const bodyBytes = message => encoder.encode(messageText(message)).length;

export class ChatWindow {
  constructor() { this.clear(); }
  clear() { this.messages = []; this.cursor = ''; this.next = 0; this.hasMore = false; this.reservedBytes = 0; }

  reserve(bytes) { this.reservedBytes = bytes; this.trim(false); }

  apply(page, older = false) {
    if (page.reset) older = false;
    const incoming = page.messages ?? [];
    const combined = older ? [...incoming, ...this.messages] : incoming;
    const seen = new Set();
    this.messages = combined.filter(message => !seen.has(message.id) && seen.add(message.id));
    this.cursor = page.cursor;
    this.next = page.next_offset ?? 0;
    this.hasMore = page.has_more;
    this.trim(older);
  }

  live(message) {
    this.messages = this.messages.filter(item => !item.transient);
    if (message) this.messages.push(message);
    this.trim(false);
  }

  replace(id, text, truncated, start = 0, next = 0) {
    const message = this.messages.find(item => item.id === id);
    if (message) {
      message.blocks = [{type: 'text', text}]; message.truncated = truncated;
      message.fragmentStart = start; message.fragmentNext = next;
    }
    this.trim(false);
  }

  makeRoom(id, requestedBytes) {
    let others = this.messages.filter(message => message.id !== id).reduce((sum, message) => sum + bodyBytes(message), 0);
    while (others > MAX_BODY_BYTES - this.reservedBytes - requestedBytes) {
      const index = this.messages.findIndex(message => message.id !== id);
      if (index < 0) break;
      others -= bodyBytes(this.messages[index]);
      this.messages.splice(index, 1);
    }
    return MAX_BODY_BYTES - this.reservedBytes - others;
  }

  trim(older) {
    let bytes = this.messages.reduce((sum, message) => sum + bodyBytes(message), 0);
    while (this.messages.length > MAX_MESSAGES || bytes > MAX_BODY_BYTES - this.reservedBytes) {
      const removed = older ? this.messages.pop() : this.messages.shift();
      bytes -= bodyBytes(removed);
    }
  }
}

// Keep each operation's confirmed delivery/terminal state monotonic across replies.
export function newerTurn(current, incoming) {
  if (!current) return incoming ?? null;
  if (!incoming) return current;
  if (current.conversation_id !== incoming.conversation_id) return incoming;
  if (current.updated_at && incoming.updated_at && Date.parse(incoming.updated_at) < Date.parse(current.updated_at)) return current;
  const same = current.operation_id === incoming.operation_id;
  if (same && ['accepted', 'rejected'].includes(current.delivery) && !['accepted', 'rejected'].includes(incoming.delivery)) return current;
  if (same && !unsettled(current) && unsettled(incoming)) return current;
  return incoming;
}

export const awaitingReply = turn => Boolean(turn && (turn.delivery === 'pending' ||
  turn.delivery === 'accepted' && ['pending', 'running'].includes(turn.run)));

export function turnStatus(turn, submitting = false) {
  if (submitting) return '正在回复';
  if (!turn) return 'Enter 发送 · Shift+Enter 换行';
  if (turn.delivery === 'unknown') return '送达待确认，正在核对；草稿已保留';
  if (turn.delivery === 'rejected') return '发送未被接受，草稿已保留';
  const states = {completed: '', cancelled: '已停止', failed: '回复失败', interrupted: '运行已中断', unknown: '运行状态待核对'};
  const state = awaitingReply(turn) ? '正在回复' : states[turn.run] ?? '正在核对';
  return [state, turn.persistence === 'unknown' ? '回复保存状态待确认' : ''].filter(Boolean).join(' · ');
}

export const unsettled = turn => Boolean(turn && ['pending', 'running', 'unknown'].includes(turn.run));
