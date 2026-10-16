// Accounts: every limit window with reset time and pace, plus web-usage fields.

import { h } from '../components/dom.js';
import { seatIndex, seatName, namesCollide } from '../components/seat.js';
import { windowRow, sortWindows, worstPace, paceChip } from '../components/windows.js';
import { planText, placesText, kvList, usageMoney, noUsage } from '../components/cards.js';
import { liveCounts } from '../components/state.js';
import { agoLabel, plural, pct } from '../format.js';
import { isOwner, ensureSessions, reorderSeats } from '../store.js';
import { moveBy, dropTarget, dropOrder, moveSentence } from '../components/order.js';
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
  // ("Default colour" only marks the form; Save sends it.)
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
      h('input', { type: 'color', value: edit.colour, 'data-key': 'seat-colour', oninput: (e) => { edit.colour = e.target.value; edit.clearColour = false; again(); } }),
      edit.clearColour ? h('span', { class: 'muted small' }, 'default') : null),
    edit.error ? h('p', { class: 'form-error', role: 'alert' }, edit.error) : null,
    h('div', { class: 'actions' },
      h('button', { type: 'submit', class: 'btn btn-primary', disabled: edit.busy ? true : undefined, 'data-key': 'seat-save' }, 'Save'),
      h('button', { type: 'button', class: 'btn', 'data-key': 'seat-reset-colour', 'aria-pressed': String(edit.clearColour), onclick: () => { edit.clearColour = true; again(); } }, 'Use default colour'),
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

// ---- the order of the accounts ---------------------------------------------------------------
// The owner drags a card by its grip (pointer events: mouse, pen and touch), or focuses the
// grip and presses the arrow keys (Alt+arrow anywhere on a card works too). The hub keeps the
// order for every device (PUT /v1/seats/order); the board shows it at once (store.reorderSeats).
// The pure parts are in components/order.js.
const drag = { id: '', drop: null };
const move = { error: '', said: '' };

let store0 = () => ({ overview: null }); // the store of the last render
const shownIds = () => (store0().overview ? store0().overview.seats.filter((s) => !s.hidden).map((s) => s.id) : []);
const cardRects = () => [...document.querySelectorAll('.grid-accounts > .seat-card')].map((el) => {
  const r = el.getBoundingClientRect();
  return { id: el.id.replace(/^seat-/, ''), left: r.left, top: r.top, width: r.width, height: r.height };
});

// Marks the dragged card and the drop place straight in the DOM (a redraw reads `drag` too).
function paintDrag() {
  const grid = document.querySelector('.grid-accounts');
  if (!grid) return;
  for (const el of grid.querySelectorAll('.seat-card')) {
    const id = el.id.replace(/^seat-/, '');
    el.classList.toggle('seat-dragging', id === drag.id);
    el.classList.toggle('drop-before', !!drag.drop && drag.drop.target === id && !drag.drop.after && id !== drag.id);
    el.classList.toggle('drop-after', !!drag.drop && drag.drop.target === id && drag.drop.after && id !== drag.id);
  }
  const tops = new Set(cardRects().map((r) => Math.round(r.top)));
  grid.dataset.cols = tops.size < grid.querySelectorAll('.seat-card').length ? 'n' : '1';
}

async function commit(ids, id, name) {
  move.error = '';
  const err = await reorderSeats(ids);
  if (err) { move.error = err; toast(err); move.said = ''; } else move.said = moveSentence(name, ids, id);
  again();
}

function endDrag(drop) {
  window.removeEventListener('pointermove', onMove);
  window.removeEventListener('pointerup', onUp);
  window.removeEventListener('pointercancel', onCancel);
  window.removeEventListener('keydown', onEsc, true);
  document.body.classList.remove('dragging-seat');
  const id = drag.id;
  drag.id = ''; drag.drop = null;
  paintDrag();
  if (!drop || !id) return;
  const next = dropOrder(shownIds(), id, drop);
  if (next) commit(next, id, nameOf(id));
}

const nameOf = (id) => {
  const s = store0().overview && store0().overview.seats.find((x) => x.id === id);
  return s ? seatName(s) : '';
};

function onMove(e) {
  if (!drag.id) return;
  drag.drop = dropTarget(cardRects(), e.clientX, e.clientY);
  paintDrag();
  // Near the top or bottom of the window the page scrolls under the pointer.
  if (e.clientY < 60) window.scrollBy(0, -14); else if (e.clientY > window.innerHeight - 60) window.scrollBy(0, 14);
}
function onUp(e) { drag.drop = dropTarget(cardRects(), e.clientX, e.clientY); endDrag(drag.drop); }
function onCancel() { endDrag(null); }
function onEsc(e) { if (e.key === 'Escape') { e.preventDefault(); endDrag(null); } }

function startDrag(e, seat) {
  if (e.button !== undefined && e.button !== 0) return;
  e.preventDefault();
  drag.id = seat.id; drag.drop = null;
  document.body.classList.add('dragging-seat');
  window.addEventListener('pointermove', onMove);
  window.addEventListener('pointerup', onUp);
  window.addEventListener('pointercancel', onCancel);
  window.addEventListener('keydown', onEsc, true);
  paintDrag();
}

const KEY_DELTA = { ArrowUp: -1, ArrowLeft: -1, ArrowDown: 1, ArrowRight: 1 };
function keyMove(e, seat, needAlt) {
  const delta = KEY_DELTA[e.key];
  if (!delta || (needAlt && !e.altKey)) return;
  e.preventDefault();
  e.stopPropagation(); // the grip and the card both listen: one move per key
  const ids = shownIds();
  const next = moveBy(ids, seat.id, delta);
  if (next !== ids) commit(next, seat.id, seatName(seat));
}

const grip = (seat, name) => h('button', { type: 'button', class: 'seat-grip', 'data-key': `seat-grip-${seat.id}`,
  'aria-label': `Move ${name}: drag, or press the arrow keys`, title: 'Drag to reorder (or use the arrow keys)',
  onpointerdown: (e) => startDrag(e, seat), onkeydown: (e) => keyMove(e, seat, false) },
  h('span', { 'aria-hidden': 'true' }, '⠿'));

let focused = '';
export const load = () => { focused = ''; return ensureSessions(); };

export function render({ store, route, now }) {
  store0 = () => store;
  const o = store.overview;
  const idx = seatIndex(o.seats);
  const seats = o.seats.filter((s) => !s.hidden);
  const counts = liveCounts(store.sessions, now);
  const anyWeb = seats.some((s) => s.web_raw_at);

  const node = h('div', { class: 'page' },
    h('header', { class: 'page-head col' }, h('h1', {}, 'Accounts'),
      h('p', { class: 'muted small' }, 'One card per Claude login, whichever profile or machine it is used on.'),
      h('p', { class: 'muted small', 'data-key': 'usage-source' }, anyWeb
        ? 'Limits come from the Claude Code status line, and from claude.ai where its usage page is read.'
        : 'Limits come from the Claude Code status line.')),
    namesCollide(seats) && isOwner() ? h('p', { class: 'calm small', 'data-key': 'label-hint' }, 'Some accounts have nearly the same name. Use Edit to give each one a short label.') : null,
    roomLine(seats, now),
    move.error ? h('p', { class: 'form-error', role: 'alert', 'data-key': 'order-error' }, move.error) : null,
    h('p', { class: 'sr-only', role: 'status', 'aria-live': 'polite', 'data-key': 'order-said' }, move.said),
    seats.length === 0 ? h('p', { class: 'calm' }, 'No accounts yet.') :
      h('div', { class: 'grid grid-accounts', 'data-key': 'accounts-grid' }, seats.map((s) => accountCard(s, idx.get(s.id).colour, now, route.query.get('seat') === s.id, counts ? counts.seat.get(s.id) || 0 : s.live_sessions, isOwner() && seats.length > 1))));
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
    ` (${left(best.five)} of the 5-hour limit left${week && week.used_pct != null ? `, ${left(week)} of the week` : ''})`);
}

