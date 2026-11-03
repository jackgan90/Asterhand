import {logDecoder} from './log-model.mjs';

const cancelled = () => Object.assign(new Error('cancelled'), {cancelled: true});

// At most one operation per visible pane waits in this queue. Status and stop
// calls use the command client directly. Two reads leave one slot each for
// status and stop inside the native surface's four-request limit.
export function logReadQueue(call, limit = 2) {
  let active = 0;
  const pending = [];
  function pump() {
    for (let index = pending.length - 1; index >= 0; index--) {
      if (!pending[index].current()) pending.splice(index, 1)[0].reject(cancelled());
    }
    while (active < limit && pending.length) {
      const task = pending.shift();
      if (!task.current()) { task.reject(cancelled()); continue; }
      active++;
      Promise.resolve().then(() => call(task.action, task.fields)).then(task.resolve, task.reject)
        .finally(() => { active--; pump(); });
    }
  }
  return (action, fields, current) => new Promise((resolve, reject) => {
    pending.push({action, fields, current, resolve, reject});
    pump();
  });
}

export function logReader({read, commandID, runID, nodeID = '', encoding = 'utf-8', change}) {
  let page = null, follow = true, loading = false, error = null, record = null;
  let disposed = false, visible = true, revision = 0, characters = 0, count = 0, flushed = false;
  let decoder = logDecoder(encoding);
  const notify = update => change({page, follow, loading, error, ...update});
  const terminal = () => record?.phase === 'finished';
  const staleError = () => Object.assign(new Error('run_changed'), {code: 'conflict', messageKey: 'err.localcmd.run_changed'});

  async function findPage(request, current) {
    let action = request.tail ? 'tail_log' : 'read_log', cursor = request.cursor;
    let boundary = null, iterations = 0;
    const backward = Boolean(request.tail || request.before);
    while (current()) {
      const fields = {command_id: commandID, run_id: runID};
      if (nodeID) fields.node_id = nodeID;
      if (action === 'read_log') {
        fields.cursor = cursor;
        if (nodeID && backward) fields.before = true;
      }
      const response = await read(action, fields, current);
      if (!current()) throw cancelled();
      const next = response.log;
      if (next?.run?.run_id !== runID) throw staleError();
      if (!nodeID) return next;
      boundary ??= backward ? {next: next.next, end: next.end} : {start: next.start, previous: next.previous};
      const more = backward ? next.start > 0 : !next.end;
      if (next.chunks.length || !more) return {...next, ...boundary};
      const advance = backward ? next.start : next.next;
      if (action === 'read_log' && advance === cursor) {
        throw Object.assign(new Error('invalid_cursor'), {messageKey: 'err.localcmd.invalid_log_cursor'});
      }
      cursor = advance;
      action = 'read_log';
      // Sparse logs can require many bounded requests. Yield to mode changes,
      // scrolling and status timers even if a test/local bridge resolves fast.
      if (++iterations % 4 === 0) await new Promise(resolve => setTimeout(resolve, 0));
    }
    throw cancelled();
  }

  function accept(next, extend) {
    if (!extend) {
      decoder = logDecoder(encoding);
      characters = 0;
      count = 0;
      flushed = false;
    }
    decoder.seed(next.context?.[encoding] ?? [], extend);
    const chunks = next.chunks.map(chunk => decoder.decode(chunk));
    // A newer status response may overtake this page. Only the page's own
    // snapshot can prove that its tail includes the node's final output.
    const source = nodeID ? next.run?.nodes?.find(node => node.node_id === nodeID) : next.run;
    if (source?.phase === 'finished' && next.end && !flushed) {
      chunks.push(...decoder.finish());
      flushed = true;
    }
    page = extend && page ? {...next, start: page.start, previous: page.previous} : next;
    characters += chunks.reduce((sum, chunk) => sum + chunk.text.length, 0);
    count += chunks.length;
    notify({chunks, reset: !extend});
  }

  async function load(request) {
    const ticket = ++revision;
    const current = () => !disposed && visible && ticket === revision;
    loading = true;
    error = null;
    notify();
    try {
      const next = await findPage(request, current);
      if (current()) accept(next, request.extend);
    } catch (err) {
      if (current() && !err.cancelled) error = err;
    } finally {
      if (current()) { loading = false; notify(); }
    }
  }

  return {
    get state() { return {page, follow, loading, error}; },
    setRecord(value) { record = value; },
    async refresh() {
      if (disposed || !visible || loading || !follow && page) return;
      if (page?.end && (terminal() && flushed || ['waiting', 'ready'].includes(record?.phase))) return;
      if (!page || characters > 128 * 1024 || count > 512) return load({tail: true});
      return load({cursor: page.next, extend: true});
    },
    async navigate(kind) {
      if (disposed || loading) return;
      follow = kind === 'latest';
      if (kind === 'latest') return load({tail: true});
      if (kind === 'first') return load({cursor: 0});
      if (kind === 'previous') return load({cursor: nodeID ? page?.start ?? 0 : page?.previous ?? 0, before: Boolean(nodeID)});
      return load({cursor: page?.next ?? 0});
    },
    pause() { if (follow) { follow = false; notify(); } },
    setVisible(value) {
      visible = value;
      if (!value) { revision++; loading = false; }
    },
    async setEncoding(value) {
      if (disposed || encoding === value) return;
      encoding = value;
      return load(follow ? {tail: true} : {cursor: page?.start ?? 0});
    },
    dispose() { disposed = true; revision++; },
  };
}
