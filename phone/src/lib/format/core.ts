// A port of webui/static/format.js (phone.md P4). Same names, same output on the shared case
// table (test/fixtures/format-cases.json). The only addition is the optional `tz` of the
// date functions.

const MIN = 60e3;
const HOUR = 36e5;
const DAY = 864e5;
const DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sept', 'Oct', 'Nov', 'Dec'];
const two = (n: number) => String(n).padStart(2, '0');

type Ts = string | null | undefined;

export const toMs = (ts: Ts): number => (ts ? Date.parse(ts) : NaN);

/** "4 min", "2 h", "3 d", "just now"; "" when unknown. */
export function ago(ts: Ts, now: number): string {
  const t = toMs(ts);
  if (Number.isNaN(t)) return '';
  const d = Math.max(0, now - t);
  if (d < 45e3) return 'just now';
  if (d < HOUR) return `${Math.round(d / MIN)} min`;
  if (d < DAY) return `${Math.round(d / HOUR)} h`;
  return `${Math.round(d / DAY)} d`;
}

export function agoLabel(ts: Ts, now: number): string {
  const a = ago(ts, now);
  return !a ? '' : a === 'just now' ? a : `${a} ago`;
}

interface Parts {
  year: number;
  month: number; // 0-11
  day: number;
  dow: number;
  h: number;
  m: number;
  s: number;
}

// Date parts in `tz` (an IANA name), or in the device's zone when tz is empty or unknown.
function partsIn(t: number, tz?: string): Parts {
  if (tz) {
    try {
      const f = new Intl.DateTimeFormat('en-GB', {
        timeZone: tz, hourCycle: 'h23', weekday: 'short', year: 'numeric', month: 'numeric', day: 'numeric',
        hour: '2-digit', minute: '2-digit', second: '2-digit',
      });
      const p: Record<string, string> = {};
      for (const x of f.formatToParts(new Date(t))) p[x.type] = x.value;
      const dow = DAYS.indexOf(p.weekday ?? '');
      if (dow >= 0) {
        return {
          year: Number(p.year), month: Number(p.month) - 1, day: Number(p.day), dow,
          h: Number(p.hour) % 24, m: Number(p.minute), s: Number(p.second),
        };
      }
    } catch {
      // Unknown zone or no Intl: fall back to the device's zone.
    }
  }
  const d = new Date(t);
  return { year: d.getFullYear(), month: d.getMonth(), day: d.getDate(), dow: d.getDay(), h: d.getHours(), m: d.getMinutes(), s: d.getSeconds() };
}

// Words and digits are written out, not taken from Intl: Hermes' CLDR data varies by device
// (en-GB "Sept" is not guaranteed), and the board's strings must be identical everywhere.
const hm = (p: Parts) => `${two(p.h)}:${two(p.m)}`;
const dm = (p: Parts) => `${p.day} ${MONTHS[p.month]}`;
const sameDay = (a: Parts, b: Parts) => a.year === b.year && a.month === b.month && a.day === b.day;

/** 141816 -> "141,816". "–" when unknown. */
export function num(n: unknown): string {
  if (n == null || Number.isNaN(Number(n))) return '–';
  const r = Math.round(Number(n));
  return r.toString().replace(/\B(?=(\d{3})+(?!\d))/g, ',');
}

/** "HH:MM" today, "Thu HH:MM" within 6 days, otherwise "3 Sept 14:02", in tz (default: the device's zone). */
export function clock(ts: Ts, now: number, tz?: string): string {
  const t = toMs(ts);
  if (Number.isNaN(t)) return '';
  const p = partsIn(t, tz);
  if (sameDay(p, partsIn(now, tz))) return hm(p);
  if (Math.abs(t - now) < 6 * DAY) return `${DAYS[p.dow]} ${hm(p)}`;
  return `${dm(p)} ${hm(p)}`;
}

/** "in 2 h 10 min" for a future time, "now" when passed. */
export function until(ts: Ts, now: number): string {
  const t = toMs(ts);
  if (Number.isNaN(t)) return '';
  const d = t - now;
  if (d <= 0) return 'now';
  const mins = Math.max(1, Math.round(d / MIN));
  if (mins < 60) return `in ${mins} min`;
  if (mins < 24 * 60) {
    const h = Math.floor(mins / 60);
    const m = mins % 60;
    return m ? `in ${h} h ${m} min` : `in ${h} h`;
  }
  const hrs = Math.round(mins / 60);
  return `in ${Math.floor(hrs / 24)} d ${hrs % 24} h`;
}

