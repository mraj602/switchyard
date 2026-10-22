// The reading scheduler (C5 cadence): per-tab timers on an injectable clock. It knows nothing
// about claude.ai or sy; it asks `read(id, ctx)` to do a read and acts on the outcome:
//
//   {type: 'ok'}                 reset the backoff, next periodic read
//   {type: 'retry'}              back off 5, 10, 20, 40, then 60 min (plus jitter)
//   {type: 'stop'}               no timers until resume
//   {type: 'skip'}               tab not readable now; next periodic read, no failure, floor not consumed
//                                ({type: 'skip', rearm: false}: no next read, for a tab that is off or stopped)
//   {type: 'dropped'}            the read was cancelled; the owner already discarded its result
//
// Rules: at most one read in flight per tab; at most one read per FLOOR (manual reads: per
// MANUAL_FLOOR); the first periodic read of tab N is PERIOD + N × STAGGER ± JITTER after it
// is tracked, later ones PERIOD ± JITTER after the previous read; cancel() bumps a sequence
// number so a read in flight is dropped by its owner after its await (ctx.cancelled()).
// stopAll() is final (shutdown): after it no read starts and no timer is armed.

import { backoffDelay, jitter } from '../../shared/backoff.js';

/** Why a read is asked for. */
export const KIND = Object.freeze({
  PERIODIC: 'periodic',
  OPEN: 'open', // did-finish-load on claude.ai + OPEN_DELAY
  NAV: 'nav', // in-page navigation, debounced
  MANUAL: 'manual', // usage:refresh, 10 s floor
  NOW: 'now', // resume, warning accepted: read now, no floor
});

/**
 * @param {{clock: {now(): number, setTimeout(fn: Function, ms: number): any, clearTimeout(h: any): void},
 *          cadence: object, backoff: number[], random?: () => number,
 *          read: (id: string, ctx: {seq: number, start: number, kind: string, cancelled: () => boolean}) => Promise<{type: string, message?: string}>,
 *          onChange?: (id: string) => void, log?: object}} deps
 */
