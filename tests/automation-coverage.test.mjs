import test from 'node:test';
import assert from 'node:assert/strict';
import {readFile,readdir,access} from 'node:fs/promises';
const root=new URL('../',import.meta.url);
const text=p=>readFile(new URL(p,root),'utf8');
const manifest=JSON.parse(await text('contracts/automation-coverage.json'));

test('every UI endpoint and closed native action has an explicit coverage classification',async()=>{
  const known=new Set([...manifest.groups.map(g=>g.id),...Object.keys(manifest.exceptions)]);
  const endpoints=new Set();
  for(const file of await readdir(new URL('web/',root))){
    if(!/\.(mjs|js)$/.test(file))continue;
    for(const match of (await text('web/'+file)).matchAll(/['"`]((?:\/api\/)[a-zA-Z0-9_\/-]+)/g))endpoints.add(match[1]);
  }
  assert.deepEqual([...endpoints].sort(),Object.keys(manifest.web_endpoints).sort(),'Update the coverage map and prove equivalent CLI/MCP behavior for new UI operations.');
  for(const classification of Object.values(manifest.web_endpoints))assert.ok(known.has(classification));
  for(const [file,mapping] of Object.entries(manifest.native_actions)){
    const source=await text(file);
    const actions=new Set([...source.matchAll(/case ((?:"[^"\n]+"\s*,?\s*)+):/g)].flatMap(m=>[...m[1].matchAll(/"([^"]+)"/g)].map(x=>x[1])));
    assert.deepEqual([...actions].sort(),Object.keys(mapping).sort(),file);
    for(const classification of Object.values(mapping))assert.ok(known.has(classification));
  }
  for(const group of manifest.groups){
    assert.ok(group.ui&&group.operations.length&&group.tests.length,group.id);
    for(const file of group.tests)await access(new URL(file,root));
  }
});
