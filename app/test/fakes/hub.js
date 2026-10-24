// A scripted hub and a fake `wy` for unit tests. Plain Node, no Electron.
//
//   const hub = await startFakeHub();            // listens on 127.0.0.1:<port>
//   hub.url                                       // 'http://127.0.0.1:<port>'
//   hub.set({overview, accounts, sessions, alerts, me})   // canned replies
//   hub.emit('session', {...})                    // push an SSE event to every open stream (returns its id)
//   hub.cutStreams()                              // drop every open stream (a network cut)
//   hub.rawWrite('retry: 50\n\n')                 // raw bytes to open streams
//   hub.requests                                  // [{method, path, headers, body}] in order
//   hub.script.<name>                             // status overrides: stream401, stream403, login (status), overview (status)
//   await hub.close()
//
//   const wy = await writeFakeWy(dir, {'desktop info --json': {stdout: '{...}'}})
//   wy.bin ; wy.calls() → [{argv, stdin, env}] ; wy.script(map)
// The fake wy is a Node script run through its shebang; its answers come from script.json next
// to it, keyed by the argv joined with spaces ({stdout, stderr, exit}), and every call is
// appended to calls.jsonl with the argv, the stdin text and the environment it saw.

import http from 'node:http';
import fs from 'node:fs';
import path from 'node:path';
import childProcess from 'node:child_process';

export async function startFakeHub(initial = {}) {
  const data = {
    overview: { time: '2026-10-13T10:00:00Z', seats: [], needs_you: [], machines: [], counts: { needs_you: 0 }, stream_id: 0 },
    accounts: { accounts: [] },
    sessions: { sessions: [], next: null },
    sessionById: {},
    alerts: {},
    me: { device: { id: 'dev_x' }, owner: false, hub: { version: '0.6.0' } },
    ...initial,
  };
  const script = { stream401: false, stream403: false, login: 204, overview: 200, loginSetsCookie: true, claim: 200, ingest: 200, revokedSelf: false };
  const requests = [];
  const streams = new Set();
  let eventId = 0;
  const log = [];

  const server = http.createServer((req, res) => {
    const chunks = [];
    req.on('data', (c) => chunks.push(c));
    req.on('end', () => {
      const body = Buffer.concat(chunks).toString('utf8');
      const u = new URL(req.url, 'http://x');
      requests.push({ method: req.method, path: u.pathname, query: u.search, headers: { ...req.headers }, body });
      const json = (status, obj, headers = {}) => {
        res.writeHead(status, { 'content-type': 'application/json', ...headers });
        res.end(obj === undefined ? '' : JSON.stringify(obj));
      };
      const bearer = /^Bearer (.+)$/.exec(req.headers.authorization ?? '')?.[1];
      const authed = (bearer === 'wy_d_good' && !script.revokedSelf) || (req.headers.cookie ?? '').includes('wy_session=ok');
      const p = u.pathname;
      if (p === '/health') return json(200, { ok: true, version: '0.6.0' });
      if (p === '/v1/pair/claim' && req.method === 'POST') {
        if (script.claim !== 200) return json(script.claim, { error: { message: 'no' } });
        const b = JSON.parse(body || '{}');
        if (!b.code && !b.secret) return json(400, { error: { message: 'no code' } });
        script.revokedSelf = false;
        return json(200, { device: 'dev_direct', token: 'wy_d_good', kind: 'desktop', scopes: ['write', 'read'] });
      }
      if (p === '/v1/session/login' && req.method === 'POST') {
        const status = script.login;
        const headers = status === 204 && script.loginSetsCookie ? { 'set-cookie': 'wy_session=ok; Path=/; HttpOnly' } : {};
        return json(status, status === 204 ? undefined : { error: { message: 'no' } }, headers);
      }
      if (!authed) return json(401, { error: { message: 'unauthorized' } });
      if (p === '/v1/stream') {
        if (script.stream401) return json(401, { error: { message: 'revoked' } });
        if (script.stream403) return json(403, { error: { message: 'forbidden' } });
        res.writeHead(200, { 'content-type': 'text/event-stream', 'cache-control': 'no-cache' });
        const s = { res };
        streams.add(s);
        res.on('close', () => streams.delete(s));
        const last = req.headers['last-event-id'];
        res.write(`id: ${++eventId}\nevent: hello\ndata: ${JSON.stringify({ version: '0.6.0', schema: 5 })}\n\n`);
        if (data.resetOnConnect && last !== undefined) res.write(`id: ${++eventId}\nevent: reset\ndata: {}\n\n`);
        return undefined;
      }
      if (p === '/v1/ingest' && req.method === 'POST') {
        if (script.ingest !== 200) return json(script.ingest, { error: { message: 'no' } });
        const b = JSON.parse(body || '{}');
        return json(200, { accepted: (b.records ?? []).map((r) => r.id), duplicates: [], rejected: [] });
      }
      if (p === '/v1/devices/self' && req.method === 'DELETE') {
        script.revokedSelf = true;
        res.writeHead(204);
        return res.end();
      }
      if (p === '/v1/overview') return script.overview === 200 ? json(200, data.overview) : json(script.overview, { error: { message: 'x' } });
      if (p === '/v1/accounts' && req.method === 'GET') return json(200, data.accounts);
      if (p === '/v1/me') return json(200, data.me);
      if (p === '/v1/sessions') return json(200, data.sessions);
      let m = /^\/v1\/sessions\/([^/]+)$/.exec(p);
      if (m) return data.sessionById[m[1]] ? json(200, data.sessionById[m[1]]) : json(404, { error: { message: 'no such session' } });
      m = /^\/v1\/alerts\/([^/]+)$/.exec(p);
      if (m) return data.alerts[m[1]] ? json(200, data.alerts[m[1]]) : json(404, { error: { message: 'no such alert' } });
      m = /^\/v1\/accounts\/([^/]+)$/.exec(p);
      if (m && req.method === 'PATCH') {
        if (!data.me.owner) return json(403, { error: { message: 'owner only' } });
        log.push({ patch: m[1], body: JSON.parse(body) });
        return json(200, { id: m[1], ...JSON.parse(body) });
      }
      if (p === '/big') return json(200, { pad: 'x'.repeat(3 * 1024 * 1024) });
      if (p === '/v1/redirect') {
        res.writeHead(302, { location: 'http://127.0.0.1:1/elsewhere' });
        return res.end();
      }
      return json(404, { error: { message: 'not found' } });
    });
  });
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  const { port } = server.address();

  return {
    url: `http://127.0.0.1:${port}`,
    port,
    requests,
    script,
    log,
    set: (patch) => Object.assign(data, patch),
    emit(event, payload) {
      const id = ++eventId;
      for (const s of streams) s.res.write(`id: ${id}\nevent: ${event}\ndata: ${JSON.stringify(payload)}\n\n`);
      return id;
    },
    rawWrite(text) {
      for (const s of streams) s.res.write(text);
    },
    cutStreams() {
      for (const s of [...streams]) s.res.destroy();
      streams.clear();
    },
    streamCount: () => streams.size,
    requestsTo: (p) => requests.filter((r) => r.path === p),
    async close() {
      for (const s of streams) s.res.destroy();
      server.closeAllConnections?.();
      await new Promise((r) => server.close(r));
    },
  };
}

