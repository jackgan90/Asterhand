import path from 'node:path';

export const MAX_SUMMARY_UNITS = 96;
export const MAX_SUMMARY_BYTES = 240;

const encoder = new TextEncoder();
const validKinds = new Set(['path', 'patch', 'query', 'none']);

export const toolDisplaySpecs = Object.freeze({
  read_file: Object.freeze({
    kind: 'path',
    pathParams: Object.freeze(['path']),
    aliasParams: Object.freeze(['file_path']),
    rangeParams: Object.freeze(['offset', 'limit']),
  }),
  write_file: Object.freeze({
    kind: 'path',
    pathParams: Object.freeze(['path']),
    aliasParams: Object.freeze(['file_path']),
  }),
  edit_file: Object.freeze({
    kind: 'path',
    pathParams: Object.freeze(['path']),
    aliasParams: Object.freeze(['file_path']),
  }),
  apply_file_patch: Object.freeze({
    kind: 'patch',
    patchParam: 'input',
    maxTargets: 3,
  }),
  list_dir: Object.freeze({
    kind: 'path',
    pathParams: Object.freeze(['path']),
    aliasParams: Object.freeze(['file_path']),
    defaultPath: '.',
  }),
  search_files: Object.freeze({
    kind: 'query',
    queryParam: 'query',
    pathParam: 'path',
  }),
  memory_search: Object.freeze({
    kind: 'query',
    queryParam: 'query',
    scopeParam: 'corpus',
    defaultScope: 'memory',
  }),
  memory_get: Object.freeze({
    kind: 'path',
    pathParams: Object.freeze(['path']),
    rangeParams: Object.freeze(['from', 'lines']),
  }),
  progress_card: Object.freeze({kind: 'none'}),
});

export const legacyToolAliases = Object.freeze({
  read: 'read_file',
  write: 'write_file',
  edit: 'edit_file',
  apply_patch: 'apply_file_patch',
  ls: 'list_dir',
});

export function resolveToolDisplaySpec(toolName) {
  if (typeof toolName !== 'string' || !toolName) return null;
  if (Object.hasOwn(toolDisplaySpecs, toolName)) return toolDisplaySpecs[toolName];
  if (Object.hasOwn(legacyToolAliases, toolName)) return toolDisplaySpecs[legacyToolAliases[toolName]] ?? null;
  return null;
}

export function validateToolDisplaySpec(tool) {
  const errors = [];
  if (!tool || typeof tool !== 'object' || typeof tool.name !== 'string' || !tool.name) {
    return {ok: false, errors: ['missing tool name']};
  }
  const display = tool.display;
  if (!display || typeof display !== 'object' || Array.isArray(display)) {
    errors.push(`missing display spec on ${tool.name}`);
    return {ok: false, errors};
  }
  if (!validKinds.has(display.kind)) {
    errors.push(`invalid display kind "${String(display.kind)}" on ${tool.name}`);
  }
  const expected = Object.hasOwn(toolDisplaySpecs, tool.name) ? toolDisplaySpecs[tool.name] : null;
  if (!expected) {
    errors.push(`unregistered display spec for ${tool.name}`);
  } else if (JSON.stringify(display) !== JSON.stringify(expected)) {
    errors.push(`display spec mismatch for ${tool.name}`);
  }
  const props = tool.parameters?.properties;
  if (!props || typeof props !== 'object' || Array.isArray(props)) {
    errors.push(`missing schema properties for ${tool.name}`);
    return {ok: false, errors};
  }
  const schemaParams = [
    ...(Array.isArray(display.pathParams) ? display.pathParams : []),
    ...(Array.isArray(display.rangeParams) ? display.rangeParams : []),
    display.patchParam,
    display.queryParam,
    display.pathParam,
    display.scopeParam,
  ].filter(name => typeof name === 'string' && name.length > 0);
  for (const paramName of schemaParams) {
    if (!Object.hasOwn(props, paramName)) {
      errors.push(`unknown display param "${paramName}" on ${tool.name}`);
    }
  }
  return {ok: errors.length === 0, errors};
}

function charUnits(ch) {
  return ch.codePointAt(0) > 0x2e7f ? 2 : 1;
}

function measureUnits(text) {
  let total = 0;
  for (const ch of text) total += charUnits(ch);
  return total;
}

