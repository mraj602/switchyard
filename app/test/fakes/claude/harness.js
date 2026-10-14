// Small stand-ins for the other work packages' modules, so WP-B's tabs/ and usage/ tests run on
// plain Node: an in-memory store with store.js's interface, a recording `sy`, an `ipc` that
// keeps its handlers, and a `security` / `window` that record what tabs/ asks of them.

import { createBus } from '../../../src/main/bus.js';

export function memStore(tabs = []) {
  let state = { v: 1, tabs: structuredClone(tabs), settings: {} };
  const updates = [];
  return {
    get: () => structuredClone(state),
    async update(mutate) {
      const draft = structuredClone(state);
      mutate(draft);
      state = draft;
      updates.push(structuredClone(draft));
      return structuredClone(state);
    },
    async flush() {},
    updates,
  };
}

/** A tab record as app-state.json holds it. */
export const tabRecord = (id, over = {}) => ({
  id,
  name: id,
  last_url: 'https://claude.ai/new',
  reading: 'never_asked',
  warning_accepted: 0,
  send_to_hub: true,
  stopped: null,
  seat: null,
  ...over,
});

/** sy: append(records) records every batch, returns ids unless `appendResult` says otherwise. */
export function fakeSy(opts = {}) {
  const sy = {
    appended: [],
    batches: [],
    flushes: 0,
    machine: opts.machine ?? 'm_01JA2W5N0G8H3Q7S2V6X9Y1Z4K',
    appendResult: null,
    async append(records) {
      sy.batches.push(records);
      sy.appended.push(...records);
      if (sy.appendResult) return sy.appendResult(records);
      return { ok: true, ids: records.map((_, i) => `id${sy.appended.length}-${i}`) };
    },
    flushSoon() {
      sy.flushes += 1;
    },
    async machineId() {
      return sy.machine;
    },
  };
  return sy;
}

export function fakeIpc() {
  const handlers = new Map();
  return {
    handlers,
    register(channel, fn) {
      if (handlers.has(channel)) throw new Error(`twice: ${channel}`);
      handlers.set(channel, fn);
    },
    call: (channel, args) => handlers.get(channel)(args),
    send() {},
  };
}

export function fakeSecurity() {
  const calls = { guardView: [], guardSession: [], webPreferences: [], openExternalSafe: [] };
  return {
    calls,
    webPreferences(kind, extra = {}) {
      calls.webPreferences.push([kind, extra]);
      return { contextIsolation: true, sandbox: true, nodeIntegration: false, ...extra };
    },
    guardView: (wc, kind, opts) => calls.guardView.push([wc, kind, opts]),
    guardSession: (ses, kind) => calls.guardSession.push([ses, kind]),
    async openExternalSafe(url) {
      calls.openExternalSafe.push(url);
      return true;
    },
  };
}

export function fakeWindow() {
  const w = {
    views: new Map(),
    selectedId: 'hub',
    shown: 0,
    addView: (id, view, kind) => w.views.set(id, { view, kind }),
    removeView: (id) => w.views.delete(id),
    select: (id) => (w.selectedId = id),
    selected: () => w.selectedId,
    showWindow: () => (w.shown += 1),
    isShowing: () => false,
  };
  return w;
}

export const silentLog = { debug() {}, info() {}, warn() {}, error() {} };

export function newBus() {
  return createBus({ onError: (e) => { throw e; } });
}

/** Poll `cond` every 5 ms (real time) until it holds, or fail after `ms`. */
export async function waitFor(cond, ms = 3000, what = 'condition') {
  const end = Date.now() + ms;
  for (;;) {
    const v = await cond();
    if (v) return v;
    if (Date.now() > end) throw new Error(`waitFor: ${what} not met in ${ms} ms`);
    await new Promise((r) => setTimeout(r, 5));
  }
}

export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
