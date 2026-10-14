// Sessions: grouped by account (or machine / project), with state filters.

import { h } from '../components/dom.js';
import { dot, sessionLink, ask } from '../components/cards.js';
import { seatIndex, sessionSeat } from '../components/seat.js';
import { stateTag, isLive } from '../components/state.js';
import { pct, ago, plural, projectOf } from '../format.js';
import { ensureSessions } from '../store.js';
import { alertLine } from '../components/alerts.js';

export const load = () => ensureSessions();

const FILTERS = [
  { id: 'needs_you', label: 'Needs you', test: (s) => s.needs_you },
  { id: 'working', label: 'Working', test: (s) => s.state === 'working' },
  { id: 'done', label: 'Done', test: (s) => s.state === 'done' },
  { id: 'all', label: 'All', test: (s) => isLive(s) },
  { id: 'ended', label: 'Ended', test: (s) => !isLive(s) },
];
const GROUPS = { account: 'Account', machine: 'Machine', project: 'Project' };

function setQuery(route, patch) {
  const q = new URLSearchParams(route.query);
  for (const [k, v] of Object.entries(patch)) (v ? q.set(k, v) : q.delete(k));
  const s = q.toString();
  location.hash = `#/sessions${s ? '?' + s : ''}`;
}

export function render({ store, route, now }) {
  const o = store.overview;
  const idx = seatIndex(o.seats);
  const all = store.sessions;
  const filterId = FILTERS.some((f) => f.id === route.query.get('filter')) ? route.query.get('filter') : 'all';
  const group = GROUPS[route.query.get('by')] ? route.query.get('by') : 'account';
  const machineFilter = route.query.get('machine') || '';

  const node = h('div', { class: 'page' });
  const counts = (f) => (all || []).filter((s) => f.test(s)).length;

  node.append(h('header', { class: 'page-head' },
    h('h1', {}, 'Sessions'),
    h('div', { class: 'filters' },
      h('div', { class: 'chips', role: 'group', 'aria-label': 'Show' }, FILTERS.map((f) => h('button', {
        type: 'button', class: `chip-btn${f.id === 'needs_you' ? ' chip-needs' : ''}${f.id === 'ended' ? ' desk-only' : ''}`, 'aria-pressed': String(f.id === filterId), 'data-key': `f-${f.id}`,
        onclick: () => setQuery(route, { filter: f.id === 'all' ? '' : f.id }),
      }, f.label, all ? h('span', { class: 'chip-n' }, ` ${counts(f)}`) : null)),
        h('button', { type: 'button', class: 'chip-btn', 'aria-pressed': String(group === 'machine'), 'data-key': 'by-machine',
          onclick: () => setQuery(route, { by: group === 'machine' ? '' : 'machine' }) }, 'By machine')),
      h('label', { class: 'field desk-only' }, 'Group by ',
        h('select', { 'data-key': 'group', onchange: (e) => setQuery(route, { by: e.target.value === 'account' ? '' : e.target.value }) },
          Object.entries(GROUPS).map(([v, l]) => h('option', { value: v, selected: v === group }, l)))),
      h('label', { class: 'field desk-only' }, 'Machine ',
        h('select', { 'data-key': 'machine', onchange: (e) => setQuery(route, { machine: e.target.value }) },
          h('option', { value: '', selected: !machineFilter }, 'All'),
          o.machines.map((m) => h('option', { value: m.id, selected: m.id === machineFilter }, m.name)))))));

  if (!all) { node.append(h('p', { class: 'muted pad' }, store.error || 'Loading sessions…')); return { title: 'Sessions · Switchyard', node }; }

  const f = FILTERS.find((x) => x.id === filterId);
  const rows = all.filter((s) => f.test(s) && (!machineFilter || s.machine === machineFilter));
  if (!rows.length) {
    node.append(h('p', { class: 'calm' }, filterId === 'all' ? 'No live sessions.' : `No ${f.label.toLowerCase()} sessions.`));
    return { title: 'Sessions · Switchyard', node };
  }

  // Group, keeping the hub's order inside each group (needs-you first, then newest).
  const groups = new Map();
  for (const s of rows) {
    let key, label, colour, sub = '';
    if (group === 'account') {
      const seat = sessionSeat(s, idx);
      key = s.seat || '_none'; label = seat.label; colour = seat.colour;
      const known = idx.get(s.seat);
      if (known) {
        const five = (known.seat.windows || []).find((w) => w.key === 'five_hour');
        sub = [known.seat.email, five && five.used_pct != null ? `${pct(five.used_pct)} of 5h` : ''].filter(Boolean).join(' · ');
      }
    } else if (group === 'machine') { key = s.machine || '_none'; label = s.machine_name || 'Unknown machine'; }
    else { key = projectOf(s); label = key; }
    if (!groups.has(key)) groups.set(key, { label, colour, sub, rows: [] });
    groups.get(key).rows.push(s);
  }
  const ordered = [...groups.values()].sort((a, b) => Number(b.rows.some((s) => s.needs_you)) - Number(a.rows.some((s) => s.needs_you)));

  const table = h('table', { class: 'sessions-table' },
    h('thead', {}, h('tr', {}, ['State', 'Session', 'Machine', 'Last line', 'Context', 'In state'].map((t) => h('th', { scope: 'col' }, t)))));
  for (const g of ordered) {
    const tb = h('tbody', {},
      h('tr', { class: 'group-row' }, h('th', { colspan: 6, scope: 'colgroup' },
        g.colour ? dot(g.colour) : null, g.label, ' ',
        h('span', { class: 'muted normal' }, [g.sub, plural(g.rows.length, 'session')].filter(Boolean).join(' · '))))
    );
    for (const s of g.rows) tb.append(sessionRow(s, now));
    table.append(tb);
  }
  node.append(h('div', { class: 'table-wrap' }, table));
  node.append(h('p', { class: 'muted small foot-note' }, 'States come from Claude Code hooks; "Asked" also catches plain-text questions at the end of a turn.'));
  return { title: 'Sessions · Switchyard', node };
}

