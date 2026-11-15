import {createHash} from 'node:crypto';
import {allowedTools, callScope, coded, createTracker, errorCode} from './files.mjs';

function abortError(signal) {
  const reason = signal?.reason;
  if (reason instanceof Error) return Object.assign(reason, {code: 'cancelled'});
  return coded('cancelled', typeof reason === 'string' && reason ? reason : 'Workspace tool call cancelled');
}

function prepareRecordSealItem(client, record, toolName, params) {
  try {
    const scope = callScope(toolName, params, client.relative);
    const execFp = createHash('sha256')
      .update(JSON.stringify([toolName, params, scope.path, scope.targets, scope.new_bytes, scope.progress]))
      .digest('hex');
    record.executionFingerprint = execFp;
    record.sealItem = {
      id: record.id,
      tool: toolName,
      fingerprint: execFp,
      path: scope.path,
      targets: scope.targets,
      new_bytes: scope.new_bytes,
      progress: scope.progress,
    };
  } catch (err) {
    record.localError = err;
    record.sealItem = {id: record.id, tool: toolName, aborted: true, reason: errorCode(err)};
  }
}

async function waitForGrantOrReplay(client, record, signal) {
  for (;;) {
    if (signal?.aborted) throw abortError(signal);
    const acq = await client.request('acquire', {
      id: record.id,
      run: record.run,
      conversation: record.conversation,
      fingerprint: record.executionFingerprint,
      wait_ms: 3500,
    }, 10000);
    if (acq.granted) return null;
    if (acq.terminal) {
      const detail = acq.operation?.detail || acq.operation?.state || 'failed';
      if (acq.operation?.state === 'completed') {
        return {content: [{type: 'text', text: JSON.stringify({replayed: true})}]};
      }
      if (detail === 'conflicting_batch_write') {
        throw coded(
          detail,
          'Workspace conflicting_batch_write: write_file cannot overwrite a path already modified earlier in the same tool batch; combine the edits or read and write in the next turn.',
        );
      }
      throw coded(detail, `Workspace: ${detail}`);
    }
  }
}

async function executeGrantedCall(client, record, signal, executeFn) {
  const tracker = createTracker();
  const bridge = client.createBridge(record.id, tracker);
  let endState = 'completed';
  let endDetail = '';
  try {
    const result = await executeFn({
      id: record.id,
      run: record.run,
      conversation: record.conversation,
      sessionKey: record.sessionKey,
      tracker,
      bridge,
      signal,
    });
    if (signal?.aborted) {
      endState = 'cancelled';
      endDetail = 'cancelled';
      throw abortError(signal);
    }
    if (result?.isError) {
      endState = 'failed';
      endDetail = 'native_tool_failed';
    }
    return result;
  } catch (err) {
    if (signal?.aborted || err?.code === 'cancelled' || err?.name === 'AbortError') {
      endState = 'cancelled';
      endDetail = 'cancelled';
    } else {
      endState = 'failed';
      endDetail = errorCode(err, 'native_tool_failed');
    }
    throw err;
  } finally {
    tracker.close();
    await tracker.drain();
    await client.request('end', {id: record.id, state: endState, detail: endDetail});
  }
}

function createBatchQueue(client) {
  const openBatches = new Map();
  let nextBatchSeq = 0;
  const pendingByNative = new Map();
  const recordsByKey = new Map();

  const enqueueNative = (nativeID, record) => {
    const queue = pendingByNative.get(nativeID) ?? [];
    queue.push(record);
    pendingByNative.set(nativeID, queue);
  };
  const dequeueNative = (nativeID, toolName) => {
    const queue = pendingByNative.get(nativeID);
    if (!queue?.length) return null;
    const index = queue.findIndex(item => item.tool === toolName && !item.enteredExecute);
    if (index < 0) return null;
    const [record] = queue.splice(index, 1);
    if (!queue.length) pendingByNative.delete(nativeID);
    return record;
  };
  const sealBatch = batchState => {
    if (batchState.sealPromise) return batchState.sealPromise;
    if (openBatches.get(batchState.run) === batchState) openBatches.delete(batchState.run);
    const items = batchState.records.map(r => r.sealItem ?? {id: r.id, tool: r.tool, aborted: true, reason: 'cancelled'});
    batchState.sealPromise = client.request('seal', {
      batch: batchState.id,
      run: batchState.run,
      conversation: batchState.conversation,
      items,
    });
    return batchState.sealPromise;
  };
  const maybeSeal = batchState => {
    if (batchState.sealPromise) return batchState.sealPromise;
    if (batchState.records.length > 0 && batchState.records.every(item => item.arrived)) return sealBatch(batchState);
    if (batchState.executionStarted && !batchState.fallbackScheduled) {
      batchState.fallbackScheduled = true;
      setImmediate(() => {
        if (!batchState.sealPromise) void sealBatch(batchState).catch(() => {});
      });
    }
    return null;
  };
  const ensureBatch = (run, conversation) => {
    let batchState = openBatches.get(run);
    if (!batchState) {
      const next = ++nextBatchSeq;
      batchState = {
        id: createHash('sha256').update(`${run}:batch:${next}`).digest('hex'),
        run,
        conversation,
        records: [],
        sealPromise: null,
        executionStarted: false,
        fallbackScheduled: false,
      };
      openBatches.set(run, batchState);
    }
    return batchState;
  };
  return {openBatches, recordsByKey, enqueueNative, dequeueNative, sealBatch, maybeSeal, ensureBatch};
}

