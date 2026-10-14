// Pairing input and claim (phone.md P2). Owner: WP-P.
import { DEVICE_KIND, HEALTH_TIMEOUT_MS, REQUEST_TIMEOUT_MS } from '../../contract/constants';
import type { PairClaim, PairClaimReply } from '../../contract/api';
import type { AppInfo, Clock, FetchLike, ResponseLike } from '../../contract/platform';
import { SCHEME } from '../../contract/routes';
import { normalizeOrigin } from '../hub';
import type { StoredHub } from '../secure';

export interface PairInput {
  origin: string; // https://host[:port], nothing else kept
  secret?: string; // 22 base64url characters
  code?: string; // 6 digits
}

export type PairInputError =
  | 'empty'
  | 'not_https'
  | 'bad_origin'
  | 'no_secret_or_code'
  | 'bad_secret'
  | 'bad_code'
  | 'too_long';

export type ParsedPair = { ok: true; input: PairInput } | { ok: false; reason: PairInputError };

export interface ParseOptions {
  /** __DEV__ builds also accept http:// for loopback and RFC 1918 addresses. */
  dev: boolean;
  /** Typed address when the code was typed separately. */
  address?: string;
}

const MAX_INPUT = 2048;
const MAX_HOST = 253;
const SECRET = /^[A-Za-z0-9_-]{22}$/;
const HOSTNAME = /^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)*$/;
const DANGEROUS_SCHEME = /^(javascript|data|vbscript|file|blob|about|intent):/i;

const fail = (reason: PairInputError): ParsedPair => ({ ok: false, reason });

function isPrivateHost(host: string): boolean {
  if (host === 'localhost' || host === '[::1]') return true;
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(host);
  if (!m) return false;
  const n = [Number(m[1]), Number(m[2]), Number(m[3]), Number(m[4])];
  if (n.some((x) => x > 255)) return false;
  const [a, b] = [n[0] ?? 0, n[1] ?? 0];
  return a === 127 || a === 10 || (a === 172 && b >= 16 && b <= 31) || (a === 192 && b === 168);
}

type Origin = { ok: true; origin: string } | { ok: false; reason: PairInputError };

