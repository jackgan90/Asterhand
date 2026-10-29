import {readFile, readdir} from 'node:fs/promises';
import {createHash} from 'node:crypto';
import {fileURLToPath, pathToFileURL} from 'node:url';

const root = new URL('../internal/quickinput/assets/vendor/katex/', import.meta.url);

export async function verifyFrontendAssets(directory = root) {
  const manifest = JSON.parse(await readFile(new URL('manifest.json', directory), 'utf8'));
  if (manifest.schema !== 1 || manifest.name !== 'katex' || manifest.license !== 'MIT' ||
      !/^\d+\.\d+\.\d+$/.test(manifest.version) ||
      manifest.source !== `https://registry.npmjs.org/katex/-/katex-${manifest.version}.tgz` ||
      !/^sha512-[A-Za-z0-9+/]{86}==$/.test(manifest.integrity)) throw Error('Invalid fixed KaTeX metadata');
  const expected = new Set(['manifest.json', ...Object.keys(manifest.files)]);
  for (const required of ['katex.mjs', 'katex.min.css', 'LICENSE']) {
    if (!expected.has(required)) throw Error(`Missing KaTeX ${required}`);
  }
  for (const [name, digest] of Object.entries(manifest.files)) {
    if (!/^(katex\.mjs|katex\.min\.css|LICENSE|fonts\/[A-Za-z0-9_-]+\.(?:woff2?|ttf))$/.test(name) ||
        !/^[a-f0-9]{64}$/.test(digest)) throw Error(`Invalid vendor asset ${name}`);
    const actual = createHash('sha256').update(await readFile(new URL(name, directory))).digest('hex');
    if (actual !== digest) throw Error(`Vendor asset checksum mismatch: ${name}`);
  }
  const actual = (await readdir(directory, {recursive: true, withFileTypes: true})).filter(entry => entry.isFile())
    .map(entry => fileURLToPath(pathToFileURL(entry.parentPath + '/' + entry.name)).slice(fileURLToPath(directory).length).replaceAll('\\', '/'));
  if (actual.length !== expected.size || actual.some(name => !expected.has(name))) throw Error('Unlisted KaTeX resource');
  const css = await readFile(new URL('katex.min.css', directory), 'utf8');
  for (const match of css.matchAll(/url\(([^)]+)\)/g)) {
    if (!manifest.files[match[1]]) throw Error(`Unlisted font ${match[1]}`);
  }
  return {version: manifest.version, files: actual.length};
}

if (process.argv[1] && pathToFileURL(process.argv[1]).href === import.meta.url) {
  console.log(await verifyFrontendAssets());
}
