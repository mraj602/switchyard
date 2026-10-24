import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import fsp from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import http from 'node:http';

import {
  normalizeFingerprint, fingerprintOf, pinOrigin, probe, createPinnedFetch, installVerifyProc, isCertError, CertificateChangedError,
} from './pin.js';
import { createDirect } from './direct.js';
import { haveOpenssl, makeCert, startTlsFront } from '../../../test/fakes/tls.js';

const NO_OPENSSL = haveOpenssl() ? false : 'the openssl CLI is not installed';
const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'sy-pintest-'));
const certs = NO_OPENSSL ? {} : { a: makeCert(dir, 'a'), b: makeCert(dir, 'b') };

/** A plain http server that answers a few routes; the TLS front forwards to it. */
async function backend() {
  const seen = [];
  const server = http.createServer((req, res) => {
    const chunks = [];
    req.on('data', (c) => chunks.push(c));
    req.on('end', () => {
      seen.push({ method: req.method, url: req.url, headers: req.headers, body: Buffer.concat(chunks).toString() });
      if (req.url === '/v1/stream') {
        res.writeHead(200, { 'content-type': 'text/event-stream' });
        res.write('event: hello\ndata: {}\n\n');
        setTimeout(() => res.end('event: ping\ndata: {}\n\n'), 30);
        return;
      }
      if (req.url === '/redirect') {
        res.writeHead(302, { location: '/elsewhere' });
        return res.end();
      }
      if (req.url === '/slow') return setTimeout(() => res.end('late'), 2000);
      if (req.url === '/empty') {
        res.writeHead(204);
        return res.end();
      }
      res.writeHead(200, { 'content-type': 'application/json', 'x-two': 'yes' });
      res.end(JSON.stringify({ ok: true, method: req.method, body: Buffer.concat(chunks).toString() }));
    });
  });
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  return { url: `http://127.0.0.1:${server.address().port}`, seen, close: () => new Promise((r) => server.close(r)) };
}

test('fingerprints: typed forms normalise to openssl’s, anything else is null', () => {
  const fp = 'AB:CD:EF:01:23:45:67:89:AB:CD:EF:01:23:45:67:89:AB:CD:EF:01:23:45:67:89:AB:CD:EF:01:23:45:67:89';
  assert.equal(normalizeFingerprint(fp.toLowerCase()), fp);
  assert.equal(normalizeFingerprint(fp.replace(/:/g, '')), fp);
  assert.equal(normalizeFingerprint(`sha256 Fingerprint=${fp}`), fp);
  assert.equal(normalizeFingerprint(`SHA256 Fingerprint: ${fp}`), fp);
  assert.equal(normalizeFingerprint(` ${fp.replace(/:/g, ' ')} `), fp);
  for (const bad of ['', 'AB:CD', fp.slice(3), `${fp}:00`, 'zz'.repeat(32), null, 42, 'x'.repeat(500)]) assert.equal(normalizeFingerprint(bad), null, String(bad));
  assert.equal(fingerprintOf('not a cert'), null);
  assert.equal(pinOrigin('https://Hub.Example:8443/x'), 'https://hub.example:8443');
  assert.equal(pinOrigin('http://hub.example/'), null);
  assert.equal(pinOrigin('https://u:p@hub.example/'), null);
});

test('probe: a self-signed hub is reported untrusted with its fingerprint, and nothing is sent', { skip: NO_OPENSSL }, async () => {
  const be = await backend();
  const front = await startTlsFront({ target: be.url, ...certs.a });
  const p = await probe(`${front.url}/`);
  assert.equal(p.ok, true);
  assert.equal(p.trusted, false);
  assert.equal(p.fingerprint, certs.a.fingerprint);
  assert.equal(p.reason, 'DEPTH_ZERO_SELF_SIGNED_CERT');
  assert.match(p.subject, /CN=sy-test-a/);
  assert.ok(p.valid_to);
  assert.equal(front.requests.length, 0, 'a probe is a handshake only');
  assert.deepEqual(await probe('http://127.0.0.1:1/'), { ok: false, error: 'not https' });
  assert.equal((await probe('https://127.0.0.1:1/')).ok, false, 'nothing listening');
  await front.close();
  await be.close();
});

