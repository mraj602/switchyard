// Pairing input and claim (phone.md P2).
import { DEVICE_KIND, PAIR_PROBE_TIMEOUT_MS, REQUEST_TIMEOUT_MS } from '../../contract/constants';
import type { DeviceKeysNew, PairClaim, PairClaimReply } from '../../contract/api';
import type { ClaimLink, LinkConfig } from '../../contract/link';
import { LINK_ERR_HUB_KEY, LINK_ERR_TAMPERED } from '../../contract/link';
import type { AppInfo, Clock, FetchLike, ResponseLike } from '../../contract/platform';
import { SCHEME } from '../../contract/routes';
import { normalizeOrigin } from '../hub';
import type { StoredHub } from '../secure';

export interface PairInput {
  origin: string; // https://host[:port], nothing else kept
  secret?: string; // 22 base64url characters
  code?: string; // 6 digits
  /** keys.md 7: the QR's `h`, the hub key's fingerprint (24 base32 characters). */
  hubFp?: string;
  /** keys.md 7: the QR's `k`, the registration seal key (22 base64url characters). */
  sealKey?: string;
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
  /**
   * __DEV__ builds also accept http:// for loopback, RFC 1918 and Tailscale (100.64.0.0/10)
   * addresses: test builds only, for hubs on a tailnet without HTTPS certificates.
   */
  dev: boolean;
  /** Typed address when the code was typed separately. */
  address?: string;
}

const MAX_INPUT = 2048;
const MAX_HOST = 253;
const SECRET = /^[A-Za-z0-9_-]{22}$/;
const HUB_FP = /^[a-z2-7]{24}$/;
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
  return a === 127 || a === 10 || (a === 172 && b >= 16 && b <= 31) || (a === 192 && b === 168) || (a === 100 && b >= 64 && b <= 127);
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

function finish(
  origin: string,
  secret: string | undefined,
  code: string | undefined,
  pins?: { h?: string | undefined; k?: string | undefined },
): ParsedPair {
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
  // keys.md 7: a damaged pin is a damaged link (never silently dropped: it is what protects the claim).
  if (pins?.h !== undefined) {
    const h = pins.h.toLowerCase();
    if (!HUB_FP.test(h)) return fail('bad_secret');
    input.hubFp = h;
  }
  if (pins?.k !== undefined) {
    if (!SECRET.test(pins.k) || secret === undefined) return fail('bad_secret');
    input.sealKey = pins.k;
  }
  return { ok: true, input };
}

/** QR URL, watchyard://pair link, or typed address plus code (P2). */
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

  // watchyard://pair?u=<origin>&s=…&c=…
  const sm = new RegExp(`^${SCHEME}://pair/?(?:\\?([^#]*))?(?:#.*)?$`, 'i').exec(t);
  if (sm) {
    const q = params(sm[1] ?? '');
    if (!q) return fail('bad_origin');
    const u = q.get('u');
    if (!u) return fail('bad_origin');
    const o = parseOrigin(u, opts.dev);
    if (!o.ok) return o;
    return finish(o.origin, q.get('s'), q.get('c'), { h: q.get('h'), k: q.get('k') });
  }
  if (new RegExp(`^${SCHEME}:`, 'i').test(t)) return fail('bad_origin');

  // https://host/#/pair?s=…&c=…  (the QR of pairing.md 2.2)
  if (!/\s/.test(t) && (/^[A-Za-z][A-Za-z0-9+.-]*:\/\//.test(t) || DANGEROUS_SCHEME.test(t))) {
    const o = parseOrigin(t, opts.dev);
    if (!o.ok) return o;
    const hash = /#\/pair\?([^#]*)$/.exec(t);
    const q = hash ? params(hash[1] ?? '') : null;
    if (!q) return fail('no_secret_or_code');
    return finish(o.origin, q.get('s'), q.get('c'), { h: q.get('h'), k: q.get('k') });
  }

  // Typed "address code": the address is the first token, the rest is the code.
  const sp = /^(\S+)\s+([\s\S]+)$/.exec(t);
  if (!sp) return fail('no_secret_or_code');
  const o = parseOrigin(sp[1] ?? '', opts.dev);
  if (!o.ok) return o;
  return finish(o.origin, undefined, sp[2]);
}

export type ClaimFailure =
  | 'invalid'
  | 'wrong_kind'
  | 'rate_limited'
  | 'unreachable'
  /** The real kind of a network failure, when the error tells it (TLS, DNS, refused, timeout). */
  | 'tls'
  | 'dns'
  | 'refused'
  | 'timeout'
  /** The hub answered, but with a server error (the status is in ClaimError.status). */
  | 'http'
  /** keys.md 7.3: the reply's hub key is not the QR's `h`. */
  | 'hub_key'
  /** keys.md 7.3: the certificate, the keys in it or `reg` are not what this phone sent. */
  | 'tampered'
  /** The phone could not make its link keys (push.md 14.3). */
  | 'keys'
  | 'other';

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
  /**
   * The wy-link module (Android): keys go into the claim and the reply is
   * checked and configured (P2 "Keys in the claim"). Absent: a claim without keys.
   */
  link?: ClaimLink;
}

