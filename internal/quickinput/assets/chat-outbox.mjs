import {bodyBytes, newerTurn} from './chat-model.mjs';

// Presentation snapshots only. Native history remains the durable authority.
// Presentation keeps a bounded set of queued, in-flight and recent snapshots.
export class ChatOutbox {
  constructor() { this.entries = new Map(); }

  begin(operation, conversation, text) {
    while (this.entries.size >= 16) {
      const settled = [...this.entries].find(([,item]) => ['accepted','rejected'].includes(item.delivery));
      if (!settled) return false;
      this.entries.delete(settled[0]);
    }
    this.entries.set(operation, {operation, conversation, text, timestamp: new Date().toISOString(),
      delivery: 'pending', run: 'pending'});
    return true;
  }

  bind(operation, conversation) {
    const entry = this.entries.get(operation);
    if (entry) entry.conversation = conversation;
  }

  observe(turn) {
    if (!turn?.operation_id || !turn.conversation_id) return;
    if (!this.entries.has(turn.operation_id)) this.begin(turn.operation_id, turn.conversation_id, '');
    const entry = this.entries.get(turn.operation_id);
    if (!entry || entry.conversation !== turn.conversation_id) return;
    const current = {operation_id: entry.operation, conversation_id: entry.conversation,
      delivery: entry.delivery, run: entry.run, updated_at: entry.updatedAt};
    if (newerTurn(current, turn) !== turn) return;
    Object.assign(entry, {turn: turn.id || entry.turn, message: turn.user_message_id || entry.message,
      delivery: turn.delivery, run: turn.run, updatedAt: turn.updated_at || entry.updatedAt});
  }

  fail(operation, dispatched, error) {
    const entry = this.entries.get(operation);
    if (!entry || entry.delivery === 'accepted' || entry.delivery === 'rejected') return entry?.delivery;
    // An attempted write with no authoritative turn remains uncertain.
    entry.delivery = dispatched && !error.notDispatched && (!error.response || error.response.turn) ? 'unknown' : 'rejected';
    return entry.delivery;
  }

  dispatched(operation) {
    const entry = this.entries.get(operation);
    if (entry) entry.dispatched = true;
  }

  queued(item) {
    if (!this.entries.has(item.operation_id)) {
      this.begin(item.operation_id, item.conversation_id, item.text ?? '');
      const entry = this.entries.get(item.operation_id);
      if (entry && item.turn?.started_at) entry.timestamp = item.turn.started_at;
    }
    const entry = this.entries.get(item.operation_id);
    if (!entry || entry.conversation !== item.conversation_id) return;
    if (item.text !== undefined) entry.text = item.text;
    this.observe(item.turn);
    entry.queueState = item.state === 'settled' ? '' : item.state;
    entry.queued = ['queued','preparing'].includes(item.state);
    entry.dispatched = ['sending','unknown'].includes(item.state);
  }

  pending(conversation) {
    const entry = [...this.entries.values()].reverse().find(item => item.conversation === conversation && item.dispatched &&
      ['pending', 'unknown'].includes(item.delivery) && ['pending', 'running', 'unknown'].includes(item.run));
    return entry ? {operation_id: entry.operation, conversation_id: conversation} : null;
  }

  reconcile(messages, conversation) {
    for (const entry of this.entries.values()) {
      if (entry.conversation !== conversation) continue;
      if (messages.some(message => this.matches(entry, message))) entry.text = '';
    }
  }

  matches(entry, message) {
    return message.role === 'user' && Boolean(entry.message && entry.message === message.id || entry.turn && entry.turn === message.turn_id);
  }

  get reservedBytes() {
    return [...this.entries.values()].reduce((sum, entry) => sum + bodyBytes({blocks: [{type: 'text', text: entry.text}]}), 0);
  }

  merge(messages, conversation) {
    const entries = [...this.entries.values()].filter(entry => entry.conversation === conversation)
      .sort((a,b) => Date.parse(a.timestamp) - Date.parse(b.timestamp));
    const decorate = (message, entry) => ({...message, ui_id: 'pending-' + entry.operation, delivery_status: status(entry)});
    const result = messages.map(message => {
      const entry = entries.find(item => this.matches(item, message));
      return entry ? decorate(message, entry) : message;
    });
    const previews = entries.filter(entry => entry.text && !messages.some(message => this.matches(entry, message)))
      .map(entry => decorate({id: 'pending-' + entry.operation, role: 'user', timestamp: entry.timestamp,
        blocks: [{type: 'text', text: entry.text}]}, entry));
    const live = result.findIndex(message => message.transient);
    result.splice(live < 0 ? result.length : live, 0, ...previews);
    // Display rows share the existing 200-row limit, including local previews.
    return result.slice(-200);
  }
}

function status(entry) {
  if (entry.delivery === 'unknown') return ['interrupted','cancelled','failed'].includes(entry.run) ? '消息送达仍未确认，运行已停止；请勿重复发送' : '送达待确认，正在核对';
  if (entry.delivery === 'accepted' && ['failed', 'interrupted'].includes(entry.run)) return '消息已送达，回复失败';
  if (entry.delivery === 'rejected' || entry.queueState === 'failed') return '消息未送达';
  return '';
}
