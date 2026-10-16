import { test } from 'node:test';
import assert from 'node:assert/strict';

import { create, WINDOW, TOP_BAR_PX, STRIP_PX, BACKGROUND } from './window.js';
import * as security from './security.js';
import { fakeElectron } from '../../test/fakes/electron.js';
import { SAFE_WEB_PREFERENCES, SHELL_URL } from '../shared/constants.js';

const quiet = { debug() {}, info() {}, warn() {}, error() {} };
const TAB_A = 't_9c1e5f0a7b2d4e68';
const TAB_B = 't_0000000000000001';

function setup(opts = {}) {
  const fake = fakeElectron();
  const e = fake.electron;
  const sec = security.create({ electron: e, log: quiet });
  sec.applyGlobal();
  const w = create({ electron: e, security: sec, preloadPath: '/app/src/preload/shell.cjs', startHidden: false, log: quiet, ...opts });
  const tabView = (id) => new e.WebContentsView({ webPreferences: sec.webPreferences('tab', { partition: `persist:tab-${id}` }) });
  return { fake, e, sec, w, tabView };
}

test('one window, 1440×940, min 720×500, hidden until ready-to-show, with the safe shell preferences', async () => {
  const { fake, w } = setup();
  const [[opts]] = fake.callsOf('BrowserWindow.new');
  assert.equal(opts.width, WINDOW.WIDTH);
  assert.equal(opts.height, WINDOW.HEIGHT);
  assert.equal(opts.minWidth, 720);
  assert.equal(opts.minHeight, 500);
  assert.equal(opts.show, false);
  assert.equal(opts.autoHideMenuBar, true);
  assert.equal(opts.backgroundColor, BACKGROUND.light);
  assert.deepEqual(opts.webPreferences, { ...SAFE_WEB_PREFERENCES, preload: '/app/src/preload/shell.cjs' });
  await new Promise((r) => setImmediate(r));
  assert.deepEqual(fake.callsOf('webContents.loadURL').map((c) => c[1]), [SHELL_URL]);
  assert.equal(w.win.isVisible(), false);
  w.win.emit('ready-to-show');
  assert.equal(w.win.isVisible(), true);
  assert.equal(w.shell, w.win.webContents);
});

test('the window carries the app icon when given one (Linux, Windows), none otherwise', () => {
  const { fake } = setup({ icon: '/app/assets/icon.png' });
  assert.equal(fake.callsOf('BrowserWindow.new')[0][0].icon, '/app/assets/icon.png');
  const plain = setup();
  assert.equal('icon' in plain.fake.callsOf('BrowserWindow.new')[0][0], false);
});

test('startHidden keeps the window hidden on ready-to-show; showWindow shows, restores and focuses', () => {
  const { w } = setup({ startHidden: true });
  w.win.emit('ready-to-show');
  assert.equal(w.win.isVisible(), false);
  w.win.minimized = true;
  w.showWindow();
  assert.equal(w.win.isVisible(), true);
  assert.equal(w.win.isMinimized(), false);
  assert.equal(w.win.isFocused(), true);
});

test('the shell webContents and the default session are guarded as the shell', () => {
  const { w, e, fake } = setup();
  assert.equal(w.shell._cancellable('will-navigate', 'https://claude.ai/'), true);
  assert.deepEqual(w.shell._windowOpen('https://claude.ai/'), { action: 'deny' });
  assert.ok(fake.callsOf('session.setPermissionRequestHandler').some((c) => c[0] === ''));
  assert.equal(e.session.defaultSession.checkPermission('notifications', 'app://shell'), false);
});

