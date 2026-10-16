// claude.ai account tabs (phone.md P17): where "Open in Claude" goes (P17.2), and whether a tab
// loaded or claude.ai refused it (P17.4). Pure: the screens pass the module's answers in.
import type { Seat } from '../../contract/api';
import { httpsUrl, seatName } from '../format';

/** A hub seat id (identity.md 1). Mirrors TabPolicy.kt. */
export const SEAT_RE = /^seat_[0-9A-HJKMNP-TV-Z]{26}$/;
export const PROFILE_PREFIX = 'sy-tab-';
export const TAB_HOME = 'https://claude.ai/';
/** A refusal answer may still turn into a page (Cloudflare's challenge starts with a 403). */
export const REFUSAL_GRACE_MS = 10_000;
export const LOAD_TIMEOUT_MS = 20_000;
/** P17.3: said on every tab and in Settings. */
export const SIGN_IN_NOTE = 'Sign in with your email and the code claude.ai sends. Google sign-in does not work inside apps.';

export const validSeat = (seat: unknown): seat is string => typeof seat === 'string' && SEAT_RE.test(seat);

/** The tab's WebView profile: by seat id, never by email or label. */
export const profileFor = (seat: unknown): string | null => (validSeat(seat) ? PROFILE_PREFIX + seat : null);

/** An https link on claude.ai (or a subdomain), default port, no user info. */
export function claudeUrl(u: unknown): string | null {
  const safe = httpsUrl(u);
  if (!safe) return null;
  const x = new URL(safe);
  const host = x.hostname.toLowerCase().replace(/\.$/, '');
  if (x.username || x.password || x.port) return null;
  return host === 'claude.ai' || host.endsWith('.claude.ai') ? safe : null;
}

export type OpenTarget =
  | { kind: 'tab'; seat: string; url: string }
  | { kind: 'browser'; url: string; reason: 'unsupported' | 'unattributed' | 'not_claude' };

/** P17.2: the seat's tab when it can, else the browser; null when there is nothing to open. */
export function openTarget(
  session: { seat?: string | null; remote_url?: string | null },
  tabs: { supported: boolean },
): OpenTarget | null {
  const url = httpsUrl(session.remote_url);
  if (!url) return null;
  if (!claudeUrl(url)) return { kind: 'browser', url, reason: 'not_claude' };
  if (!tabs.supported) return { kind: 'browser', url, reason: 'unsupported' };
  if (!validSeat(session.seat)) return { kind: 'browser', url, reason: 'unattributed' };
  return { kind: 'tab', seat: session.seat, url };
}

/** The module's tab event (modules/sy-tabs TabEvent) plus when the screen got it. */
export type TabEventAt = {
  state: 'loading' | 'loaded' | 'http' | 'error' | 'crashed' | 'download' | 'external' | 'url' | 'account';
  url?: string;
  status?: number;
  height?: number;
  reason?: string;
  account?: string;
  at: number;
};

export type TabVerdict =
  | { kind: 'ok' }
  | { kind: 'waiting' }
  | { kind: 'refused'; status: number }
  | { kind: 'blank' }
  | { kind: 'failed'; reason: 'network' | 'crashed' | 'unsupported' }
  | { kind: 'timeout' };

/**
 * P17.4, over the latest main-frame navigation: ok, still waiting, or why it failed. A main-frame
 * HTTP error counts only once REFUSAL_GRACE_MS pass without a newer navigation; no finished page
 * within LOAD_TIMEOUT_MS is a timeout.
 */
export function assessTab(events: TabEventAt[], openedAt: number, now: number): TabVerdict {
  let start = openedAt;
  let http: TabEventAt | null = null;
  let end: TabVerdict | null = null;
  for (const e of events) {
    switch (e.state) {
      case 'loading':
        start = e.at;
        http = null;
        end = null;
        break;
      case 'http':
        if ((e.status ?? 0) >= 400) http = e;
        break;
      case 'loaded':
        end = !e.height || !e.url || e.url === 'about:blank' ? { kind: 'blank' } : { kind: 'ok' };
        break;
      case 'error':
        end = { kind: 'failed', reason: e.reason === 'unsupported' || e.reason === 'profile' ? 'unsupported' : 'network' };
        break;
      case 'crashed':
        end = { kind: 'failed', reason: 'crashed' };
        break;
      default:
        break; // download, external: the page stays as it was
    }
  }
  if (end && end.kind === 'failed') return end;
  if (http) return now - http.at >= REFUSAL_GRACE_MS ? { kind: 'refused', status: http.status ?? 0 } : { kind: 'waiting' };
  if (end) return end;
  return now - start >= LOAD_TIMEOUT_MS ? { kind: 'timeout' } : { kind: 'waiting' };
}

/** The fallback strip's words (P17.4); null when the tab is fine or still loading. */
export function verdictText(v: TabVerdict): string | null {
  switch (v.kind) {
    case 'refused':
      return `claude.ai answered ${v.status} in this tab. It may not allow sign-in inside apps.`;
    case 'blank':
      return 'The page stayed blank.';
    case 'timeout':
      return 'The page did not finish loading in 20 seconds.';
    case 'failed':
      return v.reason === 'crashed'
        ? 'The tab stopped working.'
        : v.reason === 'unsupported'
          ? "This phone's WebView cannot keep separate claude.ai sign-ins."
          : 'Could not reach claude.ai.';
    default:
      return null;
  }
}

/** Settings → Claude tabs rows: profiles in seat order, unknown seats last. */
export function tabRows(profiles: string[], seats: Pick<Seat, 'id' | 'label' | 'email'>[]) {
  const order = new Map(seats.map((s, i) => [s.id, i] as const));
  return profiles
    .filter(validSeat)
    .map((id) => {
      const s = seats.find((x) => x.id === id);
      return { seat: id, name: s ? seatName(s) : 'Removed account', known: !!s };
    })
    .sort((a, b) => (order.get(a.seat) ?? 1e9) - (order.get(b.seat) ?? 1e9));
}
export * from './status';
