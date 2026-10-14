import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as fs from 'node:fs/promises';
import * as path from 'node:path';
import { tmpdir } from 'node:os';
import { fileURLToPath } from 'node:url';

import { create } from './protocol.js';
import { fakeElectron } from '../../test/fakes/electron.js';
import { SHELL_CSP, SHELL_CSP_META } from '../shared/constants.js';

const SRC = fileURLToPath(new URL('..', import.meta.url));
const quiet = { warn() {} };

function setup(srcDir = SRC) {
  const fake = fakeElectron();
  const p = create({ electron: fake.electron, fs, path, srcDir, log: quiet });
  p.registerSchemes();
  p.handle();
  const get = (url, init) => fake.electron.protocol._request(url, init);
  return { fake, p, get };
}

test('app:// is registered privileged as standard and secure, exactly', () => {
  const { fake } = setup();
  assert.deepEqual(fake.callsOf('protocol.registerSchemesAsPrivileged'), [[[{ scheme: 'app', privileges: { standard: true, secure: true } }]]]);
  assert.deepEqual(fake.callsOf('protocol.handle'), [['app']]);
});

test('mapping: shared/<p> → src/shared, everything else → src/renderer, / → index.html', () => {
  const { p } = setup();
  assert.equal(p.resolve('app://shell/index.html').file, path.join(SRC, 'renderer', 'index.html'));
  assert.equal(p.resolve('app://shell/').file, path.join(SRC, 'renderer', 'index.html'));
  assert.equal(p.resolve('app://shell').file, path.join(SRC, 'renderer', 'index.html'));
  assert.equal(p.resolve('app://shell/app.js?x=1#y').file, path.join(SRC, 'renderer', 'app.js'));
  assert.equal(p.resolve('app://shell/shared/ipc.js').file, path.join(SRC, 'shared', 'ipc.js'));
  assert.equal(p.resolve('app://shell/dev/mock-sy.js').file, path.join(SRC, 'renderer', 'dev', 'mock-sy.js'));
  assert.equal(p.resolve('app://shell/a%20b.css').file, path.join(SRC, 'renderer', 'a b.css'));
});

test('traversal and odd paths are refused with 403', () => {
  const { p } = setup();
  for (const u of [
    'app://shell/../package.json',
    'app://shell/%2e%2e/package.json',
    'app://shell/%2E%2E/%2E%2E/package.json',
    'app://shell/shared/../main/index.js',
    'app://shell/shared/..%2fmain%2findex.js',
    'app://shell/..%5cmain%5cindex.js',
    'app://shell/./index.html',
    'app://shell//index.html',
    'app://shell/.hidden.js',
    'app://shell/C:%5cx.js',
    'app://shell/%00.js',
    'app://shell/%zz.js',
    'app://shell/shared/',
    'app://shell/shared',
  ]) {
    assert.equal(p.resolve(u).status, 403, u);
  }
});

test('only the extension allow-list is served; tests are not', () => {
  const { p } = setup();
  for (const ok of ['a.html', 'a.js', 'a.css', 'a.png', 'a.svg', 'a.woff2', 'A.PNG']) assert.equal(p.resolve(`app://shell/${ok}`).status, 200, ok);
  for (const bad of ['a.json', 'a.mjs', 'a.cjs', 'a.map', 'a', 'a.html.bak', 'a.test.js', 'shared/ipc.test.js', 'index.HTML.txt']) {
    assert.equal(p.resolve(`app://shell/${bad}`).status, 403, bad);
  }
});

test('other hosts and schemes are 404', () => {
  const { p } = setup();
  assert.equal(p.resolve('app://other/index.html').status, 404);
  assert.equal(p.resolve('https://shell/index.html').status, 404);
  assert.equal(p.resolve('garbage').status, 404);
});

test('app://shell/../package.json → 403 through the handler, with the headers', async () => {
  const { get } = setup();
  const r = await get('app://shell/../package.json');
  assert.equal(r.status, 403);
  assert.equal(r.headers.get('content-security-policy'), SHELL_CSP);
  assert.equal(await r.text(), 'forbidden');
});

test('index.html is served with the CSP header (its meta tag plus frame-ancestors), nosniff and COOP', async () => {
  const { get } = setup();
  const r = await get('app://shell/index.html');
  assert.equal(r.status, 200);
  assert.equal(r.headers.get('content-type'), 'text/html; charset=utf-8');
  assert.equal(r.headers.get('content-security-policy'), SHELL_CSP);
  assert.equal(r.headers.get('x-content-type-options'), 'nosniff');
  assert.equal(r.headers.get('cross-origin-opener-policy'), 'same-origin');
  const html = await r.text();
  const meta = /<meta\s+http-equiv="Content-Security-Policy"\s+content="([^"]*)"/i.exec(html);
  assert.ok(meta, 'the meta tag is there');
  assert.equal(meta[1], SHELL_CSP_META);
  assert.equal(r.headers.get('content-security-policy'), `${meta[1]}; frame-ancestors 'none'`, 'the header is the meta policy plus frame-ancestors');
});

test('shared modules are served as JavaScript; a missing file is 404; non-GET is 405', async () => {
  const { get } = setup();
  const js = await get('app://shell/shared/constants.js');
  assert.equal(js.status, 200);
  assert.equal(js.headers.get('content-type'), 'text/javascript; charset=utf-8');
  assert.match(await js.text(), /SHELL_CSP/);
  assert.equal((await get('app://shell/nope.js')).status, 404);
  assert.equal((await get('app://shell/index.html', { method: 'POST', body: 'x' })).status, 405);
});

test('a symlink that leaves the root is refused', async (t) => {
  const dir = await fs.mkdtemp(path.join(tmpdir(), 'sy-proto-'));
  t.after(() => fs.rm(dir, { recursive: true, force: true }));
  await fs.mkdir(path.join(dir, 'renderer'));
  await fs.mkdir(path.join(dir, 'shared'));
  await fs.writeFile(path.join(dir, 'secret.js'), 'secret');
  await fs.writeFile(path.join(dir, 'renderer', 'ok.js'), 'ok');
  try {
    await fs.symlink(path.join(dir, 'secret.js'), path.join(dir, 'renderer', 'leak.js'));
  } catch {
    t.skip('symlinks not available');
    return;
  }
  const { get } = setup(dir);
  assert.equal((await get('app://shell/ok.js')).status, 200);
  assert.equal((await get('app://shell/leak.js')).status, 403);
});
