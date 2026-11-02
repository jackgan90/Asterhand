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
