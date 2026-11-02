import test from 'node:test';
import assert from 'node:assert/strict';
import {logDecoder} from '../internal/quickinput/assets/log-model.mjs';
import {timeoutSeconds,runBusy,runStoppable} from '../web/command-run.mjs';

test('stream decoders retain independent split characters and escape nothing into markup',()=>{
  const decode=logDecoder();
  const bytes=new TextEncoder().encode('中文<script>x</script>');
  const chunk=(stream,raw)=>({stream,data:Buffer.from(raw).toString('base64')});
  let out=decode.decode(chunk('stdout',bytes.slice(0,2))).text;
  const err=decode.decode(chunk('stderr',new TextEncoder().encode('error'))).text;
  out+=decode.decode(chunk('stdout',bytes.slice(2))).text;
  assert.equal(out,'中文<script>x</script>');assert.equal(err,'error');
  assert.ok(decode.finish().every(chunk=>chunk.text===''));
});
test('timeout units reject fractional seconds and overflow without rounding',()=>{
  assert.equal(timeoutSeconds('',60),null);assert.equal(timeoutSeconds('1.5',60),90);
  for(const value of ['0','-1','1.5','Infinity','9223372037'])assert.throws(()=>timeoutSeconds(value,1));
});
test('run controls preserve draining and stopping admission boundaries',()=>{
  assert.equal(runBusy({phase:'draining'}),true);assert.equal(runStoppable({phase:'draining'}),false);
  assert.equal(runStoppable({phase:'stopping'}),false);assert.equal(runStoppable({phase:'running'}),true);
  assert.equal(runBusy({phase:'finished',result:'failed'}),false);
});
