// Opt-in rendering/interaction check using real assets and an in-memory API.
// No installed node, existing commands or scripts are touched.
import assert from 'node:assert/strict';
import {createServer} from 'node:http';
import {readFile, mkdir, writeFile} from 'node:fs/promises';
import path from 'node:path';
import {fileURLToPath, pathToFileURL} from 'node:url';

const repo=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'..');
const output=process.env.ASTERHAND_COMMAND_SELECTION_OUTPUT;
const modulePath=process.env.ASTERHAND_PLAYWRIGHT_MODULE;
const executablePath=process.env.ASTERHAND_BROWSER_EXECUTABLE;
if(!output || !modulePath || !executablePath)throw Error('Set ASTERHAND_COMMAND_SELECTION_OUTPUT, ASTERHAND_PLAYWRIGHT_MODULE and ASTERHAND_BROWSER_EXECUTABLE.');
const {chromium}=await import(pathToFileURL(modulePath).href);
await mkdir(output,{recursive:true});
const fixture=`
import {localCommands} from '/local-commands.mjs';
import {newGraph} from '/graph-model.mjs';
import {setLocale,applyI18n} from '/i18n.mjs';
setLocale('zh-CN');applyI18n();
let catalog={revision:'r1',commands:Array.from({length:35},(_,i)=>({id:'c'+i,revision:'v1',name:'build_'+String(i).padStart(2,'0'),graph:newGraph()}))};
window.fixture={sizes:[],confirm:true,reads:0,errors:[]};
const controller=localCommands({
 api:async path=>{
  if(path.endsWith('/state'))return {runs:{},managed_available:true};
  fixture.reads++;
  if(fixture.pauseReads)await new Promise((resolve,reject)=>{fixture.finishRead=fail=>{
   fixture.pauseReads=false;fail?reject(Error('refresh failed')):resolve();
  };});
  return catalog;
 },
 post:async(path,body)=>{
  fixture.sizes.push(body.items.length);
  const results=body.items.map(item=>item.command_id==='c20'?{receipt:{state:'rejected'},error:{code:'conflict',message_key:'err.localcmd.revision_conflict'}}:{receipt:{state:'completed'}});
  const deleted=new Set(body.items.filter((_,i)=>!results[i].error).map(item=>item.command_id));
  catalog={revision:'r'+(fixture.sizes.length+1),commands:catalog.commands.filter(command=>!deleted.has(command.id))};
  return {results};
 },session:()=>({active:true}),listen:(el,event,fn)=>el.addEventListener(event,fn),guarded:fn=>Promise.resolve().then(fn).catch(error=>fixture.errors.push(error.message)),actions:{confirm:()=>fixture.confirm}
});
fixture.open=()=>controller.open('c0');
fixture.locale=value=>{setLocale(value);applyI18n();controller.relocalize();};
await controller.open('c0');fixture.ready=true;
`;
const html=(await readFile(path.join(repo,'web/index.html'),'utf8')).replace('src="/app.js"','src="/selection-fixture.mjs"');
const server=createServer(async(request,response)=>{
  try {
    const url=new URL(request.url,'http://127.0.0.1');
    if(url.pathname==='/'){response.setHeader('Content-Type','text/html');response.end(html);return;}
    if(url.pathname==='/selection-fixture.mjs'){response.setHeader('Content-Type','text/javascript');response.end(fixture);return;}
    const root=path.join(repo,'web'),file=path.resolve(root,'.'+decodeURIComponent(url.pathname));
    if(!file.startsWith(root+path.sep)){response.writeHead(403).end();return;}
    response.setHeader('Content-Type',file.endsWith('.css')?'text/css':'text/javascript');response.end(await readFile(file));
  } catch {response.writeHead(404).end();}
});
await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
let browser;
try {
  browser=await chromium.launch({executablePath,headless:true});
  const page=await browser.newPage({viewport:{width:1440,height:1000}}),errors=[];
  page.on('pageerror',error=>errors.push(error.message));
  await page.goto(`http://127.0.0.1:${server.address().port}/`);
  await page.waitForFunction(()=>fixture.ready);
  await page.locator('#local-commands').evaluate(async panel=>{await Promise.all(panel.getAnimations().map(animation=>animation.finished));});
  const checks=page.locator('#registered-commands input[type=checkbox]');
  const manage=page.locator('#commands-manage'), toolbar=page.locator('#commands-selection');
  assert.equal(await toolbar.isVisible(),false);
  assert.equal(await checks.nth(0).isVisible(),false);
  const normalLeft=(await page.locator('.registered-command').first().boundingBox()).x;
  assert.ok(Math.abs(normalLeft-(await page.locator('#commands-search').boundingBox()).x)<1,'hidden checkboxes leave no gap');
  for (const theme of ['star','frost']) for (const locale of ['zh-CN','en-US']) {
    await page.evaluate(({theme,locale})=>{document.documentElement.dataset.theme=theme;fixture.locale(locale);},{theme,locale});
    assert.equal(await manage.textContent(),locale==='zh-CN'?'管理':'Manage');
    assert.equal(await manage.getAttribute('class'),'secondary');
    assert.equal(await page.locator('#command-new').getAttribute('class'),'secondary');
    const searchBox=await page.locator('#commands-search').boundingBox(),refreshBox=await page.locator('#commands-refresh').boundingBox();
    assert.ok(refreshBox.x>=searchBox.x+searchBox.width && Math.abs(refreshBox.y-searchBox.y)<1,'refresh sits beside search');
    assert.equal(await page.locator('#commands-refresh').getAttribute('title'),locale==='zh-CN'?'刷新':'Refresh');
    assert.equal(await page.locator('#commands-refresh').getAttribute('aria-label'),locale==='zh-CN'?'刷新':'Refresh');
    await page.screenshot({path:path.join(output,'normal-'+theme+'-'+locale+'.png'),fullPage:true,animations:'disabled'});
    await manage.click();
    assert.equal(await toolbar.isVisible(),true);
    assert.equal(await manage.textContent(),locale==='zh-CN'?'完成':'Done');
    assert.equal(await manage.getAttribute('class'),'');
    const newBox=await page.locator('#command-new').boundingBox(),manageBox=await manage.boundingBox();
    assert.ok(Math.abs(newBox.y+newBox.height/2-manageBox.y-manageBox.height/2)<1,'manage stays in the New button row');
    await page.screenshot({path:path.join(output,'manage-'+theme+'-'+locale+'.png'),fullPage:true,animations:'disabled'});
    await checks.nth(0).check(); await manage.click();
    assert.equal(await checks.nth(0).isVisible(),false);
    await manage.click(); assert.equal(await checks.nth(0).isChecked(),false); await manage.click();
  }
  await page.evaluate(()=>{document.documentElement.dataset.theme='star';fixture.locale('zh-CN');});
  const refresh=page.locator('#commands-refresh');
  for (const fail of [false,true]) {
    const reads=await page.evaluate(()=>{fixture.pauseReads=true;return fixture.reads;});
    await refresh.click();
    assert.equal(await refresh.isDisabled(),true);
    assert.equal(await refresh.getAttribute('aria-busy'),'true');
    await page.evaluate(()=>{fixture.locale('en-US');document.querySelector('#commands-refresh').click();});
    assert.equal(await refresh.isDisabled(),true);
    assert.equal(await page.evaluate(()=>fixture.reads),reads+1,'pending refresh ignores repeated click');
    await page.evaluate(fail=>fixture.finishRead(fail),fail);
    await page.waitForFunction(()=>document.querySelector('#commands-refresh').getAttribute('aria-busy')==='false');
    assert.equal(await refresh.isDisabled(),false);
  }
  assert.deepEqual(await page.evaluate(()=>fixture.errors),['refresh failed']);
  await page.evaluate(()=>fixture.locale('zh-CN'));
  await manage.click(); await checks.nth(0).check();
  await page.locator('#commands-close').click();
  await page.evaluate(()=>fixture.open());
  assert.equal(await toolbar.isVisible(),false);
  await manage.click(); assert.equal(await checks.nth(0).isChecked(),false);
  await checks.nth(0).check();await checks.nth(2).check();
  await page.locator('#commands-search').fill('build_01');
  assert.equal(await checks.count(),1);
  assert.match(await page.locator('#commands-selection-count').textContent(),/2/);
  await page.locator('#commands-select-all').check();
  assert.match(await page.locator('#commands-selection-count').textContent(),/35/);
  await page.locator('#commands-select-all').uncheck();
  assert.equal(await page.locator('#commands-delete-selected').isDisabled(),true);
  await page.locator('#commands-select-all').check();
  await page.evaluate(()=>fixture.confirm=false);
  await page.locator('#commands-delete-selected').click();
  assert.deepEqual(await page.evaluate(()=>fixture.sizes),[]);
  await page.locator('#commands-search').fill('');
  await page.evaluate(()=>fixture.locale('en-US'));
  await page.screenshot({path:path.join(output,'selection-en.png'),fullPage:true,animations:'disabled'});
  await page.evaluate(()=>{fixture.locale('zh-CN');fixture.confirm=true;});
  await page.screenshot({path:path.join(output,'selection-zh.png'),fullPage:true,animations:'disabled'});
  await page.locator('#commands-delete-selected').click();
  await page.waitForFunction(()=>fixture.sizes.length===2 && document.querySelectorAll('.registered-command-row').length===1);
  assert.deepEqual(await page.evaluate(()=>fixture.sizes),[32,3]);
  assert.equal(await checks.isChecked(),true);
  assert.equal(await page.locator('#command-form [name=name]').inputValue(),'');
  assert.equal(await page.locator('#commands-delete-results p').count(),1);
  assert.match(await page.locator('#commands-delete-results').textContent(),/^build_20:/);
  await page.screenshot({path:path.join(output,'partial-result.png'),fullPage:true,animations:'disabled'});
  assert.deepEqual(errors,[]);
  await writeFile(path.join(output,'result.json'),JSON.stringify({status:'passed',browser:browser.version(),checks:['neutral actions and active management','refresh loading and failure recovery','management visibility and reset','close and reopen','two themes and locales','multi-select','filter retains selection','select all 35','clear','cancel','locales','32+3 chunks','partial result','deleted editor reset'],errors},null,2));
  console.log('Browser command selection checks passed.');
} finally {await browser?.close();await new Promise(resolve=>server.close(resolve));}
