// Cards shared by several pages: needs-you, account, machine.

import { h } from './dom.js';
import { ago, agoLabel, clock, plural, projectOf, humanKey, httpsUrl } from '../format.js';
import { statusOf, backlogText, reachChip } from './machines-fmt.js';
import { sessionSeat, seatName } from './seat.js';
import { stateInfo } from './state.js';
import { windowRow, sortWindows, worstPace, paceChip } from './windows.js';
import { alertLine, alertButtons, canAct } from './alerts.js';
import { swipeable } from './swipe.js';
import { toast } from './ui.js';
import { markSeen, snooze } from '../store.js';

export const dot = (colour) => h('span', { class: 'dot', 'aria-hidden': 'true', style: { background: colour } });

export const sessionLink = (s, text) => h('a', { href: `#/sessions/${encodeURIComponent(s.id)}`, 'data-key': `s-${s.id}` }, text);

async function copy(text, button) {
  const old = button.textContent;
  try {
    await navigator.clipboard.writeText(text);
    button.textContent = 'Copied';
  } catch (_) {
    button.textContent = 'Copy failed';
  }
  setTimeout(() => { button.textContent = old; }, 1600);
}

export function copyButton(text, label, key) {
  return h('button', { type: 'button', class: 'btn', 'data-key': key, onclick: (e) => copy(text, e.currentTarget) }, label);
}

// What the session wants from you, as one line of text.
export function ask(s) {
  return s.question || s.last_message || s.detail || '';
}

// Which needs-you card has its Seen / Snooze menu open (phone), kept across redraws.
export const ui = { menu: null };

export function needsCard(s, seatIdx, now) {
  const info = stateInfo(s);
  const seat = sessionSeat(s, seatIdx);
  const mono = s.state === 'needs_permission';
  const a = s.alert;
  const acts = canAct(s);
  const href = `#/sessions/${encodeURIComponent(s.id)}`;
  const open = ui.menu === s.id;
  const card = h('article', {
    class: `card needs-card needs-${info.cls}${a && a.status === 'seen' ? ' is-seen' : ''}${open ? ' menu-open' : ''}`,
    'data-session': s.id,
    onclick: (e) => { if (!e.target.closest('a, button')) location.hash = href; },
  },
    h('div', { class: 'card-top' },
      h('span', { class: `badge badge-${info.cls === 'failed' ? 'failed' : 'needs'}` }, info.long),
      h('span', { class: 'muted card-age' }, ago(s.state_since, now), h('span', { class: 'desk-only' }, ' waiting')),
      acts ? h('button', {
        type: 'button', class: 'menu-btn', 'aria-label': 'Seen and snooze', 'aria-expanded': String(open), 'data-key': `menu-${s.id}`,
        onclick: () => { ui.menu = ui.menu === s.id ? null : s.id; window.dispatchEvent(new Event('sy-rerender')); },
      }, '⋯') : null),
    h('p', { class: `ask${mono ? ' mono' : ''}` }, ask(s) || 'No details were sent.'),
    h('div', { class: 'meta-line' }, dot(seat.colour), `${seat.label} · ${projectOf(s)}${s.machine_name ? ' · ' + s.machine_name : ''}`),
    a ? h('div', { class: 'meta-line alert-meta' }, alertLine(a, now)) : null,
    acts ? alertButtons(s) : null,
    h('div', { class: 'actions card-links' },
      httpsUrl(s.remote_url) ? h('a', { class: 'btn btn-primary', href: httpsUrl(s.remote_url), target: '_blank', rel: 'noopener noreferrer' }, 'Open in Claude') : null,
      s.resume ? copyButton(s.resume, 'Copy resume command', `copy-${s.id}`) : null,
      h('a', { class: httpsUrl(s.remote_url) || s.resume ? 'btn' : 'btn btn-primary', href, 'data-key': `view-${s.id}` }, 'View session')));
  if (!acts) return card;
  // Swipe right = Seen, left = Snooze, on touch screens; the buttons above do the same.
  const wrap = h('div', { class: 'swipe' },
    h('span', { class: 'swipe-hint swipe-hint-l', 'aria-hidden': 'true' }, 'Seen'),
    h('span', { class: 'swipe-hint swipe-hint-r', 'aria-hidden': 'true' }, 'Snooze 15 min'),
    card);
  const guard = (fn) => () => fn().catch((ex) => toast(ex.status === 409 ? 'That alert already changed' : ex.message));
  swipeable(card, {
    onRight: guard(() => markSeen(a.id, s.id)),
    onLeft: guard(() => snooze(a.id, s.id)),
  });
  return wrap;
}

