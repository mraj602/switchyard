// Widget snapshot and timeline (phone.md P11). Owner: WP-W. Pure: no React, no native module.
import type { Overview, Seat, Window } from '../../contract/api';
import { WIDGET_KEY } from '../../contract/constants';
import type { LinkRefresh } from '../../contract/link';
import type { KVStore } from '../../contract/platform';
import {
  SNAPSHOT_LABEL_MAX,
  SNAPSHOT_IOS_SEATS,
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
import { orderSeats } from '../format/seat';
import { tighterWindow } from '../format/windows';

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

/** At most 8 seats in Accounts order; `top` null in private mode; labels cut to 12. */
export function buildSnapshot(overview: Overview, accounts: Seat[], now: number, prefs: SnapshotPrefs): WidgetSnapshot {
  const source = accounts.length > 0 ? accounts : overview.seats;
  // The hub's order (the owner's drag-and-drop order, `seat.order`; seats without one last).
  const ordered = orderSeats(source)
    .filter((s) => !s.hidden)
    .slice(0, SNAPSHOT_MAX_SEATS);

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
    counts: {
      working: countOf(overview.counts?.working),
      needs_you: count,
      done: countOf(overview.counts?.done),
    },
  };
}

function countOf(n: number | undefined): number {
  return typeof n === 'number' && Number.isFinite(n) ? Math.max(0, Math.trunc(n)) : 0;
}

/**
 * A link `refresh` frame (link.md 6.4, push.md 13.5) on top of the stored snapshot, so the
 * widgets redraw with no network call (P11): `needs_you` and the windows of the seats the
 * snapshot already shows. A refresh older than the snapshot, or no snapshot, changes nothing
 * (the frame has no labels or colours to build one from). `top` is cleared when nothing needs
 * you any more.
 */
export function applyLinkRefresh(snapshot: WidgetSnapshot | null, refresh: LinkRefresh | null): WidgetSnapshot | null {
  if (!snapshot || !refresh || refresh.v !== 1) return snapshot;
  const at = Date.parse(refresh.ts);
  if (!Number.isFinite(at) || at <= Date.parse(snapshot.as_of)) return snapshot;
  const seats = snapshot.seats.map((s) => {
    const next: SnapshotSeat = { ...s };
    for (const w of refresh.seats ?? []) {
      if (w.seat !== s.id || (w.window !== 'five_hour' && w.window !== 'seven_day')) continue;
      if (typeof w.used_pct !== 'number' || !Number.isFinite(w.used_pct) || !w.resets_at) continue;
      next[w.window] = { pct: Math.min(100, Math.max(0, Math.round(w.used_pct))), resets_at: w.resets_at };
    }
    return next;
  });
  const needs = Math.max(0, Math.trunc(Number.isFinite(refresh.needs_you) ? refresh.needs_you : snapshot.needs_you));
  return {
    ...snapshot,
    as_of: new Date(at).toISOString(),
    needs_you: needs,
    ...(snapshot.counts ? { counts: { ...snapshot.counts, needs_you: needs } } : {}),
    top: needs === 0 ? null : snapshot.top,
    seats,
    next_reset: earliest(seats, at),
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

/** Up to 4 rows (the iOS widgets); windows named in `reset` show 0% and "reset". */
export function seatRows(snapshot: WidgetSnapshot, reset: string[]): SeatRow[] {
  const done = new Set(reset);
  const rows: SeatRow[] = [];
  for (const s of snapshot.seats.slice(0, SNAPSHOT_IOS_SEATS)) {
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
  /** null: nothing written yet ("Open Switchyard once to load"). */
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

/** One account of the Limits widget: the tighter of 5 h and 7 d. An idle or unread account is 0%. */
export interface LimitRow {
  id: string;
  label: string;
  colour: string;
  pct: number;
  /** "5 h" or "7 d"; null when the account has no reading (0%, idle). */
  window: '5 h' | '7 d' | null;
  /** That window's reset; null when idle or already past ("reset"). */
  resets_at: string | null;
  /** The window's reset time has passed. */
  reset: boolean;
}

/** Every account in the snapshot, in Accounts order, as the Limits widget draws it (format/windows `tighterWindow`). */
export function limitRows(snapshot: WidgetSnapshot, now: number): LimitRow[] {
  return snapshot.seats.map((s) => {
    const windows: Window[] = [];
    for (const key of WINDOW_KEYS) {
      const w = s[key];
      if (w) windows.push({ key, used_pct: w.pct, resets_at: w.resets_at });
    }
    const t = tighterWindow({ windows }, now);
    const base = { id: s.id, label: s.label, colour: s.colour };
    if (!t) return { ...base, pct: 0, window: null, resets_at: null, reset: false };
    const at = t.w?.resets_at ?? null;
    const past = at ? Date.parse(at) <= now : false;
    return { ...base, pct: Math.min(100, Math.max(0, Math.round(t.used))), window: t.short, resets_at: past ? null : at, reset: past };
  });
}

/** The three counts of the Sessions widget (labels as the Now screen words them). */
export interface SessionTally {
  running: number;
  needsInput: number;
  done: number;
}

export function sessionTally(snapshot: WidgetSnapshot): SessionTally {
  const c = snapshot.counts;
  return { running: c?.working ?? 0, needsInput: c?.needs_you ?? snapshot.needs_you, done: c?.done ?? 0 };
}

/** "just now", "3 min ago", "2 h ago", "4 d ago". */
export function agoWords(asOf: string, now: number): string {
  const t = Date.parse(asOf);
  if (!Number.isFinite(t)) return '';
  const m = Math.max(0, Math.floor((now - t) / 60_000));
  if (m < 1) return 'just now';
  if (m < 60) return `${m} min ago`;
  const h = Math.floor(m / 60);
  return h < 48 ? `${h} h ago` : `${Math.floor(h / 24)} d ago`;
}

/** How many account rows fit a widget `heightDp` tall (header and padding taken off), 1..8. */
export function rowsThatFit(heightDp: number | undefined, total: number): number {
  const fit = heightDp && heightDp > 0 ? Math.floor((heightDp - 44) / 30) : 4;
  return Math.max(1, Math.min(total, SNAPSHOT_MAX_SEATS, fit));
}

/** Props of the Android widgets (resets as clock times, "resets 17:00"). */
export interface AndroidWidgetProps {
  snapshot: WidgetSnapshot | null; // null: "Open Switchyard once to load"
  now: number;
  tz?: string;
  /** The widget's current height, so the Limits widget draws as many rows as fit. */
  height?: number;
}

export function androidProps(snapshot: WidgetSnapshot | null, now: number, tz?: string, height?: number): AndroidWidgetProps {
  return { snapshot, now, ...(tz ? { tz } : {}), ...(height ? { height } : {}) };
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