export function createScheduler(deps) {
  const { clock, cadence, backoff, read } = deps;
  const random = deps.random ?? Math.random;
  const onChange = deps.onChange ?? (() => {});
  const log = deps.log ?? { debug() {}, warn() {} };
  /** @type {Map<string, any>} */
  const tabs = new Map();
  let stopped = false; // stopAll(): shutting down, nothing new starts

  const clearTimers = (t) => {
    for (const k of ['timer', 'openTimer', 'navTimer']) {
      if (t[k] != null) clock.clearTimeout(t[k]);
      t[k] = null;
    }
    t.retryAt = null;
  };

  function arm(id, first) {
    const t = tabs.get(id);
    if (!t || stopped) return;
    if (t.timer != null) clock.clearTimeout(t.timer);
    const base = cadence.PERIOD_MS + (first ? t.index * cadence.STAGGER_MS : 0);
    const delay = Math.max(0, base + jitter(random, cadence.JITTER_MS));
    t.timer = clock.setTimeout(() => {
      t.timer = null;
      void run(id, KIND.PERIODIC);
    }, delay);
  }

  function armRetry(id) {
    const t = tabs.get(id);
    if (!t || stopped) return;
    if (t.timer != null) clock.clearTimeout(t.timer);
    const delay = backoffDelay(t.failures, { random, table: backoff, jitterMs: cadence.JITTER_MS });
    t.retryAt = clock.now() + delay;
    t.timer = clock.setTimeout(() => {
      t.timer = null;
      void run(id, KIND.PERIODIC);
    }, delay);
  }

  async function run(id, kind) {
    if (stopped) return 'stopped';
    const t = tabs.get(id);
    if (!t) return 'untracked';
    if (t.inflight) {
      if (kind === KIND.MANUAL || kind === KIND.NOW) t.rerun = kind;
      return 'busy';
    }
    const now = clock.now();
    if (kind !== KIND.NOW) {
      // During a backoff only a manual read, or the backoff timer itself, may read.
      if ((kind === KIND.OPEN || kind === KIND.NAV) && t.retryAt != null && now < t.retryAt) return 'backoff';
      const floor = kind === KIND.MANUAL ? cadence.MANUAL_FLOOR_MS : cadence.FLOOR_MS;
      if (t.lastStart != null && now - t.lastStart < floor) {
        if (kind === KIND.PERIODIC) arm(id, false);
        return 'floor';
      }
    } else {
      t.failures = 0;
      t.retryAt = null;
    }
    const seq = t.seq;
    const prevStart = t.lastStart;
    t.inflight = true;
    t.lastStart = now;
    onChange(id);
    let out;
    try {
      out = await read(id, { seq, start: now, kind, cancelled: () => t.seq !== seq || !tabs.has(id) });
    } catch (err) {
      log.warn?.('usage: read threw:', err?.message ?? err);
      out = { type: 'retry', message: String(err?.message ?? err) };
    }
    t.inflight = false;
    if (t.seq !== seq || tabs.get(id) !== t) {
      // Cancelled while in flight: no timers from this read.
      if (t.rerun && tabs.get(id) === t) {
        const k = t.rerun;
        t.rerun = null;
        void run(id, k);
      }
      onChange(id);
      return 'dropped';
    }
    switch (out?.type) {
      case 'ok':
        t.failures = 0;
        t.retryAt = null;
        arm(id, false);
        break;
      case 'skip':
        t.lastStart = prevStart;
        if (out.rearm !== false) arm(id, false); // rearm: false = reading is off or stopped, stay quiet
        break;
      case 'stop':
        clearTimers(t);
        t.failures = 0;
        break;
      case 'dropped':
        break;
      default:
        t.failures += 1;
        armRetry(id);
    }
    if (t.rerun) {
      const k = t.rerun;
      t.rerun = null;
      void run(id, k);
    }
    onChange(id);
    return out?.type ?? 'retry';
  }

  return {
    /** Start tracking a tab; `index` is its position (the stagger). Does not arm. */
    track(id, index) {
      const t = tabs.get(id);
      if (t) {
        t.index = index;
        return;
      }
      tabs.set(id, { index, timer: null, openTimer: null, navTimer: null, seq: 0, inflight: false, lastStart: null, failures: 0, retryAt: null, rerun: null });
    },
    /** Stop tracking; a read in flight is dropped. */
    untrack(id) {
      const t = tabs.get(id);
      if (!t) return;
      clearTimers(t);
      t.seq += 1;
      tabs.delete(id);
    },
    /** Start the periodic timer unless one is running or a read is in flight. */
    ensureArmed(id) {
      const t = tabs.get(id);
      if (t && !stopped && t.timer == null && !t.inflight) arm(id, t.lastStart == null);
    },
    /** Clear every timer and mark a read in flight as cancelled. */
    cancel(id) {
      const t = tabs.get(id);
      if (!t) return;
      clearTimers(t);
      t.seq += 1;
      t.failures = 0;
      t.rerun = null;
    },
    /** Ask for a read of `kind` (see KIND). OPEN and NAV are delayed and debounced. */
    trigger(id, kind) {
      if (stopped) return Promise.resolve('stopped');
      const t = tabs.get(id);
      if (!t) return Promise.resolve('untracked');
      if (kind === KIND.OPEN) {
        if (t.openTimer != null) clock.clearTimeout(t.openTimer);
        t.openTimer = clock.setTimeout(() => {
          t.openTimer = null;
          void run(id, KIND.OPEN);
        }, cadence.OPEN_DELAY_MS);
        return Promise.resolve('scheduled');
      }
      if (kind === KIND.NAV) {
        if (t.navTimer != null) clock.clearTimeout(t.navTimer);
        t.navTimer = clock.setTimeout(() => {
          t.navTimer = null;
          if (t.lastStart == null || clock.now() - t.lastStart >= cadence.NAV_MIN_AGE_MS) void run(id, KIND.NAV);
        }, cadence.NAV_DEBOUNCE_MS);
        return Promise.resolve('scheduled');
      }
      return run(id, kind);
    },
    /** {inflight, failures, retryAt (ms epoch or null), lastStart (ms epoch or null)}. */
    info(id) {
      const t = tabs.get(id);
      return t ? { inflight: t.inflight, failures: t.failures, retryAt: t.retryAt, lastStart: t.lastStart } : null;
    },
    has: (id) => tabs.has(id),
    stopped: () => stopped,
    ids: () => [...tabs.keys()],
    /** Cancel everything, for good: no read starts and no timer is armed afterwards. */
    stopAll() {
      stopped = true;
      for (const t of tabs.values()) {
        clearTimers(t);
        t.seq += 1;
      }
    },
  };
}
