// Words for the wy-link state (phone.md P6.7, push.md 14.4, 14.7). Pure: the screen passes the
// LinkState and the clock.
import type { LinkState } from '../../contract/link';

/** "2 h 14 min", "5 min", "40 s". */
export function spanWords(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s < 60) return `${s} s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m} min`;
  const h = Math.floor(m / 60);
  if (h < 48) return m % 60 ? `${h} h ${m % 60} min` : `${h} h`;
  return `${Math.floor(h / 24)} d`;
}

const ERROR_WORDS: Record<string, string> = {
  UnknownHostException: 'hub unreachable',
  ConnectException: 'hub unreachable',
  NoRouteToHostException: 'hub unreachable',
  SocketTimeoutException: 'hub not answering',
  InterruptedIOException: 'hub not answering',
  SSLHandshakeException: 'TLS certificate refused',
  SSLPeerUnverifiedException: 'TLS certificate refused',
  SSLException: 'TLS error',
  EOFException: 'connection dropped',
  SocketException: 'connection dropped',
  IOException: 'network error',
  network: 'network error',
  no_keys: 'this phone has no link keys; pair again',
  not_configured: 'not set up; pair again',
  hub_key_changed: 'hub key changed',
  background_start_blocked: 'Android blocked a background start; open the app or allow unrestricted battery',
  bad_challenge: "the hub's login was not valid",
};

/** The last error in words: a close code and its word, an HTTP status, or a network class. */
export function linkErrorWords(err: string | null | undefined): string | null {
  if (!err) return null;
  const known = ERROR_WORDS[err];
  if (known) return known;
  const http = /^http_(\d{3})$/.exec(err);
  if (http) return `the hub answered ${http[1]}`;
  const close = /^(\d{4}) ([a-z_]+)$/.exec(err);
  if (close) return `closed by the hub (${close[1]} ${close[2]!.replace(/_/g, ' ')})`;
  return err.replace(/Exception$/, '').replace(/_/g, ' ');
}

/** The Connection row (P6.7). */
export function linkStatusWords(s: LinkState, now: number): string {
  switch (s.status) {
    case 'connected': {
      const age = s.connectedSince ? ` · ${spanWords(now - s.connectedSince)}` : '';
      const via = s.transport === 'poll' ? ' · long-poll' : s.transport === 'ws' ? ' · WebSocket' : '';
      return `Connected${age}${via}`;
    }
    case 'connecting':
      return 'Connecting…';
    case 'backoff':
      return s.nextRetryAt ? `Reconnecting · next try in ${spanWords(Math.max(0, s.nextRetryAt - now))}` : 'Reconnecting…';
    case 'revoked':
      return 'Removed from hub';
    case 'upgrade':
      return 'The hub needs a newer app';
    case 'error':
      return `Not connected: ${linkErrorWords(s.lastError) ?? 'stopped'}`;
    default:
      return s.enabled ? 'Starting…' : 'Off: checks every 15 minutes';
  }
}

/** The Encryption row (push.md 14.7). */
export function verifiedWords(s: Pick<LinkState, 'verified' | 'checkCode'>): string {
  if (s.verified === 'qr') return 'Verified by QR';
  if (s.verified === 'changed') return 'Hub key changed: pair this phone again';
  return s.checkCode ? `Not verified: compare check code ${s.checkCode} with the board` : 'Not verified';
}

/** The Battery row. */
export function batteryWords(exempt: boolean): string {
  return exempt ? 'Battery optimisation: off (good)' : 'Battery optimisation: on: notifications may be late';
}

/** The ping interval choices (P6.7): 3, 5 and 10 minutes. */
export const PING_CHOICES_S = [180, 300, 600] as const;