// Home-screen account card: first windows, pace chip, where it is used.
export function seatCard(seat, colour, machinesText, now) {
  const ws = sortWindows(seat.windows || []);
  const main = ws.filter((w) => w.key === 'five_hour' || w.key === 'seven_day');
  const rest = ws.filter((w) => !main.includes(w));
  const shownRest = rest.slice(0, 2);
  const worst = worstPace(ws, now);
  const name = seatName(seat);
  return h('article', { class: 'card seat-card', style: { '--seat': colour } },
    h('div', { class: 'seat-head' },
      h('span', { class: 'seat-name' }, name),
      h('span', { class: 'muted small' }, planText(seat))),
    h('div', { class: 'muted small seat-sub' }, [seat.email, machinesText].filter(Boolean).join(' · ')),
    ws.length === 0 ? h('p', { class: 'muted small' }, 'No usage reported yet.') : null,
    [...(main.length ? main : ws.slice(0, 2)), ...shownRest].map((w) => windowRow(w, { seatName: name, now })),
    rest.length > shownRest.length ? h('div', { class: 'muted small' }, `+ ${plural(rest.length - shownRest.length, 'more window')}`) : null,
    worst ? paceChip(worst.p, worst.w.key === 'five_hour' ? '' : windowRowLabel(worst.w)) : null);
}

function windowRowLabel(w) {
  return w.key === 'seven_day' ? '7-day' : (w.label || humanKey(w.key));
}

export function planText(seat) {
  return [seat.plan, seat.org_name].filter(Boolean).join(' · ');
}

// Reads like "laptop, gpu-box" from the seat's places.
export function placesText(seat) {
  const names = [...new Set((seat.places || []).map((p) => p.machine_name).filter(Boolean))];
  return names.join(', ');
}

export function machineStatus(m, now = Date.now()) {
  return statusOf(m, now, { ago, agoLabel });
}

export function machineSummary(m, now) {
  const st = machineStatus(m, now);
  const backlog = backlogText(m, now, { clock });
  const chip = reachChip(m);
  return h('div', { class: 'card machine-mini' },
    h('div', {},
      h('div', { class: 'strong' }, m.name),
      h('div', { class: 'muted small' }, [plural(m.live_sessions || 0, 'session'), backlog, chip ? chip.text : ''].filter(Boolean).join(' · '))),
    h('span', { class: `small status-${st.cls}` }, st.text));
}

// Primitive fields of a raw web-usage object (extra_usage, spend) as a definition list.
export function kvList(obj) {
  if (!obj || typeof obj !== 'object') return null;
  const rows = [];
  const walk = (o, prefix) => {
    for (const [k, v] of Object.entries(o)) {
      if (v == null) continue;
      if (typeof v === 'object' && !Array.isArray(v)) walk(v, `${prefix}${humanKey(k)} · `);
      else rows.push([prefix + humanKey(k), Array.isArray(v) ? v.join(', ') : typeof v === 'boolean' ? (v ? 'yes' : 'no') : String(v)]);
    }
  };
  walk(obj, '');
  if (!rows.length) return null;
  return h('dl', { class: 'kv' }, rows.flatMap(([k, v]) => [h('dt', {}, k), h('dd', { class: 'mono' }, v)]));
}

