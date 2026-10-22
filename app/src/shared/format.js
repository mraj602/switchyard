// Pure formatting helpers for the shell (percent, reset times in local time, relative times).
// No Node, no DOM: the renderer imports it from app://shell/shared/format.js, tests from disk.
// Times are RFC 3339 strings; every function returns '' (or '–' for a percent)
// when the input is missing or unparseable, so a view never prints "NaN" or "Invalid Date".

const MIN = 60e3;
const HOUR = 36e5;
const DAY = 864e5;
const DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const two = (n) => String(n).padStart(2, '0');

/** RFC 3339 → epoch ms, NaN when unknown. */
export const toMs = (ts) => (typeof ts === 'string' && ts ? Date.parse(ts) : NaN);

/** 41.6 → "42%"; null or NaN → "–". Clamped to 0..100 for display. */
export function pct(n) {
  if (typeof n !== 'number' || !Number.isFinite(n)) return '–';
  return `${Math.round(Math.min(100, Math.max(0, n)))}%`;
}

/** Percent as a bar width, 0..100 (0 when unknown). */
export function barWidth(n) {
  if (typeof n !== 'number' || !Number.isFinite(n)) return 0;
  return Math.min(100, Math.max(0, n));
}

/** Local clock time "15:10" on today's date, else "Mon 09:00". */
export function clock(ts, now = Date.now()) {
  const t = toMs(ts);
  if (Number.isNaN(t)) return '';
  const d = new Date(t);
  const hm = `${two(d.getHours())}:${two(d.getMinutes())}`;
  const sameDay = d.toDateString() === new Date(now).toDateString();
  return sameDay ? hm : `${DAYS[d.getDay()]} ${hm}`;
}

/** The reset text of a usage window: "15:10" today, "Mon 09:00" later, '' when unknown. */
export const resetText = clock;

/** "4 min", "2 h", "3 d"; "just now" under 45 s; '' when unknown. */
export function ago(ts, now = Date.now()) {
  const t = toMs(ts);
  if (Number.isNaN(t)) return '';
  const d = Math.max(0, now - t);
  if (d < 45e3) return 'just now';
  if (d < HOUR) return `${Math.max(1, Math.round(d / MIN))} min`;
  if (d < DAY) return `${Math.round(d / HOUR)} h`;
  return `${Math.round(d / DAY)} d`;
}

/** "2 min ago" / "just now" / ''. */
export function agoLabel(ts, now) {
  const a = ago(ts, now);
  return !a ? '' : a === 'just now' ? a : `${a} ago`;
}

/** "in 2 h 10 min" for a future time, "now" once passed, '' when unknown. */
export function until(ts, now = Date.now()) {
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

/** "five_hour" → "Five hour" (unknown window keys are shown under their raw name). */
export const humanKey = (k) => String(k ?? '').replace(/_/g, ' ').replace(/^./, (c) => c.toUpperCase());

/** "1 tab" / "3 tabs". */
export const plural = (n, one, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

/** 1536 → "1.5 KB". */
export function bytes(n) {
  if (typeof n !== 'number' || !Number.isFinite(n) || n < 0) return '';
  if (n < 1024) return `${n} B`;
  if (n < 1024 ** 2) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 ** 3) return `${(n / 1024 ** 2).toFixed(1)} MB`;
  return `${(n / 1024 ** 3).toFixed(1)} GB`;
}
