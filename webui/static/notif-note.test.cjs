// node --test webui/static/*.test.cjs
// Settings, Notifications: what a sign-in without Web Push is told, and how the board knows it
// is the desktop app's Hub view (components/notif-note.js).

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const src = fs.readFileSync(path.join(__dirname, 'components/notif-note.js'), 'utf8').replace(/^export /gm, '');
const { notifNote, meInApp } = vm.runInNewContext(`(function () {\n${src}\nreturn { notifNote, meInApp };\n})()`, {});

test('the desktop app is told it shows notifications itself, not to run owner-link', () => {
  const n = notifNote({ app: true, mobile: false });
  assert.equal(n.who, 'This app');
  assert.match(n.state, /desktop app shows these notifications itself/);
  assert.equal(n.how, 'app-settings');
});

test('a desktop browser signs in as the owner or pairs; a phone pairs with a code', () => {
  assert.deepEqual({ ...notifNote({ app: false, mobile: false }) }, { who: 'This browser', state: 'Off: this sign-in only reads the board.', how: 'owner-or-pair' });
  assert.deepEqual({ ...notifNote({ app: false, mobile: true }) }, { who: 'This phone', state: 'Off: this sign-in only reads the board.', how: 'pair-phone' });
  assert.equal(notifNote().who, 'This browser');
});

test('the Hub view of the desktop app: a session opened from a desktop device (via), not the browser kind', () => {
  // What the hub answers for the app's cookie session: a browser device, via the desktop device.
  const appBoard = { device: { kind: 'browser', name: 'board via Switchyard-desktop-box', scopes: ['read'] }, owner: false, via: { id: 'dev_1', kind: 'desktop', name: 'Switchyard-desktop-box' } };
  assert.equal(meInApp(appBoard), true);
  // an owner session opened in the app keeps the via
  assert.equal(meInApp({ device: { kind: 'browser' }, owner: true, via: { kind: 'desktop' } }), true);
  // the desktop token itself
  assert.equal(meInApp({ device: { kind: 'desktop' } }), true);
  // a browser signed in with a pasted browser token, a phone, an owner link in a browser
  assert.equal(meInApp({ device: { kind: 'browser' }, via: { kind: 'browser' } }), false);
  assert.equal(meInApp({ device: { kind: 'phone' }, via: null }), false);
  assert.equal(meInApp({ device: { kind: 'browser' }, owner: true }), false);
  assert.equal(meInApp(null), false);
});
