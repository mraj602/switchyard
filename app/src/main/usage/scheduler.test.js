import { test } from 'node:test';
import assert from 'node:assert/strict';

import { createScheduler, KIND } from './scheduler.js';
import { fakeClock } from '../../../test/fakes/claude/clock.js';
import { BACKOFF_MS, CADENCE } from '../../shared/constants.js';

const S = 1000;
const MIN = 60 * S;

/** A scheduler on a fake clock whose reads are scripted; `random` is fixed (0.5 = no jitter). */
function setup({ outcomes = [], random = () => 0.5, delayMs = 0 } = {}) {
  const clock = fakeClock();
  const reads = [];
  const read = async (id, ctx) => {
    reads.push({ id, at: clock.now(), kind: ctx.kind, cancelled: ctx.cancelled });
    if (delayMs) {
      await new Promise((r) => clock.setTimeout(r, delayMs));
    }
    if (ctx.cancelled()) return { type: 'dropped' };
    return outcomes.length ? outcomes.shift() : { type: 'ok' };
  };
  const changes = [];
  const sched = createScheduler({ clock, cadence: CADENCE, backoff: [...BACKOFF_MS], random, read, onChange: (id) => changes.push(id) });
  return { clock, reads, sched, changes, t0: clock.now() };
}
const starts = (reads, t0) => reads.map((r) => (r.at - t0) / S);

test('periodic reads every 300 s', async () => {
  const { clock, reads, sched, t0 } = setup();
  sched.track('a', 0);
  sched.ensureArmed('a');
  await clock.advance(16 * MIN);
  assert.deepEqual(starts(reads, t0), [300, 600, 900]);
});

test('jitter stays within ±15 s of the period', async () => {
  for (const [rand, offset] of [[0, -15], [1, 15]]) {
    const { clock, reads, sched, t0 } = setup({ random: () => rand });
    sched.track('a', 0);
    sched.ensureArmed('a');
    await clock.advance(11 * MIN);
    assert.deepEqual(starts(reads, t0), [300 + offset, 600 + 2 * offset]);
  }
});

test('the first periodic read is staggered 20 s per tab', async () => {
  const { clock, reads, sched, t0 } = setup();
  ['a', 'b', 'c'].forEach((id, i) => {
    sched.track(id, i);
    sched.ensureArmed(id);
  });
  await clock.advance(6 * MIN);
  assert.deepEqual(reads.map((r) => [r.id, (r.at - t0) / S]), [['a', 300], ['b', 320], ['c', 340]]);
});

test('open: 1.5 s after the tab loaded on claude.ai; nav: 3 s debounce and only if the last read is older than 60 s', async () => {
  const { clock, reads, sched, t0 } = setup();
  sched.track('a', 0);
  void sched.trigger('a', KIND.OPEN);
  await clock.advance(1400);
  assert.equal(reads.length, 0);
  await clock.advance(200);
  assert.deepEqual(starts(reads, t0), [1.5]);

  // In-page navigation right after: 3 s debounce, then refused (last read 3 s old < 60 s).
  void sched.trigger('a', KIND.NAV);
  void sched.trigger('a', KIND.NAV); // a second one restarts the debounce
  await clock.advance(10 * S);
  assert.equal(reads.length, 1);

  // Later: allowed.
  await clock.advance(70 * S);
  void sched.trigger('a', KIND.NAV);
  await clock.advance(2900);
  assert.equal(reads.length, 1, 'still debouncing');
  await clock.advance(200);
  assert.equal(reads.length, 2);
});

test('floor: never more than one read per 60 s, except manual (10 s)', async () => {
  const { clock, reads, sched } = setup();
  sched.track('a', 0);
  assert.equal(await sched.trigger('a', KIND.MANUAL), 'ok');
  assert.equal(await sched.trigger('a', KIND.MANUAL), 'floor', 'manual: 10 s since the last read');
  await clock.advance(9 * S);
  assert.equal(await sched.trigger('a', KIND.MANUAL), 'floor');
  await clock.advance(1100);
  assert.equal(await sched.trigger('a', KIND.MANUAL), 'ok', '10 s passed');
  assert.equal(reads.length, 2);
  // open and periodic obey the 60 s floor even when 10 s have passed
  await clock.advance(11 * S);
  void sched.trigger('a', KIND.OPEN);
  await clock.advance(1600);
  assert.equal(reads.length, 2, 'open 12.6 s after a read: floor');
  await clock.advance(50 * S);
  void sched.trigger('a', KIND.OPEN);
  await clock.advance(1600);
  assert.equal(reads.length, 3);
});

test('one read in flight per tab; a second request is not run in parallel', async () => {
  const { clock, reads, sched } = setup({ delayMs: 3000 });
  sched.track('a', 0);
  const first = sched.trigger('a', KIND.MANUAL);
  await clock.advance(1000);
  assert.equal(await sched.trigger('a', KIND.NOW), 'busy');
  assert.equal(reads.length, 1);
  await clock.advance(3000);
  assert.equal(await first, 'ok');
  // the NOW asked during the read runs after it
  await clock.advance(3100);
  assert.equal(reads.length, 2);
});

