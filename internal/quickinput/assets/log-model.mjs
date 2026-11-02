// Each stream has its own decoder: stdout and stderr may split characters at
// unrelated boundaries. Raw bytes remain authoritative on disk.
export function logDecoder(encoding = 'utf-8') {
  const decoders = {stdout:new TextDecoder(encoding), stderr:new TextDecoder(encoding)};
  return {
    seed(chunks) {
      for (const chunk of chunks) {
        decoders[chunk.stream] = new TextDecoder(encoding, {ignoreBOM:chunk.ignore_bom === true});
        this.decode(chunk);
      }
    },
    decode(chunk) {
      const raw = atob(chunk.data);
      const bytes = Uint8Array.from(raw, c => c.charCodeAt(0));
      return {stream:chunk.stream, text:decoders[chunk.stream].decode(bytes, {stream:true})};
    },
    finish() { return Object.entries(decoders).map(([stream, decoder]) => ({stream, text:decoder.decode()})); },
  };
}
