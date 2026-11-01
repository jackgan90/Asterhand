// Queue state lives in the node. This client restores only bounded presentation
// snapshots, one body per IPC response, and never replays a submission.
export function operationQueue(client, outbox) {
  let state = null, refreshing = null;
  const changedHistory = new Set();
  const items = () => [...state?.entries ?? [], ...state?.failures ?? []];
  function observe(next) {
    if (!next || state && next.revision < state.revision) return;
    if (state?.active && state.active.operation_id !== next.active?.operation_id) changedHistory.add(state.active.conversation_id);
    state = next;
    const present = new Set(items().map(item => item.operation_id));
    for (const entry of outbox.entries.values()) {
      if (entry.queueState && !present.has(entry.operation)) {
        entry.needsReceipt = true;
        entry.queued = false;
        entry.queueState = '';
      }
    }
    for (const item of items()) if (item.kind === 'send') {
      outbox.queued(item);
    }
    if (state.active) outbox.observe(state.active);
  }
  async function refresh() {
    if (refreshing) return refreshing;
    refreshing = (async () => {
      const result = await client.call('queue');
      observe(result.queue);
      for (const item of items()) {
        if (item.kind !== 'send' || outbox.entries.get(item.operation_id)?.text) continue;
        try {
          const reply = await client.call('queue_item', {conversation_id:item.conversation_id, operation_id:item.operation_id});
          if (items().some(entry => entry.operation_id === item.operation_id)) outbox.queued(reply.queue_item);
        } catch (error) { if (error.code !== 'not_found') throw error; }
      }
      for (const entry of [...outbox.entries.values()]) {
        if (!entry.needsReceipt && entry.delivery !== 'unknown') continue;
        try {
          const reply = await client.call('queue_item', {conversation_id:entry.conversation, operation_id:entry.operation});
          outbox.queued(reply.queue_item);
          if (reply.queue_item.state === 'settled') changedHistory.add(entry.conversation);
          entry.needsReceipt = false;
        } catch (error) { if (error.code !== 'not_found') throw error; }
      }
      return state;
    })().finally(() => { refreshing = null; });
    return refreshing;
  }
  return {observe, refresh,
    consumeHistoryChange(id) { return changedHistory.delete(id); },
    get enabled() { return state !== null; },
    get state() { return state; },
    get count() { return state?.entries.filter(item => item.kind === 'send').length ?? 0; },
    get waiting() { return Boolean(state?.entries.length || state?.active); },
    awaitingReply(id) { return state?.entries.some(item => item.kind === 'send' && item.conversation_id === id && ['queued','preparing','sending'].includes(item.state)) ?? false; },
    failure(id) { return items().filter(item => item.state === 'failed' && item.conversation_id === id)
      .sort((a,b) => Date.parse(b.turn?.started_at ?? 0) - Date.parse(a.turn?.started_at ?? 0))[0]; },
    creating(id) { return state?.entries.some(item => item.kind === 'create' && item.conversation_id === id) ?? false; },
  };
}
