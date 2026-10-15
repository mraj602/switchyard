// Analytics (docs/spec/analytics.md): tokens and API-equivalent cost over 7, 30 or 90 days,
// the daily cost bars, by model, by project, a weekday by hour heatmap and limit hits. One
// seat or all. Read-only, so it works for any sign-in. The dollar figure is what the same
// tokens cost at API list prices: a yardstick, not a bill.

import { h } from '../components/dom.js';
import { section, toast, moreTabs } from '../components/ui.js';
import { isOwner } from '../store.js';
import { getAnalytics } from '../api.js';
import { seatIndex, seatName } from '../components/seat.js';
import { dateTime, plural, num, tzLabel, localTz, sameTz } from '../format.js';
import {
  usd, tokens, tokenSum, cacheSum, cacheHit, barHeights, heatLevel, WEEKDAYS, shortDay, params, hitText, groupProjects,
} from '../components/analytics-fmt.js';

const RANGES = [7, 30, 90];
const st = { days: 30, seat: '', data: null, error: null, loading: false, bar: null, allProjects: false };
const again = () => window.dispatchEvent(new Event('sy-rerender'));
let gen = 0;

export async function load() {
  const my = ++gen;
  st.loading = true;
  st.error = null;
  again();
  const q = params(st.days, st.seat);
  try {
    const [daily, models, projects, heatmap, hits] = await Promise.all(
      ['daily', 'models', 'projects', 'heatmap', 'hits'].map((r) => getAnalytics(r, q)));
    if (my !== gen) return; // a newer choice is on its way
    st.data = { daily, models, projects, heatmap, hits };
    st.bar = null;
  } catch (e) {
    if (my !== gen || e.name === 'AuthError') return;
    st.error = e.message;
    toast(e.message);
  }
  st.loading = false;
  again();
}

const pick = (days, seat) => {
  st.days = days;
  st.seat = seat;
  load();
};

function tile(label, value, sub) {
  return h('div', { class: 'ana-tile' },
    h('span', { class: 'muted small' }, label),
    h('span', { class: 'ana-tile-v' }, value),
    sub ? h('span', { class: 'muted small' }, sub) : null);
}

function totals(d) {
  const t = d.totals;
  const hit = cacheHit(t);
  return h('div', { class: 'ana-tiles' },
    tile('API-equivalent cost', usd(t.api_usd), t.unpriced_replies ? `${plural(t.unpriced_replies, 'reply', 'replies')} not priced` : null),
    // The total is mostly cache; the sub-line says so, so the parts add up (AN2).
    tile('Tokens', tokens(tokenSum(t)), `${tokens(t.input)} in · ${tokens(t.output)} out · ${tokens(cacheSum(t))} cache`),
    tile('Replies', num(t.replies)),
    tile('Cache reads', hit == null ? '–' : `${Math.round(hit * 100)}%`, 'of input'));
}

const barText = (x) => `${shortDay(x.day)}: ${x.source === 'history' ? `${tokens(x.total_tokens)} tokens (from Claude Code history, no cost)` : `${usd(x.api_usd)}, ${tokens(x.total_tokens)} tokens`}`;

function bars(d) {
  const days = d.days;
  const heights = barHeights(days.map((x) => x.api_usd));
  const max = Math.max(0, ...days.map((x) => x.api_usd || 0));
  const chosen = st.bar != null && days[st.bar] ? days[st.bar] : null;
  // Each bar is a button: a touch screen never shows a title, so a tap shows the value (AN7).
  return h('div', {},
    h('div', { class: 'ana-bars-wrap' },
      h('span', { class: 'ana-ymax muted small', 'aria-hidden': 'true' }, usd(max)),
      h('div', { class: 'ana-bars', role: 'group', 'data-key': 'ana-bars',
        'aria-label': `Daily API-equivalent cost, ${shortDay(d.from)} to ${shortDay(d.to)}, ${usd(d.totals.api_usd)} in all` },
      days.map((x, i) => h('button', {
        type: 'button', class: `ana-bar${x.source === 'history' ? ' is-history' : ''}${st.bar === i ? ' is-picked' : ''}`, style: { height: `${heights[i]}%` },
        title: barText(x), 'aria-label': barText(x), 'aria-pressed': String(st.bar === i),
        onclick: () => { st.bar = st.bar === i ? null : i; again(); },
      })))),
    h('div', { class: 'ana-axis muted small' }, h('span', {}, shortDay(d.from)), h('span', {}, shortDay(d.to))),
    h('p', { class: 'small ana-picked', role: 'status', 'data-key': 'ana-bar-value' }, chosen ? barText(chosen) : h('span', { class: 'muted' }, 'Tap a bar to see its day.')));
}

// A list of rows with a bar behind the cost, widest = the dearest row. `value` defaults to
// the cost; a model without a price shows "–" (AN6).
function rows(items, label, sub, value = (x) => usd(x.api_usd)) {
  const max = Math.max(0, ...items.map((x) => x.api_usd || 0));
  return h('ul', { class: 'panel ana-list' }, items.map((x) => h('li', { class: 'ana-row' },
    h('span', { class: 'ana-fill', style: { width: `${max > 0 ? Math.round(((x.api_usd || 0) / max) * 100) : 0}%` }, 'aria-hidden': 'true' }),
    h('span', { class: 'ana-name' }, h('span', { class: 'strong' }, label(x)), h('span', { class: 'muted small' }, sub(x))),
    h('span', { class: 'ana-val' }, value(x)))));
}

