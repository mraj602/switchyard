// Switchyard server: a small always-on home for your usage history, for phones and other
// browsers. Zero dependencies (Node 22+). Runs in Docker (see ../docker-compose.yml) or bare:
//
//   SWITCHYARD_TOKEN=$(openssl rand -base64 24) node server/server.js
//
// It never talks to claude.ai and holds no claude.ai cookies. Usage % comes from the desktop
// app, which pushes new rows here (Settings → Background & sync). Optionally it scans Claude
// Code transcripts itself when ~/.claude* is mounted read-only (SWITCHYARD_SCAN_HOME), so
// token analytics keep updating even while the desktop app is closed.
//
// env:
//   SWITCHYARD_TOKEN      required; desktop sync, hooks and web sign-in all use it
//   PORT                  default 8787
//   SWITCHYARD_DATA       data dir (default ./data, /data in the image)
//   SWITCHYARD_SCAN_HOME  home dir whose ~/.claude* to scan (mounted read-only); off if unset
//   SWITCHYARD_SCAN_DIRS  extra comma-separated config dirs to scan
//   NTFY_URL              full ntfy topic URL (https://ntfy.sh/<topic>) for phone pushes
//   NTFY_TOKEN            ntfy access token, if the topic is protected
//   PUBLIC_URL            this server's public URL, used as the tap target of pushes
//   SWITCHYARD_ALERTS     usage alert thresholds in % (default "80,95"); empty disables
'use strict';
const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const { Worker } = require('node:worker_threads');

const ROOT = path.join(__dirname, '..');
const PORT = Number(process.env.PORT || 8787);
const DATA = path.resolve(process.env.SWITCHYARD_DATA || path.join(ROOT, 'data'));
const TOKEN = process.env.SWITCHYARD_TOKEN || '';
const SCAN_HOME = process.env.SWITCHYARD_SCAN_HOME || '';
const SCAN_DIRS = (process.env.SWITCHYARD_SCAN_DIRS || '').split(',').map((s) => s.trim()).filter(Boolean);
const NTFY_URL = process.env.NTFY_URL || '';
const NTFY_TOKEN = process.env.NTFY_TOKEN || '';
const PUBLIC_URL = (process.env.PUBLIC_URL || '').replace(/\/+$/, '');
const ALERTS = (process.env.SWITCHYARD_ALERTS ?? '80,95').split(',').map(Number).filter((n) => n > 0 && n <= 100).sort((a, b) => a - b);
const VERSION = require(path.join(ROOT, 'package.json')).version;

if (TOKEN.length < 16) {
  console.error('SWITCHYARD_TOKEN must be set to a random string of at least 16 characters (e.g. `openssl rand -base64 24`).');
  process.exit(1);
}
fs.mkdirSync(DATA, { recursive: true });
const dbPath = path.join(DATA, 'usage.db');
const store = require(path.join(ROOT, 'core', 'db')).open(dbPath);
const log = (...a) => console.log(new Date().toISOString(), ...a);

// ---------- auth: bearer token (desktop, hooks) or a signed cookie (browser) ----------
const COOKIE = 'sy_session';
const cookieValue = crypto.createHmac('sha256', TOKEN).update('switchyard-web-session-v1').digest('base64url');
const same = (a, b) => { const x = Buffer.from(String(a)), y = Buffer.from(String(b)); return x.length === y.length && crypto.timingSafeEqual(x, y); };
function cookies(req) {
  const out = {};
  for (const part of String(req.headers.cookie || '').split(';')) { const i = part.indexOf('='); if (i > 0) out[part.slice(0, i).trim()] = part.slice(i + 1).trim(); }
  return out;
}
function authed(req, { cookie = true } = {}) {
  const h = String(req.headers.authorization || '');
  if (h.startsWith('Bearer ') && same(h.slice(7), TOKEN)) return true;
  return cookie && same(cookies(req)[COOKIE] || '', cookieValue);
}
const isHttps = (req) => req.socket.encrypted || String(req.headers['x-forwarded-proto'] || '').split(',')[0].trim() === 'https';
// slow down token guessing: 10 failed sign-ins per address per 10 minutes
const failures = new Map();
function throttled(ip) {
  const now = Date.now(), f = failures.get(ip);
  if (!f || now - f.since > 600e3) return false;
  return f.n >= 10;
}
function failed(ip) {
  const now = Date.now(), f = failures.get(ip);
  if (!f || now - f.since > 600e3) failures.set(ip, { since: now, n: 1 }); else f.n++;
  if (failures.size > 5000) failures.clear();
}

