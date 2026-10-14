// Now / Home: what needs you, account limits, live sessions, machines.

import { h } from '../components/dom.js';
import { needsCard, seatCard, placesText, machineSummary, dot, sessionLink } from '../components/cards.js';
import { seatIndex, sessionSeat } from '../components/seat.js';
import { stateTag, isLive } from '../components/state.js';
import { liveBadge } from '../components/nav.js';
import { ringRow } from '../components/rings.js';
import { ensureSessions, canPush } from '../store.js';
import { agoLabel, ago, plural, projectOf } from '../format.js';

export const load = () => ensureSessions();

export function render({ store, now }) {
  const o = store.overview;
  const idx = seatIndex(o.seats);
  const needs = o.needs_you;
  const title = needs.length ? `${plural(needs.length, 'session')} need${needs.length === 1 ? 's' : ''} you` : 'Nothing needs you';
  const seats = o.seats.filter((s) => !s.hidden);
  const live = (store.sessions || []).filter(isLive).filter((s) => !s.needs_you);
  const shown = live.slice(0, 8);
  const swipe = canPush() && needs.some((s) => s.alert && ['pending', 'fired', 'snoozed'].includes(s.alert.status));

  const node = h('div', { class: 'page page-now' },
    h('header', { class: 'page-head' },
      h('h1', { class: 'desk-only' }, title),
      h('h1', { class: 'phone-only' }, 'Now'),
      h('p', { class: 'muted small head-meta' },
        h('span', { class: 'desk-only' }, `${plural(seats.length, 'account')} · ${plural(o.counts.live ?? 0, 'session')} on ${plural(o.machines.length, 'machine')} · updated ${agoLabel(new Date(store.updatedAt).toISOString(), now) || 'just now'} `),
        liveBadge(store.live, store.offline))),

    ringRow(o.seats, now),

    h('section', { 'aria-label': 'Needs you', class: 'block needs-block' },
      h('div', { class: 'block-head phone-only' },
        h('h2', {}, `Needs you · ${needs.length}`),
        swipe ? h('span', { class: 'muted small swipe-note' }, 'swipe: seen · snooze') : null),
      needs.length ? h('div', { class: 'grid grid-needs' }, needs.map((s) => needsCard(s, idx, now)))
        : h('p', { class: 'calm' }, 'All sessions are working or idle. You will see anything that needs an answer here.')),

    h('section', { 'aria-labelledby': 'h-accounts', class: 'block desk-only' },
      h('h2', { id: 'h-accounts' }, 'Accounts'),
      seats.length ? h('div', { class: 'grid grid-seats' }, seats.map((s) => seatCard(s, idx.get(s.id).colour, placesText(s), now)))
        : h('p', { class: 'muted' }, 'No accounts yet. They appear when a machine reports its first session or usage.')),

    h('section', { 'aria-labelledby': 'h-live', class: 'block desk-only' },
      h('div', { class: 'block-head' }, h('h2', { id: 'h-live' }, 'Running sessions'), h('a', { href: '#/sessions' }, 'All sessions')),
      !store.sessions ? h('p', { class: 'muted' }, 'Loading…')
        : shown.length === 0 ? h('p', { class: 'muted' }, 'No other live sessions.')
        : h('ul', { class: 'lines' }, shown.map((s) => {
          const seat = sessionSeat(s, idx);
          return h('li', { class: 'line' },
            stateTag(s),
            h('span', { class: 'line-main' }, sessionLink(s, h('b', {}, projectOf(s))), s.title || s.name ? h('span', { class: 'muted' }, ` · ${s.title || s.name}`) : null),
            h('span', { class: 'line-meta muted small' }, dot(seat.colour), `${seat.label}${s.machine_name ? ' · ' + s.machine_name : ''} · ${ago(s.state_since, now)}`));
        })),
      live.length > shown.length ? h('p', { class: 'muted small' }, `+ ${live.length - shown.length} more`) : null),

    h('section', { 'aria-labelledby': 'h-machines', class: 'block' },
      h('div', { class: 'block-head' }, h('h2', { id: 'h-machines', class: 'desk-only' }, 'Machines'), h('a', { href: '#/machines', class: 'desk-only' }, 'Details')),
      o.machines.length ? h('div', { class: 'grid grid-mini' }, o.machines.map((m) => machineSummary(m, now)))
        : h('p', { class: 'muted' }, 'No machines have connected yet.')));

  return { title: needs.length ? `(${needs.length}) Switchyard` : 'Switchyard', node };
}