test('pinned fetch: the pinned certificate carries GET, POST, streams, redirects and 204; aborts work', { skip: NO_OPENSSL }, async () => {
  const be = await backend();
  const front = await startTlsFront({ target: be.url, ...certs.a });
  const origin = front.url;
  const base = async () => assert.fail('a pinned origin never reaches the normal fetch');
  const f = createPinnedFetch({ fetch: base, getPin: (o) => (o === origin ? certs.a.fingerprint : null) });

  const r = await f(`${origin}/v1/x?q=1`, { headers: { authorization: 'Bearer sy_d_good', accept: 'application/json' }, redirect: 'manual' });
  assert.equal(r.status, 200);
  assert.equal(r.headers.get('x-two'), 'yes');
  assert.deepEqual(await r.json(), { ok: true, method: 'GET', body: '' });
  assert.equal(be.seen.at(-1).url, '/v1/x?q=1');
  assert.equal(be.seen.at(-1).headers.authorization, 'Bearer sy_d_good');

  const post = await f(`${origin}/v1/y`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: '{"a":1}' });
  assert.deepEqual(await post.json(), { ok: true, method: 'POST', body: '{"a":1}' });

  const s = await f(`${origin}/v1/stream`, { headers: { accept: 'text/event-stream' } });
  const reader = s.body.getReader();
  let text = '';
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    text += Buffer.from(value).toString();
  }
  assert.match(text, /event: hello[\s\S]*event: ping/);

  const redir = await f(`${origin}/redirect`, { redirect: 'manual' });
  assert.equal(redir.status, 302, 'never followed');
  await assert.rejects(f(`${origin}/redirect`, { redirect: 'error' }), /redirect refused/);
  assert.equal((await f(`${origin}/empty`)).status, 204);

  await assert.rejects(f(`${origin}/slow`, { signal: AbortSignal.timeout(100) }), (e) => e.name === 'TimeoutError');
  await front.close();
  await be.close();
});

test('pinned fetch: another certificate is refused before the request (and its token) is written', { skip: NO_OPENSSL }, async () => {
  const be = await backend();
  const front = await startTlsFront({ target: be.url, ...certs.a });
  const errors = [];
  const f = createPinnedFetch({ fetch: globalThis.fetch, getPin: (o) => (o === front.url ? certs.a.fingerprint : null), onCertError: (o, e) => errors.push([o, e]) });
  assert.equal((await f(`${front.url}/v1/x`)).status, 200);
  front.use(certs.b);
  const before = front.requests.length;
  await assert.rejects(f(`${front.url}/v1/x`, { headers: { authorization: 'Bearer sy_d_secret' } }), (e) => {
    assert.ok(e instanceof CertificateChangedError);
    assert.equal(e.expected, certs.a.fingerprint);
    assert.equal(e.actual, certs.b.fingerprint);
    assert.ok(isCertError(e));
    return true;
  });
  assert.equal(front.requests.length, before, 'no request reached the server');
  assert.ok(be.seen.every((r) => r.headers.authorization !== 'Bearer sy_d_secret'), 'the token never left');
  assert.deepEqual(errors.map(([o, e]) => [o, e.code]), [[front.url, 'WY_CERT_CHANGED']]);
  await front.close();
  await be.close();
});

test('unpinned origins use the normal fetch, which refuses a self-signed hub; other origins are never pinned', { skip: NO_OPENSSL }, async () => {
  const be = await backend();
  const front = await startTlsFront({ target: be.url, ...certs.a });
  const errors = [];
  const f = createPinnedFetch({ fetch: globalThis.fetch, getPin: () => null, onCertError: (o) => errors.push(o) });
  await assert.rejects(f(`${front.url}/v1/x`), (e) => isCertError(e));
  assert.deepEqual(errors, [front.url]);
  assert.equal(front.requests.length, 0);
  // A pin for one port does not cover another port of the same host.
  const other = await startTlsFront({ target: be.url, ...certs.a });
  const g = createPinnedFetch({ fetch: globalThis.fetch, getPin: (o) => (o === front.url ? certs.a.fingerprint : null) });
  await assert.rejects(g(`${other.url}/v1/x`), (e) => isCertError(e));
  assert.equal((await g(`${front.url}/v1/x`)).status, 200);
  // http and non-URLs go straight to the normal fetch
  const calls = [];
  const h = createPinnedFetch({ fetch: async (u) => (calls.push(String(u)), new Response('x')), getPin: () => certs.a.fingerprint });
  await h('http://127.0.0.1:9/x');
  assert.deepEqual(calls, ['http://127.0.0.1:9/x']);
  await other.close();
  await front.close();
  await be.close();
});

