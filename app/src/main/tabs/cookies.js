// Cookie flushing: `cookies.flushStore()` every 30 s and before quitting, so a crash never
// costs a login (Chromium writes cookies lazily).

export const FLUSH_INTERVAL_MS = 30_000;

/**
 * @param {{sessions: () => object[], log?: object, setInterval?: Function, clearInterval?: Function,
 *          intervalMs?: number}} deps  sessions: the account tabs' sessions, now
 */
export function createCookieFlusher(deps) {
  const setI = deps.setInterval ?? setInterval;
  const clearI = deps.clearInterval ?? clearInterval;
  let handle = null;

  async function flush() {
    await Promise.all(
      deps.sessions().map(async (ses) => {
        try {
          await ses.cookies.flushStore();
        } catch (err) {
          deps.log?.warn?.('tabs: cookie flush failed:', err?.message ?? err);
        }
      }),
    );
  }

  return {
    flush,
    start() {
      if (handle != null) return;
      handle = setI(() => void flush(), deps.intervalMs ?? FLUSH_INTERVAL_MS);
      handle?.unref?.();
    },
    stop() {
      if (handle != null) clearI(handle);
      handle = null;
    },
  };
}
