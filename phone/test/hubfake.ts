// Offline fakes for the hub client tests: a recording fetch, a manual clock, an app-state
// source and an in-memory KV store. No network; nothing here is imported by src/.
import type { AppStateSource, Clock, FetchLike, KVStore, RequestInitLike, ResponseLike } from '../src/contract/platform';

export const ORIGIN = 'https://hub.test.ts.net';
export const TOKEN = 'wy_p_SECRETTOKEN0000000000000000000000000000000';

export function response(status: number, body?: unknown, extra: Partial<ResponseLike> = {}): ResponseLike {
  const text = body === undefined ? '' : typeof body === 'string' ? body : JSON.stringify(body);
  return {
    status,
    ok: status >= 200 && status < 300,
    headers: { get: () => null },
    text: async () => text,
    json: async () => JSON.parse(text),
    ...extra,
  };
}

export interface FetchCall {
  url: string;
  init: RequestInitLike;
}

export type Handler = (url: string, init: RequestInitLike) => ResponseLike | Promise<ResponseLike>;

export interface FakeFetch extends FetchLike {
  calls: FetchCall[];
  handler: Handler;
}

export function fakeFetch(handler: Handler): FakeFetch {
  const f = (async (url: string, init: RequestInitLike = {}) => {
    f.calls.push({ url, init });
    return f.handler(url, init);
  }) as FakeFetch;
  f.calls = [];
  f.handler = handler;
  return f;
}

/** The POST /v1/pair/claim calls of a fake (a claim first probes GET /health without credentials). */
export function claims(f: FakeFetch): FetchCall[] {
  return f.calls.filter((c) => c.url.endsWith('/v1/pair/claim'));
}

/** A fetch that never answers until aborted (for timeouts). */
export function hangingFetch(): FakeFetch {
  return fakeFetch(
    (_url, init) =>
      new Promise((_resolve, reject) => {
        init.signal?.addEventListener('abort', () => reject(new Error('aborted')));
      }),
  );
}

export interface ManualClock extends Clock {
  advance(ms: number): Promise<void>;
  pending(): number;
}

/** Timers run only when `advance` moves time past them. */
export function manualClock(start = Date.parse('2026-10-13T12:00:00Z')): ManualClock {
  let t = start;
  let seq = 0;
  const timers = new Map<number, { at: number; fn: () => void }>();
  return {
    now: () => t,
    setTimeout(fn, ms) {
      const id = ++seq;
      timers.set(id, { at: t + ms, fn });
      return id;
    },
    clearTimeout(h) {
      timers.delete(h as number);
    },
    pending: () => timers.size,
    async advance(ms) {
      const end = t + ms;
      for (;;) {
        const due = [...timers.entries()].filter(([, v]) => v.at <= end).sort((a, b) => a[1].at - b[1].at)[0];
        if (!due) break;
        timers.delete(due[0]);
        t = Math.max(t, due[1].at);
        due[1].fn();
        for (let i = 0; i < 20; i++) await Promise.resolve();
      }
      t = end;
    },
  };
}

export interface FakeAppState extends AppStateSource {
  set(state: string): void;
}

export function fakeAppState(initial = 'active'): FakeAppState {
  let cur = initial;
  const subs = new Set<(s: string) => void>();
  return {
    current: () => cur,
    subscribe(fn) {
      subs.add(fn);
      return () => subs.delete(fn);
    },
    set(state) {
      cur = state;
      subs.forEach((f) => f(state));
    },
  };
}

export function memoryKV(): KVStore & { items: Map<string, string> } {
  const items = new Map<string, string>();
  return {
    items,
    async getItem(k) {
      return items.get(k) ?? null;
    },
    async setItem(k, v) {
      items.set(k, v);
    },
    async removeItem(k) {
      items.delete(k);
    },
  };
}

export async function flush(n = 30): Promise<void> {
  for (let i = 0; i < n; i++) await Promise.resolve();
}
