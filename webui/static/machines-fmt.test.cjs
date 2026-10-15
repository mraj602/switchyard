// node --test webui/static/*.test.cjs
// The sentences of the Machines screen (components/machines-fmt.js, docs/spec/machines.md 6.1).

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
  return vm.runInNewContext(`(function () {\n${body}\nreturn { ${names.join(', ')} };\n})()`, { Date, Number, String, Math, Array, Object, Set, Map, isNaN: Number.isNaN });
};

const M = mod('components/machines-fmt.js');
const F = mod('format.js');
const f = { ago: F.ago, agoLabel: F.agoLabel, clock: F.clock };
const NOW = Date.parse('2026-10-12T14:30:00Z');
const at = (min) => new Date(NOW - min * 60e3).toISOString();

test('thousands are grouped like every other count', () => {
  assert.equal(M.grouped(1240), '1,240');
  assert.equal(M.grouped(12), '12');
  assert.equal(M.grouped(1234567), '1,234,567');
});

test('reach chip: the three modes and unknown', () => {
  assert.equal(M.reachChip({ reach: 'direct' }).text, 'Connects directly');
  assert.equal(M.reachChip({ reach: 'forward' }).text, 'Through your laptop');
  assert.equal(M.reachChip({ reach: 'pull', pull: { interval_s: 60 } }).text, 'Collected by the hub every 1 min');
  assert.equal(M.reachChip({ reach: 'pull', pull: { interval_s: 15 } }).text, 'Collected by the hub every 15 s');
  assert.equal(M.reachChip({ reach: 'pull', pull: { interval_s: 3600 } }).text, 'Collected by the hub every 1 h');
  assert.equal(M.reachChip({ reach: 'pull' }).text, 'Collected by the hub');
  assert.equal(M.reachChip({ reach: null }), null);
});

test('status line: live, quiet, seen, never', () => {
  assert.deepEqual({ ...M.statusOf({ last_record: at(1) }, NOW, f) }, { cls: 'ok', text: 'Live' });
  assert.equal(M.statusOf({ silent: true, last_record: at(72) }, NOW, f).text, 'Quiet 1 h');
  assert.equal(M.statusOf({ silent: true, last_record: at(72) }, NOW, f).cls, 'warn');
  assert.equal(M.statusOf({ last_seen: at(12) }, NOW, f).text, 'Seen 12 min ago');
  assert.equal(M.statusOf({}, NOW, f).text, 'Never seen');
});

test('backlog shows only when it piles up', () => {
  assert.equal(M.backlogText({ backlog: 0 }, NOW, f), '');
  assert.equal(M.backlogText({ backlog: null }, NOW, f), '');
  assert.equal(M.backlogText({ backlog: 1240, backlog_oldest: at(40) }, NOW, f), '1,240 waiting to send (oldest 40 min)');
  assert.equal(M.backlogText({ backlog: 8, backlog_oldest: at(1) }, NOW, f), '', 'a few in flight are normal');
  assert.equal(M.backlogText({ backlog: 3 }, NOW, f), '', 'small count of unknown age');
  assert.equal(M.backlogText({ backlog: 212 }, NOW, f), '212 waiting to send');
});

test('version line: same, older, newer, held, unknown', () => {
  assert.deepEqual({ ...M.versionLine({ sy_version: '0.5.0', version_status: 'same' }, '0.5.0') }, { text: '0.5.0 · same as hub', version: '0.5.0', note: 'same as hub', cls: '' });
  assert.deepEqual({ ...M.versionLine({ sy_version: '0.4.2', version_status: 'older' }, '0.5.0') }, { text: '0.4.2 · older than hub 0.5.0', version: '0.4.2', note: 'older than hub 0.5.0', cls: 'warn' });
  assert.equal(M.versionLine({ sy_version: '0.6.0', version_status: 'newer' }, '0.5.0').text, '0.6.0 · newer than hub 0.5.0');
  const held = M.versionLine({ sy_version: '0.6.0', version_status: 'newer', held: 37 }, '0.5.0');
  assert.equal(held.text, '0.6.0 · holding 37 updates because the hub is too old for them');
  assert.equal(held.version, '0.6.0');
  assert.equal(held.cls, 'bad');
  assert.equal(M.versionLine({ sy_version: 'dev', version_status: 'unknown' }, '0.5.0').cls, '');
  assert.equal(M.versionLine({}, '0.5.0').text, 'not reported');
  // M1: an equal string is the same version even when the hub said "unknown"
  assert.equal(M.versionLine({ sy_version: '104cd7f', version_status: 'unknown' }, '104cd7f').text, '104cd7f · same as hub');
  assert.equal(M.versionLine({ sy_version: 'dev', version_status: 'unknown' }, '0.5.0').text, 'dev');
});

