import path from 'node:path';
import {AsyncLocalStorage} from 'node:async_hooks';
import {createHash} from 'node:crypto';
import {coded} from './files.mjs';

async function listMemoryFiles(workspace, permitted, runRequest, extraPaths = [], multimodal) {
  if (extraPaths.length || multimodal?.enabled) throw new Error('Extra Memory roots are disabled');
  const files = [];
  for (const rootDoc of ['MEMORY.md', 'USER.md']) {
    const info = await runRequest('stat', {path: rootDoc});
    if (info?.type === 'file') files.push(path.join(workspace, rootDoc));
  }
  let cursor = '';
  try {
    do {
      const page = await runRequest('search', {path: 'memory', query: '.md', cursor});
      for (const item of page.items ?? []) {
        try {
          if (item.type === 'file') files.push(path.join(workspace, permitted(item.path)));
        } catch {}
      }
      cursor = page.next;
    } while (cursor && files.length < 10000);
  } catch (error) {
    if (error?.code !== 'ENOENT') throw error;
  }
  return files;
}

async function readMemoryExcerpt(permitted, text, params) {
  const relative = permitted(params.relPath);
  let content;
  try {
    ({content} = await text(relative));
  } catch (error) {
    if (error.code === 'ENOENT') return {status: 'not_found', path: relative, text: ''};
    throw error;
  }
  const lines = content.split('\n');
  const from = Math.max(1, Math.floor(params.from ?? 1));
  const count = Math.max(1, Math.min(400, Math.floor(params.lines ?? params.defaultLines ?? 100)));
  const selected = lines.slice(from - 1, from - 1 + count).join('\n');
  const result = selected.slice(0, Math.min(params.maxChars ?? 12000, 12000));
  const truncated = from - 1 + count < lines.length || result.length < selected.length;
  return {
    status: 'ok',
    path: relative,
    text: result,
    from,
    lines: result.split('\n').length,
    truncated,
    ...(truncated ? {nextFrom: from + result.split('\n').length} : {}),
  };
}

// Native Memory retains its chunking, FTS index, ranking and tool schemas. This
// adapter supplies bounded filesystem data through the official SDK, scoped to
// the active call context via AsyncLocalStorage.
export function memoryFiles(client, signal) {
  const callStore = new AsyncLocalStorage();
  const permitted = file => {
    const relative = client.relative(file);
    if (!/^(?:MEMORY\.md|USER\.md|memory\/[^\r\n]+\.md)$/i.test(relative)) {
      throw coded('invalid_path', 'Memory path is not authorized');
    }
    return relative;
  };
  const runRequest = (action, payload) => {
    const ctx = callStore.getStore();
    if (!ctx?.id || !ctx?.tracker) {
      return Promise.reject(coded('missing_call_identity', 'Memory access requires an active tool call'));
    }
    return ctx.tracker.track(client.request(action, {...payload, id: ctx.id}, 12000, ctx.signal));
  };
  const text = async file => {
    const relative = permitted(file);
    const data = Buffer.from(await runRequest('read', {path: relative}), 'base64');
    return {content: data.toString('utf8'), canonicalRelativePath: relative};
  };
  const denied = async () => {
    throw new Error('Background Memory writes are disabled');
  };
  return {
    withCall: (ctx, fn) => callStore.run(ctx, fn),
    assertCurrent: () => {
      signal?.throwIfAborted();
      callStore.getStore()?.signal?.throwIfAborted();
    },
    listFiles: (_workspace, extraPaths = [], multimodal) =>
      listMemoryFiles(client.workspace, permitted, runRequest, extraPaths, multimodal),
    async inspectFile(file) {
      const relative = permitted(file);
      const info = await runRequest('stat', {path: relative});
      if (!info || info.type !== 'file') return null;
      const {content} = await text(file);
      return {
        path: relative,
        absPath: path.join(client.workspace, relative),
        mtimeMs: info.mtimeMs,
        size: Buffer.byteLength(content),
        hash: createHash('sha256').update(content).digest('hex'),
        kind: 'markdown',
      };
    },
    readFile: params => readMemoryExcerpt(permitted, text, params),
    readForIndexing: text,
    buildMultimodalChunk: async () => {
      throw new Error('Multimodal Memory is disabled');
    },
    watch: async (_request, _onChange, watchSignal) => {
      const active = AbortSignal.any([signal, watchSignal]);
      active.throwIfAborted();
      await new Promise(resolve => active.addEventListener('abort', resolve, {once: true}));
    },
    maintenance: {
      readFile: async file => Buffer.from((await text(file)).content),
      stat: async file => {
        const info = await runRequest('stat', {path: permitted(file)});
        if (!info) throw Object.assign(new Error('Missing memory file'), {code: 'ENOENT'});
        return {
          isFile: info.type === 'file',
          isDirectory: false,
          isSymbolicLink: false,
          size: info.size,
          mtimeMs: info.mtimeMs,
          mode: 0,
        };
      },
      listDirectory: denied,
      mkdir: denied,
      rename: denied,
      resolveWritePath: denied,
      commitContent: denied,
      resolveDreamsPath: denied,
      readDreams: denied,
      writeDreams: denied,
      replaceReport: denied,
      appendCorpus: denied,
    },
  };
}
