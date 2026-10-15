// Pure helpers of the Analytics screen (docs/spec/analytics.md): money, token counts, bar
// heights, heatmap shades, the limit-hit sentence. No imports, so node can test it.

// 1234.5 -> "$1,234.50"; under a cent but not zero -> "<$0.01".
export function usd(n) {
  if (n == null || Number.isNaN(n)) return '–';
  if (n > 0 && n < 0.005) return '<$0.01';
  const [whole, cents] = Math.abs(n).toFixed(2).split('.');
  return `${n < 0 ? '-' : ''}$${whole.replace(/\B(?=(\d{3})+$)/g, ',')}.${cents}`;
}

// 1_234_567 -> "1.2M", 1500 -> "1.5k".
export function tokens(n) {
  if (n == null) return '–';
  n = Math.round(n);
  if (n >= 1e9) return `${(n / 1e9).toFixed(1)}B`;
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}k`;
  return String(n);
}

// Every token counter of a row (input, output, cache writes and reads).
export const tokenSum = (r) => (r.input || 0) + (r.output || 0) + (r.cache_5m || 0) + (r.cache_1h || 0) + (r.cache_read || 0);

// Share of cached reads in everything read as input: the cache hit rate, or null with no input.
export function cacheHit(r) {
  const all = (r.input || 0) + (r.cache_5m || 0) + (r.cache_1h || 0) + (r.cache_read || 0);
  return all ? (r.cache_read || 0) / all : null;
}

// Bar heights in percent of the tallest (a day with spend gets at least 2 so it shows).
export function barHeights(values) {
  const max = Math.max(0, ...values);
  return values.map((v) => (max > 0 && v > 0 ? Math.max(2, Math.round((v / max) * 100)) : 0));
}

// Heatmap shade 0-4 of a cell against the busiest cell (0 = nothing).
export function heatLevel(v, max) {
  if (!(v > 0) || !(max > 0)) return 0;
  return Math.min(4, 1 + Math.floor((v / max) * 4 - 1e-9));
}

export const WEEKDAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];

// "2026-10-14" -> "14 Oct".
export function shortDay(day) {
  const m = /^\d{4}-(\d{2})-(\d{2})$/.exec(String(day || ''));
  if (!m) return String(day || '');
  return `${Number(m[2])} ${['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'][Number(m[1]) - 1]}`;
}

// Query of a report: the last `days` days (ending today in the hub's zone) and a seat.
export function params(days, seat, tz) {
  const q = { days: String(days) };
  if (seat) q.seat = seat;
  if (tz) q.tz = tz; // the viewer's zone; the hub answers 400 for one it does not know
  return q;
}

// "Hit the 5-hour limit · waited 2 h" from a hits row: a past event, so "waited" (AN5).
export function hitText(hit) {
  const kind = hit.type === 'five_hour' ? '5-hour' : hit.type === 'seven_day' ? '7-day' : hit.type ? String(hit.type).replace(/_/g, ' ') : '';
  const wait = hit.wait_s;
  const waited = wait == null ? '' : wait >= 86400 ? `waited ${Math.round(wait / 86400)} d` : wait >= 3600 ? `waited ${Math.round(wait / 3600)} h` : `waited ${Math.max(1, Math.round(wait / 60))} min`;
  return [`Hit the ${kind ? `${kind} ` : ''}limit`, waited].filter(Boolean).join(' · ');
}

// Cache tokens of a row (writes and reads): the part of the total that is neither in nor out.
export const cacheSum = (r) => (r.cache_5m || 0) + (r.cache_1h || 0) + (r.cache_read || 0);

// A subagent's working folder ("agent-a3f9c1…"): many small rows that say nothing apart.
export const AGENT_DIR = /(^|\/)agent-[0-9a-f]{6,}\/?$/i;

// Projects for the list (AN4): subagent folders folded into one row, dearest first; the first
// `limit` in `shown`, the others in `rest`.
export function groupProjects(list, limit = 10) {
  const out = [];
  let agents = null;
  for (const p of list || []) {
    if (!AGENT_DIR.test(String(p.name || ''))) { out.push(p); continue; }
    if (!agents) agents = { name: 'Subagent folders', folders: 0, sessions: 0, api_usd: 0, input: 0, output: 0, cache_5m: 0, cache_1h: 0, cache_read: 0, group: true };
    agents.folders++;
    for (const k of ['sessions', 'api_usd', 'input', 'output', 'cache_5m', 'cache_1h', 'cache_read']) agents[k] += p[k] || 0;
  }
  if (agents) out.push(agents);
  out.sort((x, y) => (y.api_usd || 0) - (x.api_usd || 0));
  return { shown: out.slice(0, limit), rest: out.slice(limit) };
}