// ---------- worker: transcript scanning (optional) and analytics ----------
let worker;
const pending = new Map();
let reqId = 0;
const status = { scan: null, progress: null, startedAt: Date.now() };
function startWorker() {
  worker = new Worker(path.join(ROOT, 'core', 'worker.js'), {
    workerData: { dbPath, scan: !!(SCAN_HOME || SCAN_DIRS.length), home: SCAN_HOME || '/nonexistent', extraDirs: SCAN_DIRS, scanMs: 120e3, firstScanMs: 2000 },
  });
  worker.on('message', (m) => {
    if (m.type === 'result') {
      const p = pending.get(m.id); if (!p) return;
      pending.delete(m.id);
      if (m.error) p.reject(new Error(m.error)); else p.resolve(m.data);
    } else if (m.type === 'scanned') { status.scan = { at: Date.now(), files: m.files, rows: m.rows, ms: m.ms }; status.progress = null; if (m.rows) log(`scan: ${m.rows} rows from ${m.files} files in ${m.ms} ms`); }
    else if (m.type === 'progress') status.progress = m;
    else if (m.type === 'error') log(`worker ${m.where}:`, m.error);
  });
  worker.on('error', (e) => log('worker:', e.message));
  worker.on('exit', () => { for (const p of pending.values()) p.reject(new Error('worker restarted')); pending.clear(); if (!stopping) setTimeout(startWorker, 5000); });
}
function analytics(q) {
  return new Promise((resolve, reject) => {
    const id = ++reqId;
    pending.set(id, { resolve, reject });
    worker.postMessage({ type: 'compute', id, q });
    setTimeout(() => { if (pending.delete(id)) reject(new Error('analytics timed out')); }, 120e3);
  });
}

// ---------- overview: latest limit windows per account + recent activity ----------
const qLatest = store.raw.prepare('SELECT account_id, max(ts) AS ts FROM snapshots GROUP BY account_id');
const qSnap = store.raw.prepare('SELECT email, org_name, plan FROM snapshots WHERE account_id = ? AND ts = ?');
const qWins = store.raw.prepare('SELECT key, label, pct, resets_at, used_dollars, limit_dollars FROM windows WHERE account_id = ? AND ts = ?');
const qAccts = store.raw.prepare('SELECT id, label, color, email, plan FROM accounts ORDER BY ord');
const qEvents = store.raw.prepare('SELECT ts, account_id, account, kind, text, cwd, session_id, url FROM events ORDER BY ts DESC LIMIT ?');
function overview() {
  const latest = new Map(qLatest.all().map((r) => [r.account_id, r.ts]));
  const accounts = qAccts.all().map((a) => {
    const ts = latest.get(a.id);
    const s = ts ? qSnap.get(a.id, ts) : null;
    return {
      id: a.id, label: a.label, color: a.color, email: a.email || (s && s.email) || null, plan: a.plan || (s && s.plan) || null,
      org: s ? s.org_name : null, sampledAt: ts || null,
      windows: ts ? qWins.all(a.id, ts).map((w) => ({ key: w.key, label: w.label, pct: w.pct, resetsAt: w.resets_at, usedDollars: w.used_dollars, limitDollars: w.limit_dollars })) : [],
    };
  });
  return {
    version: VERSION, now: Date.now(), accounts,
    events: qEvents.all(40).map((e) => ({ ts: e.ts, accountId: e.account_id, account: e.account, kind: e.kind, text: e.text, cwd: e.cwd, session: e.session_id, url: e.url })),
    sync: { lastIngest: Number(store.kv.get('ingest:at')) || null, from: store.kv.get('ingest:from') || null },
    scan: SCAN_HOME || SCAN_DIRS.length ? { ...status.scan, running: !!status.progress } : null,
    ntfy: !!NTFY_URL,
  };
}

