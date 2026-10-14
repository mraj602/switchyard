import { test } from 'node:test';
import assert from 'node:assert/strict';

import { create } from './menu.js';
import { createBus } from './bus.js';
import { fakeElectron } from '../../test/fakes/electron.js';

function setup({ isPackaged = true, platform = 'linux', withSettings = true, keep = true, login = false } = {}) {
  const fake = fakeElectron({ platform });
  const bus = createBus();
  const calls = [];
  const tabsList = [{ id: 't_aaaaaaaaaaaaaaaa', name: 'work' }, { id: 't_bbbbbbbbbbbbbbbb', name: 'home' }];
  const m = create({
    electron: fake.electron, bus, isPackaged, platform,
    window: { select: (id) => calls.push(['select', id]), showWindow: () => calls.push(['show']) },
    tabs: { list: () => tabsList },
    usage: { refresh: async () => calls.push(['refresh']) },
    autostart: { get: async () => login },
    store: { get: () => ({ settings: { keep_running: keep } }) },
    setSettings: withSettings ? async (p) => calls.push(['set', p]) : undefined,
  });
  return { fake, m, calls };
}

const last = (fake) => fake.callsOf('Menu.buildFromTemplate').at(-1)[0];

test('build sets the application menu: Switchyard menu, editMenu, no devtools when packaged', async () => {
  const { fake, m } = setup();
  await m.build();
  const t = last(fake);
  assert.equal(t[0].label, 'Switchyard');
  assert.deepEqual(t[0].submenu.map((i) => i.label ?? '-'), [
    'Hub', 'Account 1: work', 'Account 2: home', '-', 'Refresh usage', '-', 'Run at login', 'Keep running', '-', 'Quit',
  ]);
  assert.deepEqual(t[1], { role: 'editMenu' });
  assert.equal(t.length, 2);
  assert.ok(fake.callsOf('Menu.setApplicationMenu').length >= 1);
});

test('toggleDevTools only when unpackaged', async () => {
  const { fake, m } = setup({ isPackaged: false });
  await m.build();
  assert.deepEqual(last(fake)[2], { label: 'View', submenu: [{ role: 'toggleDevTools' }] });
});

test('items: hub, account, refresh, checkboxes reflect state and patch settings', async () => {
  const { fake, m, calls } = setup({ login: true, keep: false });
  await m.build();
  const sub = last(fake)[0].submenu;
  const by = (l) => sub.find((i) => i.label === l);
  assert.equal(by('Run at login').checked, true, 'refreshed from the OS');
  assert.equal(by('Keep running').checked, false);
  by('Hub').click();
  by('Account 2: home').click();
  by('Refresh usage').click();
  by('Run at login').click({ checked: false });
  by('Keep running').click({ checked: true });
  by('Quit').click();
  assert.deepEqual(calls, [
    ['select', 'hub'], ['show'], ['select', 't_bbbbbbbbbbbbbbbb'], ['show'], ['refresh'],
    ['set', { autostart: false }], ['set', { keep_running: true }],
  ]);
  assert.equal(fake.callsOf('app.quit').length, 1);
  assert.equal(by('Account 1: work').accelerator, 'CmdOrCtrl+1');
});

test('the checkboxes are disabled until settings are wired', async () => {
  const { fake, m } = setup({ withSettings: false });
  await m.build();
  assert.equal(last(fake)[0].submenu.find((i) => i.label === 'Run at login').enabled, false);
});

test('darwin names the first menu after the app; bus events rebuild', async () => {
  const { fake, m } = setup({ platform: 'darwin' });
  await m.build();
  assert.equal(last(fake)[0].label, 'Switchyard');
  const before = fake.callsOf('Menu.setApplicationMenu').length;
  // rebuilds are wired on first build via the bus
  const bus = createBus();
  const m2 = create({
    electron: fake.electron, bus, isPackaged: true, platform: 'linux', window: {}, tabs: { list: () => [] }, usage: {},
    autostart: { get: async () => false }, store: { get: () => ({ settings: {} }) },
  });
  await m2.build();
  const n = fake.callsOf('Menu.setApplicationMenu').length;
  bus.emit('tabs', { tabs: [] });
  await new Promise((r) => setImmediate(r));
  assert.ok(fake.callsOf('Menu.setApplicationMenu').length > n);
  assert.ok(n > before);
});