test('the hub view’s verify proc: the pinned certificate only, for the pinned host only', { skip: NO_OPENSSL }, () => {
  let proc = null;
  const session = { setCertificateVerifyProc: (fn) => (proc = fn) };
  const mismatches = [];
  installVerifyProc(session, {
    pins: () => [{ origin: 'https://192.168.1.5:8443', sha256: certs.a.fingerprint }, { origin: 'https://[fd00::5]', sha256: certs.a.fingerprint }],
    onMismatch: (...a) => mismatches.push(a),
  });
  const verdict = (hostname, cert) => {
    let v;
    proc({ hostname, certificate: { data: cert }, verificationResult: 'net::ERR_CERT_AUTHORITY_INVALID', errorCode: -202 }, (x) => (v = x));
    return v;
  };
  assert.equal(verdict('192.168.1.5', certs.a.cert), 0, 'the pinned certificate is accepted');
  assert.equal(verdict('192.168.1.5', certs.b.cert), -2, 'another certificate for the pinned host is refused');
  assert.deepEqual(mismatches, [['https://192.168.1.5:8443', certs.a.fingerprint, certs.b.fingerprint]]);
  assert.equal(verdict('[fd00::5]', certs.a.cert), 0);
  assert.equal(verdict('fd00::5', certs.b.cert), -2);
  assert.equal(verdict('claude.ai', certs.a.cert), -3, 'any other host keeps Chromium’s own verdict');
  assert.equal(verdict('192.168.1.6', certs.a.cert), -3);
  assert.equal(verdict('192.168.1.5', 'garbage'), -2);
});

test('direct.json keeps the pins of https origins only, and drops bad entries', async () => {
  const d = fs.mkdtempSync(path.join(os.tmpdir(), 'sy-pins-'));
  const file = path.join(d, 'direct.json');
  const fp = 'AB:CD:EF:01:23:45:67:89:AB:CD:EF:01:23:45:67:89:AB:CD:EF:01:23:45:67:89:AB:CD:EF:01:23:45:67:89';
  const mk = () => createDirect({ fs: fsp, path, file, fetch: async () => assert.fail('no fetch'), clientName: 'x', version: '1', getAuth: () => null, active: () => false, log: { warn() {} } });
  const a = mk();
  await a.load();
  await a.setPin('https://hub.lan:8443', fp);
  await assert.rejects(a.setPin('http://hub.lan', fp), /https origin/);
  await assert.rejects(a.setPin('https://hub.lan/x', fp), /https origin/);
  await assert.rejects(a.setPin('https://hub.lan', 'nope'), /fingerprint/);
  assert.equal(a.pinFor('https://hub.lan:8443'), fp);
  const saved = JSON.parse(fs.readFileSync(file, 'utf8'));
  assert.equal(saved.pins['https://hub.lan:8443'].sha256, fp);
  // Windows has no POSIX modes; elsewhere the file is private to the user.
  if (process.platform !== 'win32') assert.equal((fs.statSync(file).mode & 0o777).toString(8), '600');
  saved.pins['http://plain.lan'] = { sha256: fp };
  saved.pins['https://bad.lan'] = { sha256: 'x' };
  fs.writeFileSync(file, JSON.stringify(saved));
  const b = mk();
  await b.load();
  assert.deepEqual(b.pins().map((p) => p.origin), ['https://hub.lan:8443']);
  await b.setPin('https://hub.lan:8443', null);
  assert.equal(b.pinFor('https://hub.lan:8443'), null);
});
