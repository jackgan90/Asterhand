// Opt-in real-browser check. Uses the actual command dialog and modules with an
// in-memory API fixture; never connects to an installed node or runs a script.
import assert from 'node:assert/strict';
import {createServer} from 'node:http';
import {readFile, mkdir, writeFile} from 'node:fs/promises';
import path from 'node:path';
import {fileURLToPath, pathToFileURL} from 'node:url';

const repo = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const output = process.env.ASTERHAND_GRAPH_HISTORY_OUTPUT;
const modulePath = process.env.ASTERHAND_PLAYWRIGHT_MODULE;
const executablePath = process.env.ASTERHAND_BROWSER_EXECUTABLE;
if (!output || !modulePath || !executablePath) throw new Error('Set ASTERHAND_GRAPH_HISTORY_OUTPUT, ASTERHAND_PLAYWRIGHT_MODULE and ASTERHAND_BROWSER_EXECUTABLE.');
const {chromium} = await import(pathToFileURL(modulePath).href);
await mkdir(output, {recursive:true});
const fixture = `
import {localCommands} from '/local-commands.mjs';
import {newGraph} from '/graph-model.mjs';
import {setLocale, applyI18n} from '/i18n.mjs';
setLocale('zh-CN'); applyI18n();
const graph = newGraph(); Object.assign(graph.nodes[1], {name:'compile', script:'C:\\\\step.cmd'});
let catalog = {revision:'r1', commands:[{id:'command', revision:'c1', name:'build', graph}]};
window.fixture = {saved:0, confirms:0, fail:false};
const controller = localCommands({
  api:async path => path.endsWith('/state') ? {runs:{}, managed_available:true} : catalog,
  post:async (path, body) => {
    if (fixture.fail) throw {status:409, messageKey:'err.localcmd.catalog_version_size'};
    catalog = {revision:'r'+(++fixture.saved + 1), commands:[{...body.command, id:'command', revision:'c'+(fixture.saved+1)}]};
    return {id:'command', catalog};
  },
  session:() => ({active:true}), listen:(el, event, fn) => el.addEventListener(event, fn),
  guarded:fn => fn(), actions:{confirm:() => { fixture.confirms++; return false; }}
});
fixture.locale = value => { setLocale(value); applyI18n(); controller.relocalize(); };
await controller.open('command'); fixture.ready = true;
`;
const html = (await readFile(path.join(repo, 'web/index.html'), 'utf8')).replace('src="/app.js"', 'src="/history-fixture.mjs"');
const server = createServer(async (request, response) => {
  try {
    const url = new URL(request.url, 'http://127.0.0.1');
    if (url.pathname === '/') { response.setHeader('Content-Type', 'text/html'); response.end(html); return; }
    if (url.pathname === '/history-fixture.mjs') { response.setHeader('Content-Type', 'text/javascript'); response.end(fixture); return; }
    const root = path.join(repo, 'web'), file = path.resolve(root, '.' + decodeURIComponent(url.pathname));
    if (!file.startsWith(root + path.sep)) { response.writeHead(403).end(); return; }
    response.setHeader('Content-Type', file.endsWith('.css') ? 'text/css' : 'text/javascript');
    response.end(await readFile(file));
  } catch { response.writeHead(404).end(); }
});
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
let browser;
const checks = [], errors = [];
try {
  browser = await chromium.launch({executablePath, headless:true});
  const page = await browser.newPage({viewport:{width:1600, height:1200}});
  page.on('pageerror', error => errors.push(error.message));
  await page.goto(`http://127.0.0.1:${server.address().port}/`);
  await page.waitForFunction(() => fixture.ready);
  const step = page.locator('.graph-node[data-kind="script"]'), form = page.locator('#command-form');
  const undo = page.locator('.graph-undo'), redo = page.locator('.graph-redo');
  await page.locator('.graph-viewport').click({button:'right', position:{x:80, y:80}});
  await page.locator('.graph-menu [role="menuitem"]').first().click();
  assert.equal(await step.count(), 2);
  const added = page.locator('.graph-node[data-kind="script"][aria-pressed="true"]');
  const addedID = await added.getAttribute('data-node-id');
  const focusAfterAdd = await page.evaluate(() => ({tag:document.activeElement.tagName, node:document.activeElement.dataset.nodeId}));
  await page.keyboard.press('Control+z');
  assert.equal(await step.count(), 1, `undo immediately after menu add; focus was ${JSON.stringify(focusAfterAdd)}`);
  await page.keyboard.press('Control+y');
  assert.equal(await step.count(), 2);
  assert.equal(await page.locator('.graph-node[data-kind="script"][aria-pressed="true"]').getAttribute('data-node-id'), addedID);
  await page.keyboard.press('Control+z');
  checks.push('Right-click Add transfers focus to the new step; immediate Ctrl+Z / Ctrl+Y works without clicking the canvas');
  const viewport = page.locator('.graph-viewport');
  const readView = () => viewport.evaluate(el => ({left:el.scrollLeft, top:el.scrollTop, transform:el.querySelector('.graph-stage').style.transform}));
  for (const entry of ['menu', 'doubleclick']) {
    const before = await readView(), box = await viewport.boundingBox(), position = {x:box.width - 10, y:box.height - 10};
    if (entry === 'menu') {
      await viewport.click({button:'right', position});
      await page.locator('.graph-menu [role="menuitem"]').first().click();
    } else await viewport.dblclick({position});
    assert.equal(await step.count(), 2);
    assert.deepEqual(await readView(), before, `${entry} at the edge must not scroll the viewport on focus`);
    await page.keyboard.press('Control+z'); assert.equal(await step.count(), 1);
    await page.keyboard.press('Control+y'); assert.equal(await step.count(), 2);
    assert.deepEqual(await readView(), before, 'redo focus must preserve the view too');
    await page.keyboard.press('Control+z');
  }
  checks.push('Adding near the viewport edge and restoring through redo never scrolls or shifts the canvas');
  await step.click(); await page.keyboard.press('Delete');
  assert.equal(await step.count(), 0);
  assert.equal(await page.evaluate(() => document.activeElement.className), 'graph-viewport');
  await page.keyboard.press('Control+z'); assert.equal(await step.count(), 1);
  await page.keyboard.press('Control+y'); assert.equal(await step.count(), 0);
  await page.keyboard.press('Control+z'); await page.keyboard.press('Control+Shift+z'); assert.equal(await step.count(), 0);
  await page.keyboard.press('Control+z');
  checks.push('Delete retains shortcut focus; Ctrl+Z / Ctrl+Y / Ctrl+Shift+Z restore nodes and edges');

  const name = page.locator('.graph-properties > .field input').first();
  await name.click(); await name.press('End'); await page.keyboard.type('XYZ');
  await page.keyboard.press('Control+z');
  const nativeUndo = await name.inputValue();
  assert.ok(nativeUndo.startsWith('compile') && nativeUndo.length < 'compileXYZ'.length, 'native text undo removes text using the browser grouping');
  assert.equal(await step.count(), 1);
  await page.keyboard.press('Control+y'); assert.equal(await name.inputValue(), 'compileXYZ');
  await undo.click(); assert.equal(await name.inputValue(), 'compile');
  await redo.click(); assert.equal(await name.inputValue(), 'compileXYZ');
  checks.push('Native text undo/redo stays in the input; toolbar commits and undoes the input session');

  await step.click(); await page.keyboard.down('ArrowRight'); await page.keyboard.down('ArrowRight'); await page.keyboard.up('ArrowRight');
  const moved = await step.evaluate(el => el.style.left);
  await undo.click(); assert.notEqual(await step.evaluate(el => el.style.left), moved);
  await redo.click(); assert.equal(await step.evaluate(el => el.style.left), moved);
  await page.locator('#command-form button[type=submit]').click();
  await page.waitForFunction(() => fixture.saved === 1);
  assert.equal(await form.getAttribute('data-dirty'), 'false');
  await undo.click(); assert.equal(await form.getAttribute('data-dirty'), 'true');
  await redo.click(); assert.equal(await form.getAttribute('data-dirty'), 'false');
  checks.push('Held arrow keys merge; saving retains history and undo/redo updates saved-state comparison');

  const scriptPath = page.locator('.command-script-path input');
  await scriptPath.click(); await scriptPath.press('End'); await page.keyboard.type('x');
  await page.keyboard.press('Control+z'); assert.equal(await scriptPath.inputValue(), 'C:\\step.cmd');
  await step.click(); assert.equal(await form.getAttribute('data-dirty'), 'false');
  checks.push('Native input undo back to saved content clears dirty state without altering graph history');

  await step.dblclick();
  const rename = page.locator('.graph-rename');
  await rename.evaluate(el => {
    el.dispatchEvent(new CompositionEvent('compositionstart', {bubbles:true}));
    el.value = '中文'; el.dispatchEvent(new InputEvent('input', {bubbles:true, isComposing:true}));
    el.dispatchEvent(new KeyboardEvent('keydown', {key:'z', ctrlKey:true, isComposing:true, bubbles:true, cancelable:true}));
    el.dispatchEvent(new KeyboardEvent('keydown', {key:'Enter', isComposing:true, bubbles:true, cancelable:true}));
  });
  assert.equal(await rename.count(), 1);
  await rename.evaluate(el => el.dispatchEvent(new CompositionEvent('compositionend', {bubbles:true})));
  await rename.press('Enter'); assert.equal(await step.locator('.graph-node-title').textContent(), '中文');
  await undo.click(); assert.equal(await name.inputValue(), 'compileXYZ');
  checks.push('Synthetic composition does not trigger graph history or premature rename (physical IME remains manual)');

  await step.click(); await page.keyboard.press('ArrowRight');
  await page.evaluate(() => { fixture.fail = true; });
  await page.locator('#command-form button[type=submit]').click();
  await page.waitForFunction(() => document.querySelector('#command-form button[type=submit]').disabled === false);
  await undo.click(); assert.equal(await form.getAttribute('data-dirty'), 'false');
  await redo.click(); assert.equal(await form.getAttribute('data-dirty'), 'true');
  checks.push('Save conflict preserves draft and history');
  await page.evaluate(() => fixture.locale('en-US'));
  assert.match(await undo.getAttribute('title'), /Undo: Move step/);
  await page.evaluate(() => fixture.locale('zh-CN'));
  await page.screenshot({path:path.join(output, 'graph-history.png'), fullPage:true});
  assert.deepEqual(errors, []);
  await writeFile(path.join(output, 'browser.json'), JSON.stringify({browser:browser.version(), checks, errors, physicalIME:'not tested'}, null, 2));
  console.log(JSON.stringify({browser:browser.version(), checks}, null, 2));
} finally {
  if (browser) await browser.close();
  await new Promise(resolve => server.close(resolve));
}
