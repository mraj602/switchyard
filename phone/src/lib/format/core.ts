// A port of webui/static/format.js (phone.md P4). Same names, same output on the shared case
// table (test/fixtures/format-cases.json). The only addition is the optional `tz` of clock().

const MIN = 60e3;
const HOUR = 36e5;
const DAY = 864e5;
const DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
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
  day: number;
  dow: number;
  hm: string;
}

// Date parts in `tz` (an IANA name), or in the device's zone when tz is empty or unknown.
function partsIn(t: number, tz?: string): Parts {
  if (tz) {
    try {
      const f = new Intl.DateTimeFormat('en-GB', {
        timeZone: tz, hourCycle: 'h23', weekday: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit',
      });
      const p: Record<string, string> = {};
      for (const x of f.formatToParts(new Date(t))) p[x.type] = x.value;
      const dow = DAYS.indexOf(p.weekday ?? '');
      if (dow >= 0) return { day: Number(p.day), dow, hm: `${p.hour}:${p.minute}` };
    } catch {
      // Unknown zone or no Intl: fall back to the device's zone.
    }
  }
  const d = new Date(t);
  return { day: d.getDate(), dow: d.getDay(), hm: `${two(d.getHours())}:${two(d.getMinutes())}` };
}

/** Clock time today, otherwise "Thu 09:00", in tz (default: the device's zone). */
export function clock(ts: Ts, now: number, tz?: string): string {
  const t = toMs(ts);
  if (Number.isNaN(t)) return '';
  const p = partsIn(t, tz);
  const today = partsIn(now, tz);
  return Math.abs(t - now) < 20 * HOUR && p.day === today.day ? p.hm : `${DAYS[p.dow]} ${p.hm}`;
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

export function dateTime(ts: Ts): string {
  const t = toMs(ts);
  if (Number.isNaN(t)) return '';
  const d = new Date(t);
  return `${DAYS[d.getDay()]} ${d.getDate()}/${d.getMonth() + 1} ${two(d.getHours())}:${two(d.getMinutes())}:${two(d.getSeconds())}`;
}

export const pct = (n: number | null | undefined): string => (n == null || Number.isNaN(n) ? '–' : `${Math.round(n)}%`);

export const humanKey = (k: unknown): string =>
  String(k || '').replace(/_/g, ' ').replace(/^./, (c) => c.toUpperCase());

export const plural = (n: number, one: string, many: string = one + 's'): string => `${n} ${n === 1 ? one : many}`;

export function compact(n: number | null | undefined): string {
  if (n == null) return '–';
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}k`;
  return String(n);
}

/** Last path segment of a working directory, for when the hub has no project name. */
export const projectOf = (s: { project?: string | null; cwd?: string | null }): string =>
  s.project || (s.cwd ? s.cwd.split('/').filter(Boolean).pop() : '') || 'unknown';

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
