import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createHubView } from './view.js';
import { fakeElectron } from '../../../test/fakes/electron.js';

const BASE = 'http://127.0.0.1:8787';

function setup({ sessionFetch } = {}) {
  const fake = fakeElectron({ sessionFetch: sessionFetch ?? (async () => new Response(null, { status: 204 })) });
  const logs = [];
  const log = { warn: (m) => logs.push(m), error: (m) => logs.push(m), debug: (m) => logs.push(m), info: (m) => logs.push(m) };
  const v = createHubView({
    electron: fake.electron,
    security: { webPreferences: () => ({}), guardSession() {}, guardView() {} },
    window: { addView() {}, removeView() {}, select() {}, setViewHidden() {} },
    log,
    base: () => BASE,
    auth: () => ({ url: BASE, token: 'wy_d_good' }),
    onSessionLink() {},
    onOwner() {},
    onLoginRefused() {},
  });
  return { fake, v, logs };
}

test('a failed owner-link load logs only the error code, never the link (its secret)', async () => {
  const { v, logs } = setup();
  const view = v.ensure();
  const link = `${BASE}/#/owner?s=ONE-TIME-SECRET`;
  view.webContents.loadURL = async (u) => {
    const err = new Error(`ERR_ABORTED (-3) loading '${u}'`);
    err.code = 'ERR_ABORTED';
    err.errno = -3;
    err.url = u;
    throw err;
  };
  assert.equal(await v.loadOwnerLink(link), true);
  assert.ok(logs.length >= 1, 'the failure is logged');
  assert.ok(logs.every((m) => !m.includes('ONE-TIME-SECRET') && !m.includes('#/owner')), logs.join('\n'));
  assert.ok(logs.some((m) => m.includes('ERR_ABORTED')));
  // no code at all: still no message
  view.webContents.loadURL = async (u) => {
    throw new Error(`failed ${u}`);
  };
  await v.loadOwnerLink(link);
  assert.ok(logs.every((m) => !m.includes('ONE-TIME-SECRET')));
});

test('the view dropped while a re-login or a load awaits: no null dereference', async () => {
  let releaseLogin;
  const { v, fake } = setup({
    sessionFetch: () => new Promise((r) => (releaseLogin = () => r(new Response(null, { status: 204 })))),
  });
  const view = v.ensure();
  view.webContents._setURL(`${BASE}/#/login`);
  // the board lands on #/login: main signs in again (parked on the login POST) ...
  view.webContents.emit('did-navigate', {}, `${BASE}/#/login`);
  await new Promise((r) => setTimeout(r, 10));
  v.reset(); // ... and the hub URL changes meanwhile
  releaseLogin();
  await new Promise((r) => setTimeout(r, 20));
  assert.equal(v.view(), null);

  // reset while the navigation itself awaits: the reload after it must not touch a null view
  const w = v.ensure();
  w.webContents._setURL(`${BASE}/#/pair`);
  w.webContents.loadURL = async () => {
    v.reset();
  };
  let unhandled = null;
  const onUnhandled = (e) => (unhandled = e);
  process.on('unhandledRejection', onUnhandled);
  try {
    w.webContents.emit('did-navigate', {}, `${BASE}/#/pair`);
    await new Promise((r) => setTimeout(r, 10));
    releaseLogin();
    await new Promise((r) => setTimeout(r, 30));
  } finally {
    process.off('unhandledRejection', onUnhandled);
  }
  assert.equal(unhandled, null, String(unhandled?.stack ?? ''));
  assert.equal(fake.callsOf('webContents.reload').length, 0);
});

test('only the board cookie counts as a session', async () => {
  for (const name of ['wy_session', 'other']) {
    const { v, fake } = setup();
    await fake.electron.session.fromPartition('persist:wy-hub').cookies.set({ url: BASE, name, value: 'v' });
    assert.equal(await v.hasCookie(), name !== 'other', name);
  }
});
