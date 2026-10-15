// Now / Home: what needs you, account limits, open sessions, machines.

import { h } from '../components/dom.js';
import { needsCard, seatCard, placesText, machineSummary, dot, sessionLink } from '../components/cards.js';
import { seatIndex, sessionSeat } from '../components/seat.js';
import { stateTag, isLive, liveCounts } from '../components/state.js';
import { liveBadge } from '../components/nav.js';
import { ringRow } from '../components/rings.js';
import { ensureSessions, canPush } from '../store.js';
import { agoLabel, ago, plural, projectOf, subtitleOf } from '../format.js';

export const load = () => ensureSessions();

// Working first, then the most recent: what is worth a glance.
const byActivity = (a, b) => (Number(b.state === 'working') - Number(a.state === 'working')) ||
  (Date.parse(b.last_event_at || b.state_since || '') || 0) - (Date.parse(a.last_event_at || a.state_since || '') || 0);

export function render({ store, now }) {
  const o = store.overview;
  const idx = seatIndex(o.seats);
  const needs = o.needs_you;
  const title = needs.length ? `${plural(needs.length, 'session')} need${needs.length === 1 ? 's' : ''} you` : 'Nothing needs you';
  const seats = o.seats.filter((s) => !s.hidden);
  const counts = liveCounts(store.sessions, now);
  const open = (store.sessions || []).filter((s) => isLive(s, now) && !s.needs_you).sort(byActivity);
  const shown = open.slice(0, 8);
  const swipe = canPush() && needs.some((s) => s.alert && ['pending', 'fired', 'snoozed'].includes(s.alert.status));
  const total = counts ? counts.total : o.counts.live ?? 0;

  const line = (s, cls = '') => {
    const seat = sessionSeat(s, idx);
    return h('li', { class: `line${cls}` },
      stateTag(s),
      h('span', { class: 'line-main' }, sessionLink(s, h('b', {}, projectOf(s))), subtitleOf(s, projectOf(s)) ? h('span', { class: 'muted' }, ` · ${subtitleOf(s, projectOf(s))}`) : null),
      h('span', { class: 'line-meta muted small' }, dot(seat.colour), `${seat.label}${s.machine_name ? ' · ' + s.machine_name : ''} · ${ago(s.state_since, now) || '–'}`));
  };

  const node = h('div', { class: 'page page-now' },
    h('header', { class: 'page-head' },
      h('h1', { class: 'desk-only' }, title),
      h('h1', { class: 'phone-only' }, 'Now'),
      h('p', { class: 'muted small head-meta' },
        h('span', { class: 'desk-only', 'data-key': 'now-counts' }, `${plural(seats.length, 'account')} · ${plural(total, 'open session')} on ${plural(o.machines.length, 'machine')} · updated ${agoLabel(new Date(store.updatedAt).toISOString(), now) || 'just now'} `),
        liveBadge(store))),

    ringRow(o.seats, now),

    h('section', { 'aria-label': 'Needs you', class: 'block needs-block' },
      h('div', { class: 'block-head phone-only' },
        h('h2', {}, `Needs you · ${needs.length}`),
        swipe ? h('span', { class: 'muted small swipe-note' }, 'Swipe right: seen · left: snooze 15 min') : null),
      needs.length ? h('div', { class: 'grid grid-needs' }, needs.map((s) => needsCard(s, idx, now)))
        : h('p', { class: 'calm' }, 'Nothing is waiting on you. Sessions that ask a question or need permission show up here.')),

    h('section', { 'aria-labelledby': 'h-accounts', class: 'block desk-only' },
      h('h2', { id: 'h-accounts' }, 'Accounts'),
      seats.length ? h('div', { class: 'grid grid-seats' }, seats.map((s) => seatCard(s, idx.get(s.id).colour, placesText(s), now)))
        : h('p', { class: 'muted' }, 'No accounts yet. They appear when a machine reports its first session or usage.')),

    h('section', { 'aria-labelledby': 'h-open', class: 'block' },
      h('div', { class: 'block-head' }, h('h2', { id: 'h-open' }, 'Open sessions'), h('a', { href: '#/sessions' }, 'All sessions')),
      !store.sessions ? h('p', { class: 'muted' }, 'Loading…')
        : shown.length === 0 ? h('p', { class: 'muted' }, 'No other open sessions.')
        // The phone shows the first three.
        : h('ul', { class: 'lines', 'data-key': 'open-sessions' }, shown.map((s, i) => line(s, i >= 3 ? ' desk-only' : ''))),
      open.length > 3 ? h('p', { class: `small${open.length > shown.length ? '' : ' phone-only'}` },
        h('a', { href: '#/sessions', 'data-key': 'more-open' },
          h('span', { class: 'desk-only' }, `+ ${plural(open.length - shown.length, 'more open session')}`),
          h('span', { class: 'phone-only' }, `+ ${plural(open.length - 3, 'more open session')}`))) : null),

    h('section', { 'aria-labelledby': 'h-machines', class: 'block' },
      h('div', { class: 'block-head' }, h('h2', { id: 'h-machines' }, 'Machines'), h('a', { href: '#/machines' }, 'Details')),
      o.machines.length ? h('div', { class: 'grid grid-mini' }, o.machines.map((m) => machineSummary(m, now, counts ? counts.machine.get(m.id) || 0 : null)))
        : h('p', { class: 'muted' }, 'No machines have connected yet.')));

  return { title: needs.length ? `(${needs.length}) Now · Switchyard` : 'Now · Switchyard', node };
}
