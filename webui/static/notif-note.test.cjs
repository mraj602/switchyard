// node --test webui/static/*.test.cjs
// Settings, Notifications: what a sign-in without Web Push is told (components/notif-note.js).

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const src = fs.readFileSync(path.join(__dirname, 'components/notif-note.js'), 'utf8').replace(/^export /gm, '');
const { notifNote } = vm.runInNewContext(`(function () {\n${src}\nreturn { notifNote };\n})()`, {});

test('the desktop app is told it shows notifications itself, not to run owner-link', () => {
  const n = notifNote('desktop', false);
  assert.equal(n.who, 'This app');
  assert.match(n.state, /desktop app shows these notifications itself/);
  assert.equal(n.how, 'app-settings');
});

test('a desktop browser signs in as the owner or pairs; a phone pairs with a code', () => {
  assert.deepEqual({ ...notifNote('browser', false) }, { who: 'This browser', state: 'Off: this sign-in only reads the board.', how: 'owner-or-pair' });
  assert.deepEqual({ ...notifNote('phone', true) }, { who: 'This phone', state: 'Off: this sign-in only reads the board.', how: 'pair-phone' });
  assert.equal(notifNote(undefined, false).who, 'This browser');
});
