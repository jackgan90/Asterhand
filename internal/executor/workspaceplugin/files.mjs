import path from 'node:path';

export const allowedTools = Object.freeze([
  'read_file',
  'write_file',
  'edit_file',
  'apply_file_patch',
  'list_dir',
  'search_files',
  'memory_search',
  'memory_get',
  'progress_card',
]);

export function coded(code, message) {
  return Object.assign(new Error(message), {code});
}

export function errorCode(error, fallback = 'invalid_request') {
  const code = error?.code;
  if (code === 'ENOENT') return 'not_found';
  if (typeof code === 'string' && /^[a-z0-9_]+$/.test(code)) return code;
  return fallback;
}

export function createTracker() {
  const pending = new Set();
  let closed = false;
  return {
    track(promise) {
      if (closed) throw coded('call_closed', 'Workspace call is already closing');
      const tracked = Promise.resolve(promise).finally(() => pending.delete(tracked));
      pending.add(tracked);
      return tracked;
    },
    close() {
      closed = true;
    },
    async drain() {
      while (pending.size > 0) {
        await Promise.allSettled([...pending]);
      }
    },
  };
}

export function fileClient(config) {
  const workspace = path.resolve(config.workspace);
  const endpoint = new URL(config.endpoint);
  if (endpoint.protocol !== 'http:' || endpoint.hostname !== '127.0.0.1' || !config.token || !config.policy) {
    throw new Error('Invalid private workspace service');
  }
  const request = async (action, payload = {}, timeoutMs = 12000) => {
    const response = await fetch(endpoint, {
      method: 'POST',
      headers: {'Content-Type': 'application/json', Authorization: `Bearer ${config.token}`},
      body: JSON.stringify({action, ...payload}),
      signal: AbortSignal.timeout(timeoutMs),
    });
    if (!response.ok) throw coded('service_unavailable', 'Workspace service unavailable');
    const result = await response.json();
    if (result.error) {
      throw Object.assign(new Error(`Workspace: ${result.error}`), {
        code: result.error === 'not_found' ? 'ENOENT' : result.error,
      });
    }
    return result.value;
  };
  const resolvePath = ({filePath, cwd}) => {
    if (typeof filePath !== 'string' || /[\x00-\x1f]/u.test(filePath)) {
      throw coded('invalid_path', 'Invalid workspace path');
    }
    const hostPath = path.resolve(cwd ?? workspace, filePath);
    const relativePath = path.relative(workspace, hostPath).replaceAll('\\', '/');
    if (relativePath === '..' || relativePath.startsWith('../') || path.isAbsolute(relativePath)) {
      throw coded('outside_workspace', 'Path outside workspace');
    }
    // Go rechecks each component and pins real directory/file identities.
    return {hostPath, containerPath: hostPath, relativePath};
  };
  const location = params => resolvePath(params).relativePath;
  const deniedWrite = async () => {
    throw coded('missing_call_identity', 'Unscoped workspace write is disabled');
  };
  const toDirectoryEntry = item => ({
    name: path.basename(item.path),
    type: item.type,
    isDirectory: item.type === 'directory',
    isFile: item.type === 'file',
    size: item.size,
    mtimeMs: item.mtimeMs,
  });
  const bootstrapBridge = {
    pathMappings: [{hostRoot: workspace, containerRoot: workspace}],
    resolvePath,
    readFile: async params => Buffer.from(await request('read', {id: '', bootstrap: true, path: location(params)}), 'base64'),
    readFileWithSource: async params => ({
      data: await bootstrapBridge.readFile(params),
      canonicalPath: resolvePath(params).hostPath,
      workspaceRelativePath: location(params),
    }),
    stat: params => request('stat', {id: '', bootstrap: true, path: location(params)}),
    readDirectory: async params => (await request('list', {id: '', bootstrap: true, path: location(params)})).map(toDirectoryEntry),
    writeFile: deniedWrite,
    createFileExclusive: deniedWrite,
    remove: deniedWrite,
    mkdirp: deniedWrite,
  };
  const createBridge = (callId, tracker) => {
    if (!callId || !tracker) throw coded('missing_call_identity', 'Missing call identity');
    const readFile = params => tracker.track(
      request('read', {id: callId, path: location(params)}).then(b64 => Buffer.from(b64, 'base64')),
    );
    return {
      pathMappings: [{hostRoot: workspace, containerRoot: workspace}],
      resolvePath,
      readFile,
      readFileWithSource: async params => ({
        data: await readFile(params),
        canonicalPath: resolvePath(params).hostPath,
        workspaceRelativePath: location(params),
      }),
      stat: params => tracker.track(request('stat', {id: callId, path: location(params)})),
      readDirectory: params => tracker.track(
        request('list', {id: callId, path: location(params)}).then(items => items.map(toDirectoryEntry)),
      ),
      writeFile: params => tracker.track(
        request('write', {id: callId, path: location(params), data: Buffer.from(params.data).toString('base64')}),
      ),
      createFileExclusive: params => tracker.track(
        request('create', {id: callId, path: location(params), data: Buffer.from(params.data).toString('base64')}),
      ),
      remove: params => tracker.track(request('remove', {id: callId, path: location(params)})),
      mkdirp: params => tracker.track(request('mkdir', {id: callId, path: location(params)})),
    };
  };
  return {workspace, request, bootstrapBridge, createBridge, relative: filePath => location({filePath})};
}

