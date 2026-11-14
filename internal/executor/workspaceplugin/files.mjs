import path from 'node:path';

export const allowedTools = Object.freeze(['read', 'write', 'edit', 'apply_patch', 'ls', 'workspace_search',
  'memory_search', 'memory_get', 'progress_card']);

export function fileClient(config, current) {
  const workspace = path.resolve(config.workspace);
  const endpoint = new URL(config.endpoint);
  if (endpoint.protocol !== 'http:' || endpoint.hostname !== '127.0.0.1' || !config.token || !config.policy) {
    throw new Error('Invalid private workspace service');
  }
  const request = async (action, payload = {}) => {
    const response = await fetch(endpoint, {method: 'POST', headers: {
      'Content-Type': 'application/json', Authorization: `Bearer ${config.token}`},
    body: JSON.stringify({action, ...payload}), signal: AbortSignal.timeout(12000)});
    if (!response.ok) throw new Error('Workspace service unavailable');
    const result = await response.json();
    if (result.error) throw Object.assign(new Error(`Workspace: ${result.error}`), {
      code: result.error === 'not_found' ? 'ENOENT' : result.error});
    return result.value;
  };
  const resolvePath = ({filePath, cwd}) => {
    if (typeof filePath !== 'string' || /[\x00-\x1f]/u.test(filePath)) throw new Error('Invalid workspace path');
    const hostPath = path.resolve(cwd ?? workspace, filePath);
    const relativePath = path.relative(workspace, hostPath).replaceAll('\\', '/');
    if (relativePath === '..' || relativePath.startsWith('../') || path.isAbsolute(relativePath)) throw new Error('Path outside workspace');
    // Go rechecks each component and pins real directory/file identities.
    return {hostPath, containerPath: hostPath, relativePath};
  };
  const location = params => resolvePath(params).relativePath;
  const bridge = {
    pathMappings: [{hostRoot: workspace, containerRoot: workspace}], resolvePath,
    readFile: async params => Buffer.from(await request('read', {id: current() ?? '', path: location(params)}), 'base64'),
    readFileWithSource: async params => ({data: await bridge.readFile(params),
      canonicalPath: resolvePath(params).hostPath, workspaceRelativePath: location(params)}),
    stat: params => request('stat', {path: location(params)}),
    readDirectory: async params => (await request('list', {path: location(params)})).map(item => ({
      name: path.basename(item.path), type: item.type})),
    writeFile: params => request('write', {id: current(), path: location(params), data: Buffer.from(params.data).toString('base64')}),
    createFileExclusive: params => request('create', {id: current(), path: location(params), data: Buffer.from(params.data).toString('base64')}),
    remove: params => request('remove', {id: current(), path: location(params)}),
    mkdirp: params => request('mkdir', {id: current(), path: location(params)}),
  };
  return {workspace, request, bridge, relative: filePath => location({filePath})};
}

export function mutationScope(tool, params, relative) {
  const targets = [];
  let newBytes = 0;
  if (tool === 'write') {
    targets.push({path:relative(params.path ?? params.file_path)});
    newBytes = Buffer.byteLength(params.content ?? '');
  } else if (tool === 'edit') {
    targets.push({path:relative(params.path ?? params.file_path)});
    // The service adds the pinned old size to this expansion bound.
    newBytes = Buffer.byteLength(JSON.stringify(params));
  } else if (tool === 'apply_patch') {
    if (typeof params.input !== 'string' || Buffer.byteLength(params.input) > 2 * 1024 * 1024) throw new Error('Patch limit');
    const lines = params.input.replaceAll('\r\n', '\n').trim().split('\n');
    if (lines[0] !== '*** Begin Patch' || lines.at(-1) !== '*** End Patch') throw new Error('Invalid patch envelope');
    for (const line of lines) {
      const match = /^\*\*\* (Add File|Update File|Delete File|Move to): (.+)$/.exec(line);
      if (match) {
        if (match[1] === 'Move to' && targets.length) targets.at(-1).remove = true;
        targets.push({path:relative(match[2]),...(match[1] === 'Delete File'?{remove:true}:{})});
      }
    }
    if (!targets.length) throw new Error('Patch has no targets');
    newBytes = Buffer.byteLength(params.input);
  }
  if (targets.length > 32 || new Set(targets.map(value => value.path.toLowerCase())).size !== targets.length) throw new Error('Invalid target set');
  const progress = tool === 'progress_card' ? (params.plan ?? []).map(item => ({step: item.step, status: item.status})) : [];
  return {targets, new_bytes: newBytes, progress};
}
