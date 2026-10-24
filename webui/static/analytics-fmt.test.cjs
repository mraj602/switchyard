// node --test webui/static/*.test.cjs
// The numbers and sentences of the Analytics screen (components/analytics-fmt.js, analytics.md).

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
  return vm.runInNewContext(`(function () {\n${body}\nreturn { ${names.join(', ')} };\n})()`, { Number, String, Array, Object, Math, isNaN: Number.isNaN });
};

const A = mod('components/analytics-fmt.js');
const j = (x) => JSON.parse(JSON.stringify(x));

test('money: grouped, two decimals, tiny amounts and missing', () => {
  assert.equal(A.usd(0), '$0.00');
  assert.equal(A.usd(4.2), '$4.20');
  assert.equal(A.usd(1234567.891), '$1,234,567.89');
  assert.equal(A.usd(0.0001), '<$0.01');
  assert.equal(A.usd(null), '–');
  assert.equal(A.usd(NaN), '–');
});

test('token counts: k, M, B', () => {
  assert.equal(A.tokens(999), '999');
  assert.equal(A.tokens(1500), '1.5k');
  assert.equal(A.tokens(2_600_000), '2.6M');
  assert.equal(A.tokens(3_000_000_000), '3.0B');
  assert.equal(A.tokens(null), '–');
});

test('sum and cache hit rate', () => {
  const r = { input: 500, output: 100, cache_5m: 0, cache_1h: 0, cache_read: 1500 };
  assert.equal(A.tokenSum(r), 2100);
  assert.equal(A.cacheHit(r), 0.75);
  assert.equal(A.cacheHit({ output: 5 }), null);
});

test('bars: tallest is 100, a small day still shows, none stays 0', () => {
  assert.deepEqual(j(A.barHeights([0, 1, 100, 50])), [0, 2, 100, 50]);
  assert.deepEqual(j(A.barHeights([0, 0])), [0, 0]);
  assert.deepEqual(j(A.barHeights([])), []);
});

test('heat levels', () => {
  assert.equal(A.heatLevel(0, 10), 0);
  assert.equal(A.heatLevel(1, 0), 0);
  assert.equal(A.heatLevel(0.1, 10), 1);
  assert.equal(A.heatLevel(5, 10), 2);
  assert.equal(A.heatLevel(7.5, 10), 3);
  assert.equal(A.heatLevel(10, 10), 4);
});

test('days and query', () => {
  assert.equal(A.shortDay('2026-10-04'), '4 Oct');
  assert.equal(A.shortDay('garbage'), 'garbage');
  assert.deepEqual(j(A.params(7, '')), { days: '7' });
  assert.deepEqual(j(A.params(30, 'seat_a')), { days: '30', seat: 'seat_a' });
});

test('limit hit sentence', () => {
  assert.equal(A.hitText({ type: 'five_hour', wait_s: 7200 }), 'Hit the 5-hour limit · waited 2 h');
  assert.equal(A.hitText({ type: 'seven_day', wait_s: 3 * 86400 }), 'Hit the 7-day limit · waited 3 d');
  assert.equal(A.hitText({ type: 'five_hour', wait_s: 10 }), 'Hit the 5-hour limit · waited 1 min');
  assert.equal(A.hitText({ type: 'seven_day', wait_s: null }), 'Hit the 7-day limit');
  assert.equal(A.hitText({ type: null, wait_s: null }), 'Hit the limit');
});

test('projects: subagent folders fold into one row, top 10 then the rest', () => {
  const rows = [
    { name: 'watchyard', sessions: 3, api_usd: 40, input: 1 },
    { name: '/home/u/x/agent-a3f9c1d2', sessions: 1, api_usd: 10, input: 5 },
    { name: 'agent-0011223344', sessions: 2, api_usd: 12, input: 7 },
    ...Array.from({ length: 12 }, (_, i) => ({ name: `p${i}`, sessions: 1, api_usd: i })),
  ];
  const g = A.groupProjects(rows, 10);
  assert.equal(g.shown.length, 10);
  assert.equal(g.shown[0].name, 'watchyard');
  assert.equal(g.shown[1].name, 'Subagent folders');
  assert.equal(g.shown[1].folders, 2);
  assert.equal(g.shown[1].sessions, 3);
  assert.equal(g.shown[1].api_usd, 22);
  assert.equal(g.rest.length, 4);
  assert.equal(A.groupProjects([], 10).shown.length, 0);
  assert.equal(A.cacheSum({ cache_5m: 1, cache_1h: 2, cache_read: 3, input: 9 }), 6);
  assert.equal(A.tokens(1234.6), '1.2k');
});
