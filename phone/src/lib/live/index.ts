// Live Activity choice and end policy (phone.md P12). Owner: WP-W. Pure.
import { LIVE_END_AFTER_MS, LIVE_MAX_H, LIVE_MIN_PCT, type LiveCountdownProps } from '../../contract/live';
import type { SnapshotSeat, WidgetSnapshot, WindowKey } from '../../contract/snapshot';

const WINDOWS: readonly WindowKey[] = ['five_hour', 'seven_day'];

function pick(seats: SnapshotSeat[], now: number): LiveCountdownProps | null {
  let best: LiveCountdownProps | null = null;
  let bestMs = 0;
  for (const s of seats) {
    for (const key of WINDOWS) {
      const w = s[key];
      if (!w || w.pct < LIVE_MIN_PCT) continue;
      const ms = Date.parse(w.resets_at);
      if (!(ms > now) || ms - now > LIVE_MAX_H * 3_600_000) continue;
      // Highest pct; a tie goes to the earlier reset; a full tie keeps the first (Accounts order).
      if (!best || w.pct > best.pct || (w.pct === best.pct && ms < bestMs)) {
        best = { seat: s.label, colour: s.colour, window: key, pct: w.pct, resets_at: w.resets_at, started_at: new Date(now).toISOString() };
        bestMs = ms;
      }
    }
  }
  return best;
}

/**
 * The seat × window with the highest pct among windows resetting within LIVE_MAX_H hours and
 * at ≥ LIVE_MIN_PCT, ties by pct then the earlier reset; null when none qualifies.
 */
export function chooseCountdown(snapshot: WidgetSnapshot, now: number): LiveCountdownProps | null {
  return pick(snapshot.seats, now);
}

/** When to dismiss the activity: resets_at + 15 min (expo-widgets `end(after(date))`). */
export function endAt(props: LiveCountdownProps): Date {
  return new Date(Date.parse(props.resets_at) + LIVE_END_AFTER_MS);
}

/** The countdown for a seat the person tapped (its id or its label), if that card qualifies. */
export function countdownFor(snapshot: WidgetSnapshot, seat: string, now: number): LiveCountdownProps | null {
  return pick(
    snapshot.seats.filter((s) => s.id === seat || s.label === seat),
    now,
  );
}

/** What the app does with a running activity after a refresh. */
export type LiveStep =
  | { kind: 'none' }
  | { kind: 'start'; props: LiveCountdownProps }
  | { kind: 'update'; props: LiveCountdownProps }
  | { kind: 'end'; at: Date };

/**
 * The end policy. `running` is what the activity last showed (null when no activity runs).
 * - Nothing runs: start only when `wantStart` (the person asked, or auto countdown on a limit
 *   push) and something qualifies.
 * - Something runs and its reset has passed: end, dismissed 15 min after the reset.
 * - Something runs and a window still qualifies: update it (the choice may move to another seat).
 * - Something runs but nothing qualifies any more (usage dropped, or the snapshot is gone):
 *   end, dismissed 15 min after the reset it showed.
 */
export function planLive(snapshot: WidgetSnapshot | null, now: number, running: LiveCountdownProps | null, wantStart: boolean): LiveStep {
  const choice = snapshot ? chooseCountdown(snapshot, now) : null;
  if (!running) return wantStart && choice ? { kind: 'start', props: choice } : { kind: 'none' };
  if (Date.parse(running.resets_at) <= now) return { kind: 'end', at: endAt(running) };
  if (choice) return { kind: 'update', props: choice };
  return { kind: 'end', at: endAt(running) };
}
