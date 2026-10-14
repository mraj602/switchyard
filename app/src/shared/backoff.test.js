import { test } from 'node:test';
import assert from 'node:assert/strict';

import { backoffDelay, cadenceFor, jitter } from './backoff.js';
import { BACKOFF_MS, CADENCE } from './constants.js';

test('backoff walks 5, 10, 20, 40, then 60 min and stays there', () => {
  const min = [1, 2, 3, 4, 5, 6, 20].map((n) => backoffDelay(n, { random: () => 0 }) / 60_000);
  assert.deepEqual(min, [5, 10, 20, 40, 60, 60, 60]);
});

test('backoff jitter is added, 0 to JITTER_MS', () => {
  assert.equal(backoffDelay(1, { random: () => 1 }), BACKOFF_MS[0] + CADENCE.JITTER_MS);
  assert.equal(backoffDelay(1, { random: () => 0.5 }), BACKOFF_MS[0] + CADENCE.JITTER_MS / 2);
  assert.equal(backoffDelay(0, { random: () => 0 }), BACKOFF_MS[0], 'a non-positive count counts as the first failure');
});

test('jitter is symmetric', () => {
  assert.equal(jitter(() => 0, 15_000), -15_000);
  assert.equal(jitter(() => 1, 15_000), 15_000);
  assert.equal(jitter(() => 0.5, 15_000), 0);
});

test('cadenceFor: no override is the contract, an override scales in proportion', () => {
  assert.equal(cadenceFor(undefined).cadence, CADENCE);
  assert.equal(cadenceFor('nope').cadence, CADENCE);
  assert.equal(cadenceFor(0).cadence, CADENCE);
  const { cadence, backoff } = cadenceFor('3000'); // 1/100 of the period
  assert.equal(cadence.PERIOD_MS, 3000);
  assert.equal(cadence.FLOOR_MS, 600);
  assert.equal(cadence.MANUAL_FLOOR_MS, 100);
  assert.equal(cadence.JITTER_MS, 150);
  assert.equal(cadence.STAGGER_MS, 200);
  assert.equal(cadence.OPEN_DELAY_MS, CADENCE.OPEN_DELAY_MS);
  assert.deepEqual(backoff, [3000, 6000, 12000, 24000, 36000]);
});
