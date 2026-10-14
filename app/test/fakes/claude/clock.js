// A fake clock for scheduler tests: `now()`, `setTimeout`, `clearTimeout` and `advance(ms)`,
// which fires due timers in order (and any they schedule), awaiting microtasks in between so
// an async read started by a timer can settle.

export function fakeClock(start = 1_000_000) {
  let t = start;
  let seq = 0;
  /** @type {Map<number, {at: number, fn: Function, seq: number}>} */
  const timers = new Map();
  const flush = async () => {
    for (let i = 0; i < 20; i++) await new Promise((r) => setImmediate(r));
  };
  return {
    now: () => t,
    setTimeout(fn, ms) {
      const id = ++seq;
      timers.set(id, { at: t + Math.max(0, ms), fn, seq: id });
      return id;
    },
    clearTimeout(id) {
      timers.delete(id);
    },
    pending: () => timers.size,
    /** Advance by ms, firing every timer that falls due. */
    async advance(ms) {
      const end = t + ms;
      for (;;) {
        const due = [...timers.values()].filter((x) => x.at <= end).sort((a, b) => a.at - b.at || a.seq - b.seq)[0];
        if (!due) break;
        timers.delete(due.seq);
        t = Math.max(t, due.at);
        due.fn();
        await flush();
      }
      t = end;
      await flush();
    },
  };
}
