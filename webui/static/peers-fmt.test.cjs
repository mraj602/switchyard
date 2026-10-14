// node --test webui/static/*.test.cjs
// The matrix arithmetic and sentences of Settings → Peers (components/peers-fmt.js, peers.md 5, 10).

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const mod = (f) => {
  const src = fs.readFileSync(path.join(__dirname, f), 'utf8');
  if (/^import /m.test(src)) throw new Error(`${f} has imports; test only pure modules`);
  const names = [...src.matchAll(/^export (?:async )?(?:function|const|let|class) ([A-Za-z0-9_]+)/gm)].map((m) => m[1]);
  const body = src.replace(/^export /gm, '');
  return vm.runInNewContext(`(function () {\n${body}\nreturn { ${names.join(', ')} };\n})()`, { Number, String, Array, Object, Set, Map });
};

const P = mod('components/peers-fmt.js');
const A = 'acct-a', B = 'acct-b', C = 'acct-c';
// Objects from the vm have another realm's prototypes: compare through JSON.
const j = (x) => JSON.parse(JSON.stringify(x));

test('cells: none, one way, back and both', () => {
  assert.equal(P.cellState([], A, B), 'none');
  assert.equal(P.cellState([[A, B]], A, B), 'out');
  assert.equal(P.cellState([[A, B]], B, A), 'in');
  assert.equal(P.cellState([[A, B], [B, A]], A, B), 'both');
  assert.equal(P.cellState([[A, C]], A, B), 'none');
  assert.equal(P.CELL_GLYPH.both, '⇄');
});

test('an edit allows what it adds and denies what it removes; only adding widens', () => {
  const both = [[A, B], [B, A]];
  assert.deepEqual(j(P.cellEdit([], A, B, 'out')), { allow: [[A, B]], deny: [], widens: true });
  assert.deepEqual(j(P.cellEdit([], A, B, 'in')), { allow: [[B, A]], deny: [], widens: true });
  assert.deepEqual(j(P.cellEdit([], A, B, 'both')), { allow: [[A, B], [B, A]], deny: [], widens: true });
  assert.deepEqual(j(P.cellEdit(both, A, B, 'none')), { allow: [], deny: [[A, B], [B, A]], widens: false });
  assert.deepEqual(j(P.cellEdit(both, A, B, 'out')), { allow: [], deny: [[B, A]], widens: false });
  // One way to the other way is a swap: it adds a pair, so it widens.
  assert.deepEqual(j(P.cellEdit([[A, B]], A, B, 'in')), { allow: [[B, A]], deny: [[A, B]], widens: true });
  assert.deepEqual(j(P.cellEdit([[A, B]], A, B, 'out')), { allow: [], deny: [], widens: false });
});

test('edit body carries the base version and only what changes', () => {
  const m = { version: 4 };
  assert.deepEqual(j(P.editBody(m, { allow: [], deny: [[A, B]], widens: false })), { edit: { base_version: 4, deny: [[A, B]] } });
  assert.deepEqual(j(P.editBody(m, { same_account: false })), { edit: { base_version: 4, same_account: false } });
  assert.deepEqual(j(P.editBody(m, { paused: true })), { edit: { base_version: 4, paused: true } });
});

test('why a change is not available', () => {
  const ok = { widens: false, owner: false, push: true, offline: false };
  assert.equal(P.whyNot(ok), '');
  assert.equal(P.whyNot({ ...ok, owner: true, widens: true }), '');
  assert.match(P.whyNot({ ...ok, widens: true }), /owner sign-in/);
  assert.match(P.whyNot({ ...ok, push: false }), /only read/);
  assert.match(P.whyNot({ ...ok, offline: true }), /Offline/);
});

test('accounts: visible seats first, then accounts only the matrix names', () => {
  const seats = [{ account: A, label: 'work' }, { account: B, label: 'home', hidden: true }, { account: C, email: 'c@x' }, { account: A, label: 'dup' }];
  const m = { accounts: { 'acct-z': 'old' }, allow: [[A, 'acct-z']] };
  assert.deepEqual(j(P.peerAccounts(seats, m).map((a) => [a.account, a.label])), [[A, 'work'], [C, 'c@x'], ['acct-z', 'old']]);
});

test('audit sentences show metadata only', () => {
  assert.equal(P.eventLabel('pause'), 'All peers paused');
  assert.equal(P.eventLabel('some_new_event'), 'some new event');
  assert.equal(P.detailText({ version: 3, widens: true, paused: false, pairs: 1, text: 'secret', nested: { a: 1 } }), 'v3 · widened · not paused · 1 pair');
  assert.equal(P.detailText({ size: 2400, status: 'refused' }), '2.4 KB · refused');
  assert.equal(P.detailText(null), '');
  assert.equal(P.auditWho({ actor: 'phone', detail: {} }), 'phone');
  assert.equal(P.auditWho({ actor: 'hub', detail: { from: 'a', to: 'b' } }, (x) => x.toUpperCase()), 'A → B');
  assert.equal(P.messageWho({ from: { account: A }, to: {} }, (x) => x), 'acct-a → ?');
  assert.equal(P.outcomeLabel('target_gone'), 'target gone');
  assert.equal(P.sizeLabel(12), '12 B');
  assert.equal(P.sizeLabel(34000), '34 KB');
});
