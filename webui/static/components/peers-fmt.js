// The sentences and the matrix arithmetic of Settings → Peers (docs/spec/peers.md 5, 10, 11).
// Pure, so node can test it (peers-fmt.test.cjs): no imports.

// The accounts that make the rows and columns: the board's visible seats, then any account the
// matrix names that no seat does (a pair or a label from another time).
export function peerAccounts(seats, matrix) {
  const out = [];
  const seen = new Set();
  const add = (account, label) => {
    if (!account || seen.has(account)) return;
    seen.add(account);
    out.push({ account, label: label || (matrix && matrix.accounts && matrix.accounts[account]) || account.slice(0, 8) });
  };
  for (const s of seats || []) if (!s.hidden) add(s.account, s.label || s.email);
  for (const p of (matrix && matrix.allow) || []) { add(p[0]); add(p[1]); }
  return out;
}

export const hasPair = (allow, from, to) => (allow || []).some((p) => p[0] === from && p[1] === to);

// What the cell (row from, column to) holds: none, out (from may start a thread with to),
// in (only to may start one with from) or both.
export function cellState(allow, from, to) {
  const out = hasPair(allow, from, to), back = hasPair(allow, to, from);
  return out && back ? 'both' : out ? 'out' : back ? 'in' : 'none';
}

export const CELL_GLYPH = { none: '·', out: '→', in: '←', both: '⇄' };
export const CELL_WORD = { none: 'none', out: 'one way', in: 'one way, back', both: 'both ways' };

export function cellLabel(state, from, to) {
  if (state === 'none') return `${from} and ${to}: no thread may start`;
  if (state === 'out') return `${from} may start a thread with ${to}`;
  if (state === 'in') return `${to} may start a thread with ${from}`;
  return `${from} and ${to} may start threads with each other`;
}

// The edit that turns the cell into `target`: allow the pairs it adds, deny the pairs it
// removes. widens is true when it adds a pair (keys.md 5.2): the hub signs that only for an
// owner session.
export function cellEdit(allow, from, to, target) {
  const add = [], del = [];
  const pair = (a, b, wanted) => {
    const has = hasPair(allow, a, b);
    if (wanted && !has) add.push([a, b]);
    if (!wanted && has) del.push([a, b]);
  };
  pair(from, to, target === 'out' || target === 'both');
  pair(to, from, target === 'in' || target === 'both');
  return { allow: add, deny: del, widens: add.length > 0 };
}

// The body of PUT /v1/peers/matrix for an edit; base_version makes a stale edit a 409.
export function editBody(matrix, edit) {
  const b = { base_version: matrix.version };
  if (edit.allow && edit.allow.length) b.allow = edit.allow;
  if (edit.deny && edit.deny.length) b.deny = edit.deny;
  for (const k of ['paused', 'same_account', 'max_turns']) if (edit[k] !== undefined) b[k] = edit[k];
  return { edit: b };
}

// Why a change is not available to this caller, or '' when it is. Widening needs an owner
// session; narrowing needs one too, or a device with the push scope.
export function whyNot({ widens, owner, push, offline }) {
  if (offline) return 'Offline: the matrix cannot change now.';
  if (widens && !owner) return 'Widening needs an owner sign-in: run sy hub owner-link on the hub.';
  if (!owner && !push) return 'This sign-in can only read. Sign in as owner, or use a paired phone, to narrow.';
  return '';
}

// "12 B", "3.4 KB".
export function sizeLabel(n) {
  if (!Number.isFinite(n) || n < 0) return '';
  if (n < 1000) return `${n} B`;
  if (n < 1e6) return `${(n / 1000).toFixed(n < 1e4 ? 1 : 0)} KB`;
  return `${(n / 1e6).toFixed(1)} MB`;
}

const EVENTS = {
  matrix: 'Matrix changed',
  pause: 'All peers paused',
  resume: 'Peers resumed',
  loop_stop: 'Loop guard stopped a thread',
  loop_guard: 'Loop guard stopped a thread',
  thread_resume: 'Thread resumed',
  refused: 'Message refused',
  rejected: 'Message rejected by a machine',
  off: 'Peers turned off on a machine',
  on: 'Peers turned on on a machine',
};

export const eventLabel = (e) => EVENTS[e] || String(e || '').replace(/_/g, ' ');

const THREAD_ID = /^t_[0-9a-f]{16}$/;

// The audit ids (newest first list) of the loop-guard stops that "Resume thread" may lift
// (peers.md 6.3): for each thread, its newest `loop_guard` entry, unless a `thread_resume` of
// that thread came after it. Threads the list does not show resumed may have closed since;
// the hub then answers 409.
export function resumableStops(entries) {
  const out = new Set();
  const seen = new Set();
  for (const e of entries || []) {
    const t = e && e.thread;
    if (typeof t !== 'string' || !THREAD_ID.test(t) || seen.has(t)) continue;
    if (e.event === 'thread_resume') seen.add(t);
    else if (e.event === 'loop_guard') { seen.add(t); out.add(e.id); }
  }
  return out;
}

// What the board says after a resume (POST /v1/peers/threads/{id}/resume's answer).
export function resumeText(r) {
  const n = r && Number.isFinite(r.machines) ? r.machines : 0;
  const told = n === 1 ? '1 machine was told' : `${n} machines were told`;
  return `Thread resumed: ${told}. A machine with hub_may_widen off keeps its stop until someone types in either session.`;
}

// The audit is metadata only. Only these detail keys are shown, in this order, and only when
// they are plain values; anything else the hub sends is left out.
export function detailText(d) {
  if (!d || typeof d !== 'object') return '';
  const parts = [];
  const plain = (k) => d[k] !== undefined && d[k] !== null && typeof d[k] !== 'object';
  if (plain('version')) parts.push(`v${d.version}`);
  if (d.widens === true) parts.push('widened');
  if (plain('paused')) parts.push(d.paused ? 'paused' : 'not paused');
  if (plain('same_account')) parts.push(`same account ${d.same_account ? 'on' : 'off'}`);
  if (plain('max_turns')) parts.push(`${d.max_turns} turns`);
  if (plain('pairs')) parts.push(`${d.pairs} ${Number(d.pairs) === 1 ? 'pair' : 'pairs'}`);
  if (plain('size') && Number.isFinite(Number(d.size))) parts.push(sizeLabel(Number(d.size)));
  for (const k of ['status', 'outcome', 'reason']) if (plain(k)) parts.push(String(d[k]).slice(0, 60));
  return parts.join(' · ');
}

// who → whom of an audit entry whose detail names both accounts, else the actor alone.
export function auditWho(e, label = (x) => x) {
  const d = e.detail || {};
  if (typeof d.from === 'string' && typeof d.to === 'string') return `${label(d.from)} → ${label(d.to)}`;
  return e.actor || '';
}

// One message of GET /v1/peers/messages: who → whom (by account).
export function messageWho(m, label = (x) => x) {
  const a = (m.from && m.from.account) || '', b = (m.to && m.to.account) || '';
  return `${a ? label(a) : '?'} → ${b ? label(b) : '?'}`;
}

const OUTCOME = {
  queued: 'queued', delivered: 'delivered', refused: 'refused', rejected: 'rejected',
  expired: 'expired', target_gone: 'target gone', failed: 'failed',
};
export const outcomeLabel = (s) => OUTCOME[s] || String(s || '');