export function callScope(tool, params, relative) {
  const raw = params && typeof params === 'object' ? params : {};
  if (tool === 'read_file') {
    const file = relative(raw.path ?? raw.file_path);
    if (!file) throw coded('invalid_path', 'Missing file path');
    return {path: file, targets: [], new_bytes: 0, progress: []};
  }
  if (tool === 'list_dir') {
    return {path: relative(raw.path ?? raw.file_path ?? '.'), targets: [], new_bytes: 0, progress: []};
  }
  if (tool === 'search_files') {
    if (typeof raw.query !== 'string' || !raw.query) throw coded('invalid_request', 'Missing search query');
    return {path: relative(raw.path ?? '.'), targets: [], new_bytes: 0, progress: []};
  }
  if (tool === 'memory_get') {
    const file = relative(raw.path);
    if (!/^(?:MEMORY\.md|USER\.md|memory\/[^\r\n]+\.md)$/i.test(file)) {
      throw coded('invalid_path', 'Memory path is not authorized');
    }
    return {path: file, targets: [], new_bytes: 0, progress: []};
  }
  if (tool === 'memory_search') {
    if (typeof raw.query !== 'string' || !raw.query.trim()) throw coded('invalid_request', 'Missing memory query');
    return {path: '', targets: [], new_bytes: 0, progress: []};
  }
  if (tool === 'progress_card') {
    const progress = Array.isArray(raw.plan)
      ? raw.plan.map(item => ({step: String(item?.step ?? ''), status: String(item?.status ?? '')}))
      : [];
    return {path: '', targets: [], new_bytes: 0, progress};
  }
  const targets = [];
  let newBytes = 0;
  if (tool === 'write_file') {
    const file = relative(raw.path ?? raw.file_path);
    if (!file) throw coded('invalid_path', 'Missing file path');
    targets.push({path: file});
    newBytes = Buffer.byteLength(String(raw.content ?? ''));
  } else if (tool === 'edit_file') {
    const file = relative(raw.path ?? raw.file_path);
    if (!file) throw coded('invalid_path', 'Missing file path');
    targets.push({path: file});
    newBytes = Buffer.byteLength(JSON.stringify(raw));
  } else if (tool === 'apply_file_patch') {
    if (typeof raw.input !== 'string' || Buffer.byteLength(raw.input) > 2 * 1024 * 1024) {
      throw coded('batch_limit', 'Patch limit');
    }
    const lines = raw.input.replaceAll('\r\n', '\n').trim().split('\n');
    if (lines[0] !== '*** Begin Patch' || lines.at(-1) !== '*** End Patch') {
      throw coded('invalid_patch', 'Invalid patch envelope');
    }
    for (const line of lines) {
      const match = /^\*\*\* (Add File|Update File|Delete File|Move to): (.+)$/.exec(line);
      if (match) {
        if (match[1] === 'Move to' && targets.length) targets.at(-1).remove = true;
        const target = relative(match[2]);
        if (!target) throw coded('invalid_path', 'Invalid patch target');
        targets.push({path: target, ...(match[1] === 'Delete File' ? {remove: true} : {})});
      }
    }
    if (!targets.length) throw coded('invalid_patch', 'Patch has no targets');
    newBytes = Buffer.byteLength(raw.input);
  } else {
    throw coded('unsupported_tool', `Unsupported workspace tool: ${tool}`);
  }
  if (targets.length > 32 || new Set(targets.map(value => value.path.toLowerCase())).size !== targets.length) {
    throw coded('duplicate_target', 'Invalid target set');
  }
  return {path: '', targets, new_bytes: newBytes, progress: []};
}
