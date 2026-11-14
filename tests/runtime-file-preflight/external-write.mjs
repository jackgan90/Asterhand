import {writeFileSync, renameSync} from 'node:fs';

// Separate OS process: it does not participate in OpenClaw's mutation queue.
const [mode, target, content] = process.argv.slice(2);
if (mode === 'replace') {
  writeFileSync(`${target}.replacement`, content, {flag: 'wx'});
  renameSync(`${target}.replacement`, target);
} else if (mode === 'create' || mode === 'write') {
  writeFileSync(target, content, {flag: mode === 'create' ? 'wx' : 'w'});
} else {
  throw new Error('Unknown external mutation mode');
}