function truncateText(text, maxUnits = MAX_SUMMARY_UNITS, maxBytes = MAX_SUMMARY_BYTES) {
  if (measureUnits(text) <= maxUnits && encoder.encode(text).length <= maxBytes) return text;
  const suffix = '…';
  const suffixBytes = encoder.encode(suffix).length;
  let units = 0;
  let out = '';
  for (const ch of text) {
    const nextUnits = units + charUnits(ch);
    const next = out + ch;
    if (nextUnits + 1 > maxUnits || encoder.encode(next).length + suffixBytes > maxBytes) {
      return out + suffix;
    }
    out = next;
    units = nextUnits;
  }
  return out;
}

function clampPath(text, maxUnits = MAX_SUMMARY_UNITS, maxBytes = MAX_SUMMARY_BYTES) {
  if (measureUnits(text) <= maxUnits && encoder.encode(text).length <= maxBytes) return text;
  const rangeMatch = /(:(?:\d+-\d+|\d+\+))$/.exec(text);
  const range = rangeMatch ? rangeMatch[1] : '';
  const body = range ? text.slice(0, -range.length) : text;
  const slash = body.lastIndexOf('/');
  if (slash > 0) {
    const tail = body.slice(slash + 1) + range;
    const bridge = '…/';
    const tailUnits = measureUnits(bridge + tail);
    const tailBytes = encoder.encode(bridge + tail).length;
    if (tailUnits < maxUnits && tailBytes < maxBytes) {
      const availUnits = maxUnits - tailUnits;
      const availBytes = maxBytes - tailBytes;
      let head = '';
      let headUnits = 0;
      for (const ch of body.slice(0, slash)) {
        const nextUnits = headUnits + charUnits(ch);
        const next = head + ch;
        if (nextUnits > availUnits || encoder.encode(next).length > availBytes) break;
        head = next;
        headUnits = nextUnits;
      }
      if (head) return `${head}${bridge}${tail}`;
    }
  }
  return truncateText(text, maxUnits, maxBytes);
}

function normalizeDisplayPath(rawValue, workspaceRoot = '', defaultPath = '') {
  if (typeof rawValue !== 'string') return defaultPath;
  const cleaned = rawValue.replace(/[\x00-\x1f\x7f]/g, '').trim();
  if (!cleaned) return defaultPath;
  const slashed = cleaned.replaceAll('\\', '/').replace(/\/+/g, '/');
  const isWinAbs = /^[a-zA-Z]:(?:\/|$)/.test(slashed);
  if (isWinAbs || slashed.startsWith('/')) {
    const rootNorm = typeof workspaceRoot === 'string'
      ? workspaceRoot.trim().replaceAll('\\', '/').replace(/\/+/g, '/').replace(/\/+$/, '')
      : '';
    const slashedTrim = slashed.replace(/\/+$/, '');
    if (rootNorm) {
      const rootWin = /^[a-zA-Z]:(?:\/|$)/.test(rootNorm);
      const eq = rootWin ? slashedTrim.toLowerCase() === rootNorm.toLowerCase() : slashedTrim === rootNorm;
      if (eq) return '.';
      const prefix = rootWin
        ? slashedTrim.toLowerCase().startsWith(rootNorm.toLowerCase() + '/')
        : slashedTrim.startsWith(rootNorm + '/');
      if (prefix) {
        const sub = path.posix.normalize(slashedTrim.slice(rootNorm.length + 1).replace(/^(?:\.\/)+/, ''));
        if (!sub || sub === '.') return '.';
        if (sub !== '..' && !sub.startsWith('../')) return sub;
      }
    }
    if (!slashedTrim || /^[a-zA-Z]:$/.test(slashedTrim)) return defaultPath;
    const base = path.posix.basename(slashedTrim);
    return base && base !== '.' && base !== '..' ? base : defaultPath;
  }
  const trimmed = slashed.replace(/^(?:\.\/)+/, '').replace(/\/+$/, '');
  if (!trimmed) return '.';
  const norm = path.posix.normalize(trimmed);
  if (norm === '..' || norm.startsWith('../')) {
    const base = path.posix.basename(norm);
    return base && base !== '.' && base !== '..' ? base : defaultPath;
  }
  return norm || defaultPath;
}

function parseArgs(rawArgs) {
  if (typeof rawArgs === 'string') {
    try {
      const parsed = JSON.parse(rawArgs);
      return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed : {};
    } catch {
      return {};
    }
  }
  return rawArgs && typeof rawArgs === 'object' && !Array.isArray(rawArgs) ? rawArgs : {};
}

