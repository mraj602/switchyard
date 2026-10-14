import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as path from 'node:path';

import { create, checkUserData } from './paths.js';
import * as testHooks from './test-hooks.js';
import { fakeElectron } from '../../test/fakes/electron.js';

const P = path.posix;

test('user data is <appData>/switchyard-desktop, set before ready', () => {
  const fake = fakeElectron({ appData: '/home/u/.config' });
  const { app } = fake.electron;
  const p = create({ app, path: P, env: {}, testHooks: null });
  assert.equal(p.userData, '/home/u/.config/switchyard-desktop');
  assert.equal(app.getPath('userData'), '/home/u/.config/switchyard-desktop');
  assert.equal(p.appStateFile, '/home/u/.config/switchyard-desktop/app-state.json');
  assert.equal(p.hubTokenFile, '/home/u/.config/switchyard-desktop/hub-token.bin');
  assert.deepEqual(fake.callsOf('app.setPath'), [['userData', '/home/u/.config/switchyard-desktop']]);
});

test('paths refuses to run after ready', () => {
  const { app } = fakeElectron().electron;
  app._ready();
  assert.throws(() => create({ app, path: P, env: {}, testHooks: null }), /before ready/);
});

test('SY_APP_USER_DATA overrides only when unpackaged', () => {
  const env = { SY_APP_USER_DATA: '/tmp/t1/ud' };
  const dev = fakeElectron({ isPackaged: false }).electron.app;
  assert.equal(create({ app: dev, path: P, env, testHooks }).userData, '/tmp/t1/ud');
  const packaged = fakeElectron({ isPackaged: true, appData: '/a' }).electron.app;
  assert.equal(create({ app: packaged, path: P, env, testHooks }).userData, '/a/switchyard-desktop');
  // No test hooks (packaged builds never import them): the variable is ignored.
  const dev2 = fakeElectron({ appData: '/a' }).electron.app;
  assert.equal(create({ app: dev2, path: P, env, testHooks: null }).userData, '/a/switchyard-desktop');
});

test('a relative SY_APP_USER_DATA is refused', () => {
  const app = fakeElectron().electron.app;
  assert.throws(() => create({ app, path: P, env: { SY_APP_USER_DATA: 'rel/dir' }, testHooks }), /absolute/);
});

test('a switchyard user-data folder is refused, in any case', () => {
  for (const dir of ['/home/u/.config/switchyard', '/x/Switchyard', '/x/SWITCHYARD/', 'C:\\Users\\u\\AppData\\Roaming\\SwitchYard']) {
    assert.throws(() => checkUserData(dir), /prototype/, dir);
  }
  for (const env of [{ SY_APP_USER_DATA: '/tmp/Switchyard' }, { SY_APP_USER_DATA: '/home/u/.config/switchyard/inner' }]) {
    const fake = fakeElectron({ appData: '/home/u/.config' });
    assert.throws(() => create({ app: fake.electron.app, path: P, env, testHooks }), /prototype/);
    assert.equal(fake.callsOf('app.setPath').length, 0, 'nothing is set when refused');
  }
  assert.throws(() => checkUserData('/home/u/.CONFIG/Switchyard/x', { path: P, appData: '/home/u/.config' }), /inside/);
  assert.doesNotThrow(() => checkUserData('/home/u/.config/switchyard-desktop', { path: P, appData: '/home/u/.config' }));
  assert.doesNotThrow(() => checkUserData('/home/u/projects/switchyard/tmp/ud', { path: P, appData: '/home/u/.config' }));
  assert.throws(() => checkUserData('relative', { path: P }), /absolute/);
});

test('Electron default (productName Switchyard) is never kept', () => {
  const fake = fakeElectron({ appData: '/Users/u/Library/Application Support' });
  assert.equal(fake.electron.app.getPath('userData'), '/Users/u/Library/Application Support/Switchyard');
  create({ app: fake.electron.app, path: P, env: {}, testHooks });
  assert.equal(fake.electron.app.getPath('userData'), '/Users/u/Library/Application Support/switchyard-desktop');
});

test('test hooks read and check their variables', () => {
  assert.equal(testHooks.userData({}), null);
  assert.equal(testHooks.claudeOrigin({ SY_APP_TEST_CLAUDE_ORIGIN: 'http://127.0.0.1:9/x' }), 'http://127.0.0.1:9');
  assert.throws(() => testHooks.claudeOrigin({ SY_APP_TEST_CLAUDE_ORIGIN: 'https://example.com' }), /loopback/);
  assert.equal(testHooks.cadenceMs({ SY_APP_TEST_CADENCE_MS: '2000' }), 2000);
  assert.throws(() => testHooks.cadenceMs({ SY_APP_TEST_CADENCE_MS: '5' }), /between/);
  assert.equal(testHooks.cadenceMs({}), null);
  assert.equal(testHooks.syBin({ SY_BIN: '/opt/sy' }), '/opt/sy');
  assert.throws(() => testHooks.syBin({ SY_BIN: 'sy' }), /absolute/);
});
