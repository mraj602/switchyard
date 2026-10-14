// Web usage reading (C5): reader, scheduler, backoff and stop, warning acceptance,
// records (shared/record.js) published through sy.append / sy.flushSoon, local strip windows
// (shared/usage.js), the usage:* handlers and the usage bus event.
//
// Owned by WP-B. Contract: docs/spec/desktop.md C5.
// It reads tab facts from store.get().tabs and the live view from tabs.view(id); it learns of
// added and removed tabs from the bus `tabs` event, and hooks each tab's webContents events
// (did-finish-load, did-navigate-in-page) itself, so tabs/ and usage/ meet only through
// create() interfaces and the bus.

import { CLAUDE_ORIGIN, READING_OFF, READING_ON, WARNING_VERSION } from '../../shared/constants.js';
import { cadenceFor } from '../../shared/backoff.js';
import { buildErrorRecords, buildRecords, clientOf, seatsOf } from '../../shared/record.js';
import { isReadableUrl, pickOrg } from '../../shared/usage.js';
import { checkResult, readTab } from './reader.js';
import { createScheduler, KIND } from './scheduler.js';

const realClock = {
  now: () => Date.now(),
  setTimeout: (fn, ms) => {
    const h = setTimeout(fn, ms);
    return h;
  },
  clearTimeout: (h) => clearTimeout(h),
};

/**
 * @param {{tabs: object, sy: object, store: object, bus: object, ipc: object, log: object,
 *          clientVersion: string, cadenceMs?: number, clock?: object, random?: () => number,
 *          claudeOrigin?: string, readerTimeoutMs?: number}} deps
 *   sy: only append(records), flushSoon() and machineId() are used (main/sy/index.js)
 * @returns {{
 *   start(): void,
 *   refresh(id?: string): Promise<void>,   // manual read (usage:refresh, tray Refresh usage)
 *   pauseAll(): Promise<void>,             // every tab off (tray Pause all reading)
 *   stop(): Promise<void>,                 // cancel timers; wait ≤ 5 s for the last sy.append
 * }}
 */
