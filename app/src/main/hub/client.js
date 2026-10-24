// The hub client (C6): Node's fetch in main with `Authorization: Bearer`, a 10 s timeout, no
// redirects, replies capped at 2 MiB, and plain http: only for loopback hosts or a URL that
// `wy desktop info` reported. The bearer token is only ever sent to the hub's own origin:
// every request URL is built from the base and checked to still have that origin.

import { HUB_TIMEOUT_MS, HUB_REPLY_MAX_BYTES } from '../../shared/constants.js';
import { normalizeHubUrl } from './token.js';

export const isLoopbackHost = (h) => {
  const host = String(h).replace(/^\[|\]$/g, '').toLowerCase();
  return host === 'localhost' || host === '::1' || /^127(\.\d{1,3}){3}$/.test(host);
};

/**
 * May the app talk to this hub URL? https always; http for loopback, or when `reportedBySy`
 * (wy itself accepted the URL, e.g. with --insecure-lan).
 */
export function hubUrlAllowed(url, reportedBySy = false) {
  const n = normalizeHubUrl(url);
  if (!n) return false;
  const u = new URL(n);
  if (u.protocol === 'https:') return true;
  return isLoopbackHost(u.hostname) || reportedBySy === true;
}

/** Join a hub base URL and an absolute API path; null when the result leaves the hub's origin. */
export function hubUrl(base, path, query) {
  const b = normalizeHubUrl(base);
  if (!b || typeof path !== 'string' || !path.startsWith('/v1/')) return null;
  const root = new URL(`${b}/`);
  const u = new URL(`${root.pathname}${path.slice(1)}`, root.origin);
  if (u.origin !== root.origin) return null;
  if (query) for (const [k, v] of Object.entries(query)) u.searchParams.set(k, String(v));
  return u.toString();
}

/** Read a body with a hard byte cap; throws 'too large' past it. */
async function readCapped(res, max) {
  const len = Number(res.headers?.get?.('content-length'));
  if (Number.isFinite(len) && len > max) throw new Error('reply too large');
  if (!res.body?.getReader) {
    const t = await res.text();
    if (Buffer.byteLength(t) > max) throw new Error('reply too large');
    return t;
  }
  const reader = res.body.getReader();
  const chunks = [];
  let n = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    n += value.byteLength;
    if (n > max) {
      await reader.cancel().catch(() => {});
      throw new Error('reply too large');
    }
    chunks.push(value);
  }
  return Buffer.concat(chunks).toString('utf8');
}

/**
 * @param {{fetch: typeof fetch, getAuth: () => ({url: string, token: string}|null),
 *          reportedBySy?: () => boolean, timeoutMs?: number, maxBytes?: number}} deps
 * @returns {{request(method: string, path: string, opts?: {body?: object, query?: object}):
 *            Promise<{ok: boolean, status: number, data?: any, error?: string}>,
 *            get(path: string, query?: object): Promise<{ok: boolean, status: number, data?: any, error?: string}>}}
 */
export function createClient(deps) {
  const timeoutMs = deps.timeoutMs ?? HUB_TIMEOUT_MS;
  const maxBytes = deps.maxBytes ?? HUB_REPLY_MAX_BYTES;

  async function request(method, path, opts = {}) {
    const auth = deps.getAuth();
    if (!auth) return { ok: false, status: 0, error: 'not paired' };
    if (!hubUrlAllowed(auth.url, deps.reportedBySy?.() ?? false)) return { ok: false, status: 0, error: 'hub URL not allowed' };
    const url = hubUrl(auth.url, path, opts.query);
    if (!url) return { ok: false, status: 0, error: 'bad request path' };
    const headers = { authorization: `Bearer ${auth.token}`, accept: 'application/json' };
    let body;
    if (opts.body !== undefined) {
      headers['content-type'] = 'application/json';
      body = JSON.stringify(opts.body);
    }
    try {
      const res = await deps.fetch(url, { method, headers, body, redirect: 'manual', signal: AbortSignal.timeout(timeoutMs) });
      if (res.status >= 300 && res.status < 400) return { ok: false, status: res.status, error: 'the hub redirected; redirects are not followed' };
      const text = res.status === 204 ? '' : await readCapped(res, maxBytes);
      let data = null;
      if (text) {
        try {
          data = JSON.parse(text);
        } catch {
          return { ok: false, status: res.status, error: 'reply is not JSON' };
        }
      }
      if (res.status >= 200 && res.status < 300) return { ok: true, status: res.status, data };
      const msg = data?.error?.message ?? data?.message ?? data?.error;
      return { ok: false, status: res.status, error: typeof msg === 'string' ? msg.slice(0, 200) : `hub answered ${res.status}` };
    } catch (err) {
      const timedOut = err?.name === 'TimeoutError' || err?.name === 'AbortError';
      return { ok: false, status: 0, error: timedOut ? 'the hub did not answer in time' : String(err?.message ?? err).split('\n')[0].slice(0, 200) };
    }
  }

  return { request, get: (path, query) => request('GET', path, { query }) };
}
