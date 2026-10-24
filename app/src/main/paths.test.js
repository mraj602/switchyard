import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as path from 'node:path';

import { create, checkUserData } from './paths.js';
import * as testHooks from './test-hooks.js';
import { fakeElectron } from '../../test/fakes/electron.js';

const P = path.posix;

test('user data is <appData>/watchyard-desktop, set before ready', () => {
  const fake = fakeElectron({ appData: '/home/u/.config' });
  const { app } = fake.electron;
  const p = create({ app, path: P, env: {}, testHooks: null });
  assert.equal(p.userData, '/home/u/.config/watchyard-desktop');
  assert.equal(app.getPath('userData'), '/home/u/.config/watchyard-desktop');
  assert.equal(p.appStateFile, '/home/u/.config/watchyard-desktop/app-state.json');
  assert.equal(p.hubTokenFile, '/home/u/.config/watchyard-desktop/hub-token.bin');
  assert.deepEqual(fake.callsOf('app.setPath'), [['userData', '/home/u/.config/watchyard-desktop']]);
});

test('paths refuses to run after ready', () => {
  const { app } = fakeElectron().electron;
  app._ready();
  assert.throws(() => create({ app, path: P, env: {}, testHooks: null }), /before ready/);
});

test('WY_APP_USER_DATA overrides only when unpackaged', () => {
  const env = { WY_APP_USER_DATA: '/tmp/t1/ud' };
  const dev = fakeElectron({ isPackaged: false }).electron.app;
  assert.equal(create({ app: dev, path: P, env, testHooks }).userData, '/tmp/t1/ud');
  const packaged = fakeElectron({ isPackaged: true, appData: '/a' }).electron.app;
  assert.equal(create({ app: packaged, path: P, env, testHooks }).userData, '/a/watchyard-desktop');
  // No test hooks (packaged builds never import them): the variable is ignored.
  const dev2 = fakeElectron({ appData: '/a' }).electron.app;
  assert.equal(create({ app: dev2, path: P, env, testHooks: null }).userData, '/a/watchyard-desktop');
});

test('a relative WY_APP_USER_DATA is refused', () => {
  const app = fakeElectron().electron.app;
  assert.throws(() => create({ app, path: P, env: { WY_APP_USER_DATA: 'rel/dir' }, testHooks }), /absolute/);
});

test('a missing or relative user-data folder is refused, an absolute one accepted', () => {
  assert.throws(() => checkUserData(''), /missing/);
  assert.throws(() => checkUserData('relative', { path: P }), /absolute/);
  assert.doesNotThrow(() => checkUserData('/home/u/.config/watchyard-desktop', { path: P }));
  assert.doesNotThrow(() => checkUserData('C:\\Users\\u\\AppData\\Roaming\\watchyard-desktop'));
});

test('Electron default (productName Watchyard) is never kept', () => {
  const fake = fakeElectron({ appData: '/Users/u/Library/Application Support' });
  assert.equal(fake.electron.app.getPath('userData'), '/Users/u/Library/Application Support/Watchyard');
  create({ app: fake.electron.app, path: P, env: {}, testHooks });
  assert.equal(fake.electron.app.getPath('userData'), '/Users/u/Library/Application Support/watchyard-desktop');
});

test('test hooks read and check their variables', () => {
  assert.equal(testHooks.userData({}), null);
  assert.equal(testHooks.claudeOrigin({ WY_APP_TEST_CLAUDE_ORIGIN: 'http://127.0.0.1:9/x' }), 'http://127.0.0.1:9');
  assert.throws(() => testHooks.claudeOrigin({ WY_APP_TEST_CLAUDE_ORIGIN: 'https://example.com' }), /loopback/);
  assert.equal(testHooks.cadenceMs({ WY_APP_TEST_CADENCE_MS: '2000' }), 2000);
  assert.throws(() => testHooks.cadenceMs({ WY_APP_TEST_CADENCE_MS: '5' }), /between/);
  assert.equal(testHooks.cadenceMs({}), null);
  assert.equal(testHooks.wyBin({ WY_BIN: '/opt/wy' }), '/opt/wy');
  assert.throws(() => testHooks.wyBin({ WY_BIN: 'wy' }), /absolute/);
});
