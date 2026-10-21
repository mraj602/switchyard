// Which account is signed in where (phone.md P17.7), the same way the desktop app tells it:
// a tab is signed in when its page is a claude.ai page that is not the login page (the
// desktop's isReadableUrl), and it is signed in *as* the account whose uuid claude.ai answers
// inside the tab (the desktop's reader, desktop.md C5). Matched to seats by account uuid, never
// by email. Claude Code's side comes from the hub (Seat.places, Seat.signed_out). Pure.
import type { Seat } from '../../contract/api';
import { clock, seatName, seatUse } from '../format';
import type { TabEventAt } from './index';

/** Phone-12 and Settings → Claude tabs: what the account check does, and does not. */
export const TAB_PRIVACY_NOTE =
  'Switchyard asks claude.ai only "which account is this?" inside the tab. It never reads your chats or sends the page anywhere.';

/** claude.ai paths where a tab is not signed in (desktop NOT_READABLE_PATHS). */
export const SIGNED_OUT_PATHS = ['/login', '/logout'] as const;
/** Where "Sign out here and sign in as …" lands. */
export const TAB_LOGIN = 'https://claude.ai/login';
/** The desktop's UUID_PATTERN: what an account id looks like. */
export const ACCOUNT_RE = /^[0-9a-f-]{36}$/;
/** The sign-in hops outside claude.ai (desktop.md C2): on one, the tab is not signed in yet. */
const SIGN_IN_HOSTS = ['anthropic.com', 'claude.com'];

function parse(u: unknown): URL | null {
  if (typeof u !== 'string' || u.length > 8192) return null;
  try {
    const x = new URL(u);
    return x.protocol === 'https:' && !x.username && !x.password && (x.port === '' || x.port === '443') ? x : null;
  } catch {
    return null;
  }
}

const hostOf = (x: URL) => x.hostname.toLowerCase().replace(/\.$/, '');
const under = (h: string, d: string) => h === d || h.endsWith(`.${d}`);

/** A signed-in claude.ai page: claude.ai or a subdomain, and not /login or /logout. */
export function isReadableUrl(u: unknown): boolean {
  const x = parse(u);
  if (!x || !under(hostOf(x), 'claude.ai')) return false;
  const p = x.pathname.replace(/\/+$/, '');
  return !SIGNED_OUT_PATHS.some((bad) => p === bad || p.startsWith(`${bad}/`));
}

/** A page where the tab is not signed in: claude.ai's login or logout, or a sign-in hop. */
export function isSignInUrl(u: unknown): boolean {
  const x = parse(u);
  if (!x) return false;
  const h = hostOf(x);
  if (under(h, 'claude.ai')) return !isReadableUrl(u);
  return SIGN_IN_HOSTS.some((d) => under(h, d));
}

/** What the phone remembers of a seat's tab from the last time it was open. */
export type TabMemo = { signed_in: boolean; account: string | null; at: number };
export type TabMemos = Record<string, TabMemo>;

/**
 * The memo after one tab event. An account answer wins (a uuid: signed in as it; 401: signed
 * out; anything else changes nothing). A finished page or an in-page move to a signed-in
 * claude.ai page means signed in (keeping the account only if it was already signed in); the
 * login page or a sign-in hop means signed out. Other events change nothing.
 */
export function nextMemo(prev: TabMemo | undefined, e: Pick<TabEventAt, 'state' | 'url' | 'status' | 'height' | 'account'>, now: number): TabMemo | undefined {
  if (e.state === 'account') {
    if (typeof e.account === 'string' && ACCOUNT_RE.test(e.account)) return { signed_in: true, account: e.account, at: now };
    if (e.status === 401) return { signed_in: false, account: null, at: now };
    return prev;
  }
  if (e.state !== 'loaded' && e.state !== 'url') return prev;
  if (e.state === 'loaded' && !e.height) return prev; // a blank or refused page says nothing
  if (isReadableUrl(e.url)) return { signed_in: true, account: prev?.signed_in ? prev.account : null, at: now };
  if (isSignInUrl(e.url)) return { signed_in: false, account: null, at: now };
  return prev;
}

/** claude.ai pages that belong to sign-in but also serve a signed-in tab (OAuth consent): the URL alone decides nothing. */
const WEAK_SIGN_IN_PATHS = ['/magic-link', '/oauth', '/sso', '/signup'];

/** A page where the tab is certainly not signed in: isSignInUrl, or Google's sign-in. */
export function isSignInPage(u: unknown): boolean {
  if (isSignInUrl(u)) return true;
  const x = parse(u);
  return !!x && under(hostOf(x), 'accounts.google.com');
}

