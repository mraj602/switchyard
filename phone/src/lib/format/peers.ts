// Text and matrix arithmetic of the Peers screen (docs/spec/peers.md 5, 10, 11; a port of
// webui/static/components/peers-fmt.js). The phone only narrows, so `narrowings` is the one
// edit helper it needs.
import type { MatrixEdit, PeerAuditEntry, PeerMatrix, PeerMessage, Seat } from '../../contract/api';

export type CellState = 'none' | 'out' | 'in' | 'both';

export interface PeerAccount {
  account: string;
  label: string;
}

/** The rows and columns: visible seats first, then accounts only the matrix names. */
export function peerAccounts(seats: Pick<Seat, 'account' | 'label' | 'email' | 'hidden'>[], matrix: Pick<PeerMatrix, 'accounts' | 'allow'>): PeerAccount[] {
  const out: PeerAccount[] = [];
  const seen = new Set<string>();
  const add = (account: string | undefined, label?: string | null) => {
    if (!account || seen.has(account)) return;
    seen.add(account);
    out.push({ account, label: label || matrix.accounts?.[account] || account.slice(0, 8) });
  };
  for (const s of seats) if (!s.hidden) add(s.account, s.label || s.email);
  for (const p of matrix.allow) {
    add(p[0]);
    add(p[1]);
  }
  return out;
}

export const hasPair = (allow: [string, string][], from: string, to: string): boolean => allow.some((p) => p[0] === from && p[1] === to);

/** What the cell (row `from`, column `to`) holds. */
export function cellState(allow: [string, string][], from: string, to: string): CellState {
  const out = hasPair(allow, from, to);
  const back = hasPair(allow, to, from);
  return out && back ? 'both' : out ? 'out' : back ? 'in' : 'none';
}

export const CELL_GLYPH: Record<CellState, string> = { none: '·', out: '→', in: '←', both: '⇄' };
export const CELL_WORD: Record<CellState, string> = { none: 'none', out: 'one way', in: 'one way, back', both: 'both ways' };

export function cellLabel(state: CellState, from: string, to: string): string {
  if (state === 'none') return `${from} and ${to}: no thread may start`;
  if (state === 'out') return `${from} may start a thread with ${to}`;
  if (state === 'in') return `${to} may start a thread with ${from}`;
  return `${from} and ${to} may start threads with each other`;
}

export interface Narrowing {
  target: CellState;
  edit: MatrixEdit;
}

/**
 * The ways a tapped cell can be narrowed: each target only removes pairs. Both ways can drop to
 * either one way or to none; one way drops to none; none has nothing to narrow. Turning the
 * other direction on is a widening and is left to the board (peers.md 5.4).
 */
export function narrowings(allow: [string, string][], from: string, to: string, version: number): Narrowing[] {
  const out = hasPair(allow, from, to);
  const back = hasPair(allow, to, from);
  const edit = (...deny: [string, string][]): MatrixEdit => ({ base_version: version, deny });
  const res: Narrowing[] = [];
  if (out && back) {
    res.push({ target: 'out', edit: edit([to, from]) });
    res.push({ target: 'in', edit: edit([from, to]) });
    res.push({ target: 'none', edit: edit([from, to], [to, from]) });
  } else if (out) {
    res.push({ target: 'none', edit: edit([from, to]) });
  } else if (back) {
    res.push({ target: 'none', edit: edit([to, from]) });
  }
  return res;
}

export function sizeLabel(n: number): string {
  if (!Number.isFinite(n) || n < 0) return '';
  if (n < 1000) return `${n} B`;
  if (n < 1e6) return `${(n / 1000).toFixed(n < 1e4 ? 1 : 0)} KB`;
  return `${(n / 1e6).toFixed(1)} MB`;
}

const EVENTS: Record<string, string> = {
  matrix: 'Matrix changed',
  pause: 'All peers paused',
  resume: 'Peers resumed',
  loop_stop: 'Loop guard stopped a thread',
  loop_guard: 'Loop guard stopped a thread',
  thread_resume: 'Thread resumed',
  thread_resume_refused: 'A machine kept the stop: hub_may_widen is off',
  refused: 'Message refused',
  rejected: 'Message rejected by a machine',
  off: 'Peers turned off on a machine',
  on: 'Peers turned on on a machine',
};

/**
 * The label of an audit event; detail names the machine of a thread_resume_refused (a machine
 * that refused the owner's "Resume thread" because hub_may_widen is off, peers.md 6.3).
 */
export function eventLabel(e: string, detail?: Record<string, unknown> | null): string {
  const m = detail && typeof detail === 'object' ? detail.machine : undefined;
  if (e === 'thread_resume_refused' && typeof m === 'string' && m) return `${m.slice(0, 60)} kept the stop: hub_may_widen is off`;
  return EVENTS[e] ?? String(e || '').replace(/_/g, ' ');
}

/** Plain metadata of an audit entry's detail, never anything else the hub might send. */
export function detailText(d: Record<string, unknown> | null | undefined): string {
  if (!d || typeof d !== 'object') return '';
  const parts: string[] = [];
  const plain = (k: string) => d[k] !== undefined && d[k] !== null && typeof d[k] !== 'object';
  if (plain('version')) parts.push(`v${String(d.version)}`);
  if (d.widens === true) parts.push('widened');
  if (plain('paused')) parts.push(d.paused ? 'paused' : 'not paused');
  if (plain('same_account')) parts.push(`same account ${d.same_account ? 'on' : 'off'}`);
  if (plain('max_turns')) parts.push(`${String(d.max_turns)} turns`);
  if (plain('pairs')) parts.push(`${String(d.pairs)} ${Number(d.pairs) === 1 ? 'pair' : 'pairs'}`);
  if (plain('size') && Number.isFinite(Number(d.size))) parts.push(sizeLabel(Number(d.size)));
  for (const k of ['status', 'outcome', 'reason']) if (plain(k)) parts.push(String(d[k]).slice(0, 60));
  return parts.join(' · ');
}

/** who → whom when the detail names both accounts, else the actor. */
export function auditWho(e: Pick<PeerAuditEntry, 'actor' | 'detail'>, label: (id: string) => string = (x) => x): string {
  const d = e.detail ?? {};
  if (typeof d.from === 'string' && typeof d.to === 'string') return `${label(d.from)} → ${label(d.to)}`;
  return e.actor || '';
}

export function messageWho(m: Pick<PeerMessage, 'from' | 'to'>, label: (id: string) => string = (x) => x): string {
  const a = m.from?.account;
  const b = m.to?.account;
  return `${a ? label(a) : '?'} → ${b ? label(b) : '?'}`;
}

const OUTCOME: Record<string, string> = {
  queued: 'queued', delivered: 'delivered', refused: 'refused', rejected: 'rejected',
  expired: 'expired', target_gone: 'target gone', failed: 'failed',
};
export const outcomeLabel = (s: string): string => OUTCOME[s] ?? String(s || '');
