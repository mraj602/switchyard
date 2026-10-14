// Formatting helpers: times, percentages, names.

const MIN = 60e3, HOUR = 36e5, DAY = 864e5;
const DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const two = (n) => String(n).padStart(2, '0');

export const toMs = (ts) => (ts ? Date.parse(ts) : NaN);

// "4 min", "2 h", "3 d". Empty when the time is unknown.
export function ago(ts, now = Date.now()) {
  const t = toMs(ts);
  if (Number.isNaN(t)) return '';
  const d = Math.max(0, now - t);
  if (d < 45e3) return 'just now';
  if (d < HOUR) return `${Math.round(d / MIN)} min`;
  if (d < DAY) return `${Math.round(d / HOUR)} h`;
  return `${Math.round(d / DAY)} d`;
}

export const agoLabel = (ts, now) => {
  const a = ago(ts, now);
  return !a ? '' : a === 'just now' ? a : `${a} ago`;
};

// Clock time today, otherwise "Thu 09:00".
export function clock(ts, now = Date.now()) {
  const t = toMs(ts);
  if (Number.isNaN(t)) return '';
  const d = new Date(t);
  const hm = `${two(d.getHours())}:${two(d.getMinutes())}`;
  return Math.abs(t - now) < 20 * HOUR && d.getDate() === new Date(now).getDate() ? hm : `${DAYS[d.getDay()]} ${hm}`;
}

// "in 2 h 10 min" for a future time, "now" when passed.
export function until(ts, now = Date.now()) {
  const t = toMs(ts);
  if (Number.isNaN(t)) return '';
  const d = t - now;
  if (d <= 0) return 'now';
  const mins = Math.max(1, Math.round(d / MIN));
  if (mins < 60) return `in ${mins} min`;
  if (mins < 24 * 60) {
    const h = Math.floor(mins / 60), m = mins % 60;
    return m ? `in ${h} h ${m} min` : `in ${h} h`;
  }
  const hrs = Math.round(mins / 60);
  return `in ${Math.floor(hrs / 24)} d ${hrs % 24} h`;
}

export function dateTime(ts) {
  const t = toMs(ts);
  if (Number.isNaN(t)) return '';
  const d = new Date(t);
  return `${DAYS[d.getDay()]} ${d.getDate()}/${d.getMonth() + 1} ${two(d.getHours())}:${two(d.getMinutes())}:${two(d.getSeconds())}`;
}

export const pct = (n) => (n == null || Number.isNaN(n) ? '–' : `${Math.round(n)}%`);

export const humanKey = (k) =>
  String(k || '').replace(/_/g, ' ').replace(/^./, (c) => c.toUpperCase());

export const plural = (n, one, many = one + 's') => `${n} ${n === 1 ? one : many}`;

export function compact(n) {
  if (n == null) return '–';
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}k`;
  return String(n);
}

// Last path segment of a working directory, for when the hub has no project name.
export const projectOf = (s) => s.project || (s.cwd ? s.cwd.split('/').filter(Boolean).pop() : '') || 'unknown';

// A URL from the hub that is safe to put in an href: https only (never javascript: or
// data:), else null.
export function httpsUrl(u) {
  if (typeof u !== 'string') return null;
  try {
    const x = new URL(u);
    return x.protocol === 'https:' ? x.href : null;
  } catch (_) {
    return null;
  }
}
