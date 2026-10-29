import test from 'node:test';
import assert from 'node:assert/strict';
import katex from '../internal/quickinput/assets/vendor/katex/katex.mjs';
import {markupTokens} from '../internal/quickinput/assets/chat-markup.mjs';
import {mathRenderer, MAX_FORMULA_LENGTH, MAX_MESSAGE_FORMULAS, MAX_WINDOW_MATH_NODES, MAX_RENDER_MATH_SOURCE} from '../internal/quickinput/assets/chat-math.mjs';
import {markdown, messageRenderer} from '../internal/quickinput/assets/chat-renderer.mjs';
import {verifyFrontendAssets} from '../scripts/verify-frontend-assets.mjs';
import {RenderNode, rendererDocument, withClass} from './chat-renderer-fixture.mjs';

test('math delimiters preserve code, literal dollars, prices and incomplete blocks', () => {
  const text = '中文 ' + String.raw`\(a_1*b\) 和 $x+1$。\[\frac{1}{2}\] $$x^2$$`;
  const formulas = markupTokens(text).filter(token => token.kind === 'math');
  assert.deepEqual(formulas.map(token => token.display), [false, false, true, true]);
  assert.equal(formulas[0].text, 'a_1*b');
  const code = '`$x$`\n```latex\n\\[x^2\\]\n```\n价格 $5 和 $10；转义 \\$x\\$';
  assert.equal(markupTokens(code).filter(token => token.kind === 'math').length, 0);
  assert.equal(markupTokens(code).find(token => token.kind === 'code-block').text, '\\[x^2\\]');
  const partial = String.raw`\[\frac{1}{`;
  assert.deepEqual(markupTokens(partial), [{kind: 'source', text: partial, display: true}]);
  assert.equal(markupTokens(partial + String.raw`2}\]`).filter(token => token.kind === 'math').length, 1);
  assert.equal(markupTokens('$x +\ny$').filter(token => token.kind === 'math').length, 0);
});

test('unfinished inline dollars preserve Markdown punctuation through streaming closure', t => {
  rendererDocument(t);
  for (const text of ['$a**b**', '$1+*x*', String.raw`$\frac{a_1}{`]) {
    assert.equal(markdown(text, () => {}).children[0].textContent, text);
    assert.equal(markupTokens(text)[0].kind, 'source');
  }
  assert.equal(markupTokens('$a**b**$')[0].kind, 'math');
  assert.equal(markdown('$x+\n**next paragraph**', () => {}).querySelectorAll('*').some(node => node.tagName === 'strong'), true);
});

test('macro assignments and exhausted input/output budgets bypass synchronous typesetting', t => {
  rendererDocument(t);
  let calls = 0;
  const math = mathRenderer(() => { calls++; throw Error('discarded output'); });
  const body = 'x'.repeat(900);
  const amplified = String.raw`\def\many{` + body + '}' + String.raw`\many`.repeat(80);
  for (const command of ['def', 'gdef', 'edef', 'xdef', 'let', 'futurelet', 'newcommand', 'renewcommand', 'providecommand']) {
    const raw = '$\\' + command + '\\many{x}\\many$';
    assert.equal(math.message().render({text: raw.slice(1, -1), raw}).textContent, raw);
  }
  math.message().render({text: amplified});
  math.message(0).render({text: 'unique-non-cached-input'});
  assert.equal(calls, 0);
  const source = 'x'.repeat(1000);
  for (let i = 0; i < MAX_MESSAGE_FORMULAS; i++) math.message().render({text: source + i});
  assert.ok(calls <= Math.floor(MAX_RENDER_MATH_SOURCE / 1000), 'discarded attempts must consume a shared input budget');
  const before = calls;
  math.beginRender(); math.message().render({text: 'fresh'});
  assert.equal(calls, before + 1);
});

test('real KaTeX renders common formulas, and copying keeps original Markdown and LaTeX', t => {
  rendererDocument(t);
  const text = '# 数学\n行内 ' + String.raw`\(x^2+\sqrt{2}\)` + '\n\n' +
    String.raw`\[\int_0^1 \frac{1}{2} dx + \begin{pmatrix}a&b\\c&d\end{pmatrix}\]`;
  let copied;
  const dom = markdown(text, value => { copied = value; });
  assert.equal(withClass(dom, 'chat-math').length, 2);
  assert.equal(dom.querySelectorAll('*').filter(node => node.tagName === 'math').length, 2);
  assert.equal(dom.children[0].tagName, 'h2');
  const button = withClass(dom, 'chat-copy-source')[0]; button.listeners.get('click')();
  assert.equal(copied, text);
});

test('untrusted formulas cannot create navigation, media or executable nodes and macros do not leak', t => {
  rendererDocument(t);
  const math = mathRenderer();
  for (const text of [String.raw`\href{javascript:alert(1)}{click}`, String.raw`\includegraphics{https://example.invalid/x}`,
    String.raw`\htmlClass{evil}{x}`, String.raw`\def\loop{\loop}\loop`, '\\unknown{<script>alert(1)</script>}',
    '{'.repeat(60) + 'x' + '}'.repeat(60), 'x'.repeat(MAX_FORMULA_LENGTH + 1)]) {
    const node = math.message().render({text, raw: `\\(${text}\\)`, display: false});
    assert.equal(node.querySelectorAll('*').some(child => ['script', 'img', 'a', 'iframe'].includes(child.tagName)), false);
  }
  math.message().render({text: String.raw`\gdef\personal{x}\personal`, display: false});
  assert.ok(withClass(math.message().render({text: '\\personal', raw: '\\(\\personal\\)', display: false}), 'chat-math-source').length);
  const plain = markdown('<img src=x onerror=alert(1)>', () => {});
  assert.equal(plain.querySelectorAll('*').some(node => node.tagName === 'img'), false);
});

test('closed formulas are cached across streaming renders; count, node and cache budgets are finite', t => {
  rendererDocument(t);
  let calls = 0;
  const math = mathRenderer((...args) => { calls++; katex.render(...args); });
  const token = {text: 'x^2', raw: '$x^2$', display: false};
  const first = math.message().render(token), second = math.message().render(token);
  assert.equal(calls, 1); assert.notEqual(first, second);
  const limited = math.message();
  for (let i = 0; i < MAX_MESSAGE_FORMULAS; i++) limited.render(token);
  assert.equal(limited.render(token).textContent, token.raw);
  assert.equal(math.message(0).render(token).textContent, token.raw);
  for (let i = 0; i < 100; i++) math.message().render({...token, text: `x+${i}`});
  const before = calls; math.message().render(token); assert.equal(calls, before + 1);
  math.clear(); math.message().render(token); assert.equal(calls, before + 2);
  const container = new RenderNode('div');
  const render = messageRenderer(container, {copy() {}, expand() {}});
  const long = Array.from({length: 32}, () => '$\\frac{x^2}{1+x}$').join(' ');
  render(Array.from({length: 200}, (_, id) => ({id: String(id), role: 'assistant', blocks: [{type: 'text', text: long}]})));
  const formulaNodes = withClass(container, 'chat-math').reduce((sum, node) => sum + node.querySelectorAll('*').length + 1, 0);
  assert.ok(formulaNodes <= MAX_WINDOW_MATH_NODES);
  assert.ok(withClass(container, 'chat-math-source').length > 0);
  render([]); assert.equal(container.children.length, 0); render.dispose();
});

test('vendored formula scripts, all CSS fonts and licenses match the fixed integrity inventory', async () => {
  const result = await verifyFrontendAssets();
  assert.equal(result.version, katex.version);
  assert.ok(result.files > 3);
});
