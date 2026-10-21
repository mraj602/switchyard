// Pure helpers of the Needs-you list, Account ring, Next reset and Machines widgets (phone.md
// P11). No React, no native module: props in, plain data out.
import type { Window } from '../../contract/api';
import { WIDGET_RING_KEY, WIDGET_WINDOW_KEY } from '../../contract/constants';
import type { KVStore } from '../../contract/platform';
import type { SnapshotMachine, WidgetSnapshot } from '../../contract/snapshot';
import { resetWords, shownUse, windowPhase } from '../format/windows';
import { nextResetAt } from './index';

/** "now", "4 min", "2 h", "3 d": a compact age for rows. */
export function shortAge(from: number | string, now: number): string {
  const t = typeof from === 'number' ? from : Date.parse(from);
  if (!Number.isFinite(t)) return '';
  const m = Math.max(0, Math.floor((now - t) / 60_000));
  if (m < 1) return 'now';
  if (m < 60) return `${m} min`;
  const h = Math.floor(m / 60);
  return h < 48 ? `${h} h` : `${Math.floor(h / 24)} d`;
}

/** The state as the row words it. */
export function stateWords(what: string): string {
  return what === 'perm' ? 'needs permission' : what;
}

export interface NeedsRow {
  id: string;
  seat: string;
  colour: string;
  project: string;
  state: string;
  /** Age of the wait: "4 min". */
  age: string;
}

/** Up to `max` waiting sessions, oldest first. Empty in private mode (the snapshot holds none). */
export function needsRows(snapshot: WidgetSnapshot, now: number, max = 3): NeedsRow[] {
  return (snapshot.needs_list ?? [])
    .slice()
    .sort((a, b) => Date.parse(a.since) - Date.parse(b.since))
    .slice(0, max)
    .map((n) => ({ id: n.id, seat: n.seat, colour: n.colour, project: n.project, state: stateWords(n.what), age: shortAge(n.since, now) }));
}

/** How many needs rows fit a widget `heightDp` tall (header and padding off, ~34 dp a row), 1..3. */
export function needsRowsThatFit(heightDp: number | undefined): number {
  const fit = heightDp && heightDp > 0 ? Math.floor((heightDp - 44) / 34) : 3;
  return Math.max(1, Math.min(3, fit));
}

export interface MachineRow {
  id: string;
  name: string;
  online: boolean;
  /** "2 live", "idle" (online, none live) or "silent 1 h". */
  note: string;
}

export function machineNote(m: SnapshotMachine, now: number): string {
  if (!m.online) return m.last_seen ? `silent ${shortAge(m.last_seen, now)}` : 'silent';
  return m.live > 0 ? `${m.live} live` : 'idle';
}

export function machineRows(snapshot: WidgetSnapshot, now: number): MachineRow[] {
  return (snapshot.machines ?? []).map((m) => ({ id: m.id, name: m.name, online: m.online, note: machineNote(m, now) }));
}

/** "Machines · 2 of 3 online". */
export function machinesHeader(snapshot: WidgetSnapshot): string {
  const all = snapshot.machines ?? [];
  return `Machines · ${all.filter((m) => m.online).length} of ${all.length} online`;
}

/** Machines per line and how many show for a widget of this size (about 104 dp a machine, 24 dp a line). */
export function machineGrid(width: number | undefined, height: number | undefined, total: number): { perLine: number; shown: number } {
  const perLine = Math.max(1, width && width > 0 ? Math.floor((width - 24) / 104) : 3);
  const lines = Math.max(1, height && height > 0 ? Math.floor((height - 34) / 24) : 1);
  return { perLine, shown: Math.min(total, perLine * lines) };
}

/** "15:48", or "Thu 09:00" when the time is not today (in `tz`, the device's when omitted). */
export function resetClock(at: string | number, now: number, tz?: string): string {
  const opt = tz ? { timeZone: tz } : {};
  const day = (ms: number) => new Intl.DateTimeFormat('en-CA', { year: 'numeric', month: '2-digit', day: '2-digit', ...opt }).format(new Date(ms));
  const t = typeof at === 'number' ? at : Date.parse(at);
  const hm = new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit', hourCycle: 'h23', ...opt }).format(new Date(t));
  if (day(t) === day(now)) return hm;
  return `${new Intl.DateTimeFormat('en-GB', { weekday: 'short', ...opt }).format(new Date(t))} ${hm}`;
}

// ------------------------------------------------------------------ the ring

export interface RingModel {
  label: string;
  colour: string;
  pct: number;
  window: LimitWindow;
  /** "resets 15:48", "reset at 10:10" or "not started" (format/windows resetWords). */
  caption: string;
}

/** The window a Limits or ring widget shows (the 5 h | 7 d toggle; default 5 h). */
export type LimitWindow = '5 h' | '7 d';

/** One account's selected window as a row of the Limits widget or the ring. */
export interface WindowRow {
  id: string;
  label: string;
  colour: string;
  pct: number;
  /** Not started (or no reading for this window): "0 % · not started". */
  idle: boolean;
  /** "resets 15:48", "reset at 10:10" or "not started" (format/windows resetWords). */
  words: string;
}

