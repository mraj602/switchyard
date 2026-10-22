// Cadence helpers for web usage reading (C5): jitter, error backoff and the test-time scaling
// of the cadence. Pure functions with an injectable random source, so scheduler tests are
// deterministic.

import { BACKOFF_MS, CADENCE } from './constants.js';

/** A random offset in [-maxMs, +maxMs]. */
export function jitter(random, maxMs) {
  return (random() * 2 - 1) * maxMs;
}

/**
 * Delay before the next attempt after `failures` consecutive failures (1 = the first):
 * 5, 10, 20, 40, then 60 min (cap), plus 0..jitterMs.
 * @param {number} failures
 * @param {{random?: () => number, table?: readonly number[], jitterMs?: number}} [opts]
 */
export function backoffDelay(failures, opts = {}) {
  const random = opts.random ?? Math.random;
  const table = opts.table ?? BACKOFF_MS;
  const jitterMs = opts.jitterMs ?? CADENCE.JITTER_MS;
  const n = Math.max(1, Math.floor(failures));
  return table[Math.min(n - 1, table.length - 1)] + random() * jitterMs;
}

/**
 * The cadence in force. With no override it is CADENCE and BACKOFF_MS. With
 * SY_APP_TEST_CADENCE_MS (unpackaged only) the period is that many ms, and the floor, the
 * manual floor, the jitter, the stagger, the nav minimum age and the backoff table are scaled
 * by the same ratio (period / 300 s), so a 2 s test cadence behaves like production in
 * proportion. The open delay and the navigation debounce are not scaled.
 * @param {number|string|undefined|null} testMs
 * @returns {{cadence: typeof CADENCE, backoff: number[]}}
 */
export function cadenceFor(testMs) {
  const ms = Number(testMs);
  if (!Number.isFinite(ms) || ms <= 0) return { cadence: CADENCE, backoff: [...BACKOFF_MS] };
  const r = ms / CADENCE.PERIOD_MS;
  const s = (v) => Math.max(1, Math.round(v * r));
  return {
    cadence: Object.freeze({
      ...CADENCE,
      PERIOD_MS: ms,
      JITTER_MS: s(CADENCE.JITTER_MS),
      STAGGER_MS: s(CADENCE.STAGGER_MS),
      NAV_MIN_AGE_MS: s(CADENCE.NAV_MIN_AGE_MS),
      FLOOR_MS: s(CADENCE.FLOOR_MS),
      MANUAL_FLOOR_MS: s(CADENCE.MANUAL_FLOOR_MS),
    }),
    backoff: BACKOFF_MS.map(s),
  };
}