// ---------- phone pushes via ntfy ----------
const QUIET = new Set(['idle', 'finished', 'test']);
// JSON publishing (POST {topic, title, message, …} to the server root) keeps UTF-8 titles intact
async function ntfy({ title, body, click, tags, priority }) {
  if (!NTFY_URL) return;
  let u;
  try { u = new URL(NTFY_URL); } catch { log('ntfy: NTFY_URL is not a URL'); return; }
  const topic = u.pathname.replace(/^\/+|\/+$/g, '');
  const msg = { topic, title: String(title).slice(0, 200), message: String(body || '').slice(0, 1000), tags: [tags || 'robot'], priority: priority || 3 };
  if (click) msg.click = click;
  const headers = { 'content-type': 'application/json' };
  if (NTFY_TOKEN) headers.Authorization = `Bearer ${NTFY_TOKEN}`;
  try {
    const r = await fetch(new URL('/', u), { method: 'POST', headers, body: JSON.stringify(msg), signal: AbortSignal.timeout(10e3) });
    if (!r.ok) log(`ntfy answered ${r.status}`);
  } catch (e) { log('ntfy:', e.message); }
}

const qNewEvents = store.raw.prepare('SELECT rowid AS r, ts, account, kind, text, cwd, url FROM events WHERE rowid > ? ORDER BY rowid LIMIT 200');
const qMaxEvent = store.raw.prepare('SELECT coalesce(max(rowid), 0) AS r FROM events');
function pushNewEvents() {
  let cur = Number(store.kv.get('ntfy:events'));
  if (!Number.isFinite(cur) || store.kv.get('ntfy:events') == null) { store.kv.set('ntfy:events', qMaxEvent.get().r); return; } // never flood on first start
  const rows = qNewEvents.all(cur);
  if (!rows.length) return;
  store.kv.set('ntfy:events', rows[rows.length - 1].r);
  for (const e of rows) {
    if (QUIET.has(e.kind) || Date.now() - e.ts > 15 * 60e3) continue; // stale backfill isn't actionable
    ntfy({
      title: `${e.account || 'Claude Code'} · ${e.kind}${e.cwd ? ' · ' + e.cwd : ''}`,
      body: e.text || 'Claude Code needs you',
      click: e.url || PUBLIC_URL || undefined,
      tags: e.kind === 'permission' ? 'lock' : 'bell',
      priority: e.kind === 'permission' || e.kind === 'elicitation' ? 4 : 3,
    });
  }
}
// limit alerts: once per window cycle per threshold
const WATCH = new Set(['five_hour', 'seven_day']);
function pushLimitAlerts(ov) {
  if (!ALERTS.length) return;
  for (const a of ov.accounts) {
    if (!a.sampledAt || Date.now() - a.sampledAt > 30 * 60e3) continue;
    for (const w of a.windows) {
      if (!WATCH.has(w.key) || !(w.pct >= ALERTS[0])) continue;
      const level = ALERTS.filter((t) => w.pct >= t).pop();
      const cycle = w.resetsAt ? Math.round(Date.parse(w.resetsAt) / 600e3) : 'none';
      const k = `alert:${a.id}:${w.key}:${cycle}`;
      if (Number(store.kv.get(k)) >= level) continue;
      store.kv.set(k, level);
      const left = w.resetsAt ? Math.max(0, Date.parse(w.resetsAt) - Date.now()) : null;
      ntfy({
        title: `${a.label}: ${w.label || w.key} at ${Math.round(w.pct)}%`,
        body: left != null ? `Resets in ${fmtDur(left)}.` : 'Limit window filling up.',
        click: PUBLIC_URL || undefined, tags: level >= 95 ? 'rotating_light' : 'warning', priority: level >= 95 ? 4 : 3,
      });
    }
  }
}
const fmtDur = (ms) => { const t = Math.round(ms / 60e3), h = Math.floor(t / 60), m = t % 60; return h >= 24 ? `${Math.floor(h / 24)}d ${h % 24}h` : h ? `${h}h ${m}m` : `${m}m`; };
function afterWrite() { try { pushNewEvents(); pushLimitAlerts(overview()); } catch (e) { log('push:', e.message); } }