/** Every account's `win` window: its % and reset words (windows.ts `shownUse`, `resetWords`). */
export function windowRows(snapshot: WidgetSnapshot, win: LimitWindow, now: number, tz?: string): WindowRow[] {
  const key = win === '5 h' ? 'five_hour' : 'seven_day';
  return snapshot.seats.map((s) => {
    const base = { id: s.id, label: s.label, colour: s.colour };
    const sw = s[key];
    if (!sw) return { ...base, pct: 0, idle: true, words: 'not started' };
    const w: Window = { key, used_pct: sw.pct, resets_at: sw.resets_at, ...(sw.resets_at ? {} : { state: 'idle' as const }) };
    return {
      ...base,
      pct: Math.min(100, Math.max(0, Math.round(shownUse(w, now) ?? 0))),
      idle: windowPhase(w, now) === 'idle',
      words: resetWords(w, now, tz ? { tz } : {}) || 'not started',
    };
  });
}

/** The ring of one account's `win` window; null when the snapshot has no such account (no id: the first one). */
export function ringModel(snapshot: WidgetSnapshot, seatId: string | undefined, now: number, tz?: string, win: LimitWindow = '5 h'): RingModel | null {
  const rows = windowRows(snapshot, win, now, tz);
  const r = seatId ? rows.find((x) => x.id === seatId) : rows[0];
  if (!r) return null;
  return { label: r.label, colour: r.colour, pct: r.pct, window: win, caption: r.words };
}

/** An SVG ring (a track and an arc from 12 o'clock) of `size` px, for SvgWidget. */
export function ringSvg(pct: number, colour: string, size = 96, stroke = 10): string {
  const p = Math.max(0, Math.min(100, pct));
  const r = (size - stroke) / 2;
  const c = size / 2;
  const track = `<circle cx="${c}" cy="${c}" r="${r}" fill="none" stroke="#2A2E32" stroke-width="${stroke}"/>`;
  let arc = '';
  if (p >= 100) arc = `<circle cx="${c}" cy="${c}" r="${r}" fill="none" stroke="${colour}" stroke-width="${stroke}"/>`;
  else if (p > 0) {
    const a = (p / 100) * 2 * Math.PI;
    const x = (c + r * Math.sin(a)).toFixed(2);
    const y = (c - r * Math.cos(a)).toFixed(2);
    arc = `<path d="M ${c} ${c - r} A ${r} ${r} 0 ${p > 50 ? 1 : 0} 1 ${x} ${y}" fill="none" stroke="${colour}" stroke-width="${stroke}" stroke-linecap="round"/>`;
  }
  return `<svg xmlns="http://www.w3.org/2000/svg" width="${size}" height="${size}" viewBox="0 0 ${size} ${size}">${track}${arc}</svg>`;
}

// ------------------------------------------------------------------ next reset

export interface NextResetModel {
  seat: string;
  window: '5 h' | '7 d';
  resetsAt: string;
  /** "15:48" or "Thu 09:00". */
  at: string;
}

/** The soonest reset still ahead across every account; null when there is none. */
export function nextResetModel(snapshot: WidgetSnapshot, now: number, tz?: string): NextResetModel | null {
  const n = nextResetAt(snapshot, now);
  if (!n) return null;
  return { seat: n.seat, window: n.window === 'five_hour' ? '5 h' : '7 d', resetsAt: n.resets_at, at: resetClock(n.resets_at, now, tz) };
}

/**
 * When to redraw the widgets next so a reset shows up when it happens: the soonest reset still
 * ahead, a few seconds later (so the reading has moved on). null when no reset is ahead.
 */
export function nextRefreshAt(snapshot: WidgetSnapshot, now: number): number | null {
  const n = nextResetAt(snapshot, now);
  return n ? Date.parse(n.resets_at) + 5_000 : null;
}

// ------------------------------------------------------------------ ring configuration (per widget)

type RingMap = Record<string, string>;

async function readMap(store: KVStore, key = WIDGET_RING_KEY): Promise<RingMap> {
  try {
    const raw = await store.getItem(key);
    const x: unknown = raw ? JSON.parse(raw) : {};
    if (!x || typeof x !== 'object' || Array.isArray(x)) return {};
    return Object.fromEntries(Object.entries(x as Record<string, unknown>).filter(([, v]) => typeof v === 'string')) as RingMap;
  } catch {
    return {};
  }
}

/** The account (seat id) each ring widget was configured with, by widget id. */
export async function ringSeat(store: KVStore, widgetId: number): Promise<string | undefined> {
  return (await readMap(store))[String(widgetId)];
}

/** The window a Limits or ring widget shows, by widget id (default 5 h). */
export async function windowChoice(store: KVStore, widgetId: number): Promise<LimitWindow> {
  return (await readMap(store, WIDGET_WINDOW_KEY))[String(widgetId)] === '7 d' ? '7 d' : '5 h';
}

export async function setWindowChoice(store: KVStore, widgetId: number, win: LimitWindow): Promise<void> {
  const m = await readMap(store, WIDGET_WINDOW_KEY);
  m[String(widgetId)] = win;
  await store.setItem(WIDGET_WINDOW_KEY, JSON.stringify(m));
}

export async function dropWindowChoice(store: KVStore, widgetId: number): Promise<void> {
  const m = await readMap(store, WIDGET_WINDOW_KEY);
  if (!(String(widgetId) in m)) return;
  delete m[String(widgetId)];
  await store.setItem(WIDGET_WINDOW_KEY, JSON.stringify(m));
}

export async function setRingSeat(store: KVStore, widgetId: number, seatId: string): Promise<void> {
  const m = await readMap(store);
  m[String(widgetId)] = seatId;
  await store.setItem(WIDGET_RING_KEY, JSON.stringify(m));
}

export async function dropRingSeat(store: KVStore, widgetId: number): Promise<void> {
  const m = await readMap(store);
  if (!(String(widgetId) in m)) return;
  delete m[String(widgetId)];
  await store.setItem(WIDGET_RING_KEY, JSON.stringify(m));
}
