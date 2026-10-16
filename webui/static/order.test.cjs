// node --test webui/static/*.test.cjs
// The order of the accounts: dragging cards on the Accounts page (components/order.js).

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
  return vm.runInNewContext(`(function () {\n${body}\nreturn { ${names.join(', ')} };\n})()`, { Math, Map, Set, Array, Object });
};
const raw = mod('components/order.js');
// The module runs in its own realm: copy results across so deepEqual compares plain values.
const plain = (v) => (v === undefined || v === null ? v : JSON.parse(JSON.stringify(v)));
const wrap = (f) => (...a) => { const r = f(...a); return r === a[0] && Array.isArray(r) ? r : plain(r); };
const { applyOrder, moveTo, moveBy, dropTarget, dropOrder, moveSentence } = Object.fromEntries(Object.entries(raw).map(([k, f]) => [k, wrap(f)]));

const seats = (...ids) => ids.map((id, i) => ({ id, order: i, label: id }));
const idsOf = (list) => Array.from(list, (s) => s.id);

test('applyOrder puts the listed seats first and keeps the rest after them', () => {
  const out = applyOrder(seats('a', 'b', 'c', 'd'), ['c', 'a']);
  assert.deepEqual(idsOf(out), ['c', 'a', 'b', 'd']);
  assert.deepEqual(Array.from(out, (s) => s.order), [0, 1, 2, 3]);
});

test('applyOrder skips unknown and repeated ids and does not change its input', () => {
  const input = seats('a', 'b', 'c');
  const out = applyOrder(input, ['zzz', 'b', 'b']);
  assert.deepEqual(idsOf(out), ['b', 'a', 'c']);
  assert.deepEqual(idsOf(input), ['a', 'b', 'c']);
  assert.equal(input[1].order, 1);
  assert.deepEqual(idsOf(applyOrder(input, [])), ['a', 'b', 'c']);
  assert.deepEqual(idsOf(applyOrder(null, ['a'])), []);
});

test('moveTo moves a card before or after another', () => {
  const ids = ['a', 'b', 'c', 'd'];
  assert.deepEqual(moveTo(ids, 'd', 'b', false), ['a', 'd', 'b', 'c']);
  assert.deepEqual(moveTo(ids, 'a', 'c', true), ['b', 'c', 'a', 'd']);
  assert.deepEqual(moveTo(ids, 'b', 'a', false), ['b', 'a', 'c', 'd']);
  assert.deepEqual(moveTo(ids, 'a', 'd', true), ['b', 'c', 'd', 'a']);
});

test('moveTo returns the same array when nothing changes', () => {
  const ids = ['a', 'b', 'c'];
  assert.equal(moveTo(ids, 'a', 'a', true), ids);
  assert.equal(moveTo(ids, 'a', 'b', false), ids); // already just before b
  assert.equal(moveTo(ids, 'b', 'a', true), ids);  // already just after a
  assert.equal(moveTo(ids, 'x', 'a', true), ids);
  assert.equal(moveTo(ids, 'a', 'x', true), ids);
});

test('moveBy moves one place and stops at the ends', () => {
  const ids = ['a', 'b', 'c'];
  assert.deepEqual(moveBy(ids, 'b', -1), ['b', 'a', 'c']);
  assert.deepEqual(moveBy(ids, 'b', 1), ['a', 'c', 'b']);
  assert.equal(moveBy(ids, 'a', -1), ids);
  assert.equal(moveBy(ids, 'c', 1), ids);
  assert.equal(moveBy(ids, 'x', 1), ids);
});

// Two columns, two rows: a b / c d, each 100 x 50 with a 10 gap.
const grid = [
  { id: 'a', left: 0, top: 0, width: 100, height: 50 },
  { id: 'b', left: 110, top: 0, width: 100, height: 50 },
  { id: 'c', left: 0, top: 60, width: 100, height: 50 },
  { id: 'd', left: 110, top: 60, width: 100, height: 50 },
];

test('dropTarget takes the card under the point, after it past the middle', () => {
  assert.deepEqual(dropTarget(grid, 20, 10), { target: 'a', after: false });
  assert.deepEqual(dropTarget(grid, 90, 10), { target: 'a', after: true });
  assert.deepEqual(dropTarget(grid, 150, 100), { target: 'd', after: false });
  assert.deepEqual(dropTarget(grid, 200, 100), { target: 'd', after: true });
});

test('dropTarget takes the nearest card outside every card, and handles no cards', () => {
  assert.deepEqual(dropTarget(grid, 105, 5), { target: 'a', after: true }); // in the gap, nearer a
  assert.deepEqual(dropTarget(grid, 300, 30), { target: 'b', after: true }); // right of the grid
  assert.deepEqual(dropTarget(grid, 40, 500), { target: 'c', after: true }); // far below: below c's middle
  assert.equal(dropTarget([], 1, 1), null);
  assert.equal(dropTarget(null, 1, 1), null);
});

test('dropTarget in one column goes by the vertical middle', () => {
  const col = [
    { id: 'a', left: 0, top: 0, width: 300, height: 100 },
    { id: 'b', left: 0, top: 110, width: 300, height: 100 },
  ];
  assert.deepEqual(dropTarget(col, 10, 120), { target: 'b', after: false });
  assert.deepEqual(dropTarget(col, 290, 120), { target: 'b', after: true });
});

test('dropOrder gives the new order, or null for a drop that changes nothing', () => {
  const ids = ['a', 'b', 'c', 'd'];
  assert.deepEqual(dropOrder(ids, 'a', { target: 'd', after: true }), ['b', 'c', 'd', 'a']);
  assert.equal(dropOrder(ids, 'a', { target: 'a', after: true }), null);
  assert.equal(dropOrder(ids, 'a', { target: 'b', after: false }), null);
  assert.equal(dropOrder(ids, 'a', null), null);
});

test('moveSentence says where the card went', () => {
  assert.equal(moveSentence('work', ['a', 'b', 'c'], 'b'), 'work moved to 2 of 3');
  assert.equal(moveSentence('work', ['a'], 'x'), '');
});
