import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

import * as C from './constants.js';

const read = (rel) => readFileSync(fileURLToPath(new URL(rel, import.meta.url)), 'utf8');

test('the shell CSP meta tag equals SHELL_CSP_META, the header adds frame-ancestors, as desktop.md C2 says', () => {
  const html = read('../renderer/index.html');
  const meta = html.match(/<meta http-equiv="Content-Security-Policy" content="([^"]+)">/);
  assert.ok(meta, 'CSP meta tag missing');
  assert.equal(meta[1], C.SHELL_CSP_META);
  assert.doesNotMatch(meta[1], /frame-ancestors/, 'frame-ancestors is ignored in a meta tag');
  assert.equal(C.SHELL_CSP, `${C.SHELL_CSP_META}; frame-ancestors 'none'`);
  assert.doesNotMatch(html, /<script(?![^>]*\bsrc=)[^>]*>/, 'no inline script');
  assert.doesNotMatch(html, /<style|style=/, 'no inline style');
  const spec = read('../../../docs/spec/desktop.md').replace(/\r?\n\s*/g, ' ');
  assert.ok(spec.includes(C.SHELL_CSP), 'desktop.md C2 CSP differs');
});

test('tab ids, partitions and folder names follow C1', () => {
  for (const ok of ['t_9c1e5f0a7b2d4e68', '1a2b3c4d']) assert.match(ok, C.TAB_ID_RE);
  for (const bad of ['t_9C1E5F0A7B2D4E68', 't_9c1e', '1a2b3c4', 'acct-1a2b3c4d', '', 't_9c1e5f0a7b2d4e68\n']) {
    assert.doesNotMatch(bad, C.TAB_ID_RE, JSON.stringify(bad));
  }
  assert.equal(C.TAB_PARTITION_PREFIX + 't_9c1e5f0a7b2d4e68', 'persist:tab-t_9c1e5f0a7b2d4e68');
  assert.equal(C.HUB_PARTITION, 'persist:sy-hub');
  assert.equal(C.USER_DATA_DIRNAME, 'switchyard-desktop');
  assert.notEqual(C.USER_DATA_DIRNAME.toLowerCase(), C.PROTOTYPE_DIRNAME);
  assert.notEqual(C.DESKTOP_ENTRY, C.PROTOTYPE_DESKTOP_ENTRY);
});

test('web preferences and permissions are the C2 baseline', () => {
  assert.deepEqual({ ...C.SAFE_WEB_PREFERENCES }, {
    contextIsolation: true, sandbox: true, nodeIntegration: false, nodeIntegrationInWorker: false,
    webSecurity: true, allowRunningInsecureContent: false, webviewTag: false, spellcheck: false,
  });
  assert.ok(Object.isFrozen(C.SAFE_WEB_PREFERENCES));
  assert.deepEqual([...C.PERMISSIONS.shell], []);
  assert.deepEqual([...C.PERMISSIONS.hub], ['clipboard-sanitized-write']);
});

test('the cadence and backoff are those of C5', () => {
  assert.equal(C.WARNING_VERSION, 1);
  assert.equal(C.CADENCE.PERIOD_MS, 300_000);
  assert.equal(C.CADENCE.FLOOR_MS, 60_000);
  assert.deepEqual([...C.BACKOFF_MS].map((ms) => ms / 60_000), [5, 10, 20, 40, 60]);
  assert.deepEqual([...C.STOP_STATUSES], [401, 403, 429]);
});

test('the package pins exact versions and names minSy', () => {
  const pkg = JSON.parse(read('../../package.json'));
  assert.equal(pkg.name, 'switchyard-desktop');
  assert.equal(pkg.productName, 'Switchyard');
  assert.equal(pkg.type, 'module');
  assert.equal(pkg.main, 'src/main/index.js');
  assert.equal(pkg.switchyard.minSy, '0.6.0');
  assert.equal(pkg.dependencies, undefined, 'no runtime dependencies');
  assert.deepEqual(Object.keys(pkg.devDependencies).sort(), ['@electron/fuses', 'electron', 'electron-builder', 'playwright-core']);
  for (const [n, v] of Object.entries(pkg.devDependencies)) assert.match(v, /^\d+\.\d+\.\d+$/, `${n} must be pinned exactly`);
  for (const s of ['dist', 'dist:linux', 'dist:mac', 'dist:win']) assert.match(pkg.scripts[s], /--publish never/, s);
});
