import { test } from 'node:test';
import assert from 'node:assert/strict';

import { create, parseArgv } from './background.js';
import { createBus } from './bus.js';
import { fakeElectron } from '../../test/fakes/electron.js';

function setup({ argv = [], settings = {}, trayOn = true, singleInstance = true, onQuit } = {}) {
  const fake = fakeElectron({ singleInstance });
  const bus = createBus();
  const events = [];
  bus.on('settings', (s) => events.push(s));
  let state = { settings: { keep_running: true, notifications: true, start_hidden: false, close_notice_shown: false, ...settings } };
  const store = {
    get: () => structuredClone(state),
    async update(fn) {
      const d = structuredClone(state);
      fn(d);
      state = d;
      return d;
    },
  };
  let login = false;
  const autostartCalls = [];
  const autostart = { get: async () => login, set: async (on) => (autostartCalls.push(on), (login = on)) };
  const shown = [];
  const win = fake.electron.BrowserWindow ? new fake.electron.BrowserWindow({}) : null;
  const window = { win, showWindow: () => shown.push('show') };
  const tray = { available: () => trayOn };
  const quits = [];
  const bg = create({
    electron: fake.electron, window, store, autostart, tray, argv, bus, platform: 'linux', log: console,
    onQuit: onQuit ?? (async () => quits.push('flush')),
  });
  return { fake, bg, win, shown, autostartCalls, events, quits, store: () => state };
}

const tick = () => new Promise((r) => setImmediate(r));

test('parseArgv', () => {
  assert.deepEqual(parseArgv(['x', '--background', '--autostart=on']), { background: true, autostart: true });
  assert.deepEqual(parseArgv(['--autostart=off']), { background: false, autostart: false });
  assert.deepEqual(parseArgv(['--autostart=maybe']), { background: false, autostart: null });
});

test('claim: the lock decides, startHidden follows --background only', () => {
  assert.equal(setup().bg.claim(), true);
  assert.equal(setup({ singleInstance: false }).bg.claim(), false);
  assert.equal(setup({ argv: ['--background'] }).bg.startHidden(), true);
  assert.equal(setup({ settings: { start_hidden: true } }).bg.startHidden(), false); // a stale setting never hides a normal launch
  assert.equal(setup().bg.startHidden(), false);
});

test('a second launch shows the window; with --autostart it applies it and shows nothing', async () => {
  const { fake, bg, shown, autostartCalls, events } = setup();
  bg.claim();
  fake.electron.app.emit('second-instance', {}, ['app']);
  assert.deepEqual(shown, ['show']);
  fake.electron.app.emit('second-instance', {}, ['app', '--autostart=on']);
  await tick();
  assert.deepEqual(shown, ['show']);
  assert.deepEqual(autostartCalls, [true]);
  assert.equal(events.at(-1).autostart, true);
});

test('a second launch from the app menu shows and focuses the window even with start_hidden stored; a login launch does not', () => {
  const { fake, bg, shown } = setup({ settings: { start_hidden: true } });
  bg.claim();
  fake.electron.app.emit('second-instance', {}, ['app', '--no-sandbox']);
  assert.deepEqual(shown, ['show']);
  fake.electron.app.emit('second-instance', {}, ['app', '--background']);
  assert.deepEqual(shown, ['show']);
});

test('this launch with --autostart=off applies it, exits and claim() is false', async () => {
  const { fake, bg, autostartCalls } = setup({ argv: ['x', '--autostart=off'] });
  assert.equal(bg.claim(), false);
  await bg.applied();
  assert.deepEqual(autostartCalls, [false]);
  assert.deepEqual(fake.callsOf('app.exit'), [[0]]);
});

test('close hides to the tray and shows the notice once', async () => {
  const { fake, bg, win, store } = setup();
  bg.claim();
  bg.start();
  win.visible = true;
  win.close();
  await tick();
  assert.equal(win.destroyed, false);
  assert.equal(win.visible, false);
  assert.equal(fake.notifications.length, 1);
  assert.match(fake.notifications[0].options.body, /tray/);
  assert.equal(store().settings.close_notice_shown, true);
  win.close();
  await tick();
  assert.equal(fake.notifications.length, 1, 'the notice is one-time');
});

test('without a tray the notice explains it', async () => {
  const { fake, bg, win } = setup({ trayOn: false });
  bg.claim();
  bg.start();
  win.close();
  await tick();
  assert.match(fake.notifications[0].options.body, /no tray icon/);
});

test('keep_running false: closing the window quits, through the flush', async () => {
  const { fake, bg, win, quits } = setup({ settings: { keep_running: false } });
  bg.claim();
  bg.start();
  win.close();
  await tick();
  await tick();
  assert.deepEqual(quits, ['flush']);
  assert.ok(fake.callsOf('app.quit').length >= 1);
  assert.equal(fake.notifications.length, 0);
});

test('before-quit waits for onQuit, then quits for real, once', async () => {
  let release;
  const { fake, bg, quits } = setup({ onQuit: () => new Promise((r) => (release = () => (quits.push('flush'), r()))) });
  bg.claim();
  bg.start();
  fake.electron.app.quit();
  assert.equal(fake.callsOf('app.quit').length, 1, 'held while flushing');
  assert.equal(bg.quitting(), true);
  release();
  await tick();
  assert.equal(fake.callsOf('app.quit').length, 2);
  assert.deepEqual(quits, ['flush']);
});

test('setSettings: stores booleans, drives autostart, emits settings, ignores junk', async () => {
  const { bg, store, autostartCalls, events } = setup();
  const s = await bg.setSettings({ notifications: false, autostart: true, keep_running: 'yes', bogus: 1 });
  assert.equal(store().settings.notifications, false);
  assert.equal(store().settings.keep_running, true);
  assert.equal('bogus' in store().settings, false);
  assert.deepEqual(autostartCalls, [true]);
  assert.deepEqual(s, {
    keep_running: true, notifications: false, start_hidden: false, autostart: true, close_notice_shown: false, storage_backend: 'gnome_libsecret',
  });
  assert.deepEqual(events.at(-1), s);
});