/** "Tue 13 Oct, 14:02" (no seconds; the session timeline uses timeOfDay for those). */
export function dateTime(ts: Ts, tz?: string): string {
  const t = toMs(ts);
  if (Number.isNaN(t)) return '';
  const p = partsIn(t, tz);
  return `${DAYS[p.dow]} ${dm(p)}, ${hm(p)}`;
}

/** "14 Nov", or "14 Nov 2027" when not this year. */
export function day(ts: Ts, now: number, tz?: string): string {
  const t = toMs(ts);
  if (Number.isNaN(t)) return '';
  const p = partsIn(t, tz);
  return p.year === partsIn(now, tz).year ? dm(p) : `${dm(p)} ${p.year}`;
}

/** "19:51:54", for the event timeline only. */
export function timeOfDay(ts: Ts, tz?: string): string {
  const t = toMs(ts);
  if (Number.isNaN(t)) return '';
  const p = partsIn(t, tz);
  return `${hm(p)}:${two(p.s)}`;
}

export const pct = (n: number | null | undefined): string => (n == null || Number.isNaN(n) ? '–' : `${Math.round(n)}%`);

export const humanKey = (k: unknown): string =>
  String(k || '').replace(/_/g, ' ').replace(/^./, (c) => c.toUpperCase());

export const plural = (n: number, one: string, many: string = one + 's'): string => `${num(n)} ${n === 1 ? one : many}`;

/** 1_234_567 -> "1.2M", 1500 -> "1.5k", 24_100_000_000 -> "24.1B". */
export function compact(n: number | null | undefined): string {
  if (n == null || Number.isNaN(Number(n))) return '–';
  if (n >= 1e9) return `${(n / 1e9).toFixed(1)}B`;
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}k`;
  return String(Math.round(n));
}

// Offset of a zone from UTC at `at`, in minutes; null when the zone is unknown. Built from
// formatToParts (always present with Intl), not timeZoneName 'shortOffset' (newer engines only).
function offsetMin(tz: string, at: number): number | null {
  try {
    const f = new Intl.DateTimeFormat('en-GB', {
      timeZone: tz, hourCycle: 'h23', year: 'numeric', month: 'numeric', day: 'numeric', hour: 'numeric', minute: 'numeric', second: 'numeric',
    });
    const p: Record<string, number> = {};
    for (const x of f.formatToParts(new Date(at))) if (x.type !== 'literal') p[x.type] = Number(x.value);
    const local = Date.UTC(p.year!, p.month! - 1, p.day!, p.hour! % 24, p.minute!, p.second!);
    return Math.round((local - Math.floor(at / 1000) * 1000) / MIN);
  } catch {
    return null;
  }
}

/** A time zone for people: "UTC", or "Kolkata (GMT+5:30)". Falls back to the raw name. */
export function tzLabel(tz: string | null | undefined, at: number = Date.now()): string {
  if (!tz) return '';
  if (/^(UTC|Etc\/UTC|GMT|Etc\/GMT)$/i.test(tz)) return 'UTC';
  const off = offsetMin(tz, at);
  if (off == null) return tz;
  const city = String(tz).split('/').pop()!.replace(/_/g, ' ');
  const a = Math.abs(off);
  const sign = off < 0 ? '-' : '+';
  const v = off === 0 ? 'GMT' : `GMT${sign}${Math.floor(a / 60)}${a % 60 ? ':' + two(a % 60) : ''}`;
  return `${city} (${v})`;
}

/** The device's own zone, or '' when it cannot say. */
export function localTz(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || '';
  } catch {
    return '';
  }
}

/** Do two zone names mean the same clock right now? ("UTC" and "Etc/UTC" do.) */
export function sameTz(a: string | null | undefined, b: string | null | undefined, at: number = Date.now()): boolean {
  if (!a || !b) return true;
  if (a === b) return true;
  const x = offsetMin(a, at);
  const y = offsetMin(b, at);
  return x != null && y != null && x === y;
}

/** Last path segment of a working directory, for when the hub has no project name. */
export const projectOf = (s: { project?: string | null; cwd?: string | null }): string =>
  s.project || (s.cwd ? s.cwd.split('/').filter(Boolean).pop() : '') || '(no project)';

/** An https URL safe to open (never javascript: or data:), else null. */
export function httpsUrl(u: unknown): string | null {
  if (typeof u !== 'string' || !/^https:/i.test(u)) return null;
  try {
    const x = new URL(u);
    return x.protocol === 'https:' ? x.href : null;
  } catch {
    return null;
  }
}