export function create(deps) {
  const { tabs, sy, store, bus, ipc, log } = deps;
  const clock = deps.clock ?? realClock;
  const claudeOrigin = deps.claudeOrigin ?? CLAUDE_ORIGIN;
  const client = clientOf(deps.clientVersion);
  const { cadence, backoff } = cadenceFor(deps.cadenceMs);

  /** In-memory facts per tab. */
  /** @type {Map<string, {lastRead: string|null, windows: object[], windowsSource: 'local'|null, seats: object[]}>} */
  const facts = new Map();
  const attached = new WeakSet();
  const pending = new Set();
  let machine = null;
  let started = false;

  const iso = (ms) => new Date(ms).toISOString();
  const tabRec = (id) => store.get().tabs?.find((t) => t.id === id) ?? null;
  const factsOf = (id) => {
    if (!facts.has(id)) facts.set(id, { lastRead: null, windows: [], windowsSource: null, seats: [] });
    return facts.get(id);
  };
  const liveUrl = (id) => {
    try {
      return tabs.view(id)?.webContents?.getURL?.() ?? '';
    } catch {
      return '';
    }
  };

  /** C5 "readable": reading on, warning accepted, not stopped, a claude.ai URL that is not login or logout. */
  const readable = (tab) =>
    !!tab && tab.reading === READING_ON && (tab.warning_accepted ?? 0) >= WARNING_VERSION && !tab.stopped && isReadableUrl(liveUrl(tab.id), claudeOrigin);

  const emitTabs = () => bus.emit('tabs', { tabs: tabs.list() });

  async function patchTab(id, fn) {
    if (!tabRec(id)) return false;
    await store.update((draft) => {
      const t = draft.tabs?.find((x) => x.id === id);
      if (t) fn(t);
    });
    return true;
  }

  // ---- state

  function emitUsage(id) {
    const tab = tabRec(id);
    if (!tab) return;
    const f = factsOf(id);
    const info = scheduler.info(id);
    let read_state = 'idle';
    if (tab.reading !== READING_ON) read_state = 'off';
    else if (tab.stopped) read_state = 'stopped';
    else if (info?.inflight) read_state = 'reading';
    else if (info?.retryAt) read_state = 'retrying';
    bus.emit('usage', {
      id,
      read_state,
      stopped: tab.stopped ?? null,
      retry_at: read_state === 'retrying' ? iso(info.retryAt) : null,
      last_read: f.lastRead,
      windows: f.windows,
      windows_source: f.windowsSource,
    });
  }

  // ---- one read

  async function getMachine() {
    if (machine) return machine;
    try {
      machine = (await sy.machineId()) || null;
    } catch (err) {
      log.warn?.('usage: sy machine id failed:', err?.message ?? err);
    }
    return machine;
  }

  /** Append records through sy; the ids must match in count, else a failure that is not retried. */
  async function publish(records, id) {
    if (!records.length) return;
    let res;
    try {
      res = await sy.append(records);
    } catch (err) {
      log.warn?.(`usage: ${id}: sy outbox append failed:`, err?.message ?? err);
      return;
    }
    if (!res?.ok || !Array.isArray(res.ids) || res.ids.length !== records.length) {
      log.warn?.(`usage: ${id}: sy outbox append failed (${res?.error ?? `got ${res?.ids?.length ?? 0} ids for ${records.length} records`}); not retried`);
      return;
    }
    sy.flushSoon();
  }

  async function readOnce(id, ctx) {
    const tab = tabRec(id);
    if (!readable(tab)) return { type: 'skip', rearm: tab?.reading === READING_ON && !tab?.stopped };
    const wc = tabs.view(id)?.webContents;
    if (!wc) return { type: 'skip' };
    const ts = iso(ctx.start);
    const m = await getMachine(); // before the read: nothing is awaited between the cancel check and the append
    let raw;
    try {
      raw = await readTab(wc, { claudeOrigin, log, timeoutMs: deps.readerTimeoutMs, expectAccount: tab.seat?.account ?? null });
    } catch (err) {
      if (ctx.cancelled()) return { type: 'dropped' };
      log.warn?.(`usage: ${id}: read failed:`, err?.message ?? err);
      return { type: 'retry', message: String(err?.message ?? err) };
    }
    if (ctx.cancelled()) return { type: 'dropped' }; // reading turned off, or the tab removed, while in flight
    const res = checkResult(raw);
    const f = factsOf(id);
    const fresh = tabRec(id);
    const send = fresh?.send_to_hub !== false;

    if (res.kind === 'stop') {
      const records = send && m ? buildErrorRecords({ seats: knownSeats(id, fresh), status: res.status, message: res.message, tabId: id, machine: m, client, ts }) : [];
      const done = publish(records, id);
      track(done);
      await patchTab(id, (t) => {
        t.stopped = { status: res.status, at: iso(clock.now()) };
      });
      await done;
      emitTabs();
      return { type: 'stop', message: res.message };
    }
    if (res.kind === 'retry') {
      log.warn?.(`usage: ${id}: bad read (${res.message}); backing off`);
      return { type: 'retry', message: res.message };
    }

    const records = send && m ? buildRecords({ orgs: res.orgs, account: res.account, activeOrg: res.activeOrg, fetchMs: res.fetchMs, tabId: id, machine: m, client, ts }) : [];
    const done = publish(records, id); // synchronous up to sy.append
    track(done);
    f.lastRead = ts;
    f.seats = seatsOf(res.orgs, res.account, res.activeOrg);
    const pick = pickOrg(res.orgs, res.activeOrg);
    f.windows = pick ? pick.windows : [];
    f.windowsSource = pick ? 'local' : null;
    const seat = pick ? { account: res.account.uuid, org: pick.org.uuid } : null;
    if (seat && (fresh?.seat?.account !== seat.account || fresh?.seat?.org !== seat.org)) {
      await patchTab(id, (t) => {
        t.seat = seat;
      });
      emitTabs();
    }
    await done;
    return { type: 'ok' };
  }

  const track = (p) => {
    pending.add(p);
    p.finally(() => pending.delete(p));
  };

  /** The seats of a tab to write error records for: the last read's, else the stored one. */
  function knownSeats(id, tab) {
    const f = factsOf(id);
    if (f.seats.length) return f.seats;
    return tab?.seat?.account && tab?.seat?.org ? [{ account: tab.seat.account, org: tab.seat.org }] : [];
  }

  const scheduler = createScheduler({
    clock,
    cadence,
    backoff,
    random: deps.random,
    read: readOnce,
    onChange: emitUsage,
    log,
  });

  // ---- tab facts and triggers

  /** Keep the scheduler in line with the stored tab: armed when reading is on and not stopped. */
  function sync(id) {
    const tab = tabRec(id);
    if (!tab) return;
    if (tab.reading === READING_ON && !tab.stopped) scheduler.ensureArmed(id);
    else scheduler.cancel(id);
  }

  function attach(id) {
    const wc = tabs.view(id)?.webContents;
    if (!wc || attached.has(wc)) return;
    attached.add(wc);
    wc.on('did-finish-load', () => {
      if (isReadableUrl(wc.getURL(), claudeOrigin) && tabRec(id)) void scheduler.trigger(id, KIND.OPEN);
    });
    wc.on('did-navigate-in-page', (_e, url) => {
      if (isReadableUrl(url ?? wc.getURL(), claudeOrigin) && tabRec(id)) void scheduler.trigger(id, KIND.NAV);
    });
  }

  function syncTabs() {
    const list = store.get().tabs ?? [];
    const ids = new Set(list.map((t) => t.id));
    for (const id of scheduler.ids()) {
      if (!ids.has(id)) {
        scheduler.untrack(id);
        facts.delete(id);
      }
    }
    list.forEach((t, i) => {
      scheduler.track(t.id, i);
      attach(t.id);
      sync(t.id);
    });
    for (const t of list) emitUsage(t.id);
  }

  // ---- handlers

  async function setReading(id, on) {
    if (!tabRec(id)) return;
    await patchTab(id, (t) => {
      t.reading = on ? READING_ON : READING_OFF;
    });
    if (on) {
      sync(id);
      void scheduler.trigger(id, KIND.MANUAL);
    } else {
      scheduler.cancel(id); // timers cleared; a read in flight is dropped before the outbox
    }
    emitTabs();
    emitUsage(id);
  }

  async function acceptWarning(id, version) {
    if (!tabRec(id)) return;
    await patchTab(id, (t) => {
      t.warning_accepted = version;
      t.reading = READING_ON;
    });
    sync(id);
    emitTabs();
    emitUsage(id);
    void scheduler.trigger(id, KIND.NOW);
  }

  async function resume(id) {
    if (!tabRec(id)) return;
    await patchTab(id, (t) => {
      t.stopped = null;
    });
    sync(id);
    emitTabs();
    emitUsage(id);
    void scheduler.trigger(id, KIND.NOW);
  }

  async function refresh(id) {
    const ids = id ? [id] : (store.get().tabs ?? []).map((t) => t.id);
    await Promise.all(ids.map((i) => scheduler.trigger(i, KIND.MANUAL)));
  }

  async function pauseAll() {
    const ids = (store.get().tabs ?? []).map((t) => t.id);
    await store.update((draft) => {
      for (const t of draft.tabs ?? []) t.reading = READING_OFF;
    });
    for (const id of ids) scheduler.cancel(id);
    emitTabs();
    for (const id of ids) emitUsage(id);
  }

  let offTabs = null;

  return {
    start() {
      if (started) return;
      started = true;
      ipc.register('usage:accept-warning', ({ id, version }) => acceptWarning(id, version));
      ipc.register('usage:set-reading', ({ id, on }) => setReading(id, on));
      ipc.register('usage:set-send', async ({ id, on }) => {
        await patchTab(id, (t) => {
          t.send_to_hub = on;
        });
        emitTabs();
      });
      ipc.register('usage:resume', ({ id }) => resume(id));
      ipc.register('usage:refresh', (args) => refresh(args?.id));
      offTabs = bus.on('tabs', syncTabs);
      void getMachine();
      syncTabs();
      // A tab that loaded before we started.
      for (const t of store.get().tabs ?? []) if (isReadableUrl(liveUrl(t.id), claudeOrigin)) void scheduler.trigger(t.id, KIND.OPEN);
    },
    refresh,
    pauseAll,
    async stop() {
      scheduler.stopAll(); // final: tab events and handlers can no longer start a read
      offTabs?.();
      offTabs = null;
      if (pending.size) {
        let h;
        const limit = new Promise((r) => {
          h = setTimeout(r, 5000);
          h.unref?.();
        });
        await Promise.race([Promise.allSettled([...pending]), limit]);
        clearTimeout(h);
      }
    },
    /** Test seam. */
    _scheduler: scheduler,
  };
}
