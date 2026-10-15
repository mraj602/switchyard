// Which sessions are open, ended or bare imported history. No imports, so node can test it
// (pwa.test.cjs); state.js re-exports it.

// How long a finished turn may sit before the session counts as history, not open. Matches
// the hub's 12 h "stale" mark.
export const IDLE_MS = 12 * 36e5;

export const isEnded = (s) => s.state === 'ended' || s.state === 'gone';

// A row with nothing to show: no project, folder, title or text (sessions imported from old
// history look like this). The board folds them away.
export const isHistory = (s) => !s.needs_you && !s.project && !s.cwd && !s.title && !s.name && !s.last_message && !s.question &&
  (s.state === 'done' || isEnded(s));

// Last sign of life: the last event, else when the state began.
const lastActive = (s) => Date.parse(s.last_event_at || s.state_since || s.started_at || '');

// Open: not ended, and not a finished session that has been idle for 12 h (or that is bare
// imported history). Old `done` rows therefore never count as live or running.
export function isLive(s, now = Date.now()) {
  if (isEnded(s)) return false;
  if (s.needs_you || s.state !== 'done') return true;
  if (isHistory(s) || s.stale) return false;
  const t = lastActive(s);
  return Number.isNaN(t) || now - t < IDLE_MS;
}

// Open sessions per machine and per seat, from the session list (null while it is not loaded;
// callers then fall back to the hub's own counts).
export function liveCounts(sessions, now = Date.now()) {
  if (!sessions) return null;
  const out = { total: 0, machine: new Map(), seat: new Map() };
  for (const s of sessions) {
    if (!isLive(s, now)) continue;
    out.total++;
    if (s.machine) out.machine.set(s.machine, (out.machine.get(s.machine) || 0) + 1);
    if (s.seat) out.seat.set(s.seat, (out.seat.get(s.seat) || 0) + 1);
  }
  return out;
}
