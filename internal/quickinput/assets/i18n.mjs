import {catalog, defaultFallback, locales} from './i18n-catalog.mjs';

export {catalog, defaultFallback, locales};

const listeners = new Set();
let activeLocale = defaultFallback;

function normalizeTag(raw) {
  let tag = String(raw || '').trim();
  if (!tag) return '';
  const dot = tag.indexOf('.');
  if (dot >= 0) tag = tag.slice(0, dot);
  const at = tag.indexOf('@');
  if (at >= 0) tag = tag.slice(0, at);
  return tag.replaceAll('_', '-');
}

export function validLocale(id) {
  return locales.some((entry) => entry.id === id);
}

export function validPreference(value) {
  return value === 'auto' || validLocale(value);
}

export function matchLocale(candidates) {
  const list = Array.isArray(candidates) ? candidates : candidates ? [candidates] : [];
  for (const raw of list) {
    const tag = normalizeTag(raw);
    if (!tag) continue;
    const lower = tag.toLowerCase();
    if (lower === 'c' || lower === 'posix') continue;
    const exact = locales.find((entry) => entry.id.toLowerCase() === lower);
    if (exact) return exact.id;
    const primary = lower.split('-')[0];
    if (!primary) continue;
    const matched = locales.find((entry) =>
      entry.prefixes.some((p) => {
        const pl = p.toLowerCase();
        return pl === primary || pl === lower;
      }),
    );
    if (matched) return matched.id;
  }
  return defaultFallback;
}

export function getLocale() {
  return activeLocale;
}

export function currentLocaleEntry() {
  return locales.find((entry) => entry.id === activeLocale) || locales.find((entry) => entry.id === defaultFallback) || locales[0];
}

export function onLocaleChange(listener) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function setLocale(nextId, {forceNotify = false} = {}) {
  const resolved = validLocale(nextId) ? nextId : matchLocale([nextId]);
  const changed = resolved !== activeLocale;
  activeLocale = resolved;
  const entry = currentLocaleEntry();
  if (globalThis.document?.documentElement) {
    globalThis.document.documentElement.lang = entry.htmlLang;
  }
  if (changed || forceNotify) {
    for (const listener of [...listeners]) listener(activeLocale, entry);
  }
  return activeLocale;
}

export function hasKey(key) {
  if (!key || typeof key !== 'string') return false;
  const active = catalog[activeLocale];
  if (active && Object.hasOwn(active, key)) return true;
  const fb = catalog[defaultFallback];
  return Boolean(fb && Object.hasOwn(fb, key));
}

export function t(key, args = undefined) {
  if (!key || typeof key !== 'string') return '';
  const template = catalog[activeLocale]?.[key] ?? catalog[defaultFallback]?.[key];
  if (template === undefined) return key;
  if (!args || typeof args !== 'object') return template;
  return template.replace(/\{([a-zA-Z0-9_]+)\}/g, (full, name) => {
    const value = args[name];
    return value === undefined || value === null ? full : String(value);
  });
}

export function localizeMessage(key, args, fallback = '') {
  if (key && hasKey(key)) return t(key, args);
  if (fallback) return String(fallback);
  return key ? String(key) : '';
}

export function localizeError(err, fallbackKey = '') {
  if (!err) return fallbackKey ? t(fallbackKey) : '';
  if (typeof err === 'string') return err;
  const key = err.messageKey || err.message_key || err.error_key || '';
  const args = err.messageArgs || err.message_args || err.error_args;
  const raw = err.rawMessage ?? err.message ?? '';
  const text = localizeMessage(key, args, raw);
  return text || (fallbackKey ? t(fallbackKey) : '');
}

export function applyI18n(root = globalThis.document) {
  if (!root || typeof root.querySelectorAll !== 'function') return;
  const entry = currentLocaleEntry();
  if (root === globalThis.document && root.documentElement) {
    root.documentElement.lang = entry.htmlLang;
  }
  for (const el of root.querySelectorAll('[data-i18n]')) {
    const key = el.getAttribute('data-i18n');
    if (key) el.textContent = t(key);
  }
  for (const [attr, target] of [
    ['data-i18n-placeholder', 'placeholder'],
    ['data-i18n-aria-label', 'aria-label'],
    ['data-i18n-title', 'title'],
  ]) {
    for (const el of root.querySelectorAll(`[${attr}]`)) {
      const key = el.getAttribute(attr);
      if (key) el.setAttribute(target, t(key));
    }
  }
  for (const el of root.querySelectorAll('[data-i18n-brand-sub]')) {
    el.hidden = !entry.showBrandSubtitle;
  }
}

export function formatDateTime(value) {
  if (!value) return '';
  const dt = new Date(value);
  if (Number.isNaN(dt.getTime())) return String(value);
  return dt.toLocaleString(currentLocaleEntry().htmlLang, {hour12: false});
}

export function formatShortTime(value) {
  if (!value) return '';
  const dt = new Date(value);
  if (Number.isNaN(dt.getTime())) return '';
  return dt.toLocaleTimeString(currentLocaleEntry().htmlLang, {hour: '2-digit', minute: '2-digit', hour12: false});
}
