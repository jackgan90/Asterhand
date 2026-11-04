import test from 'node:test';
import assert from 'node:assert/strict';
import {graphHistory, historyLimits} from '../web/graph-history.mjs';
import {newGraph} from '../web/graph-model.mjs';

const draft = () => ({graph: newGraph(), selection: null});
function move(history, value, x) {
  history.begin(value, 'move'); value.graph.nodes[1].x = x; history.commit(value);
}

test('snapshots own their arrays, IDs and selection across undo and redo', () => {
  const history = graphHistory(), value = draft(), initial = structuredClone(value);
  history.begin(value, 'delete');
  value.graph.nodes.splice(1, 1); value.graph.edges = []; value.selection = {kind:'node', id:value.graph.nodes[0].id};
  history.commit(value);
  const restored = history.undo();
  assert.deepEqual(restored, initial);
  restored.graph.nodes[0].name = 'changed outside history';
  assert.deepEqual(history.redo(), value);
  assert.deepEqual(history.undo(), initial);
});

test('no-op and cancelled edits preserve redo; only effective edits abandon it', () => {
  const history = graphHistory(); let value = draft();
  move(history, value, 400); value = history.undo();
  history.begin(value, 'noop'); value.selection = {kind:'node', id:value.graph.nodes[0].id};
  assert.equal(history.commit(value), false);
  assert.equal(history.state(value.graph).redo, 'move');
  history.begin(value, 'cancel'); value.graph.nodes[1].x = 500; value = history.cancel();
  assert.equal(history.state(value.graph).redo, 'move');
  move(history, value, 600);
  assert.equal(history.redo(), null);
});

test('pending input forms one operation and invalid numeric drafts survive', () => {
  const history = graphHistory(), value = draft();
  history.begin(value, 'input');
  value.graph.parallelism = NaN;
  assert.equal(history.state(value.graph).undo, 'input');
  history.commit(value);
  assert.equal(history.undo().graph.parallelism, 4);
  assert.ok(Number.isNaN(history.redo().graph.parallelism));
  history.begin(value, 'unchanged NaN');
  assert.equal(history.commit(value), false);
});

test('optional server fields and explicit empty draft fields do not create an operation', () => {
  const history = graphHistory(), value = draft();
  delete value.graph.nodes[1].arguments; delete value.graph.nodes[1].directory;
  history.begin(value, 'empty');
  Object.assign(value.graph.nodes[1], {arguments:[], directory:'', timeout_seconds:null});
  assert.equal(history.commit(value), false);
  assert.equal(history.state(value.graph).undo, undefined);
});

test('history keeps at most 100 steps and budget eviction retains a continuous suffix', () => {
  const history = graphHistory(), value = draft();
  for (let index = 0; index < 120; index++) move(history, value, index);
  assert.equal(history.state(value.graph).steps, historyLimits.steps);
  let restored;
  for (let index = 0; index < 100; index++) restored = history.undo();
  assert.equal(restored.graph.nodes[1].x, 19);
  assert.equal(history.undo(), null);
  for (let index = 0; index < 100; index++) restored = history.redo();
  assert.equal(restored.graph.nodes[1].x, 119);
  const bounded = graphHistory({bytes: 6500});
  for (let index = 0; index < 20; index++) move(bounded, value, index);
  assert.ok(bounded.state(value.graph).steps > 0 && bounded.state(value.graph).steps < 20);
  assert.ok(bounded.state(value.graph).bytes <= 6500);
  assert.equal(bounded.undo().graph.nodes[1].x, 18);
});

test('an oversized operation clears unreachable history, reports the limit and allows later edits', () => {
  const history = graphHistory({bytes: 6000}), value = draft();
  move(history, value, 350);
  history.begin(value, 'large'); value.graph.nodes[1].script = 'x'.repeat(6000); history.commit(value);
  assert.equal(history.state(value.graph).steps, 0);
  assert.equal(history.state(value.graph).limited, true);
  history.begin(value, 'shrink'); value.graph.nodes[1].script = ''; history.commit(value);
  move(history, value, 400);
  assert.equal(history.undo().graph.nodes[1].x, 350);
  history.reset();
  assert.equal(history.state(value.graph).limited, false);
  assert.equal(history.redo(), null);
});

test('maximum-size graph edits remain bounded without losing the latest operation', () => {
  const value = draft(), step = value.graph.nodes[1];
  step.arguments = ['x'.repeat(4091), 'y'.repeat(4091)];
  while (value.graph.nodes.length < 64) value.graph.nodes.push({...structuredClone(step), id:crypto.randomUUID()});
  for (let from = 0; from < 64 && value.graph.edges.length < 512; from++) {
    for (let to = from + 1; to < 64 && value.graph.edges.length < 512; to++) value.graph.edges.push({from:value.graph.nodes[from].id, to:value.graph.nodes[to].id});
  }
  const history = graphHistory();
  for (let index = 0; index < 20; index++) move(history, value, index);
  const state = history.state(value.graph);
  assert.ok(state.bytes <= historyLimits.bytes && state.steps > 0 && state.steps < 20);
  assert.equal(history.undo().graph.nodes[1].x, 18);
  assert.equal(history.redo().graph.nodes[1].x, 19);
});