function isWeakSignInPage(u: unknown): boolean {
  const x = parse(u);
  if (!x || !under(hostOf(x), 'claude.ai')) return false;
  const p = x.pathname.replace(/\/+$/, '');
  return WEAK_SIGN_IN_PATHS.some((w) => p === w || p.startsWith(`${w}/`));
}

/** What one open tab knows of its sign-in right now: the P17.7 rule over this visit's events. */
export type HintState = { signedIn: boolean | null; url: string | null };
export const HINT_START: HintState = { signedIn: null, url: null };

/** Fold one tab event into the hint state (same rule as nextMemo, plus the page the tab is on). */
export function nextHint(prev: HintState, e: Pick<TabEventAt, 'state' | 'url' | 'status' | 'height' | 'account'>): HintState {
  const nav0 = (e.state === 'loading' || e.state === 'loaded' || e.state === 'url') && typeof e.url === 'string';
  if (nav0 && isWeakSignInPage(e.url)) return { signedIn: prev.signedIn, url: e.url as string };
  const memo = nextMemo(prev.signedIn === null ? undefined : { signed_in: prev.signedIn, account: null, at: 0 }, e, 0);
  const signedIn = memo ? memo.signed_in : prev.signedIn;
  const nav = (e.state === 'loading' || e.state === 'loaded' || e.state === 'url') && typeof e.url === 'string';
  return { signedIn, url: nav ? (e.url as string) : prev.url };
}

/**
 * P17.3: the "email and code" hint shows only while the tab is on a sign-in page or is known to
 * be signed out, never once signed in on a normal claude.ai page, and never after the owner
 * dismissed it for this account. Unknown (nothing loaded yet) shows nothing.
 */
export function showSignInHint(h: HintState, dismissed: boolean): boolean {
  if (dismissed) return false;
  if (isSignInPage(h.url)) return true;
  if (isWeakSignInPage(h.url)) return h.signedIn !== true;
  return h.signedIn === false;
}

/** Stored memos, checked: bad seats and entries are dropped. */
export function readMemos(x: unknown): TabMemos {
  const out: TabMemos = {};
  if (!x || typeof x !== 'object' || Array.isArray(x)) return out;
  for (const [seat, m] of Object.entries(x as Record<string, unknown>)) {
    if (!/^seat_[0-9A-HJKMNP-TV-Z]{26}$/.test(seat) || !m || typeof m !== 'object') continue;
    const { signed_in, account, at } = m as Record<string, unknown>;
    if (typeof signed_in !== 'boolean' || typeof at !== 'number' || !Number.isFinite(at)) continue;
    const acct = typeof account === 'string' && ACCOUNT_RE.test(account) && signed_in ? account : null;
    out[seat] = { signed_in, account: acct, at };
  }
  return out;
}

/** The phone tab of one seat (Phone-11's second line). */
export type PhoneTab =
  | { kind: 'unsupported' }
  | { kind: 'none' } // no tab profile on this phone
  | { kind: 'unchecked' } // a profile, but no visit remembered
  | { kind: 'signed_out'; at: number }
  | { kind: 'signed_in'; at: number } // the account is not known yet
  | { kind: 'match'; at: number }
  | { kind: 'other'; at: number; seat: Seat } // signed in as another seat's account
  | { kind: 'stranger'; at: number }; // signed in as an account no seat has

/**
 * The tab's state for `seat`: from its profile (does this phone have a tab for it) and what the
 * tab showed when it was last open, matched by account uuid against every seat the hub lists.
 */
export function phoneTab(seat: Seat, seats: Seat[], tabs: { supported: boolean; profiles: string[] | null; memo?: TabMemo }): PhoneTab {
  if (!tabs.supported) return { kind: 'unsupported' };
  if (tabs.profiles && !tabs.profiles.includes(seat.id)) return { kind: 'none' };
  const m = tabs.memo;
  if (!m) return { kind: 'unchecked' }; // profiles still loading, or a tab never seen since this was built
  if (!m.signed_in) return { kind: 'signed_out', at: m.at };
  if (!m.account) return { kind: 'signed_in', at: m.at };
  if (m.account === seat.account) return { kind: 'match', at: m.at };
  const other = seats.find((s) => s.id !== seat.id && s.account === m.account);
  return other ? { kind: 'other', at: m.at, seat: other } : { kind: 'stranger', at: m.at };
}

/** What the card offers for the tab: Open claude.ai, Sign in on this phone, Fix this tab. */
export type TabAction = 'open' | 'sign_in' | 'fix' | null;

export function tabAction(v: PhoneTab): TabAction {
  switch (v.kind) {
    case 'unsupported':
      return null;
    case 'none':
    case 'signed_out':
      return 'sign_in';
    case 'other':
    case 'stranger':
      return 'fix';
    default:
      return 'open';
  }
}

