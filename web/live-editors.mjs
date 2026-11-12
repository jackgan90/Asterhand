// The original editor owns its fields, undo history, pending submission and save action.
const failure = code => Object.assign(new Error(code), {code});
async function digest(value) {
  const bytes = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(value));
  return Array.from(new Uint8Array(bytes), byte => byte.toString(16).padStart(2, '0')).join('');
}

export function liveEditors() {
  const entries = new Set(), page = crypto.randomUUID();
  let running = false, generation = 0, nodeID = '', composing = false;
  const compositionStart = () => { composing = true; };
  const compositionEnd = () => { composing = false; };
  function edited(event) {
    for (const entry of entries) {
      if (!entry.adapter.ownsInput?.(event.target)) continue;
      entry.edits++;
      if (event.type === 'input') entry.inputEdits++;
    }
  }
  function listen(enabled) {
    const method = enabled ? 'addEventListener' : 'removeEventListener';
    document[method]('compositionstart', compositionStart, true);
    document[method]('compositionend', compositionEnd, true);
    for (const type of ['input', 'submit']) document[method](type, edited, true);
  }
  function register(adapter) {
    const entry = {adapter, id:null, targetKey:null, edits:0, inputEdits:0};
    entries.add(entry);
    return {
      invalidate() { entry.targetKey = null; },
      dispose() { entries.delete(entry); }
    };
  }
  function inspect(entry) {
    const target = entry.adapter.target();
    if (!target) {
      entry.targetKey = null;
      return null;
    }
    if (!target.node_id) target.node_id = nodeID;
    const key = JSON.stringify([target, entry.adapter.targetVersion?.()]);
    if (entry.targetKey !== key) {
      entry.id = crypto.randomUUID();
      entry.targetKey = key;
    }
    return {draft_id:entry.id, kind:target.kind, target};
  }
  function snapshot(entry) {
    const meta = inspect(entry);
    if (!meta) throw failure('editor_closed');
    if (composing || entry.adapter.busy?.()) throw failure('busy');
    return {...meta, content:entry.adapter.read()};
  }
  async function execute(request) {
    if (request.action === 'list') return [...entries].map(inspect).filter(Boolean);
    const entry = [...entries].find(item => inspect(item)?.draft_id === request.editor);
    if (!entry) throw failure('editor_closed');

    const fingerprint = value => JSON.stringify([entry.edits, entry.adapter.version?.(), value]);
    const before = snapshot(entry), raw = fingerprint(before), revision = await digest(raw);
    if (request.action === 'get') return {...before, revision};
    if (request.expected_revision !== revision || fingerprint(snapshot(entry)) !== raw) throw failure('conflict');
    if (request.action === 'update') {
      entry.adapter.apply(structuredClone(request.content));
      entry.edits++;
      return {revision:await digest(fingerprint(snapshot(entry))), state:'editable'};
    }
    if (request.action !== 'commit') throw failure('invalid_parameters');
    // Start the human editor's pending action before another event can submit the same input.
    entry.edits++;
    const inputEdits = entry.inputEdits;
    const stillCurrent = () => entries.has(entry) && inspect(entry)?.draft_id === before.draft_id && entry.inputEdits === inputEdits;
    const state = await entry.adapter.commit(request.operation_id, before, stillCurrent);
    return {state, revision};
  }
  async function receive(message, send) {
    if (!running || !message.request?.id) return;
    nodeID = message.node_id;
    const id = message.request.id, activeGeneration = generation;
    let reply;
    try { reply = {id, data:await execute(message.request)}; }
    catch (error) { reply = {id, error:error.code || 'result_unknown'}; }
    if (running && generation === activeGeneration) await send({page, reply});
  }
  return {
    register, execute, receive, page,
    attach() {
      if (!running) listen(true);
      running = true;
    },
    stop() {
      if (running) listen(false);
      running = false;
      generation++;
      composing = false;
      for (const entry of entries) entry.targetKey = null;
    },
    get nodeID() { return nodeID; }
  };
}

// Forward only the ordinary fields the editor would commit. The durable child
// operation lets the node confirm a save even when the window closes mid-reply.
export async function editorOperation(post, operation, id, args) {
  const response = await post('/api/automation/human-call', {operation,operation_id:id,arguments:args});
  if (response.error) throw Object.assign(new Error(response.error.message), {code:response.error.code});
  return response.data;
}
