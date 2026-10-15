// Settings → Peers (peers.md 5, 6.3, 10, 11): the account matrix (rows send, columns receive),
// the matrix version, Pause all peers, and the audit list (metadata only, never text) with
// "Resume thread" on loop-guard stops for an owner session.
// Narrowing works from any sign-in that can write; widening needs the owner session, and the
// hub signs it only then. A control that is not available says why.

import { h } from '../components/dom.js';
import { section, toggle, toast, moreTabs, details } from '../components/ui.js';
import { seatIndex, seatName } from '../components/seat.js';
import { store, isOwner, canPush } from '../store.js';
import { getMatrix, editMatrix, pausePeers, getPeerAudit, getPeerMessages, resumePeerThread } from '../api.js';
import { agoLabel, dateTime } from '../format.js';
import {
  peerAccounts, cellState, cellLabel, cellEdit, editBody, whyNot, detailText, eventLabel, auditWho,
  messageWho, outcomeLabel, sizeLabel, resumableStops, resumeText, isSetup, CELL_GLYPH, CELL_WORD, LEGEND,
} from '../components/peers-fmt.js';

const st = { matrix: null, audit: null, messages: null, error: null, pick: null, busy: false };
const again = () => window.dispatchEvent(new Event('sy-rerender'));

export async function load() {
  st.error = null;
  st.pick = null;
  const [m, a, msg] = await Promise.allSettled([getMatrix(), getPeerAudit({ limit: '50' }), getPeerMessages({ limit: '50' })]);
  if (m.status === 'fulfilled') st.matrix = m.value;
  else if (m.reason && m.reason.name !== 'AuthError') st.error = m.reason.message;
  if (a.status === 'fulfilled') st.audit = a.value.entries || [];
  // The messages list is optional: an older hub answers 404 or 501, a read-only sign-in 403.
  st.messages = msg.status === 'fulfilled' ? msg.value.messages || [] : null;
  again();
}

async function refreshAudit() {
  try { st.audit = (await getPeerAudit({ limit: '50' })).entries || []; } catch (_) { /* keep the old list */ }
}

// Sends an edit (or the pause switch); a stale version (409) reloads the matrix.
async function change(widens, send) {
  const why = whyNot({ widens, owner: isOwner(), push: canPush(), offline: !!store.offline });
  if (why) { toast(why); return; }
  st.busy = true;
  again();
  try {
    st.matrix = await send();
    st.pick = null;
    await refreshAudit();
  } catch (ex) {
    if (ex.status === 409) { try { st.matrix = await getMatrix(); } catch (_) { /* shown below */ } toast('The matrix changed meanwhile. Check it and try again.'); }
    else toast(ex.message || 'Could not change the matrix');
  }
  st.busy = false;
  again();
}

const edit = (patch, widens) => change(widens, () => editMatrix(editBody(st.matrix.matrix, patch)));

// ---- Pause all peers ----------------------------------------------------------------------

function killSwitch() {
  const m = st.matrix.matrix;
  const owner = isOwner();
  const off = whyNot({ widens: m.paused, owner, push: canPush(), offline: !!store.offline });
  // While nothing can pass (no pair allowed, no message yet) the switch is a small control (PE4).
  const quiet = !m.paused && !(m.allow || []).length && !(st.messages && st.messages.length);
  return h('section', { class: `block kill${m.paused ? ' kill-on' : ''}`, 'aria-label': 'Pause all peers' },
    h('div', { class: 'panel pad-s col' },
      h('p', { class: m.paused ? 'strong status-warn' : 'strong status-ok', role: 'status', 'data-key': 'peers-state' },
        m.paused ? 'All peers are paused' : 'Peers are on'),
      h('p', { class: 'muted small' }, m.paused
        ? 'No message passes between sessions on any machine until the owner resumes.'
        : 'Pausing stops every machine at once: new messages are refused and queued ones dropped.'),
      h('button', {
        type: 'button', class: m.paused ? 'btn' : quiet ? 'btn btn-sm' : 'btn btn-danger btn-big', 'data-key': 'peers-pause',
        disabled: st.busy || !!off, title: off || undefined,
        onclick: () => change(m.paused, () => pausePeers(!m.paused)),
      }, m.paused ? 'Resume peers' : 'Pause all peers'),
      off ? h('p', { class: 'muted small', 'data-key': 'peers-pause-why' }, off) : null));
}

// Seat names as the rest of the board shows them, told apart when they collide (PE2, G7).
function accounts(seats, m) {
  seatIndex(seats);
  return peerAccounts(seats, m, seatName);
}

