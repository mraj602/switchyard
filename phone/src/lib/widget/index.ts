// Widget snapshot and timeline (phone.md P11). Owner: WP-W. Pure: no React, no native module.
import type { Overview, Seat, Window } from '../../contract/api';
import { WIDGET_KEY } from '../../contract/constants';
import type { KVStore } from '../../contract/platform';
import {
  SNAPSHOT_LABEL_MAX,
  SNAPSHOT_MAX_SEATS,
  TIMELINE_MAX,
  type SnapshotNextReset,
  type SnapshotSeat,
  type SnapshotTop,
  type SnapshotWindow,
  type TimelineEntry,
  type WidgetSnapshot,
  type WindowKey,
} from '../../contract/snapshot';
import { validateWidgetSnapshot } from '../../contract/validate';

export interface SnapshotPrefs {
  private: boolean;
  /** The hub origin; only its host name goes into the snapshot. */
  origin: string;
}

const WINDOW_KEYS: readonly WindowKey[] = ['five_hour', 'seven_day'];
const HOUR = 3_600_000;
const FALLBACK_COLOUR = '#8a8a8a';

/** The host name of an origin ("https://hub.example.ts.net:8443" -> "hub.example.ts.net"). */
function hostOf(origin: string): string {
  const m = /^[a-z][a-z0-9+.-]*:\/\/(?:[^/@?#]*@)?(\[[^\]]*\]|[^/:?#]*)/i.exec(origin.trim());
  return m?.[1] ?? '';
}

/** The short state word (P11 example: "asked", "perm", "failed"). */
function whatOf(state: string): string {
  return state === 'needs_permission' ? 'perm' : state;
}

function seatLabel(s: Seat): string {
  const raw = s.label || s.org_name || (s.email ? s.email.split('@')[0] : '') || s.account || s.id;
  return [...(raw ?? '')].slice(0, SNAPSHOT_LABEL_MAX).join('');
}

function windowOf(s: Seat, key: WindowKey): SnapshotWindow | null {
  const w: Window | undefined = s.windows?.find((x) => x.key === key);
  if (!w || typeof w.used_pct !== 'number' || !Number.isFinite(w.used_pct) || !w.resets_at) return null;
  return { pct: Math.min(100, Math.max(0, Math.round(w.used_pct))), resets_at: w.resets_at };
}

/** The earliest reset after `after` among the given seats' windows (ties: higher pct). */
function earliest(seats: SnapshotSeat[], after: number, skip?: Set<string>): SnapshotNextReset | null {
  let best: SnapshotNextReset | null = null;
  let bestMs = 0;
  for (const s of seats) {
    for (const key of WINDOW_KEYS) {
      const w = s[key];
      if (!w || skip?.has(resetKey(s.id, key))) continue;
      const ms = Date.parse(w.resets_at);
      if (!(ms > after)) continue;
      if (!best || ms < bestMs || (ms === bestMs && w.pct > best.pct)) {
        best = { seat: s.label, window: key, resets_at: w.resets_at, pct: w.pct };
        bestMs = ms;
      }
    }
  }
  return best;
}

/** At most 4 seats in Accounts order; `top` null in private mode; labels cut to 12. */
export function buildSnapshot(overview: Overview, accounts: Seat[], now: number, prefs: SnapshotPrefs): WidgetSnapshot {
  const source = accounts.length > 0 ? accounts : overview.seats;
  const ordered = source
    .map((s, i) => ({ s, i }))
    .filter(({ s }) => !s.hidden)
    .sort((a, b) => (a.s.order ?? 0) - (b.s.order ?? 0) || a.i - b.i)
    .slice(0, SNAPSHOT_MAX_SEATS)
    .map(({ s }) => s);

  const seats: SnapshotSeat[] = ordered.map((s) => ({
    id: s.id,
    label: seatLabel(s),
    colour: s.colour || FALLBACK_COLOUR,
    five_hour: windowOf(s, 'five_hour'),
    seven_day: windowOf(s, 'seven_day'),
  }));

  const needs = overview.needs_you ?? [];
  const count = overview.counts?.needs_you ?? needs.length;
  const first = needs[0];
  const top: SnapshotTop | null =
    prefs.private || !first || count === 0
      ? null
      : {
          seat: (first.seat_label || first.seat || '').slice(0, SNAPSHOT_LABEL_MAX),
          project: first.project || first.name || first.title || first.cwd?.split('/').filter(Boolean).pop() || 'session',
          what: whatOf(first.state),
          since: first.state_since,
        };

  return {
    v: 1,
    as_of: new Date(now).toISOString(),
    hub: hostOf(prefs.origin),
    needs_you: count,
    top,
    seats,
    next_reset: earliest(seats, now),
    private: prefs.private,
  };
}

// ------------------------------------------------------------------ timeline

/** Key of a seat window in `WidgetTimelineProps.reset`. */
export function resetKey(seatId: string, window: WindowKey): string {
  return `${seatId}:${window}`;
}

/** Windows whose reset time is at or before `ms`. */
export function resetKeysAt(snapshot: WidgetSnapshot, ms: number): string[] {
  const out: string[] = [];
  for (const s of snapshot.seats) {
    for (const key of WINDOW_KEYS) {
      const w = s[key];
      if (w && Date.parse(w.resets_at) <= ms) out.push(resetKey(s.id, key));
    }
  }
  return out;
}

/** The next reset after `ms` (the snapshot's own `next_reset` can be in the past by then). */
export function nextResetAt(snapshot: WidgetSnapshot, ms: number): SnapshotNextReset | null {
  return earliest(snapshot.seats, ms);
}

/** One account as a widget draws it. */
export interface SeatRow {
  id: string;
  label: string;
  colour: string;
  /** The ring: the 5-hour window, or the 7-day one when there is no 5-hour window. */
  window: WindowKey;
  pct: number;
  /** When the ring resets; null once it has (it then reads "reset" at 0%). */
  resets_at: string | null;
  reset: boolean;
  /** The other window, small. */
  other: { window: WindowKey; pct: number; reset: boolean } | null;
}

/** Up to 4 rows; windows named in `reset` show 0% and "reset". */
export function seatRows(snapshot: WidgetSnapshot, reset: string[]): SeatRow[] {
  const done = new Set(reset);
  const rows: SeatRow[] = [];
  for (const s of snapshot.seats.slice(0, SNAPSHOT_MAX_SEATS)) {
    const present = WINDOW_KEYS.filter((k) => s[k]);
    const main = present[0];
    if (!main) continue;
    const w = s[main] as SnapshotWindow;
    const isReset = done.has(resetKey(s.id, main));
    const o = present[1];
    const ow = o ? (s[o] as SnapshotWindow) : null;
    rows.push({
      id: s.id,
      label: s.label,
      colour: s.colour,
      window: main,
      pct: isReset ? 0 : w.pct,
      resets_at: isReset ? null : w.resets_at,
      reset: isReset,
      other: o && ow ? { window: o, pct: done.has(resetKey(s.id, o)) ? 0 : ow.pct, reset: done.has(resetKey(s.id, o)) } : null,
    });
  }
  return rows;
}

/** What a widget component renders for one timeline entry. Plain JSON: it crosses to Swift. */
export interface WidgetTimelineProps {
  /** null: nothing written yet ("Open Switchyard to set up"). */
  snapshot: WidgetSnapshot | null;
  /** Windows whose reset has passed by this entry read "reset" at 0% (see resetKey). */
  reset: string[];
  /** "as of 14:02" once the snapshot is an hour old; null before. */
  stale: string | null;
  /** The next reset still ahead at this entry. */
  next: SnapshotNextReset | null;
  /** Up to 4 accounts, ready to draw. */
  rows: SeatRow[];
}

/** "14:02" in the given time zone (the device's when omitted). */
export function clock(ms: number | string, tz?: string): string {
  const d = new Date(ms);
  return new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit', hourCycle: 'h23', ...(tz ? { timeZone: tz } : {}) }).format(d);
}

function entryProps(snapshot: WidgetSnapshot, ms: number, staleAt: number, tz: string | undefined): WidgetTimelineProps {
  const reset = resetKeysAt(snapshot, ms);
  return {
    snapshot,
    reset,
    stale: ms >= staleAt ? `as of ${clock(snapshot.as_of, tz)}` : null,
    next: nextResetAt(snapshot, ms),
    rows: seatRows(snapshot, reset),
  };
}

/** The "nothing yet" timeline: one entry that tells the person to open the app. */
export function emptyTimeline(now: number): TimelineEntry<WidgetTimelineProps>[] {
  return [{ date: new Date(now), props: { snapshot: null, reset: [], stale: null, next: null, rows: [] } }];
}

/**
 * Now, each reset in the next 24 h, and the stale entry at as_of + 60 min; at most 8. When more
 * than 8 would apply, the earliest resets win, and "now" and "stale" always stay. `tz` only
 * decides how "as of 14:02" is written; entry dates are absolute instants.
 */
export function buildTimeline(snapshot: WidgetSnapshot, now: number, tz?: string): TimelineEntry<WidgetTimelineProps>[] {
  const staleAt = Date.parse(snapshot.as_of) + HOUR;
  const horizon = now + 24 * HOUR;
  const resets = new Set<number>();
  for (const s of snapshot.seats) {
    for (const key of WINDOW_KEYS) {
      const w = s[key];
      if (!w) continue;
      const ms = Date.parse(w.resets_at);
      if (ms > now && ms <= horizon && ms !== staleAt) resets.add(ms);
    }
  }
  const stale = staleAt > now;
  const room = TIMELINE_MAX - 1 - (stale ? 1 : 0);
  const times = [now, ...[...resets].sort((a, b) => a - b).slice(0, room), ...(stale ? [staleAt] : [])].sort((a, b) => a - b);
  return times.map((ms) => ({ date: new Date(ms), props: entryProps(snapshot, ms, staleAt, tz) }));
}

// ------------------------------------------------------------------ Android

/** Props of the Android widgets (resets as clock times, "resets 17:00"). */
export interface AndroidWidgetProps {
  snapshot: WidgetSnapshot | null; // null: "Open Switchyard to set up"
  now: number;
  tz?: string;
}

export function androidProps(snapshot: WidgetSnapshot | null, now: number, tz?: string): AndroidWidgetProps {
  return { snapshot, now, ...(tz ? { tz } : {}) };
}

// ------------------------------------------------------------------ storage (Android)

/** Writes the snapshot where the Android task handler reads it (never a secret). */
export async function writeSnapshot(store: KVStore, snapshot: WidgetSnapshot): Promise<void> {
  await store.setItem(WIDGET_KEY, JSON.stringify(snapshot));
}

/** The last snapshot, or null when none was written or it does not validate. */
export async function readSnapshot(store: KVStore): Promise<WidgetSnapshot | null> {
  try {
    const raw = await store.getItem(WIDGET_KEY);
    if (!raw) return null;
    const x: unknown = JSON.parse(raw);
    return validateWidgetSnapshot(x).length === 0 ? (x as WidgetSnapshot) : null;
  } catch {
    return null;
  }
}

export async function clearSnapshot(store: KVStore): Promise<void> {
  await store.removeItem(WIDGET_KEY);
}