/** Write a fake `wy` into dir; returns {bin, calls(), script(map)}. */
/**
 * The execFile to give create() when it runs a fake wy. Windows cannot run a script through its
 * shebang (and refuses to exec a .cmd without a shell), so there the fake is run as
 * `node <fake> args`; elsewhere undefined, so the real execFile executes the shebang script.
 */
export const wyExecFile = process.platform === 'win32'
  ? (file, args, opts, cb) => childProcess.execFile(process.execPath, [file, ...args], opts, cb)
  : undefined;

export function writeFakeWy(dir, map = {}, name = 'wy') {
  fs.mkdirSync(dir, { recursive: true });
  // A real wy is wy.exe on Windows, and resolution looks for that name.
  const bin = path.join(dir, process.platform === 'win32' ? `${name}.exe` : name);
  const scriptFile = path.join(dir, `${name}.script.json`);
  const callsFile = path.join(dir, `${name}.calls.jsonl`);
  fs.writeFileSync(scriptFile, JSON.stringify(map));
  fs.writeFileSync(
    bin,
    `#!${process.execPath}
const fs = require('node:fs');
const argv = process.argv.slice(2);
let stdin = '';
try { stdin = fs.readFileSync(0, 'utf8'); } catch {}
fs.appendFileSync(${JSON.stringify(callsFile)}, JSON.stringify({ argv, stdin, env: process.env }) + '\\n');
const map = JSON.parse(fs.readFileSync(${JSON.stringify(scriptFile)}, 'utf8'));
const r = map[argv.join(' ')] ?? map['*'] ?? { stdout: '', stderr: 'fake wy: no script for ' + argv.join(' '), exit: 1 };
if (r.stdout) process.stdout.write(r.stdout);
if (r.stderr) process.stderr.write(r.stderr);
process.exit(r.exit ?? 0);
`,
    { mode: 0o755 },
  );
  return {
    bin,
    calls: () => (fs.existsSync(callsFile) ? fs.readFileSync(callsFile, 'utf8').split('\n').filter(Boolean).map((l) => JSON.parse(l)) : []),
    script: (m) => fs.writeFileSync(scriptFile, JSON.stringify(m)),
  };
}