// ---------- hooks straight from a machine without the desktop app ----------
const qAcctByEmail = store.raw.prepare('SELECT id, label FROM accounts WHERE lower(email) = ?');
const qAcctByLabel = store.raw.prepare('SELECT id, label FROM accounts WHERE lower(label) = ?');
function hook(url, p) {
  const email = String(url.searchParams.get('email') || '').toLowerCase();
  const label0 = String(url.searchParams.get('account') || '').slice(0, 60);
  const acct = (email && qAcctByEmail.get(email)) || (label0 && qAcctByLabel.get(label0.toLowerCase())) || null;
  const label = acct ? acct.label : (label0 || email || 'unknown');
  const ts = Date.now();
  if (p.hook_event_name === 'SessionStart') {
    store.upsertSession({ sessionId: p.session_id, accountId: acct && acct.id, account: label, ts, cwd: p.cwd, transcriptPath: p.transcript_path, model: p.model });
    return;
  }
  const kind = p.hook_event_name === 'Stop' ? 'finished' : String(p.notification_type || 'notification').replace(/_(prompt|dialog)$/, '');
  const text = p.hook_event_name === 'Stop' ? String(p.last_assistant_message || 'Finished').replace(/\s+/g, ' ').slice(0, 160) : String(p.message || '').slice(0, 200);
  store.addEvent({ ts, account: label, accountId: acct && acct.id, kind, text, cwd: p.cwd ? path.basename(String(p.cwd)) : '', session: p.session_id }, p);
}

// ---------- http ----------
const WEB = path.join(ROOT, 'web');
const STATIC = {
  '/': ['index.html', 'text/html; charset=utf-8'],
  '/web/app.js': ['app.js', 'text/javascript; charset=utf-8'],
  '/web/app.css': ['app.css', 'text/css; charset=utf-8'],
  '/web/charts.js': ['charts.js', 'text/javascript; charset=utf-8'],
  '/web/analytics.js': ['analytics.js', 'text/javascript; charset=utf-8'],
  '/web/analytics.css': ['analytics.css', 'text/css; charset=utf-8'],
  '/manifest.webmanifest': ['manifest.webmanifest', 'application/manifest+json'],
  '/icon.png': [path.join('..', 'assets', 'icon.png'), 'image/png'],
};
const SECURITY = {
  'Content-Security-Policy': "default-src 'self'; style-src 'self'; script-src 'self'; img-src 'self' data:; connect-src 'self'; manifest-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'",
  'X-Content-Type-Options': 'nosniff',
  'Referrer-Policy': 'no-referrer',
  'X-Frame-Options': 'DENY',
  'Cross-Origin-Opener-Policy': 'same-origin',
};
function send(res, code, body, type = 'application/json; charset=utf-8', extra = {}) {
  const buf = Buffer.isBuffer(body) ? body : Buffer.from(typeof body === 'string' ? body : JSON.stringify(body));
  res.writeHead(code, { ...SECURITY, 'Content-Type': type, 'Content-Length': buf.length, 'Cache-Control': 'no-store', ...extra });
  res.end(buf);
}
function readBody(req, max) {
  return new Promise((resolve, reject) => {
    const chunks = []; let n = 0;
    req.on('data', (c) => { n += c.length; if (n > max) { reject(Object.assign(new Error('body too large'), { code: 413 })); req.destroy(); } else chunks.push(c); });
    req.on('end', () => resolve(Buffer.concat(chunks)));
    req.on('error', reject);
  });
}
const json = (buf) => { try { return JSON.parse(buf.toString('utf8') || '{}'); } catch { throw Object.assign(new Error('invalid JSON'), { code: 400 }); } };
const num = (v) => (v == null || v === '' ? undefined : Number.isFinite(Number(v)) ? Number(v) : undefined);

