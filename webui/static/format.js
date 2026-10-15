// Formatting helpers: times, numbers, percentages, names. One place for every number and
// date the board shows (Intl underneath), so all pages read the same. No imports, so node
// can test it (format.test.cjs).

const MIN = 60e3, HOUR = 36e5, DAY = 864e5;

export const toMs = (ts) => (ts ? Date.parse(ts) : NaN);

// The board's fixed formats: "141,816", "Thu 15 Oct, 19:51", "14 Nov", "19:51". English
// words and 24-hour clocks everywhere, whatever the browser's locale, so pages agree.
const NUM = new Intl.NumberFormat('en-US', { maximumFractionDigits: 0 });
const HM = new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' });
const HMS = new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' });
// Weekday and month names are written out, not taken from Intl: en-GB gives "Sept" in some
// engines and "Sep" in others, and every board string must read the same everywhere (R9).
const DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
const WD = { format: (d) => DAYS[d.getDay()] };
const DM = { format: (d) => `${d.getDate()} ${MONTHS[d.getMonth()]}` };
const DMY = { format: (d) => `${d.getDate()} ${MONTHS[d.getMonth()]} ${d.getFullYear()}` };

// 141816 -> "141,816". "–" when unknown.
export const num = (n) => (n == null || Number.isNaN(Number(n)) ? '–' : NUM.format(Math.round(Number(n))));

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

const sameDay = (a, b) => a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();

// Clock time today, otherwise "Thu 09:00" within a week, otherwise "14 Nov 09:00".
export function clock(ts, now = Date.now()) {
  const t = toMs(ts);
  if (Number.isNaN(t)) return '';
  const d = new Date(t);
  const hm = HM.format(d);
  if (sameDay(d, new Date(now))) return hm;
  if (Math.abs(t - now) < 6 * DAY) return `${WD.format(d)} ${hm}`;
  return `${DM.format(d)} ${hm}`;
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

// "Thu 15 Oct, 19:51" (no seconds; the session timeline uses timeOfDay for those).
export function dateTime(ts) {
  const t = toMs(ts);
  if (Number.isNaN(t)) return '';
  const d = new Date(t);
  return `${WD.format(d)} ${DM.format(d)}, ${HM.format(d)}`;
}

// "14 Nov", or "14 Nov 2027" when not this year.
export function day(ts, now = Date.now()) {
  const t = toMs(ts);
  if (Number.isNaN(t)) return '';
  const d = new Date(t);
  return d.getFullYear() === new Date(now).getFullYear() ? DM.format(d) : DMY.format(d);
}

// "19:51:54", for the event timeline only.
export function timeOfDay(ts) {
  const t = toMs(ts);
  return Number.isNaN(t) ? '' : HMS.format(new Date(t));
}

export const pct = (n) => (n == null || Number.isNaN(n) ? '–' : `${Math.round(n)}%`);

export const humanKey = (k) =>
  String(k || '').replace(/_/g, ' ').replace(/^./, (c) => c.toUpperCase());

export const plural = (n, one, many = one + 's') => `${num(n)} ${n === 1 ? one : many}`;

// 1_234_567 -> "1.2M", 1500 -> "1.5k", 24_100_000_000 -> "24.1B".
export function compact(n) {
  if (n == null || Number.isNaN(Number(n))) return '–';
  if (n >= 1e9) return `${(n / 1e9).toFixed(1)}B`;
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}k`;
  return String(Math.round(n));
}

// Older names of zones whose city was renamed; browsers and hubs may report either, so the
// board shows the current one everywhere (R8).
const TZ_CITY = {
  Calcutta: 'Kolkata', Saigon: 'Ho Chi Minh City', Katmandu: 'Kathmandu', Rangoon: 'Yangon', Kiev: 'Kyiv',
  Dacca: 'Dhaka', Thimbu: 'Thimphu', Ujung_Pandang: 'Makassar', Macao: 'Macau', Ulan_Bator: 'Ulaanbaatar',
  Buenos_Aires: 'Buenos Aires', Godthab: 'Nuuk', Faeroe: 'Faroe', Asmera: 'Asmara', Reykjavik: 'Reykjavik',
};

// A time zone for people: "UTC", or "Kolkata (GMT+5:30)". Falls back to the raw name.
export function tzLabel(tz, at = Date.now()) {
  if (!tz) return '';
  if (/^(UTC|Etc\/UTC|GMT|Etc\/GMT)$/i.test(tz)) return 'UTC';
  let offset = '';
  try {
    const part = new Intl.DateTimeFormat('en-US', { timeZone: tz, timeZoneName: 'shortOffset' }).formatToParts(new Date(at)).find((p) => p.type === 'timeZoneName');
    offset = part ? part.value : '';
  } catch (_) { return tz; }
  const last = String(tz).split('/').pop();
  const city = (TZ_CITY[last] || last).replace(/_/g, ' ');
  return offset ? `${city} (${offset})` : city;
}

// The browser's own zone, or '' when it cannot say.
export function localTz() {
  try { return Intl.DateTimeFormat().resolvedOptions().timeZone || ''; } catch (_) { return ''; }
}

// Do two zone names mean the same clock right now? ("UTC" and "Etc/UTC" do.)
export function sameTz(a, b, at = Date.now()) {
  if (!a || !b) return true;
  if (a === b) return true;
  try {
    const f = (tz) => new Intl.DateTimeFormat('en-US', { timeZone: tz, timeZoneName: 'longOffset' }).format(new Date(at));
    return f(a).split(' ').pop() === f(b).split(' ').pop();
  } catch (_) { return false; }
}

// Last path segment of a working directory, for when the hub has no project name.
export const projectOf = (s) => s.project || (s.cwd ? s.cwd.split('/').filter(Boolean).pop() : '') || '(no project)';

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