test('backoff 5, 10, 20, 40, 60, 60 min on retry, reset by a success', async () => {
  const { clock, reads, sched, t0 } = setup({ outcomes: Array(6).fill({ type: 'retry' }) });
  sched.track('a', 0);
  assert.equal(await sched.trigger('a', KIND.NOW), 'retry');
  // random 0.5: the jitter added is half of 15 s
  const info = () => sched.info('a');
  assert.equal(info().failures, 1);
  assert.equal(info().retryAt - clock.now(), 5 * MIN + 7500);
  const gaps = [];
  for (let i = 0; i < 5; i++) {
    const before = reads.length;
    const at = info().retryAt;
    await clock.advance(at - clock.now());
    assert.equal(reads.length, before + 1);
    gaps.push(Math.round((info()?.retryAt ?? 0) - clock.now()));
  }
  assert.deepEqual(gaps.slice(0, 4), [10 * MIN + 7500, 20 * MIN + 7500, 40 * MIN + 7500, 60 * MIN + 7500]);
  assert.equal(gaps[4], 60 * MIN + 7500, 'capped at 60 min');
  // a later success resets
  await clock.advance(info().retryAt - clock.now());
  assert.equal(info().failures, 0);
  assert.equal(info().retryAt, null);
  void t0;
});

test('during a backoff an open or nav trigger does not read; manual does', async () => {
  const { clock, reads, sched } = setup({ outcomes: [{ type: 'retry' }] });
  sched.track('a', 0);
  await sched.trigger('a', KIND.NOW);
  void sched.trigger('a', KIND.OPEN);
  void sched.trigger('a', KIND.NAV);
  await clock.advance(11 * S);
  assert.equal(reads.length, 1);
  assert.equal(await sched.trigger('a', KIND.MANUAL), 'ok');
  assert.equal(reads.length, 2);
});

test('stop: no timers and no reads until resume (NOW)', async () => {
  const { clock, reads, sched } = setup({ outcomes: [{ type: 'stop' }] });
  sched.track('a', 0);
  sched.ensureArmed('a');
  await sched.trigger('a', KIND.NOW);
  assert.equal(clock.pending(), 0);
  await clock.advance(2 * 60 * MIN);
  assert.equal(reads.length, 1);
  assert.equal(await sched.trigger('a', KIND.NOW), 'ok', 'resume reads at once, with no floor');
  assert.equal(reads.length, 2);
  assert.equal(clock.pending(), 1, 'periodic timer is back');
});

test('skip (tab not readable) keeps the cadence, counts no failure and does not use the floor', async () => {
  const { clock, reads, sched } = setup({ outcomes: [{ type: 'skip' }, { type: 'skip' }] });
  sched.track('a', 0);
  sched.ensureArmed('a');
  await clock.advance(11 * MIN);
  assert.equal(reads.length, 2);
  assert.equal(sched.info('a').failures, 0);
  assert.equal(sched.info('a').lastStart, null);
});

test('cancel: timers cleared, nothing read afterwards, a read in flight is told it was cancelled', async () => {
  const { clock, reads, sched } = setup({ delayMs: 3000 });
  sched.track('a', 0);
  sched.ensureArmed('a');
  void sched.trigger('a', KIND.MANUAL);
  await clock.advance(1000);
  assert.equal(reads[0].cancelled(), false);
  sched.cancel('a');
  assert.equal(reads[0].cancelled(), true);
  await clock.advance(3000);
  assert.equal(clock.pending(), 0, 'no timer armed from the dropped read');
  await clock.advance(30 * MIN);
  assert.equal(reads.length, 1);
});

test('untrack drops the tab; unknown ids are harmless', async () => {
  const { clock, reads, sched } = setup();
  sched.track('a', 0);
  sched.ensureArmed('a');
  sched.untrack('a');
  assert.equal(await sched.trigger('a', KIND.NOW), 'untracked');
  await clock.advance(20 * MIN);
  assert.equal(reads.length, 0);
  sched.cancel('zzz');
  sched.untrack('zzz');
  assert.equal(sched.info('zzz'), null);
});

test('a read that throws is a failure, not a crash', async () => {
  const clock = fakeClock();
  const sched = createScheduler({
    clock, cadence: CADENCE, backoff: [...BACKOFF_MS], random: () => 0, log: { warn() {} },
    read: async () => {
      throw new Error('boom');
    },
  });
  sched.track('a', 0);
  assert.equal(await sched.trigger('a', KIND.NOW), 'retry');
  assert.equal(sched.info('a').failures, 1);
});

test('stopAll is final: no trigger, timer, rerun or ensureArmed starts a read afterwards', async () => {
  const { clock, reads, sched } = setup({ delayMs: 3000 });
  sched.track('a', 0);
  sched.track('b', 1);
  sched.ensureArmed('a');
  void sched.trigger('a', KIND.MANUAL);
  await clock.advance(1000);
  void sched.trigger('a', KIND.NOW); // queued as a rerun behind the read in flight
  sched.stopAll();
  assert.equal(sched.stopped(), true);
  assert.equal(await sched.trigger('b', KIND.NOW), 'stopped');
  assert.equal(await sched.trigger('a', KIND.MANUAL), 'stopped');
  void sched.trigger('b', KIND.OPEN);
  sched.ensureArmed('b');
  sched.track('c', 2);
  sched.ensureArmed('c');
  await clock.advance(60 * MIN);
  assert.equal(reads.length, 1, 'only the read that was in flight at stop');
  assert.equal(clock.pending(), 0, 'no timer left armed');
});