async function route(req, res) {
  const url = new URL(req.url, 'http://x');
  const p = url.pathname;
  const ip = req.socket.remoteAddress || '?';

  if (req.method === 'GET' && p === '/healthz') return send(res, 200, { ok: true, version: VERSION });
  if ((req.method === 'GET' || req.method === 'HEAD') && STATIC[p]) {
    const [f, type] = STATIC[p];
    return send(res, 200, fs.readFileSync(path.join(WEB, f)), type, { 'Cache-Control': p === '/' ? 'no-store' : 'no-cache' });
  }

  if (req.method === 'POST' && p === '/login') {
    if (throttled(ip)) return send(res, 429, { error: 'too many attempts, try again in a few minutes' });
    const body = json(await readBody(req, 4096));
    if (!same(String(body.token || ''), TOKEN)) { failed(ip); return send(res, 401, { error: 'wrong token' }); }
    const attrs = `${COOKIE}=${cookieValue}; Path=/; HttpOnly; SameSite=Strict; Max-Age=${90 * 86400}${isHttps(req) ? '; Secure' : ''}`;
    return send(res, 200, { ok: true }, undefined, { 'Set-Cookie': attrs });
  }
  if (req.method === 'POST' && p === '/logout') return send(res, 200, { ok: true }, undefined, { 'Set-Cookie': `${COOKIE}=; Path=/; HttpOnly; SameSite=Strict; Max-Age=0` });

  // everything below needs the token
  if (p.startsWith('/api/') || p === '/hook') {
    // writes only take the bearer token: a browser cookie must never be able to post data
    const write = req.method === 'POST';
    if (!authed(req, { cookie: !write })) { if (!throttled(ip)) failed(ip); return send(res, 401, { error: 'unauthorized' }); }
  }

  if (req.method === 'GET' && p === '/api/overview') return send(res, 200, overview());
  if (req.method === 'GET' && p === '/api/analytics') {
    const q = { from: num(url.searchParams.get('from')), to: num(url.searchParams.get('to')), tz: num(url.searchParams.get('tz')) };
    const a = url.searchParams.get('accounts');
    if (a) q.accounts = a.split(',').map((s) => s.trim()).filter(Boolean).slice(0, 50);
    for (const k of Object.keys(q)) if (q[k] === undefined) delete q[k];
    return send(res, 200, await analytics(q));
  }
  if (req.method === 'POST' && p === '/api/ingest') {
    const body = json(await readBody(req, 128 << 20));
    store.applyChanges(body);
    store.kv.set('ingest:at', Date.now());
    if (req.headers['user-agent']) store.kv.set('ingest:from', String(req.headers['user-agent']).slice(0, 80));
    afterWrite();
    return send(res, 200, { ok: true });
  }
  if (req.method === 'POST' && p === '/api/ntfy-test') {
    if (!NTFY_URL) return send(res, 400, { error: 'NTFY_URL is not set' });
    await ntfy({ title: 'Switchyard test', body: 'Push notifications work.', click: PUBLIC_URL || undefined, tags: 'white_check_mark' });
    return send(res, 200, { ok: true });
  }
  if (req.method === 'POST' && p === '/hook') {
    let payload = {};
    try { payload = json(await readBody(req, 1 << 20)); } catch {}
    hook(url, payload);
    afterWrite();
    res.writeHead(204, SECURITY).end();
    return;
  }
  send(res, 404, { error: 'not found' });
}

const server = http.createServer((req, res) => {
  route(req, res).catch((e) => {
    if (e.code !== 400 && e.code !== 413) log(`${req.method} ${req.url}:`, e.message);
    if (!res.headersSent) send(res, e.code === 400 || e.code === 413 ? e.code : 500, { error: e.code ? e.message : 'internal error' });
  });
});
server.requestTimeout = 300e3;

let stopping = false;
function stop() {
  if (stopping) return; stopping = true;
  log('shutting down');
  server.close();
  if (worker) worker.terminate();
  setTimeout(() => { try { store.close(); } catch {} process.exit(0); }, 300);
}
process.on('SIGTERM', stop);
process.on('SIGINT', stop);

startWorker();
pushNewEvents(); // sets the cursor on first start
server.listen(PORT, () => log(`switchyard server ${VERSION} on :${PORT} · data ${DATA} · scan ${SCAN_HOME || SCAN_DIRS.join(',') || 'off'} · ntfy ${NTFY_URL ? 'on' : 'off'}`));
