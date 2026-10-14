// The main-process event bus (docs/phases/6.md wave 0): how modules talk without importing
// each other's internals. A typed emitter over a closed set of events; emitting or listening
// to any other name throws, and a payload is checked to be an object. Listeners run
// synchronously in subscription order; one that throws is reported through `onError` and the
// others still run. `last(event)` returns the newest payload, so a module created later (for
// example state.js assembling ShellState) starts from the current picture.
//
// Wave 0 owns this file. Events and their payloads:
//
//   tabs       {tabs: TabInfo[]}                     B, after any tab change (add, remove, reorder,
//                                                    rename, load, sign-in, reading/send flags)
//   usage      {id, read_state, stopped, retry_at,   B, after every read, stop, resume or off;
//               last_read, windows, windows_source}  per tab (id = tab id)
//   hub        HubState (shared/ipc.js)              C, after info, pairing, stream or service changes
//   needs_you  {count, sessions: [{id, route, title}]}  C, debounced 1 s after `session` events;
//                                                    sessions is at most TRAY_SESSIONS_MAX
//   alert      {alert, session}                      C, for a stream `alert` with change "fired"
//                                                    (both fetched: /v1/alerts/{id}, /v1/sessions/{id})
//   settings   Settings (shared/ipc.js)              E, after settings:set or an autostart change
//
// TabInfo = {id, name, reading, warning_accepted, send_to_hub, stopped, seat, last_url,
//            signed_in, loaded}  (app-state.json tabs[] plus live view facts)

export const EVENTS = Object.freeze(['tabs', 'usage', 'hub', 'needs_you', 'alert', 'settings']);

/**
 * @param {{onError?: (err: unknown, event: string) => void}} [opts]
 */
export function createBus(opts = {}) {
  const onError = opts.onError ?? ((err, event) => console.error(`bus: ${event} listener failed:`, err));
  /** @type {Map<string, Set<Function>>} */
  const listeners = new Map(EVENTS.map((e) => [e, new Set()]));
  /** @type {Map<string, object>} */
  const lastPayload = new Map();

  const known = (event) => {
    if (!listeners.has(event)) throw new Error(`bus: unknown event ${JSON.stringify(String(event))}`);
  };

  /** Subscribe; returns a function that unsubscribes. */
  const on = (event, fn) => {
    known(event);
    if (typeof fn !== 'function') throw new TypeError('bus.on: listener must be a function');
    // A wrapper per subscription, so the same function can subscribe twice and unsubscribe once.
    const entry = (p) => fn(p);
    listeners.get(event).add(entry);
    return () => {
      listeners.get(event).delete(entry);
    };
  };

  return Object.freeze({
    on,
    /** Subscribe for one emit. */
    once(event, fn) {
      const off = on(event, (p) => {
        off();
        fn(p);
      });
      return off;
    },
    /** Emit to every listener of event; payload must be a plain object. */
    emit(event, payload) {
      known(event);
      if (typeof payload !== 'object' || payload === null || Array.isArray(payload)) {
        throw new TypeError(`bus.emit(${event}): payload must be an object`);
      }
      lastPayload.set(event, payload);
      for (const fn of [...listeners.get(event)]) {
        try {
          fn(payload);
        } catch (err) {
          onError(err, event);
        }
      }
    },
    /** The newest payload of event, or undefined. */
    last(event) {
      known(event);
      return lastPayload.get(event);
    },
    /** Number of listeners (tests, leak checks). */
    listenerCount(event) {
      known(event);
      return listeners.get(event).size;
    },
  });
}
