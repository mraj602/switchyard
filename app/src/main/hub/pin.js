// Trust on first use for an https hub whose certificate fails normal validation, typically a
// self-signed or private-CA certificate on a LAN (docs/spec/desktop.md C6).
//
// - probe(url): one TLS handshake with validation off, no request sent; it reports whether the
//   certificate passes normal validation and its SHA-256 fingerprint (of the DER certificate,
//   the value `openssl x509 -noout -fingerprint -sha256` prints).
// - The person compares that fingerprint with the one on the hub's computer and confirms; the
//   hub module stores it with the hub config (direct.json `pins`, keyed by origin).
// - createPinnedFetch: the app's own requests to a pinned origin (scheme + host + port) go
//   through node:https with a connection that is handed to the request only after the
//   handshake presented exactly the pinned certificate: no byte of the request (and so no
//   bearer token) is written before that check. Any other origin goes to the normal fetch,
//   with normal validation.
// - installVerifyProc: the hub view's partition (persist:sy-hub) accepts exactly the pinned
//   certificate for the pinned host and refuses any other certificate for that host;
//   everything else keeps Chromium's own verdict. Chromium's verify proc names the host, not
//   the port, so in the view the pin binds the host (the certificate itself is still exact).
//   No other session gets a verify proc: claude.ai tabs and the shell are never relaxed.

import crypto from 'node:crypto';
import nodeTls from 'node:tls';
import nodeHttps from 'node:https';
import net from 'node:net';
import { Readable } from 'node:stream';

/** A SHA-256 fingerprint as stored and shown: 32 upper-case hex pairs joined by colons. */
export const FINGERPRINT_RE = /^([0-9A-F]{2}:){31}[0-9A-F]{2}$/;
export const PROBE_TIMEOUT_MS = 8_000;

/** Certificate validation failures (Node's codes), as opposed to a hub that is down. */
const TLS_CODES = new Set([
  'DEPTH_ZERO_SELF_SIGNED_CERT', 'SELF_SIGNED_CERT_IN_CHAIN', 'UNABLE_TO_VERIFY_LEAF_SIGNATURE',
  'UNABLE_TO_GET_ISSUER_CERT', 'UNABLE_TO_GET_ISSUER_CERT_LOCALLY', 'CERT_HAS_EXPIRED', 'CERT_NOT_YET_VALID',
  'CERT_UNTRUSTED', 'CERT_REJECTED', 'CERT_SIGNATURE_FAILURE', 'CERT_REVOKED', 'INVALID_CA', 'HOSTNAME_MISMATCH',
  'ERR_TLS_CERT_ALTNAME_INVALID', 'WY_CERT_CHANGED',
]);

/**
 * The canonical form of a fingerprint typed or pasted by a person: "ab:cd…", "ABCD…" (64 hex),
 * or openssl's "sha256 Fingerprint=AB:CD:…". Null when it is not a SHA-256 fingerprint.
 */
export function normalizeFingerprint(s) {
  if (typeof s !== 'string' || s.length > 200) return null;
  const hex = s.trim().replace(/^sha-?256\s*(fingerprint)?\s*[=:]?\s*/i, '').replace(/[\s:]/g, '');
  if (!/^[0-9a-fA-F]{64}$/.test(hex)) return null;
  return hex.toUpperCase().match(/../g).join(':');
}

/** SHA-256 fingerprint of a certificate (PEM text or DER bytes); null when it does not parse. */
export function fingerprintOf(cert) {
  if (!cert) return null;
  try {
    return new crypto.X509Certificate(cert).fingerprint256;
  } catch {
    return null;
  }
}

/** The https origin of url, or null (only https URLs can be pinned). */
export function pinOrigin(url) {
  try {
    const u = new URL(url);
    return u.protocol === 'https:' && !u.username && !u.password ? u.origin : null;
  } catch {
    return null;
  }
}

const bareHost = (h) => String(h ?? '').toLowerCase().replace(/^\[|\]$/g, '');
const hostOfOrigin = (origin) => {
  try {
    return bareHost(new URL(origin).hostname);
  } catch {
    return null;
  }
};

/** The pinned certificate for this origin is not the one the hub presented. */
export class CertificateChangedError extends Error {
  constructor(origin, expected, actual) {
    super(`the certificate of ${origin} is not the pinned one (pinned ${expected}, now ${actual ?? 'unreadable'})`);
    this.name = 'CertificateChangedError';
    this.code = 'WY_CERT_CHANGED';
    this.origin = origin;
    this.expected = expected;
    this.actual = actual ?? null;
  }
}