function projects(d) {
  const list = d.projects.projects;
  if (!list.length) return h('p', { class: 'muted small' }, 'No token data in this range.');
  const { shown, rest } = groupProjects(list, 10);
  const items = st.allProjects ? [...shown, ...rest] : shown;
  return h('div', {},
    rows(items, (p) => p.name, (p) => [p.group ? plural(p.folders, 'folder') : '', plural(p.sessions, 'session'), `${tokens(tokenSum(p))} tokens`].filter(Boolean).join(' · ')),
    rest.length ? h('button', { type: 'button', class: 'linkish', 'data-key': 'ana-all-projects', 'aria-expanded': String(st.allProjects),
      onclick: () => { st.allProjects = !st.allProjects; again(); } }, st.allProjects ? 'Show the top 10' : `Show all ${num(shown.length + rest.length)}`) : null);
}

function heatmap(d) {
  const flat = d.api_usd.flat();
  const max = Math.max(0, ...flat);
  const cells = [h('span', {})];
  for (let hr = 0; hr < 24; hr++) cells.push(h('span', { class: 'ana-hr' }, hr % 6 === 0 ? String(hr) : ''));
  d.api_usd.forEach((row, wd) => {
    cells.push(h('span', { class: 'ana-wd' }, WEEKDAYS[wd]));
    row.forEach((v, hr) => cells.push(h('span', {
      class: `ana-cell lvl-${heatLevel(v, max)}`, title: `${WEEKDAYS[wd]} ${hr}:00 · ${usd(v)} · ${plural(d.replies[wd][hr], 'reply', 'replies')}`,
    })));
  });
  // The hours are the hub's; say which zone, and when this device is elsewhere (AN3, G9).
  const tz = d.tz || '';
  const here = localTz();
  return h('div', {},
    h('div', { class: 'ana-heat', role: 'img', 'data-key': 'ana-heat', 'aria-label': 'API-equivalent cost by weekday and hour' }, cells),
    h('div', { class: 'ana-legend muted small', 'aria-hidden': 'true' }, 'Less',
      [0, 1, 2, 3, 4].map((l) => h('span', { class: `ana-cell lvl-${l}` })), 'More'),
    h('p', { class: 'muted small', 'data-key': 'ana-tz' }, tz ? `Hours in ${tzLabel(tz)}, the hub’s time zone.` : 'Hours in the hub’s time zone.',
      tz && here && !sameTz(tz, here) ? ` This device is in ${tzLabel(here)}.` : ''));
}

function hits(d, idx) {
  if (!d.hits.length) return h('p', { class: 'muted small' }, 'No limit hits in this range.');
  const who = (x) => (idx.get(x.seat) ? seatName(idx.get(x.seat).seat) : x.seat_name || 'Unknown account');
  return h('ul', { class: 'panel ana-list' }, d.hits.slice().reverse().map((x) => h('li', { class: 'ana-row' },
    h('span', { class: 'ana-name' }, h('span', { class: 'strong' }, hitText(x)), h('span', { class: 'muted small' }, `${who(x)} · ${dateTime(x.ts)}`)))));
}

export function render({ store }) {
  const owner = isOwner();
  const d = st.data;
  const seats = (store.overview && store.overview.seats) || [];
  const idx = seatIndex(seats);
  const seatSel = h('select', { class: 'select', 'aria-label': 'Account', 'data-key': 'ana-seat', onchange: (e) => pick(st.days, e.target.value) },
    h('option', { value: '' }, 'All accounts'),
    seats.map((s) => h('option', { value: s.id, selected: s.id === st.seat }, seatName(s))),
    h('option', { value: 'unattributed', selected: st.seat === 'unattributed' }, 'Unattributed'));
  const node = h('div', { class: 'page', 'data-key': 'analytics' },
    h('header', { class: 'page-head col' }, h('h1', {}, 'Analytics'), moreTabs('analytics', { owner }),
      d ? h('p', { class: 'muted small' }, `${shortDay(d.daily.from)} to ${shortDay(d.daily.to)}${d.daily.tz ? ` · days in ${tzLabel(d.daily.tz)}` : ''}`) : null),
    h('div', { class: 'chips ana-controls' },
      RANGES.map((n) => h('button', { type: 'button', class: 'chip-btn', 'aria-pressed': String(st.days === n), 'data-key': `ana-days-${n}`, onclick: () => pick(n, st.seat) }, `${n} days`)),
      seatSel),
    st.error ? h('p', { class: 'status-warn', role: 'status' }, st.error) : null,
    !d ? h('p', { class: 'calm' }, st.loading ? 'Loading…' : 'No data.') : h('div', { class: st.loading ? 'ana-body is-loading' : 'ana-body' },
      totals(d.daily),
      section('Cost per day', bars(d.daily)),
      section('By model', d.models.models.length
        ? rows(d.models.models, (m) => m.label, (m) => `${tokens(tokenSum(m))} tokens · ${plural(m.replies, 'reply', 'replies')}${m.priced ? '' : ' · no price known'}`, (m) => (m.priced ? usd(m.api_usd) : '–'))
        : h('p', { class: 'muted small' }, 'No token data in this range.')),
      section('By project', projects(d)),
      section('When you work', heatmap(d.heatmap)),
      section('Limit hits', hits(d.hits, idx)),
      h('p', { class: 'muted small' }, 'Cost is the API list price of the same tokens: a yardstick, not a bill.')));
  return { title: 'Analytics · Switchyard', node };
}
