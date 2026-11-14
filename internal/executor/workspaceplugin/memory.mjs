import path from 'node:path';
import {createHash} from 'node:crypto';

// Native Memory retains its chunking, FTS index, ranking and tool schemas. This
// adapter supplies only bounded filesystem data through the official SDK.
export function memoryFiles(client, signal) {
  const watchers = new Set();
  const permitted = file => {
    const relative = client.relative(file);
    if (!/^(?:MEMORY\.md|USER\.md|memory\/[^\r\n]+\.md)$/i.test(relative)) throw new Error('Memory path is not authorized');
    return relative;
  };
  const text = async file => {
    const relative = permitted(file);
    const data = Buffer.from(await client.request('read', {path: relative}), 'base64');
    return {content: data.toString('utf8'), canonicalRelativePath: relative};
  };
  const denied = async () => { throw new Error('Background Memory writes are disabled'); };
  return {
    changed: () => { for (const notify of watchers) notify('change'); },
    assertCurrent: () => signal.throwIfAborted(),
    async listFiles(_workspace, extraPaths = [], multimodal) {
      if (extraPaths.length || multimodal?.enabled) throw new Error('Extra Memory roots are disabled');
      let cursor = '';
      const files = [];
      do {
        const page = await client.request('search', {path: '', query: '.md', cursor});
        for (const item of page.items) {
          try { if (item.type === 'file') files.push(path.join(client.workspace, permitted(item.path))); } catch {}
        }
        cursor = page.next;
      } while (cursor && files.length < 10000);
      return files;
    },
    async inspectFile(file) {
      const relative = permitted(file);
      const info = await client.request('stat', {path: relative});
      if (!info || info.type !== 'file') return null;
      const {content} = await text(file);
      return {path: relative, absPath: path.join(client.workspace, relative), mtimeMs: info.mtimeMs,
        size: Buffer.byteLength(content), hash: createHash('sha256').update(content).digest('hex'), kind: 'markdown'};
    },
    async readFile(params) {
      const relative = permitted(params.relPath);
      let content;
      try { ({content} = await text(relative)); } catch (error) {
        if (error.code === 'ENOENT') return {status: 'not_found', path: relative, text: ''};
        throw error;
      }
      const lines = content.split('\n');
      const from = Math.max(1, Math.floor(params.from ?? 1));
      const count = Math.max(1, Math.min(400, Math.floor(params.lines ?? params.defaultLines ?? 100)));
      const selected = lines.slice(from - 1, from - 1 + count).join('\n');
      const result = selected.slice(0, Math.min(params.maxChars ?? 12000, 12000));
      const truncated = from - 1 + count < lines.length || result.length < selected.length;
      return {status: 'ok', path: relative, text: result, from, lines: result.split('\n').length,
        truncated, ...(truncated ? {nextFrom: from + result.split('\n').length} : {})};
    },
    readForIndexing: text,
    buildMultimodalChunk: async () => { throw new Error('Multimodal Memory is disabled'); },
    // No filesystem watcher or periodic job is created. Only an explicit
    // search invalidates the native deterministic index after draining old work.
    watch: async (_request, onChange, watchSignal) => {
      const active = AbortSignal.any([signal,watchSignal]);
      active.throwIfAborted();
      watchers.add(onChange);
      await new Promise(resolve => active.addEventListener('abort',resolve,{once:true}));
      watchers.delete(onChange);
    },
    maintenance: {
      readFile: async file => Buffer.from((await text(file)).content),
      stat: async file => {
        const info = await client.request('stat', {path: permitted(file)});
        if (!info) throw Object.assign(new Error('Missing memory file'), {code: 'ENOENT'});
        return {isFile: info.type === 'file', isDirectory: false, isSymbolicLink: false, size: info.size, mtimeMs: info.mtimeMs, mode: 0};
      },
      listDirectory: denied, mkdir: denied, rename: denied, resolveWritePath: denied, commitContent: denied,
      resolveDreamsPath: denied, readDreams: denied, writeDreams: denied, replaceReport: denied, appendCorpus: denied,
    },
  };
}
