import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { CDP_SWITCHES, cdpAllowed, refusedCdpSwitch } from './cdp.js';

const appDir = fileURLToPath(new URL('../../', import.meta.url));
const none = () => false;

test('packaged: each remote debugging switch is refused, in argv or on the command line', () => {
  assert.deepEqual([...CDP_SWITCHES], ['remote-debugging-port', 'remote-debugging-pipe', 'remote-debugging-address']);
  const exe = '/opt/Watchyard/watchyard-desktop';
  for (const a of ['--remote-debugging-port=9222', '--remote-debugging-port', '-remote-debugging-port=1', '--remote-debugging-pipe', '--remote-debugging-address=0.0.0.0', '--REMOTE-DEBUGGING-PORT=9']) {
    assert.ok(refusedCdpSwitch({ isPackaged: true, hasSwitch: none, argv: [exe, a], pkg: {} }), a);
  }
  for (const name of CDP_SWITCHES) {
    assert.equal(refusedCdpSwitch({ isPackaged: true, hasSwitch: (n) => n === name, argv: [exe], pkg: {} }), name);
  }
  assert.equal(refusedCdpSwitch({ isPackaged: true, hasSwitch: none, argv: [exe, '--ozone-platform=x11', '--remote-debugging-portx=1'], pkg: {} }), null);
  // a throwing commandLine still lets argv decide
  assert.equal(refusedCdpSwitch({ isPackaged: true, hasSwitch: () => { throw new Error('x'); }, argv: [exe, '--remote-debugging-pipe'], pkg: null }), 'remote-debugging-pipe');
});

test('the build-time flag (watchyard.allowCdp) allows them; unpackaged runs are never refused', () => {
  const argv = ['x', '--remote-debugging-port=9222'];
  assert.equal(refusedCdpSwitch({ isPackaged: true, hasSwitch: none, argv, pkg: { watchyard: { minSy: '0.6.0', allowCdp: true } } }), null);
  assert.equal(refusedCdpSwitch({ isPackaged: true, hasSwitch: none, argv, pkg: { watchyard: { allowCdp: 'true' } } }), null);
  for (const pkg of [null, {}, { watchyard: {} }, { watchyard: { allowCdp: false } }, { watchyard: { allowCdp: 1 } }, { allowCdp: true }]) {
    assert.equal(refusedCdpSwitch({ isPackaged: true, hasSwitch: none, argv, pkg }), 'remote-debugging-port', JSON.stringify(pkg));
  }
  assert.equal(refusedCdpSwitch({ isPackaged: false, hasSwitch: () => true, argv, pkg: null }), null);
});

test('the source package.json (what release builds pack) never allows CDP', () => {
  const pkg = JSON.parse(readFileSync(appDir + 'package.json', 'utf8'));
  assert.equal(cdpAllowed(pkg), false);
  assert.equal('allowCdp' in (pkg.watchyard ?? {}), false);
  const yml = readFileSync(appDir + 'electron-builder.yml', 'utf8');
  assert.doesNotMatch(yml, /allowCdp/, 'electron-builder.yml (release config) must not set it');
  const e2e = readFileSync(appDir + 'scripts/dist-e2e.sh', 'utf8');
  assert.match(e2e, /-c\.extraMetadata\.watchyard\.allowCdp=true/);
});
