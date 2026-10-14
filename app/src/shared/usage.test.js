import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';

import { isClaudeUrl, isReadableUrl, pickOrg, planOf, stripWindows, windowsOf } from './usage.js';

const root = new URL('../../../testdata/usage/', import.meta.url);
const golden = JSON.parse(fs.readFileSync(new URL('./usage.golden.json', import.meta.url), 'utf8'));
const fixture = (f) => JSON.parse(fs.readFileSync(new URL(f, root), 'utf8'));
const files = JSON.parse(fs.readFileSync(new URL('index.json', root), 'utf8')).map((e) => e.file);

// usage.golden.json is the output of the prototype's own windowsOf / planOf (main.js, read-only)
// on these fixtures.
test('windowsOf matches the prototype on every fixture', () => {
  assert.deepEqual(Object.keys(golden.windows).sort(), [...files].sort());
  for (const f of files) assert.deepEqual(JSON.parse(JSON.stringify(windowsOf(fixture(f)))), golden.windows[f], f);
});

test('planOf matches the prototype', () => {
  for (const { org, plan } of golden.plans) assert.equal(planOf(org), plan, JSON.stringify(org));
});

test('every utilization key is shown, unknown keys under their raw name, scoped weeks from limits', () => {
  const w = windowsOf({
    five_hour: { utilization: 41.26, resets_at: 'r1' },
    mystery_counter: { utilization: 7, resets_at: 'r2' },
    seven_day: null,
    seven_day_opus: { utilization: null },
    limits: [
      { kind: 'weekly_scoped', group: 'weekly', percent: 12.4, resets_at: 'r3', scope: { model: { display_name: 'Fable' } }, is_active: true },
      { kind: 'session', percent: 1 },
    ],
  });
  assert.deepEqual(
    w.map((x) => [x.key, x.label, x.pct]),
    [['five_hour', '5-hour', 41.3], ['mystery_counter', 'mystery_counter', 7], ['weekly_fable', '7-day · Fable', 12]],
  );
});

test('stripWindows: the shell shape, inactive scoped weeks left out', () => {
  const s = stripWindows(fixture('response-01.json'));
  assert.deepEqual(s.map((x) => x.key), ['five_hour', 'seven_day', 'iguana_necktie']);
  assert.deepEqual(s[0], { key: 'five_hour', label: '5-hour', used_pct: 2, resets_at: '2026-10-10T07:39:59.867309+00:00' });
});

test('pickOrg: the active org, else the one with the most windows', () => {
  const a = { org: { uuid: 'a' }, usage: { five_hour: { utilization: 1 } } };
  const b = { org: { uuid: 'b' }, usage: { five_hour: { utilization: 1 }, seven_day: { utilization: 2 } } };
  const bad = { org: { uuid: 'c' }, usage: { __status: 500 } };
  assert.equal(pickOrg([a, b, bad], 'a').org.uuid, 'a');
  assert.equal(pickOrg([a, b, bad], 'zzz').org.uuid, 'b');
  assert.equal(pickOrg([a, b, bad], null).org.uuid, 'b');
  assert.equal(pickOrg([bad], null), null);
  assert.equal(pickOrg([], null), null);
});

test('isClaudeUrl and isReadableUrl', () => {
  assert.ok(isClaudeUrl('https://claude.ai/new'));
  assert.ok(isClaudeUrl('https://www.claude.ai/x'));
  assert.ok(!isClaudeUrl('http://claude.ai/new'));
  assert.ok(!isClaudeUrl('https://claude.ai.evil.com/'));
  assert.ok(!isClaudeUrl('https://example.com/'));
  assert.ok(!isClaudeUrl('not a url'));
  assert.ok(!isClaudeUrl(''));
  assert.ok(isClaudeUrl('http://127.0.0.1:9000/new', 'http://127.0.0.1:9000'), 'the test fake origin');
  assert.ok(!isClaudeUrl('http://127.0.0.1:9001/new', 'http://127.0.0.1:9000'));
  assert.ok(isReadableUrl('https://claude.ai/chat/abc'));
  assert.ok(isReadableUrl('https://claude.ai/'));
  for (const p of ['/login', '/login/', '/logout', '/login/magic']) assert.ok(!isReadableUrl(`https://claude.ai${p}`), p);
  assert.ok(!isReadableUrl('about:blank'));
});
