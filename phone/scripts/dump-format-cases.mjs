// Writes test/fixtures/format-cases.json: the shared case table of src/lib/format and
// webui/static/format.js. Each case is {fn, args, want}; `want` is what the
// webui's format.js returns, run here under TZ=UTC. The jest test (test/format.test.ts) runs
// the port, and format.js itself when it is present, over the same table.
//
//   node phone/scripts/dump-format-cases.mjs          (from the repo root)
//
// Cases with a `tz` argument exist only for the port (format.js has no tz); their `want` is
// written by hand in PORT_ONLY below.
import { writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

process.env.TZ = 'UTC';

const here = dirname(fileURLToPath(import.meta.url));
const F = await import(pathToFileURL(join(here, '..', '..', 'webui', 'static', 'format.js')).href);

const NOW = Date.parse('2026-10-13T14:02:00.000Z');
const at = (ms) => new Date(NOW + ms).toISOString();
const MIN = 60e3;
const HOUR = 36e5;
const DAY = 864e5;

const times = [
  null, undefined, '', 'not a date',
  at(0), at(-10e3), at(-44e3), at(-45e3), at(-90e3), at(-4 * MIN), at(-59 * MIN), at(-60 * MIN), at(-89 * MIN),
  at(-3 * HOUR), at(-23 * HOUR), at(-24 * HOUR), at(-36 * HOUR), at(-3 * DAY), at(-40 * DAY),
  at(30 * 1000), at(45 * MIN), at(59 * MIN + 31e3), at(60 * MIN), at(2 * HOUR + 10 * MIN), at(5 * HOUR), at(23 * HOUR + 40 * MIN),
  at(25 * HOUR), at(3 * DAY + 5 * HOUR), '2026-10-13T17:00:00.000Z', '2026-10-16T09:00:00Z', '2026-10-13T00:05:00Z', '2026-10-12T23:55:00Z',
];

const cases = [];
const add = (fn, ...args) => cases.push({ fn, args, want: F[fn](...args) });

for (const t of times) {
  add('toMs', t);
  add('ago', t, NOW);
  add('agoLabel', t, NOW);
  add('clock', t, NOW);
  add('until', t, NOW);
}
for (const n of [null, undefined, NaN, 0, 61.6, 99.5, 100, 4.4, -3]) add('pct', n);
for (const n of [0, 1, 2, 11]) {
  add('plural', n, 'session');
  add('plural', n, 'query', 'queries');
}
for (const k of ['five_hour', '', null, undefined, 'seven_day_opus', 'x']) add('humanKey', k);
for (const n of [null, undefined, 0, 999, 1000, 1234, 999999, 1000000, 2500000]) add('compact', n);
for (const s of [{ project: 'api' }, { project: '', cwd: '/home/me/api/' }, { cwd: '/' }, { cwd: '' }, {}, { project: null, cwd: '/a/b/c' }]) add('projectOf', s);
for (const u of [
  'https://claude.ai/code/session_demo', 'https://Claude.AI', 'http://claude.ai', 'javascript:alert(1)', 'data:text/html,x',
  'https://', 'not a url', '', null, undefined, 42, 'HTTPS://example.com/a b',
]) add('httpsUrl', u);
for (const t of ['2026-10-13T14:02:03Z', '2026-10-10T00:00:00Z', '2026-09-03T14:02:00Z', 'bad', null]) add('dateTime', t);
for (const t of times) {
  add('day', t, NOW);
  add('timeOfDay', t);
}
for (const t of ['2026-09-03T14:02:00Z', '2025-12-31T23:59:00Z', '2027-01-01T00:00:00Z', '2026-01-01T00:00:00Z']) add('day', t, NOW);
for (const t of ['2026-09-03T14:02:00Z', '2026-03-01T09:00:00Z', '2026-12-25T09:05:00Z', '2026-10-07T14:03:00Z', '2026-10-19T14:01:00Z']) add('clock', t, NOW);
for (const n of [null, undefined, NaN, 'x', 0, 7, 999, 1000, 141816, 1234567.6, -3, -1234]) add('num', n);
for (const n of [1000, 1234567]) add('plural', n, 'token');
for (const n of [999.6, 1e9, 24.1e9, NaN]) add('compact', n);
for (const z of ['', null, 'UTC', 'Etc/UTC', 'GMT', 'Asia/Kolkata', 'Asia/Calcutta', 'Asia/Saigon', 'America/New_York', 'Asia/Kathmandu', 'No/Such_Zone']) add('tzLabel', z, NOW);
for (const [a, b] of [['UTC', 'Etc/UTC'], ['Asia/Kolkata', 'Asia/Calcutta'], ['UTC', 'Asia/Kolkata'], ['', 'UTC'], ['UTC', ''], ['UTC', 'No/Such_Zone'], ['Europe/London', 'Europe/London']]) add('sameTz', a, b, NOW);

// Cases only the port can answer: clock() in a named zone (format.js reads the device zone).
const PORT_ONLY = [
  { fn: 'clock', args: ['2026-10-13T17:00:00.000Z', NOW, 'Asia/Kolkata'], want: '22:30' },
  { fn: 'clock', args: ['2026-10-13T14:30:00.000Z', NOW, 'Asia/Kolkata'], want: '20:00' },
  { fn: 'clock', args: ['2026-10-13T17:00:00.000Z', NOW, 'America/New_York'], want: '13:00' },
  { fn: 'clock', args: ['2026-10-16T09:00:00.000Z', NOW, 'UTC'], want: 'Fri 09:00' },
  { fn: 'clock', args: ['2026-10-13T17:00:00.000Z', NOW, 'No/Such_Zone'], want: '17:00' },
  { fn: 'clock', args: ['2026-10-13T17:00:00.000Z', NOW, ''], want: '17:00' },
].map((c) => ({ ...c, webui: false }));

const out = { _meta: { source: 'webui/static/format.js', tz: 'UTC', now: NOW }, cases: [...cases, ...PORT_ONLY] };
const file = join(here, '..', 'test', 'fixtures', 'format-cases.json');
// NaN and undefined do not survive JSON: encode them (the test decodes with the same tags).
const enc = (v) => (Number.isNaN(v) ? { $: 'NaN' } : v === undefined ? { $: 'undefined' } : v);
const walk = (v) => (Array.isArray(v) ? v.map(walk) : v && typeof v === 'object' && !('$' in v) ? Object.fromEntries(Object.entries(v).map(([k, x]) => [k, walk(x)])) : enc(v));
writeFileSync(file, JSON.stringify(walk(out), null, 1) + '\n');
console.log(`wrote ${out.cases.length} cases to ${file}`);
