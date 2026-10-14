// Limit windows: labels, order, pace and "most room" (webui components/windows.js, views/accounts.js).
import type { Seat, Window } from '../../contract/api';
import { clock, humanKey } from './core';

const LABELS: Record<string, string> = {
  five_hour: '5-hour',
  seven_day: '7-day',
  seven_day_opus: '7-day · Opus',
  seven_day_sonnet: '7-day · Sonnet',
  iguana_necktie: 'Cloud session credits',
};

export function windowLabel(w: Pick<Window, 'key' | 'label'>): string {
  if (LABELS[w.key]) return LABELS[w.key]!;
  if (w.label) return w.label;
  const m = /^seven_day_(.+)$/.exec(w.key);
  return m ? `7-day · ${humanKey(m[1])}` : humanKey(w.key);
}

/** five_hour and seven_day first, then the rest in the order the hub sent them. */
export function sortWindows(ws: Window[]): Window[] {
  const rank = (w: Window) => (w.key === 'five_hour' ? 0 : w.key === 'seven_day' ? 1 : 2);
  return [...ws].sort((a, b) => rank(a) - rank(b));
}

export type PaceClass = 'ok' | 'warn' | 'bad';

const BAND: Record<string, { cls: PaceClass; text: string }> = {
  comfortable: { cls: 'ok', text: 'Comfortable' },
  on_track: { cls: 'ok', text: 'On pace' },
  fast: { cls: 'warn', text: 'Fast' },
  runaway: { cls: 'bad', text: 'Runaway' },
};

export interface PaceInfo {
  cls: PaceClass;
  text: string;
  band: string;
}

/** null when the window has no projection. */
export function paceInfo(w: Window, now: number, tz?: string): PaceInfo | null {
  const p = w.pace;
  if (!p || !p.band) return null;
  const b = BAND[p.band] ?? { cls: 'ok' as const, text: humanKey(p.band) };
  const proj = p.projected_pct != null ? Math.round(p.projected_pct) : null;
  let text: string;
  if (p.band === 'runaway' && p.full_at) text = `At this pace: 100% ${clock(p.full_at, now, tz)}`;
  else if (proj != null) text = `${b.text}: ~${proj}% at reset`;
  else text = b.text;
  return { cls: b.cls, text, band: p.band };
}

/** The worst pace among a seat's windows. */
export function worstPace(ws: Window[], now: number, tz?: string): { p: PaceInfo; w: Window } | null {
  const order = ['comfortable', 'on_track', 'fast', 'runaway'];
  let best: { p: PaceInfo; w: Window } | null = null;
  for (const w of ws) {
    const p = paceInfo(w, now, tz);
    if (p && (!best || order.indexOf(p.band) > order.indexOf(best.p.band))) best = { p, w };
  }
  return best;
}

/**
 * Who has the most room in the 5-hour window (push.md 5.7): lowest use among visible accounts
 * with a reading from the last 6 hours, and only when under 80%.
 */
export function mostRoom(seats: Seat[], now: number): { seat: Seat; five: Window } | null {
  let best: { seat: Seat; five: Window } | null = null;
  for (const s of seats.filter((x) => !x.hidden)) {
    const five = (s.windows ?? []).find((w) => w.key === 'five_hour');
    if (!five || five.used_pct == null || five.used_pct >= 80) continue;
    const at = Date.parse(five.updated_at || '');
    if (Number.isNaN(at) || now - at > 6 * 36e5) continue;
    if (!best || five.used_pct < (best.five.used_pct as number)) best = { seat: s, five };
  }
  return best;
}
