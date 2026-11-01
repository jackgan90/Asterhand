import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import test from 'node:test';
import {
  checkFrontendI18n,
  checkHtmlTemplate,
  checkJsSource,
  validateCatalogs,
} from '../scripts/sync-i18n.mjs';

test('G1/G2/G3/G4 repository frontend and catalog guardrails pass cleanly', async () => {
  const {errors, referencedKeys, catalogs, manifest} = await checkFrontendI18n();
  assert.deepEqual(errors, []);
  assert.ok(referencedKeys.size > 100, 'expected HTML and JS references to be collected');

  // Every concrete key example in docs/development/i18n-guide.md must exist in the authoritative catalog.
  const guide = await readFile(new URL('../docs/development/i18n-guide.md', import.meta.url), 'utf8');
  const allKeys = new Set(Object.keys(catalogs[manifest.default_fallback] || {}));
  for (const match of guide.matchAll(/["']((?:common|brand|web|quickinput|desktop|status|err)\.[a-z0-9_.]+)["']/g)) {
    assert.equal(allKeys.has(match[1]), true, `i18n-guide.md references undefined example key: ${match[1]}`);
  }
});

test('G1 validateCatalogs enforces key symmetry, placeholders, disallow_cjk, and allows Kanji locales', () => {
  const manifest = {
    schema: 1,
    default_fallback: 'en-US',
    locales: [
      {id: 'en-US', label: 'English', html_lang: 'en-US', prefixes: ['en'], disallow_cjk: true},
      {id: 'ja-JP', label: '日本語', html_lang: 'ja-JP', prefixes: ['ja'], disallow_cjk: false},
    ],
  };
  assert.deepEqual(
    validateCatalogs(manifest, {
      'en-US': {'web.topbar.settings': 'Settings ({count})'},
      'ja-JP': {'web.topbar.settings': '設定 ({count})'},
    }),
    [],
  );

  const cjkLeak = validateCatalogs(manifest, {
    'en-US': {'web.topbar.settings': 'Settings 设置 ({count})'},
    'ja-JP': {'web.topbar.settings': '設定 ({count})'},
  });
  assert.ok(cjkLeak.some((err) => err.includes('disallow_cjk')));

  const missingAndParam = validateCatalogs(manifest, {
    'en-US': {'web.topbar.settings': 'Settings ({count})', 'web.only.en': 'Only EN'},
    'ja-JP': {'web.topbar.settings': '設定 ({total})', 'web.only.ja': '余分'},
  });
  assert.ok(missingAndParam.some((err) => err.includes('missing key: web.only.en')));
  assert.ok(missingAndParam.some((err) => err.includes('extra key')));
  assert.ok(missingAndParam.some((err) => err.includes('placeholder mismatch')));
});

test('G3 checkHtmlTemplate blocks unbound text, missing attribute bindings, and unknown keys', () => {
  const validKeys = new Set(['web.topbar.settings', 'web.input.placeholder']);
  const okRes = checkHtmlTemplate(
    'web/index.html',
    '<!doctype html><button data-i18n="web.topbar.settings">设置</button><input placeholder="输入" data-i18n-placeholder="web.input.placeholder" /><span>×</span>',
    ['×'],
    validKeys,
  );
  assert.deepEqual(okRes.errors, []);

  const badRes = checkHtmlTemplate(
    'web/index.html',
    '<div>未绑定文本</div><input placeholder="Search" /><button aria-label="Close" data-i18n="web.unknown.key">Close</button>',
    ['×'],
    validKeys,
  );
  assert.ok(badRes.errors.some((err) => err.includes('unbound HTML text "未绑定文本"')));
  assert.ok(badRes.errors.some((err) => err.includes('missing data-i18n-placeholder')));
  assert.ok(badRes.errors.some((err) => err.includes('missing data-i18n-aria-label')));
  assert.ok(badRes.errors.some((err) => err.includes('unknown i18n key "web.unknown.key"')));
});

test('G4 checkJsSource blocks CJK literals, unknown keys, and direct English UI sink literals', () => {
  const validKeys = new Set(['web.topbar.settings', 'err.bridge.invalid_request']);
  const cleanSource = `
    // 中文注释不会触发守卫
    const label = t('web.topbar.settings');
    button.textContent = label;
    const err = failure('invalid_request', 'err.bridge.invalid_request');
  `;
  assert.deepEqual(checkJsSource('web/sample.mjs', cleanSource, validKeys).errors, []);

  const dirtySource = `
    const raw = '硬编码中文';
    const badKey = t('web.missing.key');
    const spacedKey = t ('web.not_a_real_key');
    const commentedKey = t /* inline comment */ ('web.commented.missing');
    const keys = {done: 'web.not_in_catalog'};
    t(keys.done);
    const unprefixedMap = {done: 'totally_invalid_key'};
    t(unprefixedMap.done);
    panel.textContent = 'Save settings';
    notice ('Save changes');
    node('span', 'Direct literal');
    confirm('Delete this command?');
    say('Saved successfully');
    btn.setAttribute('aria-label', 'Close dialog');
  `;
  const {errors} = checkJsSource('web/sample.mjs', dirtySource, validKeys);
  assert.ok(errors.some((err) => err.includes('hardcoded CJK string literal')));
  assert.ok(errors.some((err) => err.includes('unknown i18n key "web.missing.key"')));
  assert.ok(errors.some((err) => err.includes('web.not_a_real_key')));
  assert.ok(errors.some((err) => err.includes('web.commented.missing')));
  assert.ok(errors.some((err) => err.includes('web.not_in_catalog')));
  assert.ok(errors.some((err) => err.includes('totally_invalid_key')));
  assert.ok(errors.some((err) => err.includes('Save settings')));
  assert.ok(errors.some((err) => err.includes('Save changes')));
  assert.ok(errors.some((err) => err.includes('Direct literal')));
  assert.ok(errors.some((err) => err.includes('Delete this command?')));
  assert.ok(errors.some((err) => err.includes('Saved successfully')));
  assert.ok(errors.some((err) => err.includes('Close dialog')));
});

test('G4 js_ui_sinks configuration dynamically drives JS sink detection', () => {
  const validKeys = new Set(['web.topbar.settings']);
  const customCode = `
    customToast ('Connection restored');
    statusBanner('info', 'Disk space low');
  `;
  assert.deepEqual(checkJsSource('web/custom.mjs', customCode, validKeys, [{kind: 'property', name: 'textContent'}]).errors, []);

  const configuredSinks = [
    'customToast',
    {kind: 'call', name: 'statusBanner', arg_indices: [1]},
  ];
  const {errors} = checkJsSource('web/custom.mjs', customCode, validKeys, configuredSinks);
  assert.equal(errors.length, 2);
  assert.ok(errors.some((err) => err.includes('Connection restored')));
  assert.ok(errors.some((err) => err.includes('Disk space low')));
});
