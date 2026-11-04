// Editing history contains draft data, never DOM, execution state or persisted
// revisions. A transaction owns one before snapshot until committed/cancelled.
import {graphContent} from './graph-model.mjs';
export const historyLimits = {steps: 100, bytes: 16 * 1024 * 1024};

export function sameValue(a, b) {
  if (Object.is(a, b)) return true;
  if (!a || !b || typeof a !== 'object' || typeof b !== 'object') return false;
  if (Array.isArray(a) !== Array.isArray(b)) return false;
  const keys = Object.keys(a), other = Object.keys(b);
  return keys.length === other.length && keys.every(key => Object.hasOwn(b, key) && sameValue(a[key], b[key]));
}

// UTF-16 payload estimate plus per-entry bookkeeping; not a heap measurement.
const snapshotBytes = value => JSON.stringify(value).length * 2 + 128;
const sameGraph = (a, b) => sameValue(graphContent(a), graphContent(b));

export function graphHistory({steps = historyLimits.steps, bytes = historyLimits.bytes} = {}) {
  let entries = [], cursor = 0, used = 0, pending = null, limited = false;
  const reset = () => { entries = []; cursor = 0; used = 0; pending = null; limited = false; };
  function begin(value, label) {
    if (pending) throw new Error('History transaction already active');
    pending = {before: structuredClone(value), label};
  }
  function commit(value) {
    if (!pending) return false;
    const transaction = pending; pending = null;
    if (sameGraph(transaction.before.graph, value.graph)) return false;
    const after = structuredClone(value);
    const size = snapshotBytes(transaction.before) + snapshotBytes(after);
    // Discard the abandoned future only after an actual change is confirmed.
    entries.splice(cursor);
    used = entries.reduce((total, entry) => total + entry.size, 0);
    if (size > bytes) { reset(); limited = true; return true; }
    entries.push({...transaction, after, size}); used += size;
    while (entries.length > steps || used > bytes) { used -= entries.shift().size; limited = true; }
    cursor = entries.length;
    return true;
  }
  function cancel() {
    const before = pending?.before; pending = null;
    return before ? structuredClone(before) : null;
  }
  function state(graph) {
    const editing = pending && !sameGraph(pending.before.graph, graph);
    return {undo: editing ? pending.label : entries[cursor - 1]?.label,
      redo: editing ? null : entries[cursor]?.label, limited, steps: entries.length, bytes: used};
  }
  return {
    begin, commit, cancel, reset, state,
    undo() { if (pending) throw new Error('Finish editing before undo'); return cursor ? structuredClone(entries[--cursor].before) : null; },
    redo() { if (pending) throw new Error('Finish editing before redo'); return cursor < entries.length ? structuredClone(entries[cursor++].after) : null; },
  };
}