test('pull state per state; host key and refused cannot be pulled now', () => {
  const st = (state, extra = {}) => M.pullLine({ state, ...extra }, NOW, f);
  assert.equal(M.pullLine(null, NOW, f), null);
  assert.equal(st('ok', { last_ok: at(1) }).text, 'OK, last pull 1 min ago');
  assert.equal(st('failing', { last_error: 'timeout' }).text, 'Failing: timeout');
  for (const s of ['ok', 'new', 'failing', 'auth']) assert.equal(st(s).canRun, true, s);
  for (const s of ['host_key', 'refused', 'disabled', 'frozen']) assert.equal(st(s).canRun, false, s);
  assert.equal(st('host_key').cls, 'bad');
  assert.match(st('frozen').text, /frozen/);
});

test('cards sort silent first, then by name, removed last', () => {
  const out = M.sortMachines([
    { name: 'b' }, { name: 'z', removed_at: 'x' }, { name: 'c', silent: true }, { name: 'a' },
  ]).map((m) => m.name);
  assert.deepEqual([...out], ['c', 'a', 'b', 'z']);
});

test('silent alert sentence', () => {
  assert.equal(M.alertText(null, NOW, f), '');
  assert.equal(M.alertText({ status: 'fired', fired_at: at(165) }, NOW, f), 'Quiet-machine notification sent 3 h ago');
  assert.equal(M.alertText({ status: 'snoozed' }, NOW, f), 'Quiet-machine notification snoozed');
});

test('lifetime: an owner link lifetime in words (pairing.md 4)', () => {
  assert.equal(F.lifetime(60), '1 minute');
  assert.equal(F.lifetime(600), '10 minutes');
  assert.equal(F.lifetime(3600), '1 hour');
  assert.equal(F.lifetime(90), '90 seconds');
  assert.equal(F.lifetime(0), '');
  assert.equal(F.lifetime(undefined), '');
});

test('deviceLabel: later twins get their place, the first and unique names stay', () => {
  const all = [
    { id: 'dev_b', name: 'pixel 9 · chrome', created_at: '2026-10-12T09:00:00.000Z' },
    { id: 'dev_a', name: 'Pixel 9 · Chrome', created_at: '2026-10-10T09:00:00.000Z' },
    { id: 'dev_c', name: 'iPhone · Safari', created_at: '2026-10-11T09:00:00.000Z' },
    { id: 'dev_d', name: 'Pixel 9 · Chrome', created_at: '2026-10-14T09:00:00.000Z' },
  ];
  assert.equal(F.deviceLabel(all[1], all), 'Pixel 9 · Chrome');
  assert.equal(F.deviceLabel(all[0], all), 'pixel 9 · chrome · 2');
  assert.equal(F.deviceLabel(all[3], all), 'Pixel 9 · Chrome · 3');
  assert.equal(F.deviceLabel(all[2], all), 'iPhone · Safari');
  assert.equal(F.deviceLabel(all[1], null), 'Pixel 9 · Chrome');
});

test('subtitleOf: a session named after its folder does not repeat it', () => {
  assert.equal(F.subtitleOf({ project: 'infra', name: 'infra' }, 'infra'), '');
  assert.equal(F.subtitleOf({ project: 'infra', title: 'Infra ' }, 'infra'), '');
  assert.equal(F.subtitleOf({ project: 'api', title: 'fix the login', name: 'api' }, 'api'), 'fix the login');
  assert.equal(F.subtitleOf({ project: 'api' }, 'api'), '');
});
