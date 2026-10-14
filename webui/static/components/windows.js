// Limit windows: meters, labels, reset times and pace text.

import { h } from './dom.js';
import { clock, until, pct, humanKey } from '../format.js';

const LABELS = {
  five_hour: '5-hour',
  seven_day: '7-day',
  seven_day_opus: '7-day · Opus',
  seven_day_sonnet: '7-day · Sonnet',
  iguana_necktie: 'Cloud session credits',
};

export function windowLabel(w) {
  if (LABELS[w.key]) return LABELS[w.key];
  if (w.label) return w.label;
  const m = /^seven_day_(.+)$/.exec(w.key);
  return m ? `7-day · ${humanKey(m[1])}` : humanKey(w.key);
}

// five_hour and seven_day first, then the rest in the order the hub sent them.
export function sortWindows(ws) {
  const rank = (w) => (w.key === 'five_hour' ? 0 : w.key === 'seven_day' ? 1 : 2);
  return [...ws].sort((a, b) => rank(a) - rank(b));
}

const BAND = {
  comfortable: { cls: 'ok', text: 'Comfortable' },
  on_track: { cls: 'ok', text: 'On pace' },
  fast: { cls: 'warn', text: 'Fast' },
  runaway: { cls: 'bad', text: 'Runaway' },
};

// null when the window has no projection.
export function paceInfo(w, now = Date.now()) {
  const p = w.pace;
  if (!p || !p.band) return null;
  const b = BAND[p.band] || { cls: 'ok', text: humanKey(p.band) };
  const proj = p.projected_pct != null ? Math.round(p.projected_pct) : null;
  let text;
  if (p.band === 'runaway' && p.full_at) text = `At this pace: 100% ${clock(p.full_at, now)}`;
  else if (proj != null) text = `${b.text}: ~${proj}% at reset`;
  else text = b.text;
  return { cls: b.cls, text, band: p.band, projected: p.projected_pct || 0 };
}

// The worst pace among a seat's windows, for the one-line summary on cards.
export function worstPace(windows, now) {
  const order = ['comfortable', 'on_track', 'fast', 'runaway'];
  let best = null;
  for (const w of windows) {
    const p = paceInfo(w, now);
    if (p && (!best || order.indexOf(p.band) > order.indexOf(best.p.band))) best = { p, w };
  }
  return best;
}

export function paceChip(info, prefix) {
  return h('div', { class: `chip chip-${info.cls}` }, prefix ? `${prefix} · ` : '', info.text);
}

// One meter row. `full` adds the reset countdown and pace line (Accounts page).
export function windowRow(w, { seatName, full = false, now = Date.now() } = {}) {
  const used = w.used_pct;
  const hot = used != null && used >= 90;
  const label = windowLabel(w);
  const value = used == null ? 'no data' : `${pct(used)}${w.resets_at ? ` · ${clock(w.resets_at, now)}` : ''}`;
  const pace = full ? paceInfo(w, now) : null;
  return h('div', { class: 'win' },
    h('div', { class: 'win-head' },
      h('span', {}, label),
      h('span', { class: `mono${hot ? ' hot' : ''}` }, value, hot ? h('span', { class: 'hot-tag' }, ' high') : null)),
    h('div', {
      class: 'meter', role: 'meter', 'aria-valuemin': 0, 'aria-valuemax': 100,
      'aria-valuenow': used == null ? undefined : Math.round(used),
      'aria-label': `${seatName ? seatName + ' ' : ''}${label} used`,
    }, h('div', { class: 'meter-fill', style: { width: `${Math.min(100, Math.max(0, used || 0))}%` } })),
    full && w.resets_at ? h('div', { class: 'win-sub' },
      `resets ${until(w.resets_at, now)}`, ' (', clock(w.resets_at, now), ')',
      w.peak_pct != null ? ` · peak ${pct(w.peak_pct)}` : '',
      w.source ? ` · from ${w.source === 'both' ? 'status line and web' : w.source === 'web' ? 'claude.ai' : 'status line'}` : '') : null,
    pace ? h('div', { class: `win-pace pace-${pace.cls}` }, pace.text) : null);
}