function formatLineRange(args, rangeParams) {
  if (!Array.isArray(rangeParams) || rangeParams.length < 2) return '';
  const startRaw = args[rangeParams[0]];
  const countRaw = args[rangeParams[1]];
  const hasStart = typeof startRaw === 'number' && Number.isInteger(startRaw) && startRaw >= 1;
  const hasCount = typeof countRaw === 'number' && Number.isInteger(countRaw) && countRaw >= 1;
  if (hasStart && hasCount) return `:${startRaw}-${startRaw + countRaw - 1}`;
  if (hasStart && startRaw > 1) return `:${startRaw}+`;
  if (!hasStart && hasCount) return `:1-${countRaw}`;
  return '';
}

function summarizePathSpec(spec, args, workspaceRoot) {
  const keys = [...(spec.pathParams ?? []), ...(spec.aliasParams ?? [])];
  let chosen = '';
  for (const key of keys) {
    if (typeof args[key] === 'string' && args[key].replace(/[\x00-\x1f\x7f]/g, '').trim()) {
      chosen = args[key];
      break;
    }
  }
  const normalized = normalizeDisplayPath(chosen, workspaceRoot, spec.defaultPath ?? '');
  if (!normalized) return '';
  return clampPath(normalized + formatLineRange(args, spec.rangeParams));
}

function summarizePatchSpec(spec, args, workspaceRoot) {
  const input = args[spec.patchParam];
  if (typeof input !== 'string' || !input) return '';
  const entries = [];
  let pos = 0;
  while (pos < input.length) {
    const end = input.indexOf('\n', pos);
    const lineEnd = end < 0 ? input.length : end;
    const line = input.slice(pos, lineEnd).replace(/\r$/, '');
    const match = /^\*\*\* (Add File|Update File|Delete File|Move to): (.+)$/.exec(line);
    if (match) {
      const target = normalizeDisplayPath(match[2], workspaceRoot);
      if (match[1] === 'Move to' && entries.length && !entries.at(-1).dst) {
        if (target) entries.at(-1).dst = target;
      } else if (target) {
        entries.push({src: target, dst: ''});
      }
    }
    if (end < 0) break;
    pos = end + 1;
  }
  const labels = [];
  const seen = new Set();
  for (const entry of entries) {
    const label = entry.dst && entry.dst !== entry.src ? `${entry.src} → ${entry.dst}` : entry.src;
    if (label && !seen.has(label)) {
      seen.add(label);
      labels.push(label);
    }
  }
  if (!labels.length) return '';
  const maxTargets = spec.maxTargets ?? 3;
  const shown = labels.slice(0, maxTargets).join(', ');
  const combined = labels.length > maxTargets ? `${shown} (+${labels.length - maxTargets})` : shown;
  return truncateText(combined, MAX_SUMMARY_UNITS, MAX_SUMMARY_BYTES);
}

function summarizeQuerySpec(spec, args, workspaceRoot) {
  const rawQuery = args[spec.queryParam];
  if (typeof rawQuery !== 'string') return '';
  const cleanedQuery = rawQuery.replace(/[\x00-\x1f\x7f]/g, ' ').replace(/\s+/g, ' ').trim();
  if (!cleanedQuery) return '';
  const parts = [`"${truncateText(cleanedQuery, 56, 128)}"`];
  if (spec.pathParam) {
    const scopePath = normalizeDisplayPath(args[spec.pathParam], workspaceRoot);
    if (scopePath && scopePath !== '.') parts.push(clampPath(scopePath, 32, 96));
  }
  if (spec.scopeParam && typeof args[spec.scopeParam] === 'string') {
    const scope = args[spec.scopeParam].trim();
    if (scope && /^[a-z0-9_-]+$/i.test(scope) && scope !== spec.defaultScope) parts.push(scope);
  }
  return truncateText(parts.join(' · '), MAX_SUMMARY_UNITS, MAX_SUMMARY_BYTES);
}

export function summarizeToolCall(toolName, rawArgs, workspaceRoot = '') {
  const spec = resolveToolDisplaySpec(toolName);
  if (!spec || spec.kind === 'none') return '';
  const args = parseArgs(rawArgs);
  if (spec.kind === 'path') return summarizePathSpec(spec, args, workspaceRoot);
  if (spec.kind === 'patch') return summarizePatchSpec(spec, args, workspaceRoot);
  if (spec.kind === 'query') return summarizeQuerySpec(spec, args, workspaceRoot);
  return '';
}