/** "hub.example.ts.net:8443" from an origin. */
export function hostOf(origin: string): string {
  return origin.replace(/^https?:\/\//, '');
}

/** A tailnet address (100.x or *.ts.net): the only hosts where "Is Tailscale on?" helps. */
export function isTailscaleHost(host: string): boolean {
  const h = host.replace(/:\d+$/, '').toLowerCase();
  return /^100\.\d{1,3}\.\d{1,3}\.\d{1,3}$/.test(h) || h === 'ts.net' || h.endsWith('.ts.net');
}

/** What a failed fetch was, from the error the platform threw (React Native gives only text). */
export function networkKind(e: unknown, timedOut = false): 'tls' | 'dns' | 'refused' | 'timeout' | 'unreachable' {
  if (timedOut) return 'timeout';
  const text = e instanceof Error ? `${e.name} ${e.message}` : typeof e === 'string' ? e : '';
  if (/certificate|ssl|tls|handshake|trust anchor|CertPath|self.signed/i.test(text)) return 'tls';
  if (/unknown host|UnknownHost|unable to resolve|ENOTFOUND|EAI_AGAIN|getaddrinfo|no address associated|name not resolved|nodename/i.test(text)) return 'dns';
  if (/refused|ECONNREFUSED|failed to connect/i.test(text)) return 'refused';
  if (/timed? ?out|ETIMEDOUT|SocketTimeout/i.test(text)) return 'timeout';
  return 'unreachable';
}

/** The words shown for a failed claim (P2 error mapping). */
export function claimMessage(reason: ClaimFailure, host: string, kind?: string, status?: number | null): string {
  const hint = isTailscaleHost(host) ? ' Is Tailscale on?' : '';
  switch (reason) {
    case 'invalid':
      return 'Invalid or expired code';
    case 'wrong_kind':
      return `That code is for ${kind ? `a ${kind}` : 'a different kind of device'}, not a phone`;
    case 'rate_limited':
      return 'Too many tries, wait 10 minutes';
    case 'unreachable':
      return `Can't reach ${host}.${hint}`;
    case 'tls':
      return `The secure connection to ${host} failed: its certificate is not trusted. Check the address.`;
    case 'dns':
      return `${host} was not found. Check the address.${hint}`;
    case 'refused':
      return `${host} refused the connection. Is the hub running?${hint}`;
    case 'timeout':
      return `${host} did not answer in time.${hint}`;
    case 'http':
      return status === 503 ? `${host} is not taking phones right now (HTTP 503).` : `${host} answered with an error (HTTP ${status ?? '?'}).`;
    case 'hub_key':
      return "This hub's key does not match the QR code";
    case 'tampered':
      return 'Pairing was tampered with; try again on your home network';
    case 'keys':
      return "This phone couldn't make its keys. Try again";
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
  let timedOut = false;
  const timer = deps.clock.setTimeout(() => {
    timedOut = true;
    ctl.abort();
  }, PAIR_PROBE_TIMEOUT_MS);
  let res: ResponseLike;
  try {
    res = await deps.fetch(`${origin}/health`, { method: 'GET', headers: { Accept: 'application/json' }, signal: ctl.signal, redirect: 'error' });
  } catch (e) {
    const kind = networkKind(e, timedOut);
    throw new ClaimError(kind, null, claimMessage(kind, host));
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
  const keys = await claimKeys(deps, input, host);
  const body: PairClaim = {
    ...(input.secret !== undefined ? { secret: input.secret } : { code: input.code }),
    name,
    expect_kind: DEVICE_KIND,
    cookie: false,
    client: `app/${deps.app.version}+${deps.app.build}`,
    ...(keys?.sealed !== undefined ? { keys_sealed: keys.sealed } : {}),
    ...(keys?.clear !== undefined ? { keys: keys.clear } : {}),
  };
  const ctl = new AbortController();
  let timedOut = false;
  const timer = deps.clock.setTimeout(() => {
    timedOut = true;
    ctl.abort();
  }, REQUEST_TIMEOUT_MS);
  let res;
  try {
    res = await deps.fetch(`${origin}/v1/pair/claim`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
      body: JSON.stringify(body),
      signal: ctl.signal,
      redirect: 'error',
    });
  } catch (e) {
    const kind = networkKind(e, timedOut);
    throw new ClaimError(kind, null, claimMessage(kind, host));
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
  if (res.status >= 500) throw new ClaimError('http', res.status, claimMessage('http', host, undefined, res.status));
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
  const hub: StoredHub = {
    origin,
    token: reply.token,
    device: reply.device,
    name: typeof reply.name === 'string' ? reply.name : name,
    paired_at: new Date(deps.clock.now()).toISOString(),
  };
  if (keys && deps.link) await configureLink(deps, deps.link, input, keys, reply, hub, host);
  return hub;
}

type ClaimKeys = { sealed?: string; clear?: DeviceKeysNew };

/**
 * keys.md 7.1: with `k` from the QR the keys go sealed; with a typed code in the clear; with a
 * QR secret but no `k` (a hub before F1) not at all, since the hub refuses clear keys with a
 * secret. Null: no keys in this claim.
 */
async function claimKeys(deps: ClaimDeps, input: PairInput, host: string): Promise<ClaimKeys | null> {
  const link = deps.link;
  if (!link) return null;
  if (input.secret !== undefined && input.sealKey === undefined) return null;
  try {
    const k = await link.prepareKeys();
    if (input.secret !== undefined) return { sealed: await link.sealKeys(input.sealKey!, input.secret) };
    return { clear: { sign: k.sign, box: k.box, auth: k.auth } };
  } catch {
    throw new ClaimError('keys', null, claimMessage('keys', host));
  }
}

const codeOf = (e: unknown): string | undefined => (e && typeof e === 'object' ? (e as { code?: unknown }).code : undefined) as string | undefined;

/**
 * keys.md 7.3: check the reply and keep the pins. The QR's `h` against `hub_key.fp` and, after a
 * sealed claim, a `cert` with `reg: qr` here; the certificate's signature and its keys natively
 * (configure). A refusal deletes the new device with its token and throws; a reply from a hub
 * without keys (no `hub_key`) after clear keys pairs without instant delivery.
 */
async function configureLink(
  deps: ClaimDeps,
  link: ClaimLink,
  input: PairInput,
  keys: ClaimKeys,
  reply: PairClaimReply,
  hub: StoredHub,
  host: string,
): Promise<void> {
  const refuse = async (reason: 'hub_key' | 'tampered'): Promise<never> => {
    await deleteSelf(deps, hub);
    throw new ClaimError(reason, 200, claimMessage(reason, host));
  };
  const hk = reply.hub_key;
  const sealed = keys.sealed !== undefined;
  if (!hk || typeof hk.pub !== 'string' || typeof hk.fp !== 'string') {
    if (sealed || input.hubFp !== undefined) await refuse('tampered');
    return; // a hub before F1 took a typed code: paired, no link
  }
  if (input.hubFp !== undefined && hk.fp.toLowerCase() !== input.hubFp) await refuse('hub_key');
  const reg = reply.reg;
  if (!reply.cert || (reg !== 'qr' && reg !== 'tofu') || (sealed && reg !== 'qr')) await refuse('tampered');
  const urls = Array.isArray(reply.link?.urls) ? reply.link!.urls.filter((u) => typeof u === 'string') : [];
  const cfg: LinkConfig = {
    origin: hub.origin,
    device: hub.device,
    urls,
    pingS: typeof reply.link?.ping_s === 'number' ? reply.link.ping_s : 180,
    hubKey: { pub: hk.pub, fp: hk.fp.toLowerCase() },
    reg: reg as 'qr' | 'tofu',
    cert: reply.cert ?? null,
    qrFp: input.hubFp ?? null,
  };
  try {
    await link.configure(cfg);
  } catch (e) {
    const code = codeOf(e);
    if (code === LINK_ERR_HUB_KEY) await refuse('hub_key');
    if (code === LINK_ERR_TAMPERED) await refuse('tampered');
    // Anything else (no urls, a Keystore hiccup): paired; Settings → Notifications shows it.
  }
}

/** DELETE /v1/devices/self with the new token after a refused reply (keys.md 7.3); best effort. */
async function deleteSelf(deps: ClaimDeps, hub: StoredHub): Promise<void> {
  const ctl = new AbortController();
  const timer = deps.clock.setTimeout(() => ctl.abort(), REQUEST_TIMEOUT_MS);
  try {
    await deps.fetch(`${hub.origin}/v1/devices/self`, {
      method: 'DELETE',
      headers: { Authorization: `Bearer ${hub.token}`, Accept: 'application/json' },
      signal: ctl.signal,
      redirect: 'error',
    });
  } catch {
    // the hub will expire an unused device; the token is forgotten either way
  } finally {
    deps.clock.clearTimeout(timer);
  }
}

export type { PairClaimReply };
