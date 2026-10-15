// Numbers and sentences of the Analytics screen (docs/spec/analytics.md; a port of
// webui/static/components/analytics-fmt.js). `usd` is the API list price of the tokens: a
// yardstick, not a bill.

/** 1234.5 -> "$1,234.50"; under a cent but not zero -> "<$0.01". */
export function usd(n: number | null | undefined): string {
  if (n == null || Number.isNaN(n)) return '–';
  if (n > 0 && n < 0.005) return '<$0.01';
  const [whole = '0', cents = '00'] = Math.abs(n).toFixed(2).split('.');
  return `${n < 0 ? '-' : ''}$${whole.replace(/\B(?=(\d{3})+$)/g, ',')}.${cents}`;
}

/** 1_234_567 -> "1.2M", 1500 -> "1.5k". */
export function tokens(n: number | null | undefined): string {
  if (n == null) return '–';
  if (n >= 1e9) return `${(n / 1e9).toFixed(1)}B`;
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}k`;
  return String(n);
}

interface Counts {
  input?: number;
  output?: number;
  cache_5m?: number;
  cache_1h?: number;
  cache_read?: number;
}

/** Every token counter of a row (input, output, cache writes and reads). */
export const tokenSum = (r: Counts): number => (r.input ?? 0) + (r.output ?? 0) + (r.cache_5m ?? 0) + (r.cache_1h ?? 0) + (r.cache_read ?? 0);

/** Share of cached reads in everything read as input, or null with no input. */
export function cacheHit(r: Counts): number | null {
  const all = (r.input ?? 0) + (r.cache_5m ?? 0) + (r.cache_1h ?? 0) + (r.cache_read ?? 0);
  return all ? (r.cache_read ?? 0) / all : null;
}

/** Bar sizes in percent of the largest value (a value above 0 gets at least 2 so it shows). */
export function barPercents(values: number[]): number[] {
  const max = Math.max(0, ...values);
  return values.map((v) => (max > 0 && v > 0 ? Math.max(2, Math.round((v / max) * 100)) : 0));
}

/** "2026-10-14" -> "14 Oct". */
export function shortDay(day: string): string {
  const m = /^\d{4}-(\d{2})-(\d{2})$/.exec(day);
  if (!m) return day;
  return `${Number(m[2])} ${['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'][Number(m[1]) - 1]}`;
}

/** "5-hour limit, back in 2 h" from a hits row. */
export function hitText(hit: { type: string | null; wait_s: number | null }): string {
  const kind = hit.type === 'five_hour' ? '5-hour' : hit.type === 'seven_day' ? '7-day' : hit.type ? hit.type.replace(/_/g, ' ') : '';
  const w = hit.wait_s;
  const back =
    w == null ? '' : w >= 86400 ? `back in ${Math.round(w / 86400)} d` : w >= 3600 ? `back in ${Math.round(w / 3600)} h` : `back in ${Math.max(1, Math.round(w / 60))} min`;
  return [kind ? `${kind} limit` : 'limit', back].filter(Boolean).join(', ');
}