// ---- the grid -----------------------------------------------------------------------------

function grid(accts, m) {
  const pick = st.pick;
  const head = h('tr', {}, h('th', { scope: 'col', class: 'mx-corner' }, h('span', { class: 'sr-only' }, 'From, to'), h('span', { 'aria-hidden': 'true' }, 'from \\ to')),
    accts.map((a) => h('th', { scope: 'col', class: 'mx-col', title: a.label }, h('span', { class: 'mx-name' }, a.label))));
  const rows = accts.map((r) => h('tr', {}, h('th', { scope: 'row', class: 'mx-row', title: r.label }, h('span', { class: 'mx-name' }, r.label)),
    accts.map((c) => {
      if (r.account === c.account) {
        return h('td', { class: 'mx-cell mx-self', title: `Same account: ${m.same_account ? 'on' : 'off'}` },
          h('span', { 'aria-label': `${r.label} to itself: same account ${m.same_account ? 'on' : 'off'}` }, '='));
      }
      const state = cellState(m.allow, r.account, c.account);
      const on = pick && pick.from === r.account && pick.to === c.account;
      return h('td', { class: 'mx-cell' }, h('button', {
        type: 'button', class: `mx-btn mx-${state}`, 'aria-pressed': String(!!on), 'data-key': `cell-${r.account}-${c.account}`,
        'aria-label': cellLabel(state, r.label, c.label), title: CELL_WORD[state],
        onclick: () => { st.pick = on ? null : { from: r.account, to: c.account }; again(); },
      }, CELL_GLYPH[state]));
    })));
  return h('div', { class: 'mx-wrap' }, h('table', { class: 'mx', 'data-key': 'matrix' }, h('thead', {}, head), h('tbody', {}, rows)));
}

function editor(accts, m) {
  const p = st.pick;
  if (!p) return h('p', { class: 'muted small' }, 'Pick a cell to change it. A row starts threads with a column.');
  const from = accts.find((a) => a.account === p.from), to = accts.find((a) => a.account === p.to);
  if (!from || !to) return null;
  const cur = cellState(m.allow, p.from, p.to);
  const owner = isOwner(), push = canPush(), offline = !!store.offline;
  const opts = [['none', 'None'], ['out', `${from.label} → ${to.label}`], ['in', `${to.label} → ${from.label}`], ['both', 'Both ways']];
  const whys = [];
  const btns = opts.map(([target, text]) => {
    const e = cellEdit(m.allow, p.from, p.to, target);
    const why = target === cur ? '' : whyNot({ widens: e.widens, owner, push, offline });
    if (why && !whys.includes(why)) whys.push(why);
    return h('button', {
      type: 'button', class: `btn btn-sm${target === cur ? ' btn-primary' : ''}`, 'data-key': `set-${target}`,
      'aria-pressed': String(target === cur), disabled: st.busy || target === cur || !!why, title: why || undefined,
      onclick: () => edit(e, e.widens),
    }, text);
  });
  return h('div', { class: 'col mx-edit', 'data-key': 'cell-editor' },
    h('p', { class: 'strong' }, `${from.label} and ${to.label}`),
    h('div', { class: 'actions' }, btns),
    whys.map((w) => h('p', { class: 'muted small', 'data-key': 'cell-why' }, w)));
}

function matrixBlock() {
  const info = st.matrix;
  const m = info.matrix;
  const seats = (store.overview && store.overview.seats) || [];
  const accts = accounts(seats, m);
  const owner = isOwner();
  const sameOff = whyNot({ widens: !m.same_account, owner, push: canPush(), offline: !!store.offline });
  return section('Accounts',
    h('div', { class: 'panel pad-s col' },
      accts.length < 2
        ? h('p', { class: 'muted' }, 'Peers between accounts need at least two accounts on this hub.')
        : [grid(accts, m), editor(accts, m)],
      h('ul', { class: 'mx-legend muted small', 'aria-label': 'Legend' }, LEGEND.map(([g, t]) => h('li', {}, h('span', { class: 'mx-glyph', 'aria-hidden': 'true' }, g), ` ${t}`))),
      h('p', { class: 'muted small' }, 'Replies inside a thread are always allowed.'),
      toggle({
        label: 'Same account', sub: 'Sessions of one account may message each other (what native /peers allows).', checked: m.same_account,
        disabled: st.busy || !!sameOff, key: 'same-account',
        onChange: (v) => edit({ same_account: v }, v),
      }),
      sameOff ? h('p', { class: 'muted small' }, sameOff) : null,
      details({ text: 'Details', key: 'peers-details' },
        h('p', { class: 'muted small', 'data-key': 'matrix-version' },
          `Version ${m.version} · signed by ${info.signer}${info.created_at ? ` · ${agoLabel(info.created_at, Date.now())}` : ''} · at most ${m.max_turns} turns per thread`))));
}

