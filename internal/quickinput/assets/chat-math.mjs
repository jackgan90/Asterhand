import katex from './vendor/katex/katex.mjs';

export const MAX_FORMULA_LENGTH = 2048;
export const MAX_MESSAGE_FORMULAS = 32;
export const MAX_MESSAGE_MATH_NODES = 4096;
export const MAX_WINDOW_MATH_NODES = 32768;
export const MAX_RENDER_MATH_SOURCE = 8192;
const MAX_FORMULA_NODES = 1024;
const MAX_CACHE_NODES = 16384;
const MAX_CACHE_BYTES = 256 * 1024;
const MACRO_ASSIGNMENTS = new Set(['def', 'gdef', 'edef', 'xdef', 'let', 'futurelet',
  'newcommand', 'renewcommand', 'providecommand']);

function definesMacro(text) {
  // Read control words, including escaped backslashes, instead of matching
  // substrings of ordinary commands. Built-in KaTeX macros remain available.
  return [...text.matchAll(/\\([A-Za-z@]+|.)/gs)].some(match => MACRO_ASSIGNMENTS.has(match[1]));
}

function boundedNesting(text) {
  let depth = 0;
  for (let index = 0; index < text.length; index++) {
    if (text[index] === '\\') { index++; continue; }
    if (text[index] === '{' && ++depth > 48) return false;
    if (text[index] === '}') depth--;
  }
  return true;
}

function sourceNode(token) {
  const node = document.createElement(token.display ? 'div' : 'span');
  node.className = 'chat-math-source';
  node.textContent = token.raw ?? token.text;
  return node;
}

// Each panel owns a bounded cache; source messages and macros are never global.
export function mathRenderer(typeset = katex.render) {
  const cache = new Map();
  let cachedNodes = 0, cachedBytes = 0;
  let remainingSource = MAX_RENDER_MATH_SOURCE;
  function remember(key, entry) {
    cache.set(key, entry);
    cachedNodes += entry.cost; cachedBytes += entry.bytes;
    while (cache.size > 64 || cachedNodes > MAX_CACHE_NODES || cachedBytes > MAX_CACHE_BYTES) {
      const oldest = cache.keys().next().value, removed = cache.get(oldest);
      cachedNodes -= removed.cost; cachedBytes -= removed.bytes;
      cache.delete(oldest);
    }
  }
  function formula(token) {
    const key = `${token.display ? 'block' : 'inline'}:${token.text}`;
    if (cache.has(key)) {
      const entry = cache.get(key);
      cache.delete(key); cache.set(key, entry);
      return entry;
    }
    // Charge failed/oversized attempts too, before invoking the synchronous
    // engine. Output limits alone do not bound discarded rendering work.
    if (token.text.length > remainingSource) return {node: null, cost: 0};
    remainingSource -= token.text.length;
    let node = null, cost = 0, bytes = key.length * 2;
    try {
      node = document.createElement(token.display ? 'div' : 'span');
      node.className = token.display ? 'chat-math chat-math-block' : 'chat-math';
      typeset(token.text, node, {displayMode: token.display, output: 'htmlAndMathml',
        trust: false, strict: 'error', throwOnError: true, maxExpand: 256, maxSize: 20,
        macros: Object.create(null), globalGroup: false});
      cost = node.querySelectorAll('*').length + 1;
      bytes += node.outerHTML.length * 2;
      if (cost > MAX_FORMULA_NODES || bytes > 64 * 1024) { node = null; cost = 0; bytes = key.length * 2; }
    } catch {
      // Never interpolate KaTeX error messages: they can contain unescaped input.
      node = null; cost = 0;
    }
    const entry = {node, cost, bytes};
    remember(key, entry);
    return entry;
  }
  return {
    beginRender() { remainingSource = MAX_RENDER_MATH_SOURCE; },
    message(available = MAX_MESSAGE_MATH_NODES) {
      let count = 0, nodes = 0;
      return {
        render(token) {
          if (++count > MAX_MESSAGE_FORMULAS || !token.text.trim() ||
            nodes >= Math.min(available, MAX_MESSAGE_MATH_NODES) ||
            token.text.length > MAX_FORMULA_LENGTH || !boundedNesting(token.text) ||
            definesMacro(token.text)) return sourceNode(token);
          const entry = formula(token);
          if (!entry.node || nodes + entry.cost > Math.min(available, MAX_MESSAGE_MATH_NODES)) return sourceNode(token);
          nodes += entry.cost;
          return entry.node.cloneNode(true);
        },
        get nodes() { return nodes; },
      };
    },
    clear() { cache.clear(); cachedNodes = 0; cachedBytes = 0; },
  };
}
