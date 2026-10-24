// node --test webui/static/*.test.cjs
// Settings, Notifications: what a sign-in without Web Push is told, and how the board knows it
// is the desktop app's Hub view (components/notif-note.js).

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const src = fs.readFileSync(path.join(__dirname, 'components/notif-note.js'), 'utf8').replace(/^export /gm, '');
const { notifNote, meInApp, markerInApp, ownerHint, ownerHow, signedOutReason, ownerLinkTarget } = vm.runInNewContext(
  `(function () {\n${src}\nreturn { notifNote, meInApp, markerInApp, ownerHint, ownerHow, signedOutReason, ownerLinkTarget };\n})()`, { URL, URLSearchParams });

test('the desktop app marks its Hub view: a user agent token or ?in=desktop', () => {
  assert.equal(markerInApp('Mozilla/5.0 (X11; Linux x86_64) Chrome/130.0 Electron/33.0 WatchyardDesktop/0.4.0', ''), true);
  assert.equal(markerInApp('Mozilla/5.0 Chrome/130.0', '?in=desktop'), true);
  assert.equal(markerInApp('Mozilla/5.0 Chrome/130.0', '?in=browser'), false);
  assert.equal(markerInApp('Mozilla/5.0 Chrome/130.0', ''), false);
  assert.equal(markerInApp(undefined, undefined), false);
});

test('a non-owner is told to open an owner link from an owner device; the app names its Hub tab; no CLI', () => {
  assert.equal(ownerHint(false), 'Sign in as owner: open an owner link from an owner device (More, Devices, Make another device owner).');
  assert.equal(ownerHint(true), 'Use Sign in as owner on the Hub tab.');
  for (const app of [true, false]) {
    assert.doesNotMatch(ownerHint(app), /wy hub|owner-link/);
    assert.doesNotMatch(ownerHow(app), /wy hub|owner-link|Settings, Hub/);
  }
});

test('the sign-in page says why a browser was signed out', () => {
  assert.equal(signedOutReason('revoked', false), 'This browser was removed from the hub.');
  assert.equal(signedOutReason('revoked', true), 'This phone was removed from the hub.');
  assert.equal(signedOutReason('owner_expired'), 'Your owner access ended.');
  assert.equal(signedOutReason('expired'), 'Your sign-in ended.');
  assert.equal(signedOutReason(''), '');
});

test('a pasted owner link must be for this hub', () => {
  const o = 'https://hub.example.ts.net';
  assert.deepEqual({ ...ownerLinkTarget(`  ${o}/#/owner?s=abcdefghijklmnopqrstuv  `, o) }, { hash: '#/owner?s=abcdefghijklmnopqrstuv' });
  const other = ownerLinkTarget('https://other.example/#/owner?s=abcdefghijklmnopqrstuv', o);
  assert.match(other.error, /not this hub/);
  assert.equal(other.other, 'https://other.example');
  assert.equal(other.link, 'https://other.example/#/owner?s=abcdefghijklmnopqrstuv');
  // Only an http(s) link is offered as a link to follow (never javascript: or data:).
  for (const bad of ['javascript:alert(1)', 'data:text/html,x']) {
    const r = ownerLinkTarget(bad, o);
    assert.ok(r.error, bad);
    assert.equal(r.other, undefined, bad);
    assert.equal(r.link, undefined, bad);
  }
  assert.match(ownerLinkTarget(`${o}/#/pair?s=x&c=1`, o).error, /not an owner link/);
  assert.match(ownerLinkTarget(`${o}/#/owner`, o).error, /not an owner link/);
  assert.match(ownerLinkTarget('', o).error, /Paste/);
});

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
  const appBoard = { device: { kind: 'browser', name: 'board via Watchyard-desktop-box', scopes: ['read'] }, owner: false, via: { id: 'dev_1', kind: 'desktop', name: 'Watchyard-desktop-box' } };
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
