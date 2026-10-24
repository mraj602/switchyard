// Self-signed certificates and an https front for the fake hub, for the certificate pin tests
// (docs/spec/desktop.md C6). Certificates are made with the openssl CLI into a temp folder at
// test time (nothing secret is committed); tests skip when openssl is missing.
//
//   const a = makeCert(dir, 'a')            // {key, cert, fingerprint} (PEM strings, openssl's SHA-256 form)
//   const front = await startTlsFront({target: hub.url, ...a})   // https://127.0.0.1:<port> → the fake hub
//   front.requests                           // requests that reached the front (after the handshake)
//   front.use(b)                             // present another certificate from now on
//   await front.close()

import https from 'node:https';
import http from 'node:http';
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import { spawnSync } from 'node:child_process';

/** True when the openssl CLI can make a certificate here. */
export function haveOpenssl() {
  const r = spawnSync('openssl', ['version'], { encoding: 'utf8' });
  return r.status === 0;
}

/** A self-signed EC certificate for 127.0.0.1 and localhost, valid for 2 days. */
export function makeCert(dir, name, { cn = `wy-test-${name}` } = {}) {
  const key = path.join(dir, `${name}.key`);
  const cert = path.join(dir, `${name}.crt`);
  const r = spawnSync('openssl', [
    'req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:prime256v1', '-nodes', '-keyout', key, '-out', cert,
    '-days', '2', '-subj', `/CN=${cn}`, '-addext', 'subjectAltName=IP:127.0.0.1,DNS:localhost',
  ], { encoding: 'utf8' });
  if (r.status !== 0) throw new Error(`openssl failed: ${r.stderr}`);
  const pem = fs.readFileSync(cert, 'utf8');
  return { key: fs.readFileSync(key, 'utf8'), cert: pem, fingerprint: new crypto.X509Certificate(pem).fingerprint256 };
}

/** An https server on 127.0.0.1 that forwards every request (streaming) to an http target. */
export async function startTlsFront({ target, key, cert, port = 0 }) {
  const requests = [];
  const server = https.createServer({ key, cert }, (req, res) => {
    requests.push({ method: req.method, url: req.url, headers: { ...req.headers } });
    const up = http.request(new URL(req.url, target), { method: req.method, headers: req.headers }, (r) => {
      res.writeHead(r.statusCode, r.headers);
      r.pipe(res);
    });
    up.on('error', () => res.destroy());
    req.pipe(up);
  });
  const sockets = new Set();
  server.on('secureConnection', (s) => {
    sockets.add(s);
    s.on('close', () => sockets.delete(s));
  });
  await new Promise((r) => server.listen(port, '127.0.0.1', r));
  return {
    url: `https://127.0.0.1:${server.address().port}`,
    requests,
    use({ key: k, cert: c }) {
      server.setSecureContext({ key: k, cert: c });
      for (const s of sockets) s.destroy(); // kept-alive connections would still show the old certificate
    },
    async close() {
      for (const s of sockets) s.destroy();
      await new Promise((r) => server.close(r));
    },
  };
}