// ---- audit --------------------------------------------------------------------------------

// "Resume thread" (peers.md 6.3): owner session only; the hub resets its loop guard and tells
// both machines, which take it only with hub_may_widen on; one that refuses reports it, and
// its thread_resume_refused entry shows in the audit.
async function resumeThread(thread) {
  st.busy = true;
  again();
  try {
    toast(resumeText(await resumePeerThread(thread)));
    await refreshAudit();
  } catch (ex) {
    toast(ex.status === 409 ? 'That thread has closed; a new message starts a new one.' : ex.message || 'Could not resume the thread');
  }
  st.busy = false;
  again();
}

function auditBlock(label) {
  const list = st.audit;
  const rows = [];
  if (list) {
    const owner = isOwner(), offline = !!store.offline;
    const stops = resumableStops(list);
    for (const e of list) {
      const resume = stops.has(e.id) && owner
        ? h('button', {
          type: 'button', class: 'btn btn-sm', 'data-key': `resume-${e.thread}`, disabled: st.busy || offline,
          title: offline ? 'Offline: the thread cannot be resumed now.' : undefined,
          onclick: () => resumeThread(e.thread),
        }, 'Resume thread')
        : null;
      rows.push(h('li', { class: 'row audit-row', 'data-key': `audit-${e.id}` },
        h('span', { class: 'row-text' },
          h('span', {}, eventLabel(e.event, e.detail)),
          h('span', { class: 'muted small' }, [auditWho(e, label), e.event === 'matrix' && isSetup(e.detail) ? '' : detailText(e.detail)].filter(Boolean).join(' · '))),
        resume,
        h('time', { class: 'muted small', datetime: e.at, title: dateTime(e.at) }, agoLabel(e.at, Date.now()))));
    }
  }
  return section('Audit',
    h('p', { class: 'muted small' }, 'Who changed what, and when. Metadata only: no message text is ever stored or shown.'),
    !list ? h('p', { class: 'muted' }, 'Loading…') : !rows.length ? h('p', { class: 'muted' }, 'Nothing yet.') : h('ul', { class: 'panel audit-list' }, rows));
}

function messagesBlock(label) {
  const list = st.messages;
  if (!list) return null;
  return section('Messages',
    h('p', { class: 'muted small' }, 'Who messaged whom: when, size and outcome. Never the text.'),
    !list.length ? h('p', { class: 'muted' }, 'No messages yet.') : h('ul', { class: 'panel audit-list' }, list.map((x) =>
      h('li', { class: 'row audit-row', 'data-key': `msg-${x.id}` },
        h('span', { class: 'row-text' },
          h('span', {}, messageWho(x, label)),
          h('span', { class: 'muted small' }, [x.kind, sizeLabel(x.size), outcomeLabel(x.status)].filter(Boolean).join(' · '))),
        h('time', { class: 'muted small', datetime: x.created_at, title: dateTime(x.created_at) }, agoLabel(x.created_at, Date.now()))))));
}

export function render() {
  const owner = isOwner();
  const head = h('header', { class: 'page-head col' }, h('h1', {}, 'Peers'), moreTabs('peers', { owner }),
    h('p', { class: 'muted small', 'data-key': 'peers-intro' }, 'Lets Claude sessions on different accounts send each other messages. Off by default: nothing passes between two accounts until you allow it below.'));
  if (!st.matrix) {
    return { title: 'Peers · Switchyard', node: h('div', { class: 'page' }, head, h('p', { class: 'muted' }, st.error || 'Loading…')) };
  }
  const seats = (store.overview && store.overview.seats) || [];
  const names = new Map(accounts(seats, st.matrix.matrix).map((a) => [a.account, a.label]));
  const label = (id) => names.get(id) || String(id).slice(0, 8);
  const node = h('div', { class: 'page' }, head,
    killSwitch(),
    matrixBlock(),
    auditBlock(label),
    messagesBlock(label),
    !owner ? h('p', { class: 'muted small' }, 'Widening, and resuming after a pause or a loop-guard stop, need an owner sign-in: run sy hub owner-link on the hub.') : null);
  return { title: 'Peers · Switchyard', node };
}