/** Text in parts: `b` parts are bold (names). */
export type Part = { t: string; b?: boolean };
export type Line = { tone: 'ok' | 'muted' | 'warn'; parts: Part[] };

/** Phone-11 "Phone tab: …"; null where there are no tabs. */
export function phoneTabLine(v: PhoneTab): Line | null {
  switch (v.kind) {
    case 'unsupported':
      return null;
    case 'none':
      return { tone: 'muted', parts: [{ t: 'Phone tab: not signed in' }] };
    case 'unchecked':
      return { tone: 'muted', parts: [{ t: 'Phone tab: not checked yet. Open it to check.' }] };
    case 'signed_out':
      return { tone: 'muted', parts: [{ t: 'Phone tab: not signed in' }] };
    case 'signed_in':
      return { tone: 'ok', parts: [{ t: 'Phone tab: signed in' }] };
    case 'match':
      return { tone: 'ok', parts: [{ t: 'Phone tab: signed in, matches this account' }] };
    case 'other':
      return { tone: 'warn', parts: [{ t: 'Phone tab: signed in as ' }, { t: seatName(v.seat), b: true }, { t: ', not this account' }] };
    case 'stranger':
      return { tone: 'warn', parts: [{ t: 'Phone tab: signed in as an account no seat has' }] };
  }
}

const names = (xs: string[]): Part[] => xs.flatMap((n, i) => (i ? [{ t: ', ' }, { t: n, b: true }] : [{ t: n, b: true }]));
const uniq = (xs: (string | null | undefined)[]) => [...new Set(xs.filter((x): x is string => !!x))];

/**
 * Phone-11 "Claude Code: signed in on mraj602, worker · signed out on X", from the hub: the
 * machines with a folder logged in to the seat (Seat.places with a slot), and those with a
 * folder that logged out of it and is not also signed in there (Seat.signed_out).
 */
function codeMachines(seat: Seat): { inOn: string[]; outOn: string[] } {
  const inOn = uniq((seat.places ?? []).filter((p) => p.slot).map((p) => p.machine_name || p.machine));
  const outOn = uniq((seat.signed_out ?? []).map((p) => p.machine_name || p.machine)).filter((m) => !inOn.includes(m));
  return { inOn, outOn };
}

export function codeLine(seat: Seat): Line {
  const { inOn, outOn } = codeMachines(seat);
  if (!inOn.length && !outOn.length) return { tone: 'muted', parts: [{ t: 'Claude Code: not signed in on any machine' }] };
  const parts: Part[] = [{ t: 'Claude Code: ' }];
  if (inOn.length) parts.push({ t: 'signed in on ' }, ...names(inOn));
  if (outOn.length) parts.push({ t: inOn.length ? ' · signed out on ' : 'signed out on ' }, ...names(outOn));
  return { tone: inOn.length ? 'ok' : 'muted', parts };
}

/** Phone-12: what "Fix this tab" offers. `use` only when the other seat has no tab on this phone. */
export function fixChoices(v: PhoneTab, profiles: string[] | null): { signOut: true; use: Seat | null } | null {
  if (v.kind === 'stranger') return { signOut: true, use: null };
  if (v.kind !== 'other') return null;
  return { signOut: true, use: profiles && !profiles.includes(v.seat.id) ? v.seat : null };
}

/**
 * The one status line of an Accounts row, only when something needs attention, in this order:
 * the phone tab signed in as another account, a window near its limit (a runaway 5-hour pace, or
 * either window at 90% or more), Claude Code signed out on a machine. `more` counts the others.
 */
export function attention(seat: Seat, tab: PhoneTab, now: number): { line: Line; more: number } | null {
  const found: Part[][] = [];
  if (tab.kind === 'other') found.push([{ t: 'Phone tab is signed in as ' }, { t: seatName(tab.seat), b: true }]);
  else if (tab.kind === 'stranger') found.push([{ t: 'Phone tab is signed in as an account no seat has' }]);
  const u = seatUse(seat, now);
  if (u) {
    const full = u.fiveW?.pace?.full_at;
    if (u.five >= 90 || (u.fiveW?.pace?.band === 'runaway' && u.five > 0)) {
      found.push([{ t: full ? `Near the 5-hour limit: full by ${clock(full, now)}` : 'Near the 5-hour limit' }]);
    }
    if (u.seven >= 90) {
      found.push([{ t: `7-day limit nearly used${u.sevenW?.resets_at ? `, resets ${clock(u.sevenW.resets_at, now)}` : ''}` }]);
    }
  }
  const { outOn } = codeMachines(seat);
  if (outOn.length) found.push([{ t: 'Claude Code signed out on ' }, ...names(outOn)]);
  const first = found[0];
  return first ? { line: { tone: 'warn', parts: first }, more: found.length - 1 } : null;
}
