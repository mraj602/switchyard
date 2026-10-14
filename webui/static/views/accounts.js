// Accounts: every limit window with reset time and pace, plus web-usage fields.

import { h } from '../components/dom.js';
import { seatIndex, seatName } from '../components/seat.js';
import { windowRow, sortWindows, worstPace, paceChip } from '../components/windows.js';
import { planText, placesText, kvList } from '../components/cards.js';
import { agoLabel, plural, pct } from '../format.js';
import { isOwner } from '../store.js';
import { patchJSON } from '../api.js';
import { toast } from '../components/ui.js';

// Owner edit of a seat's label and colour (desktop.md C10): PATCH /v1/accounts/{seat}. The
// hub keeps a UI label over any label `sy` sends from a folder.
const LABEL_RE = /^[a-z0-9][a-z0-9-]{0,23}$/;
const again = () => window.dispatchEvent(new Event('sy-rerender'));
const edit = { id: '', label: '', colour: '', clearColour: false, error: '', busy: false };

function startEdit(seat, colour) {
  Object.assign(edit, { id: seat.id, label: seat.label || '', colour: /^#[0-9a-f]{6}$/.test(colour) ? colour : '#0e7c76',
    clearColour: false, error: '', busy: false });
  again();
}

async function saveEdit(seat) {
  const label = edit.label.trim();
  if (label && !LABEL_RE.test(label)) { edit.error = 'Use 1 to 24 characters: a to z, 0 to 9 and -, starting with a letter or digit.'; again(); return; }
  const body = { label: label || null, colour: edit.clearColour ? null : edit.colour };
  edit.busy = true; edit.error = ''; again();
  try {
    Object.assign(seat, await patchJSON(`/v1/accounts/${encodeURIComponent(seat.id)}`, body));
    edit.id = '';
  } catch (ex) {
    edit.error = ex.status === 403 ? 'Sign in as owner to edit accounts.' : ex.message;
    if (ex.name === 'AuthError') toast(ex.message);
  }
  edit.busy = false;
  again();
}

function editForm(seat) {
  return h('form', { class: 'seat-edit', 'data-key': 'seat-edit', onsubmit: (e) => { e.preventDefault(); saveEdit(seat); } },
    h('label', { class: 'field' }, 'Label',
      h('input', { type: 'text', value: edit.label, maxlength: 24, autocomplete: 'off', placeholder: 'work', 'data-key': 'seat-label',
        oninput: (e) => { edit.label = e.target.value; } })),
    h('label', { class: 'field' }, 'Colour',
      h('input', { type: 'color', value: edit.colour, 'data-key': 'seat-colour', oninput: (e) => { edit.colour = e.target.value; edit.clearColour = false; } })),
    edit.error ? h('p', { class: 'form-error', role: 'alert' }, edit.error) : null,
    h('div', { class: 'actions' },
      h('button', { type: 'submit', class: 'btn btn-primary', disabled: edit.busy ? true : undefined, 'data-key': 'seat-save' }, 'Save'),
      h('button', { type: 'button', class: 'btn', 'data-key': 'seat-reset-colour', onclick: () => { edit.clearColour = true; saveEdit(seat); } }, 'Default colour'),
      h('button', { type: 'button', class: 'linkish', 'data-key': 'seat-cancel', onclick: () => { edit.id = ''; again(); } }, 'Cancel')));
}

// Who has the most room in the 5-hour window (push.md 5.7): lowest use among visible
// accounts with a reading from the last 6 hours, and only when under 80%.
export function mostRoom(seats, now = Date.now()) {
  let best = null;
  for (const s of seats.filter((x) => !x.hidden)) {
    const five = (s.windows || []).find((w) => w.key === 'five_hour');
    if (!five || five.used_pct == null || five.used_pct >= 80) continue;
    const at = Date.parse(five.updated_at || '');
    if (Number.isNaN(at) || now - at > 6 * 36e5) continue;
    if (!best || five.used_pct < best.five.used_pct) best = { seat: s, five };
  }
  return best;
}

let focused = '';
export const load = () => { focused = ''; };

export function render({ store, route, now }) {
  const o = store.overview;
  const idx = seatIndex(o.seats);
  const seats = o.seats.filter((s) => !s.hidden);

  const node = h('div', { class: 'page' },
    h('header', { class: 'page-head' }, h('h1', {}, 'Accounts'),
      h('p', { class: 'muted small head-meta' }, 'One card per login. Folders and tabs with the same login share a card.')),
    roomLine(seats, now),
    seats.length === 0 ? h('p', { class: 'calm' }, 'No accounts yet.') :
      h('div', { class: 'grid grid-accounts' }, seats.map((s) => accountCard(s, idx.get(s.id).colour, now, route.query.get('seat') === s.id))));
  const want = route.query.get('seat');
  if (want && focused !== want) {
    focused = want;
    requestAnimationFrame(() => { const el = document.getElementById(`seat-${want}`); if (el) el.scrollIntoView({ block: 'center' }); });
  }
  return { title: 'Accounts · Switchyard', node };
}

function roomLine(seats, now) {
  const best = mostRoom(seats, now);
  if (!best) return null;
  const week = (best.seat.windows || []).find((w) => w.key === 'seven_day');
  const left = (w) => pct(100 - w.used_pct);
  return h('p', { class: 'room', 'data-key': 'most-room' }, 'Most room right now: ', h('b', {}, seatName(best.seat)),
    ` (${left(best.five)} of 5h${week && week.used_pct != null ? `, ${left(week)} of week` : ''})`);
}

function accountCard(seat, colour, now, highlight) {
  const ws = sortWindows(seat.windows || []);
  const name = seatName(seat);
  const worst = worstPace(ws, now);
  const extra = kvList(seat.extra_usage);
  const spend = kvList(seat.spend);
  const where = placesText(seat);
  return h('article', { class: `card seat-card seat-card-full${highlight ? ' highlight' : ''}`, id: `seat-${seat.id}`, style: { '--seat': colour }, 'aria-label': name },
    h('div', { class: 'seat-head' },
      h('h2', { class: 'seat-name' }, name),
      h('span', { class: 'muted small' }, planText(seat)),
      isOwner() && edit.id !== seat.id ? h('button', { type: 'button', class: 'linkish', 'data-key': `seat-edit-${seat.id}`,
        'aria-label': `Edit ${name}`, onclick: () => startEdit(seat, colour) }, 'Edit') : null),
    edit.id === seat.id ? editForm(seat) : null,
    h('div', { class: 'muted small seat-sub' },
      [seat.email, where ? `used on ${where}` : '', seat.live_sessions ? plural(seat.live_sessions, 'live session') : ''].filter(Boolean).join(' · ')),
    ws.length ? ws.map((w) => windowRow(w, { seatName: name, full: true, now })) : h('p', { class: 'muted' }, 'No limit data yet.'),
    worst && worst.p.band === 'runaway' ? paceChip(worst.p) : null,
    extra ? h('details', { class: 'extra', open: true }, h('summary', {}, 'Extra usage'), extra) : null,
    spend ? h('details', { class: 'extra' }, h('summary', {}, 'Spend'), spend) : null,
    seat.web_raw_at ? h('div', { class: 'muted small' }, `claude.ai usage read ${agoLabel(seat.web_raw_at, now)}`)
      : h('div', { class: 'muted small' }, 'No claude.ai web usage: figures come from the status line only.'));
}
