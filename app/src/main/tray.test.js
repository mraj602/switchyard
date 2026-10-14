import { test } from 'node:test';
import assert from 'node:assert/strict';

import { create } from './tray.js';
import { createBus } from './bus.js';
import { fakeElectron } from '../../test/fakes/electron.js';

function setup(platform, { confirm = async () => true, throwTray = false } = {}) {
  const fake = fakeElectron({ platform });
  if (throwTray) {
    fake.electron.Tray = class {
      constructor() {
        throw new Error('no appindicator');
      }
    };
  }
  const bus = createBus();
  const calls = [];
  const win = { isDestroyed: () => false, setOverlayIcon: (...a) => calls.push(['overlay', ...a]) };
  const window = { win, showWindow: () => calls.push(['show']) };
  const usage = { refresh: async () => calls.push(['refresh']), pauseAll: async () => calls.push(['pause']) };
  const hub = { route: async (r) => calls.push(['route', r]) };
  const tray = create({ electron: fake.electron, bus, window, usage, hub, platform, assetsDir: '/assets', confirm });
  return { fake, bus, calls, tray };
}

const needs = (count, sessions = []) => ({ count, sessions });
const labels = (menu) => menu.template.map((i) => i.label ?? '-');
const imgs = (fake) => fake.callsOf('Tray.setImage').map(([p]) => p);

test('start() creates the tray with the empty image', () => {
  const { fake, tray } = setup('linux');
  assert.equal(tray.start(), true);
  assert.equal(tray.available(), true);
  assert.deepEqual(fake.callsOf('Tray.new')[0][0], '/assets/tray/tray-0.png');
});

test('start() is false when the tray cannot be made', () => {
  const { tray } = setup('linux', { throwTray: true });
  assert.equal(tray.start(), false);
  assert.equal(tray.available(), false);
});

test('linux: badge count through app.setBadgeCount, image by key', () => {
  const { fake, bus, tray } = setup('linux');
  tray.start();
  bus.emit('needs_you', needs(3));
  bus.emit('needs_you', needs(12));
  assert.deepEqual(fake.callsOf('app.setBadgeCount').map((a) => a[0]), [0, 3, 12]);
  assert.deepEqual(imgs(fake).slice(-2), ['/assets/tray/tray-3.png', '/assets/tray/tray-9plus.png']);
  assert.equal(fake.callsOf('Tray.setTitle').length, 0);
});

test('darwin: tray title and dock badge', () => {
  const { fake, bus, tray } = setup('darwin');
  tray.start();
  bus.emit('needs_you', needs(4));
  bus.emit('needs_you', needs(10));
  assert.deepEqual(fake.callsOf('Tray.setTitle').map((a) => a[0]), ['', '4', '9+']);
  assert.deepEqual(fake.callsOf('app.dock.setBadge').map((a) => a[0]), ['', '4', '9+']);
  assert.equal(fake.callsOf('app.setBadgeCount').length, 0);
});

test('win32: overlay icon, none at zero', () => {
  const { calls, bus, tray } = setup('win32');
  tray.start();
  bus.emit('needs_you', needs(2));
  bus.emit('needs_you', needs(0));
  const overlays = calls.filter((c) => c[0] === 'overlay');
  assert.equal(overlays[1][1].path, '/assets/tray/tray-2.png');
  assert.equal(overlays[2][1], null);
});

test('menu: open, up to five sessions with routes, refresh, pause (confirmed), quit', async () => {
  const { fake, bus, calls, tray } = setup('linux');
  tray.start();
  const sessions = Array.from({ length: 7 }, (_, i) => ({ id: `s${i}`, route: `#/sessions/s${i}`, title: `work · api${i} · asked` }));
  bus.emit('needs_you', needs(7, sessions));
  const menu = fake.trays[0].menu;
  assert.deepEqual(labels(menu), [
    'Open Switchyard', '-', 'work · api0 · asked', 'work · api1 · asked', 'work · api2 · asked', 'work · api3 · asked', 'work · api4 · asked',
    '-', 'Refresh usage', 'Pause all reading', '-', 'Quit',
  ]);
  const by = (l) => menu.template.find((i) => i.label === l);
  by('Open Switchyard').click();
  by('work · api1 · asked').click();
  by('Refresh usage').click();
  await by('Pause all reading').click();
  by('Quit').click();
  assert.deepEqual(calls, [['show'], ['show'], ['route', '#/sessions/s1'], ['refresh'], ['pause']]);
  assert.equal(fake.callsOf('app.quit').length, 1);
});

test('pause all is not run without the confirmation', async () => {
  const { fake, calls, tray } = setup('linux', { confirm: async () => false });
  tray.start();
  await fake.trays[0].menu.template.find((i) => i.label === 'Pause all reading').click();
  assert.deepEqual(calls, []);
});

test('an empty list says nothing needs you; stop() destroys the tray', () => {
  const { fake, tray } = setup('linux');
  tray.start();
  assert.ok(labels(fake.trays[0].menu).includes('Nothing needs you'));
  tray.stop();
  assert.equal(fake.trays[0].destroyed, true);
  assert.equal(tray.available(), false);
});

test('start() takes the newest needs_you already on the bus', () => {
  const { fake, bus, tray } = setup('linux');
  bus.emit('needs_you', needs(5));
  tray.start();
  assert.equal(fake.trays[0].image.path, '/assets/tray/tray-5.png');
});