/** An https origin from "https://host[:port][/...]" or a bare "host[:port]". Path, query, fragment dropped. */
function parseOrigin(raw: string, dev: boolean): Origin {
  const text = raw.trim();
  if (!text) return { ok: false, reason: 'empty' };
  if (text.includes('\\')) return { ok: false, reason: 'bad_origin' };
  if (DANGEROUS_SCHEME.test(text)) return { ok: false, reason: 'not_https' };
  let scheme = 'https';
  let rest = text;
  const m = /^([A-Za-z][A-Za-z0-9+.-]*):\/\/([\s\S]*)$/.exec(text);
  if (m) {
    scheme = (m[1] ?? '').toLowerCase();
    rest = m[2] ?? '';
  }
  if (scheme !== 'https' && scheme !== 'http') return { ok: false, reason: 'not_https' };
  const authority = rest.split(/[/?#]/, 1)[0] ?? '';
  if (authority.includes('@')) return { ok: false, reason: 'bad_origin' };
  const pm = /^(\[[0-9a-fA-F:]+\]|[^:]+)(?::(\d{1,5}))?$/.exec(authority);
  if (!pm) return { ok: false, reason: 'bad_origin' };
  const host = (pm[1] ?? '').toLowerCase();
  const port = pm[2];
  if (host.length > MAX_HOST) return { ok: false, reason: 'too_long' };
  if (port !== undefined && (Number(port) < 1 || Number(port) > 65535)) return { ok: false, reason: 'bad_origin' };
  const bracketed = host.startsWith('[');
  if (!bracketed && !HOSTNAME.test(host)) return { ok: false, reason: 'bad_origin' };
  if (bracketed && !dev) return { ok: false, reason: 'bad_origin' };
  if (scheme === 'http' && !(dev && isPrivateHost(host))) return { ok: false, reason: 'not_https' };
  // The default port is dropped ("https://hub:443" is "https://hub"), so every later
  // same-origin check compares like with like.
  return { ok: true, origin: normalizeOrigin(`${scheme}://${host}${port !== undefined ? `:${Number(port)}` : ''}`) };
}

function decode(s: string): string | null {
  try {
    return decodeURIComponent(s.replace(/\+/g, ' '));
  } catch {
    return null;
  }
}

function params(query: string): Map<string, string> | null {
  const out = new Map<string, string>();
  for (const part of query.split('&')) {
    if (!part) continue;
    const i = part.indexOf('=');
    const k = decode(i < 0 ? part : part.slice(0, i));
    const v = decode(i < 0 ? '' : part.slice(i + 1));
    if (k === null || v === null) return null;
    if (!out.has(k)) out.set(k, v);
  }
  return out;
}

/** "482913", "482-913" or "482 913" to six digits; null for anything else. */
function normaliseCode(raw: string): string | null {
  const t = raw.trim();
  return /^\d{3}[\s-]?\d{3}$/.test(t) ? t.replace(/[\s-]/g, '') : null;
}

function finish(origin: string, secret: string | undefined, code: string | undefined): ParsedPair {
  if (secret === undefined && code === undefined) return fail('no_secret_or_code');
  const input: PairInput = { origin };
  if (secret !== undefined) {
    if (!SECRET.test(secret)) return fail('bad_secret');
    input.secret = secret;
  }
  if (code !== undefined) {
    const c = normaliseCode(code);
    if (c === null) return fail('bad_code');
    input.code = c;
  }
  return { ok: true, input };
}

/** QR URL, switchyard://pair link, or typed address plus code (P2). */
export function parsePairInput(text: string, opts: ParseOptions): ParsedPair {
  const t = text.trim();
  const address = opts.address?.trim() ?? '';
  if (!t && !address) return fail('empty');
  if (t.length > MAX_INPUT || address.length > MAX_INPUT) return fail('too_long');

  // Typed address in its own field: the text is the code.
  if (address) {
    const o = parseOrigin(address, opts.dev);
    if (!o.ok) return o;
    if (!t) return fail('no_secret_or_code');
    return finish(o.origin, undefined, t);
  }

  // switchyard://pair?u=<origin>&s=…&c=…
  const sm = new RegExp(`^${SCHEME}://pair/?(?:\\?([^#]*))?(?:#.*)?$`, 'i').exec(t);
  if (sm) {
    const q = params(sm[1] ?? '');
    if (!q) return fail('bad_origin');
    const u = q.get('u');
    if (!u) return fail('bad_origin');
    const o = parseOrigin(u, opts.dev);
    if (!o.ok) return o;
    return finish(o.origin, q.get('s'), q.get('c'));
  }
  if (new RegExp(`^${SCHEME}:`, 'i').test(t)) return fail('bad_origin');

  // https://host/#/pair?s=…&c=…  (the QR of pairing.md 2.2)
  if (!/\s/.test(t) && (/^[A-Za-z][A-Za-z0-9+.-]*:\/\//.test(t) || DANGEROUS_SCHEME.test(t))) {
    const o = parseOrigin(t, opts.dev);
    if (!o.ok) return o;
    const hash = /#\/pair\?([^#]*)$/.exec(t);
    const q = hash ? params(hash[1] ?? '') : null;
    if (!q) return fail('no_secret_or_code');
    return finish(o.origin, q.get('s'), q.get('c'));
  }

  // Typed "address code": the address is the first token, the rest is the code.
  const sp = /^(\S+)\s+([\s\S]+)$/.exec(t);
  if (!sp) return fail('no_secret_or_code');
  const o = parseOrigin(sp[1] ?? '', opts.dev);
  if (!o.ok) return o;
  return finish(o.origin, undefined, sp[2]);
}

export type ClaimFailure = 'invalid' | 'wrong_kind' | 'rate_limited' | 'unreachable' | 'other';

export class ClaimError extends Error {
  constructor(
    readonly reason: ClaimFailure,
    readonly status: number | null,
    message: string,
  ) {
    super(message);
    this.name = 'ClaimError';
  }
}

export interface ClaimDeps {
  fetch: FetchLike;
  clock: Clock;
  app: AppInfo;
}

/** "hub.example.ts.net:8443" from an origin. */
export function hostOf(origin: string): string {
  return origin.replace(/^https?:\/\//, '');
}

/** The words shown for a failed claim (P2 error mapping). */
export function claimMessage(reason: ClaimFailure, host: string, kind?: string): string {
  switch (reason) {
    case 'invalid':
      return 'Invalid or expired code';
    case 'wrong_kind':
      return `That code is for ${kind ? `a ${kind}` : 'a different kind of device'}, not a phone`;
    case 'rate_limited':
      return 'Too many tries, wait 10 minutes';
    case 'unreachable':
      return `Can't reach ${host}. Is Tailscale on?`;
    default:
      return 'Pairing failed. Try again';
  }
}

/** The inline words for a refused input. */
export function pairInputMessage(reason: PairInputError): string {
  switch (reason) {
    case 'empty':
      return 'Enter the hub address and code';
    case 'not_https':
      return 'The hub address must start with https://';
    case 'bad_origin':
      return "That doesn't look like a hub address";
    case 'no_secret_or_code':
      return 'Add the 6-digit code from Add phone';
    case 'bad_secret':
      return 'That pairing link is damaged. Make a new one';
    case 'bad_code':
      return 'The code is 6 digits, like 482-913';
    default:
      return 'That is too long to be a hub address';
  }
}

const KINDS = ['machine', 'desktop', 'browser', 'owner'];

/** Only a whitelisted kind word is taken from the body; the body itself is never kept. */
async function kindFrom(res: { text(): Promise<string> }): Promise<string | undefined> {
  try {
    const body = (await res.text()).toLowerCase();
    return KINDS.find((k) => new RegExp(`\\b${k}\\b`).test(body));
  } catch {
    return undefined;
  }
}

/** The answer is a redirect, or came from somewhere other than `origin`. */
function redirected(res: ResponseLike, origin: string): boolean {
  if (res.status >= 300 && res.status < 400) return true;
  if (!res.url) return res.redirected === true;
  const m = /^([A-Za-z][A-Za-z0-9+.-]*:\/\/[^/?#]*)/.exec(res.url);
  return !m || normalizeOrigin(m[1]!) !== normalizeOrigin(origin);
}

/**
 * GET <origin>/health with no credentials and no redirects, before the secret or code is sent
 * (P3): React Native's fetch may follow a redirect whatever `redirect` says, so the first
 * contact with a new origin proves it answers itself. Any status but a 3xx is an answer (a 503
 * hub is still that hub). Throws 'unreachable', or 'other' for a redirect.
 */
async function probeOrigin(deps: ClaimDeps, origin: string, host: string): Promise<void> {
  const ctl = new AbortController();
  const timer = deps.clock.setTimeout(() => ctl.abort(), HEALTH_TIMEOUT_MS);
  let res: ResponseLike;
  try {
    res = await deps.fetch(`${origin}/health`, { method: 'GET', headers: { Accept: 'application/json' }, signal: ctl.signal, redirect: 'error' });
  } catch {
    throw new ClaimError('unreachable', null, claimMessage('unreachable', host));
  } finally {
    deps.clock.clearTimeout(timer);
  }
  if (redirected(res, origin)) throw new ClaimError('other', res.status, claimMessage('other', host));
}

/** POST <origin>/v1/pair/claim as a phone with cookie: false; returns what to keep (P2, P3). */
export async function claim(deps: ClaimDeps, input: PairInput): Promise<StoredHub> {
  const origin = normalizeOrigin(input.origin);
  const host = hostOf(origin);
  await probeOrigin(deps, origin, host);
  const name = `${deps.app.model} · app`;
  const body: PairClaim = {
    ...(input.secret !== undefined ? { secret: input.secret } : { code: input.code }),
    name,
    expect_kind: DEVICE_KIND,
    cookie: false,
    client: `app/${deps.app.version}+${deps.app.build}`,
  };
  const ctl = new AbortController();
  const timer = deps.clock.setTimeout(() => ctl.abort(), REQUEST_TIMEOUT_MS);
  let res;
  try {
    res = await deps.fetch(`${origin}/v1/pair/claim`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
      body: JSON.stringify(body),
      signal: ctl.signal,
      redirect: 'error',
    });
  } catch {
    throw new ClaimError('unreachable', null, claimMessage('unreachable', host));
  } finally {
    deps.clock.clearTimeout(timer);
  }
  // A token that came back through a redirect is not this origin's: keep nothing.
  if (redirected(res, origin)) throw new ClaimError('other', res.status, claimMessage('other', host));
  if (res.status === 401) throw new ClaimError('invalid', 401, claimMessage('invalid', host));
  if (res.status === 409) {
    throw new ClaimError('wrong_kind', 409, claimMessage('wrong_kind', host, await kindFrom(res)));
  }
  if (res.status === 429) throw new ClaimError('rate_limited', 429, claimMessage('rate_limited', host));
  if (res.status !== 200) throw new ClaimError('other', res.status, claimMessage('other', host));
  let reply: PairClaimReply;
  try {
    reply = (await res.json()) as PairClaimReply;
  } catch {
    throw new ClaimError('other', 200, claimMessage('other', host));
  }
  if (typeof reply?.token !== 'string' || !reply.token || typeof reply.device !== 'string') {
    throw new ClaimError('other', 200, claimMessage('other', host));
  }
  return {
    origin,
    token: reply.token,
    device: reply.device,
    name: typeof reply.name === 'string' ? reply.name : name,
    paired_at: new Date(deps.clock.now()).toISOString(),
  };
}

export type { PairClaimReply };
