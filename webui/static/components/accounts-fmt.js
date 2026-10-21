// Accounts page: the tighter window, what needs a look, and "most room right now" (pure; no imports).

const win = (seat, key) => (seat.windows || []).find((w) => w.key === key);
const has = (w) => !!w && w.used_pct != null && !Number.isNaN(Number(w.used_pct));

// True when the seat has any reading at all.
export const hasReading = (seat) => (seat.windows || []).some(has);

// How a window reads now (api.yaml Window.state): 'idle' (claude.ai says it has not started:
// 0 %, no reset time; an older hub sends no state but the same 0 % with no reset), 'reset' (its
// resets_at has passed: nothing used since) or 'current'.
export function windowPhase(w, now = Date.now()) {
  if (w.state === 'idle' || (!w.resets_at && w.used_pct != null && Number(w.used_pct) === 0)) return 'idle';
  const reset = Date.parse(w.resets_at || '');
  return !Number.isNaN(reset) && reset <= now ? 'reset' : 'current';
}

// Use of one window for ranking: 0 once its reset has passed or while it is idle (not
// started), or when the seat has no reading for it but has one for another window; otherwise
// its used_pct (an old reading counts until its reset). null when the seat has no reading at all.
export function effectiveUse(seat, key, now = Date.now()) {
  if (!hasReading(seat)) return null;
  const w = win(seat, key);
  if (!has(w)) return 0;
  if (windowPhase(w, now) !== 'current') return 0;
  return Number(w.used_pct);
}

// Push.md 5.7 / pwa.md: lowest of max(5 h, 7 d) among visible seats, only under 80%.
// Ties: lower 7 d, then lower 5 h, then list order. Returns { seat, five, seven } or null.
export function mostRoom(seats, now = Date.now()) {
  let best = null;
  for (const s of (seats || []).filter((x) => !x.hidden)) {
    const five = effectiveUse(s, 'five_hour', now);
    if (five == null) continue;
    const seven = effectiveUse(s, 'seven_day', now);
    const score = Math.max(five, seven);
    if (score >= 80) continue;
    if (!best || score < best.score || (score === best.score && (seven < best.seven || (seven === best.seven && five < best.five)))) {
      best = { seat: s, five, seven, score };
    }
  }
  return best ? { seat: best.seat, five: best.five, seven: best.seven } : null;
}

// The tighter of the 5 h and 7 d windows: { key, label, used, phase, resets_at } or null.
// phase is windowPhase of that window (null when the seat has no such window); resets_at is
// its reset time (passed when phase is 'reset'), null when idle or unknown.
export function tighter(seat, now = Date.now()) {
  const five = effectiveUse(seat, 'five_hour', now);
  if (five == null) return null;
  const seven = effectiveUse(seat, 'seven_day', now);
  const key = seven > five ? 'seven_day' : 'five_hour';
  const w = win(seat, key);
  const used = key === 'seven_day' ? seven : five;
  const phase = has(w) ? windowPhase(w, now) : null;
  return { key, label: key === 'seven_day' ? '7 d' : '5 h', used, phase, resets_at: phase && phase !== 'idle' ? w.resets_at || null : null };
}

// Machines where Claude Code is signed out on this seat (not also signed in there).
export function signedOutOn(seat) {
  const inOn = new Set((seat.places || []).filter((p) => p.slot).map((p) => p.machine));
  const out = [];
  for (const p of seat.signed_out || []) {
    if (inOn.has(p.machine) || out.includes(p.machine_name || p.machine)) continue;
    out.push(p.machine_name || p.machine);
  }
  return out;
}

// What needs a look, most urgent first: [{ kind: 'near' | 'signed_out' | 'other', ... }].
export function attention(seat, now = Date.now()) {
  const out = [];
  const five = win(seat, 'five_hour');
  const seven = effectiveUse(seat, 'seven_day', now);
  const pace = five && five.pace;
  if (pace && pace.band === 'runaway' && effectiveUse(seat, 'five_hour', now) > 0) out.push({ kind: 'near', key: 'five_hour', at: pace.full_at || null });
  else if (seven != null && seven >= 90) out.push({ kind: 'near', key: 'seven_day', at: (win(seat, 'seven_day') || {}).resets_at || null });
  const so = signedOutOn(seat);
  if (so.length) out.push({ kind: 'signed_out', machines: so });
  for (const p of seat.places || []) {
    if (p.stopped_reason) { out.push({ kind: 'other', text: 'claude.ai usage reading stopped' }); break; }
  }
  return out;
}
