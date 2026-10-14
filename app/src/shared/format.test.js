import { test } from 'node:test';
import assert from 'node:assert/strict';
import { pct, barWidth, clock, resetText, ago, agoLabel, until, humanKey, plural, bytes, toMs } from './format.js';

const at = (y, mo, d, h, mi) => new Date(y, mo - 1, d, h, mi, 0);
const iso = (d) => d.toISOString();
const NOW = at(2026, 10, 13, 14, 0).getTime(); // local time

test('pct rounds, clamps and dashes', () => {
  assert.equal(pct(41.6), '42%');
  assert.equal(pct(0), '0%');
  assert.equal(pct(120), '100%');
  assert.equal(pct(-3), '0%');
  assert.equal(pct(null), '–');
  assert.equal(pct(undefined), '–');
  assert.equal(pct(NaN), '–');
  assert.equal(pct('41'), '–');
});

test('barWidth is 0..100', () => {
  assert.equal(barWidth(41), 41);
  assert.equal(barWidth(250), 100);
  assert.equal(barWidth(-5), 0);
  assert.equal(barWidth(null), 0);
});

test('clock is local time today and "Day HH:MM" otherwise', () => {
  assert.equal(clock(iso(at(2026, 10, 13, 15, 10)), NOW), '15:10');
  assert.equal(clock(iso(at(2026, 10, 13, 9, 5)), NOW), '09:05');
  assert.equal(clock(iso(at(2026, 10, 19, 9, 0)), NOW), 'Mon 09:00');
  assert.equal(clock(iso(at(2026, 10, 12, 23, 59)), NOW), 'Mon 23:59');
  assert.equal(resetText(iso(at(2026, 10, 14, 1, 0)), NOW), 'Wed 01:00');
  assert.equal(clock(null, NOW), '');
  assert.equal(clock('garbage', NOW), '');
});

test('ago and agoLabel', () => {
  const t = (ms) => iso(new Date(NOW - ms));
  assert.equal(ago(t(10e3), NOW), 'just now');
  assert.equal(agoLabel(t(10e3), NOW), 'just now');
  assert.equal(ago(t(60e3), NOW), '1 min');
  assert.equal(agoLabel(t(2 * 60e3), NOW), '2 min ago');
  assert.equal(ago(t(3 * 36e5), NOW), '3 h');
  assert.equal(ago(t(2 * 864e5), NOW), '2 d');
  assert.equal(ago(t(-5000), NOW), 'just now'); // a clock a little ahead is not negative
  assert.equal(ago(undefined, NOW), '');
  assert.equal(agoLabel('nope', NOW), '');
});

test('until', () => {
  const f = (ms) => iso(new Date(NOW + ms));
  assert.equal(until(f(-1000), NOW), 'now');
  assert.equal(until(f(20e3), NOW), 'in 1 min');
  assert.equal(until(f(14 * 60e3), NOW), 'in 14 min');
  assert.equal(until(f(2 * 36e5), NOW), 'in 2 h');
  assert.equal(until(f(2 * 36e5 + 10 * 60e3), NOW), 'in 2 h 10 min');
  assert.equal(until(f(30 * 36e5), NOW), 'in 1 d 6 h');
  assert.equal(until(null, NOW), '');
});

test('humanKey, plural, bytes, toMs', () => {
  assert.equal(humanKey('seven_day_opus'), 'Seven day opus');
  assert.equal(humanKey(null), '');
  assert.equal(plural(1, 'tab'), '1 tab');
  assert.equal(plural(3, 'tab'), '3 tabs');
  assert.equal(plural(0, 'copy', 'copies'), '0 copies');
  assert.equal(bytes(512), '512 B');
  assert.equal(bytes(1536), '1.5 KB');
  assert.equal(bytes(5 * 1024 ** 2), '5.0 MB');
  assert.equal(bytes(-1), '');
  assert.ok(Number.isNaN(toMs(undefined)));
  assert.ok(Number.isNaN(toMs(12)));
});