function accountCard(seat, colour, now, highlight, live, canMove) {
  const ws = sortWindows(seat.windows || []);
  const name = seatName(seat);
  const worst = worstPace(ws, now);
  const money = usageMoney(seat);
  // Fields the board has no sentence for still show, as a list.
  const extra = money.length ? null : kvList(seat.extra_usage);
  const spend = money.length ? null : kvList(seat.spend);
  const where = placesText(seat);
  const dropClass = drag.id && drag.drop && drag.drop.target === seat.id && drag.id !== seat.id ? (drag.drop.after ? ' drop-after' : ' drop-before') : '';
  return h('article', { class: `card seat-card seat-card-full${highlight ? ' highlight' : ''}${drag.id === seat.id ? ' seat-dragging' : ''}${dropClass}`, id: `seat-${seat.id}`, style: { '--seat': colour }, 'aria-label': name,
    onkeydown: canMove ? (e) => { if (e.altKey) keyMove(e, seat, true); } : undefined },
    h('div', { class: 'seat-head' },
      canMove ? grip(seat, name) : null,
      h('h2', { class: 'seat-name' }, name),
      h('span', { class: 'muted small' }, planText(seat)),
      isOwner() && edit.id !== seat.id ? h('button', { type: 'button', class: 'linkish', 'data-key': `seat-edit-${seat.id}`,
        'aria-label': `Edit ${name}`, onclick: () => startEdit(seat, colour) }, 'Edit') : null),
    edit.id === seat.id ? editForm(seat) : null,
    h('div', { class: 'muted small seat-sub' },
      [seat.email, where ? `used on ${where}` : '', live ? plural(live, 'open session') : ''].filter(Boolean).join(' · ')),
    ws.length ? ws.map((w) => windowRow(w, { seatName: name, full: true, now })) : noUsage(),
    worst && worst.p.band === 'runaway' ? paceChip(worst.p) : null,
    money.length ? h('p', { class: 'small', 'data-key': `money-${seat.id}` }, money.join(' ')) : null,
    extra ? h('details', { class: 'extra', open: true }, h('summary', {}, 'Extra usage'), extra) : null,
    spend ? h('details', { class: 'extra' }, h('summary', {}, 'Spend'), spend) : null,
    seat.web_raw_at ? h('div', { class: 'muted small' }, `claude.ai usage read ${agoLabel(seat.web_raw_at, now)}`) : null);
}
