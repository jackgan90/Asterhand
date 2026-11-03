// Each stream has its own decoder: stdout and stderr may split characters at
// unrelated boundaries. Raw bytes remain authoritative on disk.
export function logDecoder(encoding = 'utf-8') {
  const decoders = new Map();
  const key = chunk => `${chunk.node_id || ''}:${chunk.stream}`;
  const create = (chunk, options) => {const entry={node_id:chunk.node_id,stream:chunk.stream,decoder:new TextDecoder(encoding,options)};decoders.set(key(chunk),entry);return entry;};
  return {
    seed(chunks, onlyMissing = false) {
      for (const chunk of chunks) {
        if (onlyMissing && decoders.has(key(chunk))) continue;
        create(chunk, {ignoreBOM:chunk.ignore_bom === true});
        this.decode(chunk);
      }
    },
    decode(chunk) {
      const raw = atob(chunk.data);
      const bytes = Uint8Array.from(raw, c => c.charCodeAt(0));
      const entry = decoders.get(key(chunk)) || create(chunk);
      return {node_id:chunk.node_id, stream:chunk.stream, text:entry.decoder.decode(bytes, {stream:true})};
    },
    finish() { return [...decoders.values()].map(({node_id,stream,decoder}) => ({node_id,stream,text:decoder.decode()})); },
  };
}

// The merged view labels lines, not chunks. Programs often write a line and its
// newline as separate frames, and other sources can interleave between them.
// Each source (node + stream) keeps its own open line: when another source
// wrote in between, the continuation starts a new labelled line, and a newline
// that only closes the already broken line is not shown as an empty row.
// Labels are applied with one native replace per chunk, so dense short lines
// cost neither per-line objects nor per-line DOM elements.
export function logLineMerger() {
  let last = '', atLineStart = true;
  const open = new Set();
  return {
    reset() { last = ''; atLineStart = true; open.clear(); },
    // Returns {text, breakBefore, labelFirst} for display, or null.
    split(chunk) {
      const key = `${chunk.node_id || ''}:${chunk.stream}`;
      let text = chunk.text, breakBefore = false;
      if (key !== last) {
        if (open.has(key)) {
          text = text.replace(/^\r?\n/, '');
          if (!text) { open.delete(key); return null; }
        }
        if (!atLineStart) { breakBefore = true; atLineStart = true; }
      }
      if (!text) return null;
      const labelFirst = atLineStart;
      atLineStart = text.endsWith('\n');
      last = key;
      if (atLineStart) open.delete(key); else open.add(key);
      return {text, breakBefore, labelFirst};
    },
  };
}

// Writes the label at the start of every line in a merged piece. The label
// contains user-defined node names, so it is inserted through a callback:
// a replacement string would interpret "$&", "$'" and similar patterns.
export function labelLines(piece, label) {
  return (piece.breakBefore ? '\n' : '') + (piece.labelFirst ? label : '') + piece.text.replace(/\n(?=[^])/g, () => '\n' + label);
}