export function createBatchCoordinator(client) {
  const q = createBatchQueue(client);

  async function beforeToolCall(event, context) {
    const run = context.runId ?? event.runId;
    const nativeID = context.toolCallId ?? event.toolCallId;
    if (!allowedTools.includes(event.toolName) || !run || !nativeID || !context.sessionKey?.startsWith('agent:main:asterhand:')) {
      return {block: true, blockReason: 'Workspace tool is not authorized'};
    }
    if (context.abortSignal?.aborted) return {block: true, blockReason: 'Workspace check failed: cancelled'};
    const conversation = context.sessionKey.slice('agent:main:asterhand:'.length);
    const batchState = q.ensureBatch(run, conversation);
    const id = createHash('sha256').update(`${run}:${nativeID}`).digest('hex');
    const inputFingerprint = createHash('sha256').update(JSON.stringify([event.toolName, event.params])).digest('hex');
    const record = {
      id, run, conversation, sessionKey: context.sessionKey, nativeID, tool: event.toolName,
      batch: batchState, order: batchState.records.length, inputFingerprint, executionFingerprint: '',
      enteredExecute: false, arrived: false, sealItem: null, localError: null,
    };
    batchState.records.push(record);
    q.recordsByKey.set(`${run}:${nativeID}`, record);
    q.enqueueNative(nativeID, record);
    try {
      await client.request('register', {
        id, batch: batchState.id, order: record.order, run, conversation, tool: event.toolName, input_fingerprint: inputFingerprint,
      });
      return undefined;
    } catch (error) {
      const idx = batchState.records.indexOf(record);
      if (idx >= 0) batchState.records.splice(idx, 1);
      q.recordsByKey.delete(`${run}:${nativeID}`);
      q.dequeueNative(nativeID, event.toolName);
      if (!batchState.records.length && q.openBatches.get(run) === batchState) q.openBatches.delete(run);
      return {block: true, blockReason: `Workspace check failed: ${errorCode(error)}`};
    }
  }

  function afterToolCall(event, context) {
    const run = context.runId ?? event.runId;
    const nativeID = context.toolCallId ?? event.toolCallId;
    if (!run || !nativeID) return;
    const record = q.recordsByKey.get(`${run}:${nativeID}`);
    q.recordsByKey.delete(`${run}:${nativeID}`);
    if (!record || record.enteredExecute) return;
    q.dequeueNative(nativeID, record.tool);
    record.arrived = true;
    record.sealItem = {id: record.id, tool: record.tool, aborted: true, reason: event.error ? 'hook_blocked' : 'cancelled'};
    void q.maybeSeal(record.batch)?.catch(() => {});
  }

  async function runTool(toolName, nativeID, params, signal, executeFn) {
    const record = q.dequeueNative(nativeID, toolName);
    if (!record) throw coded('not_authorized', 'Missing authorized workspace tool registration');
    q.recordsByKey.delete(`${record.run}:${record.nativeID}`);
    record.enteredExecute = true;
    record.arrived = true;
    record.batch.executionStarted = true;
    if (q.openBatches.get(record.run) === record.batch) q.openBatches.delete(record.run);
    prepareRecordSealItem(client, record, toolName, params);
    q.maybeSeal(record.batch);
    if (!record.batch.sealPromise) {
      await new Promise(resolve => setImmediate(resolve));
      await q.sealBatch(record.batch);
    } else {
      await record.batch.sealPromise;
    }
    if (record.localError) throw record.localError;
    if (signal?.aborted) {
      await client.request('cancel', {id: record.id, detail: 'cancelled'}).catch(() => {});
      throw abortError(signal);
    }
    const onAbort = () => void client.request('cancel', {id: record.id, detail: 'cancelled'}).catch(() => {});
    signal?.addEventListener('abort', onAbort, {once: true});
    try {
      const replayed = await waitForGrantOrReplay(client, record, signal);
      if (replayed) return replayed;
      return await executeGrantedCall(client, record, signal, executeFn);
    } finally {
      signal?.removeEventListener('abort', onAbort);
    }
  }

  return {beforeToolCall, afterToolCall, runTool};
}