/** True when err (or the error it wraps, as fetch's TypeError does) is a certificate failure. */
export function isCertError(err) {
  for (let e = err, i = 0; e && i < 4; e = e.cause, i++) {
    if (typeof e.code === 'string' && TLS_CODES.has(e.code)) return true;
  }
  return false;
}

const tlsOptions = (host, port) => ({
  host,
  port,
  ...(net.isIP(host) ? {} : { servername: host }),
  rejectUnauthorized: false, // checked here: by the pin (connect), or reported (probe)
  ALPNProtocols: ['http/1.1'],
});

const describe = (subject) => {
  if (!subject || typeof subject !== 'object') return null;
  const parts = ['CN', 'O', 'OU'].filter((k) => subject[k]).map((k) => `${k}=${[].concat(subject[k]).join('+')}`);
  return parts.length ? parts.join(', ').slice(0, 200) : null;
};

/**
 * One TLS handshake with url's host, nothing sent after it.
 * @returns {Promise<{ok: true, trusted: boolean, reason: string|null, fingerprint: string,
 *   subject: string|null, issuer: string|null, valid_from: string|null, valid_to: string|null}
 *   | {ok: false, error: string}>}  ok false: no handshake (not https, unreachable, timeout)
 */
export function probe(url, { tls = nodeTls, timeoutMs = PROBE_TIMEOUT_MS } = {}) {
  let u;
  try {
    u = new URL(url);
  } catch {
    return Promise.resolve({ ok: false, error: 'not a URL' });
  }
  if (u.protocol !== 'https:') return Promise.resolve({ ok: false, error: 'not https' });
  const host = bareHost(u.hostname);
  const port = Number(u.port || 443);
  return new Promise((resolve) => {
    let done = false;
    let sock = null;
    const finish = (r) => {
      if (done) return;
      done = true;
      clearTimeout(timer);
      try {
        sock?.destroy();
      } catch {
        // gone
      }
      resolve(r);
    };
    const timer = setTimeout(() => finish({ ok: false, error: 'the hub did not answer in time' }), timeoutMs);
    timer.unref?.();
    try {
      sock = tls.connect(tlsOptions(host, port));
    } catch (err) {
      finish({ ok: false, error: String(err?.message ?? err).slice(0, 200) });
      return;
    }
    sock.once('secureConnect', () => {
      const cert = sock.getPeerCertificate(false);
      const fingerprint = fingerprintOf(cert?.raw);
      if (!fingerprint) return finish({ ok: false, error: 'the hub sent no readable certificate' });
      const reason = sock.authorized ? null : sock.authorizationError;
      finish({
        ok: true,
        trusted: sock.authorized === true,
        reason: reason ? String(reason?.code ?? reason).slice(0, 80) : null,
        fingerprint,
        subject: describe(cert.subject),
        issuer: describe(cert.issuer),
        valid_from: typeof cert.valid_from === 'string' ? cert.valid_from : null,
        valid_to: typeof cert.valid_to === 'string' ? cert.valid_to : null,
      });
    });
    sock.once('error', (err) => finish({ ok: false, error: String(err?.code ?? err?.message ?? err).slice(0, 200) }));
  });
}

const NULL_BODY = new Set([101, 103, 204, 205, 304]);

function headersOf(init) {
  const out = {};
  new Headers(init ?? {}).forEach((v, k) => {
    out[k] = v;
  });
  return out;
}