test('layout: the selected view fills the window under the top bar (and the strip for a tab); others are hidden', () => {
  const { w, e, tabView } = setup();
  w.win.setBounds({ width: 1200, height: 800 });
  const hub = new e.WebContentsView({});
  const a = tabView(TAB_A);
  const b = tabView(TAB_B);
  w.addView('hub', hub, 'hub');
  w.addView(TAB_A, a, 'tab');
  w.addView(TAB_B, b, 'tab');
  assert.deepEqual(w.win.contentView.children, [hub, a, b]);
  assert.equal(w.selected(), 'hub');
  assert.deepEqual(hub.getBounds(), { x: 0, y: TOP_BAR_PX, width: 1200, height: 800 - TOP_BAR_PX });
  assert.deepEqual([hub.visible, a.visible, b.visible], [true, false, false]);

  w.select(TAB_A);
  assert.deepEqual(a.getBounds(), { x: 0, y: TOP_BAR_PX + STRIP_PX, width: 1200, height: 800 - TOP_BAR_PX - STRIP_PX });
  assert.deepEqual([hub.visible, a.visible, b.visible], [false, true, false]);

  w.win.setBounds({ width: 720, height: 500 }); // resize
  assert.deepEqual(a.getBounds(), { x: 0, y: 88, width: 720, height: 412 });

  w.select('settings');
  assert.deepEqual([hub.visible, a.visible, b.visible], [false, false, false], 'settings shows the shell only');
});

test('select notifies listeners on change; removeView of the selected view falls back', () => {
  const { w, tabView } = setup();
  const seen = [];
  const off = w.onSelect((id) => seen.push(id));
  const a = tabView(TAB_A);
  const b = tabView(TAB_B);
  w.addView(TAB_A, a, 'tab');
  w.addView(TAB_B, b, 'tab');
  w.select(TAB_B);
  w.select(TAB_B);
  w.removeView(TAB_B);
  assert.equal(w.selected(), TAB_A);
  assert.equal(a.visible, true);
  assert.ok(!w.win.contentView.children.includes(b));
  w.removeView(TAB_A);
  assert.equal(w.selected(), 'hub');
  off();
  w.select('settings');
  assert.deepEqual(seen, [TAB_B, TAB_A, 'hub']);
});

test('setViewHidden keeps the selected view out of sight (the hub state panel shows) until shown again', () => {
  const { w, e } = setup();
  const hub = new e.WebContentsView({});
  w.addView('hub', hub, 'hub');
  assert.equal(hub.visible, true);
  w.setViewHidden('hub', true);
  assert.equal(hub.visible, false);
  w.win.setBounds({ width: 900, height: 700 }); // a resize does not bring it back
  assert.equal(hub.visible, false);
  w.setViewHidden('hub', false);
  assert.equal(hub.visible, true);
});

test('addView checks its arguments', () => {
  const { w, e, tabView } = setup();
  const a = tabView(TAB_A);
  w.addView(TAB_A, a, 'tab');
  assert.throws(() => w.addView(TAB_A, tabView(TAB_A), 'tab'), /already/);
  assert.throws(() => w.addView('x', new e.WebContentsView({}), 'shell'), /bad kind/);
  assert.throws(() => w.addView('settings', new e.WebContentsView({}), 'tab'), /bad id/);
  assert.throws(() => w.addView('other', new e.WebContentsView({}), 'hub'), /'hub'/);
  assert.throws(() => w.addView('hub', new e.WebContentsView({}), 'tab'), /'hub'/);
});

test('isShowing: visible, focused, not minimised and selected', () => {
  const { w } = setup();
  assert.equal(w.isShowing('hub'), false);
  w.showWindow();
  assert.equal(w.isShowing('hub'), true);
  assert.equal(w.isShowing(TAB_A), false);
  w.win.minimized = true;
  assert.equal(w.isShowing('hub'), false);
  w.win.minimized = false;
  w.win.focused = false;
  assert.equal(w.isShowing('hub'), false);
});

test('dark theme background', () => {
  const fake = fakeElectron();
  const e = { ...fake.electron, nativeTheme: { shouldUseDarkColors: true } };
  const sec = security.create({ electron: e, log: quiet });
  create({ electron: e, security: sec, preloadPath: '/p.cjs', startHidden: false, log: quiet });
  assert.equal(fake.callsOf('BrowserWindow.new')[0][0].backgroundColor, BACKGROUND.dark);
});
