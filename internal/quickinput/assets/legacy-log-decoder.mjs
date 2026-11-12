// Deterministic WHATWG legacy decoders, shared with the public log API through
// the same pinned indexes and conformance cases. UTF decoding stays native.
import {legacyLogTables} from './legacy-log-tables.mjs';

const tables = new Map();
function table(name) {
  if (!tables.has(name)) {
    const bytes = Uint8Array.from(atob(legacyLogTables[name]), c => c.charCodeAt(0));
    tables.set(name, new DataView(bytes.buffer));
  }
  return tables.get(name);
}
function point(name, index) {
  const data = table(name);
  return index >= 0 && index * 4 < data.byteLength ? data.getUint32(index * 4, true) : 0;
}
function rangePoint(pointer) {
  if (pointer > 39419 && pointer < 189000 || pointer > 1237575) return 0;
  if (pointer === 7457) return 0xe7c7;
  const ranges = table('index-gb18030-ranges.bin');
  let low = 0, high = ranges.byteLength / 8;
  while (low < high) { const mid = (low + high) >>> 1; if (ranges.getUint32(mid * 8, true) <= pointer) low = mid + 1; else high = mid; }
  const i = low - 1;
  return i < 0 ? 0 : ranges.getUint32(i * 8 + 4, true) + pointer - ranges.getUint32(i * 8, true);
}
const specialBig5 = new Map([[1133, 'Ê̄'], [1135, 'Ê̌'], [1164, 'ê̄'], [1166, 'ê̌']]);

export function createLogTextDecoder(encoding = 'utf-8', options) {
  if (!['gbk', 'gb18030', 'big5'].includes(encoding)) return new TextDecoder(encoding, options);
  const gb = encoding !== 'big5';
  let pending = [];
  return {
    decode(input = new Uint8Array(), {stream = false} = {}) {
      const bytes = new Uint8Array(pending.length + input.length);
      bytes.set(pending); bytes.set(input, pending.length); pending = [];
      const output = [];
      let i = 0;
      const incomplete = () => {
        if (stream) pending = [...bytes.slice(i)]; else output.push('\ufffd');
        i = bytes.length;
      };
      while (i < bytes.length) {
        const a = bytes[i];
        if (a < 0x80) { output.push(String.fromCharCode(a)); i++; continue; }
        if (gb && a === 0x80) { output.push('€'); i++; continue; }
        if (a < 0x81 || a > 0xfe) { output.push('\ufffd'); i++; continue; }
        if (i + 1 === bytes.length) { incomplete(); break; }
        const b = bytes[i + 1];
        if (gb && b >= 0x30 && b <= 0x39) {
          if (i + 2 === bytes.length) { incomplete(); break; }
          const c = bytes[i + 2];
          if (c < 0x81 || c > 0xfe) { output.push('\ufffd'); i++; continue; }
          if (i + 3 === bytes.length) { incomplete(); break; }
          const d = bytes[i + 3];
          if (d < 0x30 || d > 0x39) { output.push('\ufffd'); i++; continue; }
          output.push(String.fromCodePoint(rangePoint(((a - 0x81) * 10 + b - 0x30) * 1260 + (c - 0x81) * 10 + d - 0x30) || 0xfffd));
          i += 4; continue;
        }
        let code = 0;
        if (gb && (b >= 0x40 && b <= 0x7e || b >= 0x80 && b <= 0xfe)) code = point('index-gb18030.bin', (a - 0x81) * 190 + b - (b < 0x7f ? 0x40 : 0x41));
        if (!gb && (b >= 0x40 && b <= 0x7e || b >= 0xa1 && b <= 0xfe)) {
          const pointer = (a - 0x81) * 157 + b - (b < 0x7f ? 0x40 : 0x62);
          if (specialBig5.has(pointer)) { output.push(specialBig5.get(pointer)); i += 2; continue; }
          code = point('index-big5.bin', pointer);
        }
        if (code) { output.push(String.fromCodePoint(code)); i += 2; }
        else { output.push('\ufffd'); i += b < 0x80 ? 1 : 2; }
      }
      return output.join('');
    },
  };
}