/** One request to a pinned origin; resolves to a standard Response. */
function pinnedRequest(url, init, pin, { https, tls }) {
  const signal = init.signal ?? null;
  if (signal?.aborted) return Promise.reject(signal.reason);
  const host = bareHost(url.hostname);
  const port = Number(url.port || 443);
  const method = String(init.method ?? 'GET').toUpperCase();
  return new Promise((resolve, reject) => {
    let sock = null;
    let res = null;
    let settled = false;
    const fail = (err) => {
      if (!settled) {
        settled = true;
        reject(err);
      }
    };
    const req = https.request({
      protocol: 'https:',
      hostname: host,
      port,
      method,
      path: `${url.pathname}${url.search}`,
      headers: headersOf(init.headers),
      // No agent: the request writes nothing until createConnection hands over a socket,
      // which happens only after the presented certificate matched the pin.
      createConnection: (_opts, cb) => {
        let handed = false;
        sock = tls.connect(tlsOptions(host, port));
        sock.once('secureConnect', () => {
          const fp = fingerprintOf(sock.getPeerCertificate(false)?.raw);
          handed = true;
          if (fp === pin) cb(null, sock);
          else {
            sock.destroy();
            cb(new CertificateChangedError(url.origin, pin, fp));
          }
        });
        sock.once('error', (err) => {
          if (!handed) {
            handed = true;
            cb(err);
          }
        });
        return undefined;
      },
    });
    const onAbort = () => {
      const why = signal.reason;
      req.destroy(why);
      sock?.destroy();
      res?.destroy(why);
      fail(why);
    };
    signal?.addEventListener('abort', onAbort, { once: true });
    req.on('error', (err) => {
      signal?.removeEventListener('abort', onAbort);
      fail(err);
    });
    req.once('response', (r) => {
      res = r;
      const status = r.statusCode;
      if (init.redirect === 'error' && status >= 300 && status < 400) {
        r.destroy();
        return fail(new TypeError('fetch failed: redirect refused'));
      }
      const headers = new Headers();
      for (const [k, v] of Object.entries(r.headers)) {
        if (Array.isArray(v)) v.forEach((x) => headers.append(k, x));
        else if (v !== undefined) headers.set(k, String(v));
      }
      const nullBody = NULL_BODY.has(status) || method === 'HEAD';
      if (nullBody) r.resume();
      r.once('close', () => signal?.removeEventListener('abort', onAbort));
      settled = true;
      resolve(new Response(nullBody ? null : Readable.toWeb(r), { status, statusText: r.statusMessage ?? '', headers }));
    });
    if (init.body !== undefined && init.body !== null) req.end(typeof init.body === 'string' ? init.body : Buffer.from(init.body));
    else req.end();
  });
}

/**
 * A fetch for the app's own hub requests: a pinned https origin goes through pinnedRequest,
 * anything else to deps.fetch (normal validation). onCertError(origin, err) hears about every
 * certificate failure (a changed pin, or an untrusted certificate without one).
 * @param {{fetch: typeof fetch, getPin: (origin: string) => string|null,
 *          onCertError?: (origin: string, err: Error) => void, https?: object, tls?: object}} deps
 * @returns {typeof fetch}
 */
export function createPinnedFetch(deps) {
  const https = deps.https ?? nodeHttps;
  const tls = deps.tls ?? nodeTls;
  return async function hubFetch(input, init = {}) {
    let url;
    try {
      url = new URL(String(input));
    } catch {
      return deps.fetch(input, init);
    }
    const pin = url.protocol === 'https:' ? deps.getPin(url.origin) : null;
    try {
      return pin ? await pinnedRequest(url, init ?? {}, pin, { https, tls }) : await deps.fetch(input, init);
    } catch (err) {
      if (isCertError(err)) {
        try {
          deps.onCertError?.(url.origin, err);
        } catch {
          // a listener's failure is not the request's
        }
      }
      throw err;
    }
  };
}

/**
 * The hub partition's certificate check (Electron's session.setCertificateVerifyProc). For a
 * host with a pin: the pinned certificate is accepted (0), any other is refused (-2) and
 * reported. Any other host keeps Chromium's verdict (-3).
 * @param {object} session  electron session of persist:sy-hub
 * @param {{pins: () => {origin: string, sha256: string}[], onMismatch?: (origin: string, expected: string, actual: string|null) => void,
 *          log?: object}} deps
 */
export function installVerifyProc(session, deps) {
  session.setCertificateVerifyProc((request, callback) => {
    let verdict = -3;
    try {
      const host = bareHost(request?.hostname);
      const forHost = deps.pins().filter((p) => hostOfOrigin(p.origin) === host);
      if (forHost.length) {
        const fp = fingerprintOf(request?.certificate?.data);
        if (fp && forHost.some((p) => p.sha256 === fp)) verdict = 0;
        else {
          verdict = -2;
          deps.log?.warn?.(`hub pin: refused a certificate for ${host} that is not the pinned one`);
          deps.onMismatch?.(forHost[0].origin, forHost[0].sha256, fp);
        }
      }
    } catch (err) {
      deps.log?.warn?.(`hub pin: verify failed: ${err?.message ?? err}`);
      verdict = -2;
    }
    callback(verdict);
  });
}