function sessionRow(s, now) {
  const last = s.state === 'asked' || s.state === 'needs_permission' ? ask(s) : (s.last_message || s.question || s.detail || '');
  const ctx = s.context_pct;
  const href = `#/sessions/${encodeURIComponent(s.id)}`;
  return h('tr', { class: `srow${s.needs_you ? ' srow-needs' : ''}${s.alert && s.alert.status === 'seen' ? ' is-seen' : ''}`, onclick: (e) => { if (!e.target.closest('a')) location.hash = href; } },
    h('td', { class: 'c-state', 'data-label': 'State' }, stateTag(s), s.stale ? h('span', { class: 'flag' }, ' stale') : null, s.machine_stale ? h('span', { class: 'flag' }, ' machine quiet') : null),
    h('td', { class: 'c-session', 'data-label': 'Session' }, sessionLink(s, h('b', {}, projectOf(s))), s.title || s.name ? h('span', { class: 'muted' }, ` · ${s.title || s.name}`) : null),
    h('td', { class: 'c-machine', 'data-label': 'Machine' }, h('span', { class: 'machine-chip' }, s.machine_name || '–')),
    h('td', { class: `c-last${s.state === 'needs_permission' ? ' mono' : ''}`, 'data-label': 'Last line' }, h('span', { class: 'clamp' }, last || '–'), s.alert && s.needs_you ? h('span', { class: 'alert-meta' }, alertLine(s.alert, now)) : null),
    h('td', { class: 'c-ctx', 'data-label': 'Context' }, ctx == null ? h('span', { class: 'muted' }, '–') : h('span', { class: `ctx${ctx >= 85 ? ' hot' : ''}` },
      h('span', { class: 'meter meter-s', role: 'meter', 'aria-valuemin': 0, 'aria-valuemax': 100, 'aria-valuenow': Math.round(ctx), 'aria-label': 'Context used' }, h('span', { class: 'meter-fill', style: { width: `${Math.min(100, ctx)}%` } })),
      h('span', { class: 'mono small' }, pct(ctx)))),
    h('td', { class: 'c-age muted', 'data-label': 'In state' }, ago(s.state_since, now)));
}
