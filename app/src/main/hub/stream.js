// The hub stream (C6): `GET /v1/stream` with `Last-Event-ID`, parsed by shared/sse.js.
// It reconnects after 1, 2, 4 … 60 s (the delay resets after a `hello`), reconnects when the
// hub sends nothing for 60 s (the hub pings every 25 s), calls onReset on `reset` (the caller
// refetches /v1/overview), and stops on 401 (onUnauthorized: pair again). A 403 does not mean
// the token is dead: onForbidden reports it and the stream retries with its backoff.

import { STREAM_RETRY_MS, STREAM_PING_TIMEOUT_MS } from '../../shared/constants.js';
import { createParser } from '../../shared/sse.js';
import { hubUrl, hubUrlAllowed } from './client.js';

/**
 * @param {{fetch: typeof fetch, getAuth: () => ({url: string, token: string}|null),
 *          reportedBySy?: () => boolean,
 *          onEvent: (type: string, data: any, id: string) => void,
 *          onReset: () => void, onUnauthorized: (status: number) => void,
 *          onForbidden?: (status: number) => void,
 *          onConnection?: (up: boolean) => void,
 *          timers?: {setTimeout: Function, clearTimeout: Function},
 *          retryMs?: number[], pingTimeoutMs?: number, log?: object}} deps
 * @returns {{start(): void, stop(): void, lastEventId(): string, running(): boolean}}
 */
export function createStream(deps) {
  const timers = deps.timers ?? { setTimeout, clearTimeout };
  const retryMs = deps.retryMs ?? STREAM_RETRY_MS;
  const pingMs = deps.pingTimeoutMs ?? STREAM_PING_TIMEOUT_MS;
  const log = deps.log ?? console;

  let running = false;
  let generation = 0; // a new start() or stop() invalidates the loop of the old one
  let attempt = 0;
  let lastId = '';
  let abort = null;
  let retryTimer = null;
  let pingTimer = null;
  let serverRetry = null;

  const clearTimers = () => {
    if (retryTimer) timers.clearTimeout(retryTimer);
    if (pingTimer) timers.clearTimeout(pingTimer);
    retryTimer = pingTimer = null;
  };

  function scheduleReconnect(gen) {
    if (!running || gen !== generation) return;
    const base = retryMs[Math.min(attempt, retryMs.length - 1)];
    const delay = serverRetry ?? base;
    serverRetry = null;
    attempt++;
    retryTimer = timers.setTimeout(() => {
      retryTimer = null;
      connect(gen);
    }, delay);
    retryTimer?.unref?.();
  }

  async function connect(gen) {
    if (!running || gen !== generation) return;
    const auth = deps.getAuth();
    if (!auth || !hubUrlAllowed(auth.url, deps.reportedBySy?.() ?? false)) return scheduleReconnect(gen);
    const url = hubUrl(auth.url, '/v1/stream');
    if (!url) return scheduleReconnect(gen);
    const ctl = new AbortController();
    abort = ctl;
    const headers = { authorization: `Bearer ${auth.token}`, accept: 'text/event-stream' };
    if (lastId) headers['last-event-id'] = lastId;

    const armPing = () => {
      if (pingTimer) timers.clearTimeout(pingTimer);
      pingTimer = timers.setTimeout(() => {
        log.warn?.('hub stream: no ping, reconnecting');
        ctl.abort();
      }, pingMs);
      pingTimer?.unref?.();
    };

    const parser = createParser({
      onRetry: (ms) => {
        serverRetry = ms;
      },
      onComment: () => {},
      onEvent: ({ event, data, id }) => {
        if (id) lastId = id;
        if (event === 'hello') attempt = 0;
        if (event === 'ping') return;
        let parsed = null;
        if (data) {
          try {
            parsed = JSON.parse(data);
          } catch {
            return; // a broken payload is skipped, the stream goes on
          }
        }
        if (event === 'reset') return deps.onReset();
        deps.onEvent(event, parsed, id);
      },
    });

    try {
      const res = await deps.fetch(url, { method: 'GET', headers, redirect: 'manual', signal: ctl.signal });
      if (gen !== generation) return;
      if (res.status === 401) {
        running = false;
        clearTimers();
        deps.onConnection?.(false);
        deps.onUnauthorized(res.status);
        return;
      }
      if (res.status === 403) {
        // The hub is there and knows the token, but refuses the stream: an error to show, not
        // a reason to drop the token. Retry with the backoff.
        log.warn?.('hub stream: the hub answered 403 (forbidden), retrying');
        deps.onForbidden?.(res.status);
        scheduleReconnect(gen);
        return;
      }
      if (res.status !== 200 || !res.body?.getReader) throw new Error(`stream answered ${res.status}`);
      deps.onConnection?.(true);
      armPing();
      const reader = res.body.getReader();
      const dec = new TextDecoder('utf-8');
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        armPing();
        parser.feed(dec.decode(value, { stream: true }));
      }
    } catch (err) {
      if (gen !== generation) return;
      if (err?.name !== 'AbortError') log.warn?.(`hub stream: ${String(err?.message ?? err).split('\n')[0]}`);
    }
    if (gen !== generation) return;
    if (pingTimer) timers.clearTimeout(pingTimer);
    pingTimer = null;
    parser.reset();
    deps.onConnection?.(false);
    scheduleReconnect(gen);
  }

  return {
    start() {
      if (running) return;
      running = true;
      attempt = 0;
      const gen = ++generation;
      connect(gen);
    },
    stop() {
      running = false;
      generation++;
      clearTimers();
      try {
        abort?.abort();
      } catch {
        // already closed
      }
      abort = null;
    },
    lastEventId: () => lastId,
    running: () => running,
  };
}
