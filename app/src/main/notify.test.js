import { test } from 'node:test';
import assert from 'node:assert/strict';

import { create } from './notify.js';
import { createBus } from './bus.js';
import { fakeElectron } from '../../test/fakes/electron.js';

function setup({ notifications = true, showing = false, url = '' } = {}) {
  const fake = fakeElectron();
  const bus = createBus();
  const routes = [];
  const shown = [];
  const hub = {
    route: async (r) => routes.push(r),
    view: () => ({ webContents: { getURL: () => url } }),
    seats: () => [{ id: 'st1', label: 'client' }],
  };
  const window = { isShowing: (id) => showing && id === 'hub', showWindow: () => shown.push('show') };
  const store = { get: () => ({ settings: { notifications } }) };
  const n = create({ electron: fake.electron, bus, hub, window, store, log: console });
  n.start();
  return { fake, bus, routes, shown, n };
}

const fired = (id = 'al1') => ({
  alert: { id, trigger: 'asked', session: 's1' },
  session: { id: 's1', seat_label: 'work', project: 'api', detail: 'question', question: 'Proceed?' },
});

test('a fired alert gives one notification, a second fired for the same id gives none', () => {
  const { fake, bus } = setup();
  bus.emit('alert', fired());
  bus.emit('alert', fired());
  assert.equal(fake.notifications.length, 1);
  assert.equal(fake.notifications[0].options.title, 'work · api');
  assert.equal(fake.notifications[0].options.body, 'asked a question\nProceed?');
  assert.equal(fake.notifications[0].shown, true);
  bus.emit('alert', fired('al2'));
  assert.equal(fake.notifications.length, 2);
});

test('a notification carries the app icon when given one, so libnotify shows the logo', () => {
  const fake = fakeElectron();
  const bus = createBus();
  const n = create({
    electron: fake.electron, bus, log: console, icon: '/app/assets/icon.png',
    hub: { route: async () => {}, view: () => null, seats: () => [] },
    window: { isShowing: () => false, showWindow: () => {} },
    store: { get: () => ({ settings: { notifications: true } }) },
  });
  n.start();
  bus.emit('alert', fired('al9'));
  assert.equal(fake.notifications[0].options.icon, '/app/assets/icon.png');
  n.stop();
  assert.equal('icon' in setupShown().options, false);
});

function setupShown() {
  const { fake, bus } = setup();
  bus.emit('alert', fired('al8'));
  return fake.notifications[0];
}

test('a click shows the window and calls hub.route with the deep link', async () => {
  const { fake, bus, routes, shown } = setup();
  bus.emit('alert', fired());
  fake.notifications[0]._click();
  assert.deepEqual(shown, ['show']);
  assert.deepEqual(routes, ['#/sessions/s1?alert=al1']);
});

test('a limit alert routes to the accounts view', () => {
  const { fake, bus, routes } = setup();
  bus.emit('alert', { alert: { id: 'l1', trigger: 'limit_80', seat: 'st1', window_key: 'five_hour', title: 'client · 5-hour at 82%' }, session: null });
  assert.equal(fake.notifications[0].options.title, 'client');
  assert.equal(fake.notifications[0].options.body, '5-hour at 82 %');
  fake.notifications[0]._click();
  assert.deepEqual(routes, ['#/accounts?seat=st1']);
});

test('settings.notifications false shows nothing', () => {
  const { fake, bus } = setup({ notifications: false });
  bus.emit('alert', fired());
  assert.equal(fake.notifications.length, 0);
});

test('focus rule: focused on the Hub view of that very session shows nothing', () => {
  const on = setup({ showing: true, url: 'https://hub.example/#/sessions/s1?alert=al1' });
  on.bus.emit('alert', fired());
  assert.equal(on.fake.notifications.length, 0);
  const other = setup({ showing: true, url: 'https://hub.example/#/sessions/s2' });
  other.bus.emit('alert', fired());
  assert.equal(other.fake.notifications.length, 1);
  const unfocused = setup({ showing: false, url: 'https://hub.example/#/sessions/s1' });
  unfocused.bus.emit('alert', fired());
  assert.equal(unfocused.fake.notifications.length, 1);
});

test('stop() unsubscribes', () => {
  const { fake, bus, n } = setup();
  n.stop();
  bus.emit('alert', fired());
  assert.equal(fake.notifications.length, 0);
});
