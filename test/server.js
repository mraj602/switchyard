// Server test: starts server/server.js on a temp data dir and checks auth, ingest, overview,
// analytics, hooks and the static web UI.   node test/server.js
const { spawn } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const data = fs.mkdtempSync(path.join(os.tmpdir(), 'sy-srv-'));
const port = 48700 + Math.floor(Math.random() * 100);
const TOKEN = 'server-test-token-0123456789';
const base = `http://127.0.0.1:${port}`;
const ok = (c, m) => { console.log(`${c ? 'PASS' : 'FAIL'}  ${m}`); if (!c) process.exitCode = 1; };
const auth = { authorization: `Bearer ${TOKEN}` };
const j = (r) => r.json();

(async () => {
  const short = spawn(process.execPath, [path.join(__dirname, '..', 'server', 'server.js')], { env: { ...process.env, SWITCHYARD_TOKEN: 'short', SWITCHYARD_DATA: data, PORT: String(port) } });
  ok(await new Promise((r) => short.on('exit', r)) === 1, 'refuses to start with a short token');

  const srv = spawn(process.execPath, [path.join(__dirname, '..', 'server', 'server.js')], { env: { ...process.env, SWITCHYARD_TOKEN: TOKEN, SWITCHYARD_DATA: data, PORT: String(port), NODE_NO_WARNINGS: '1' }, stdio: ['ignore', 'pipe', 'inherit'] });
  await new Promise((r) => srv.stdout.on('data', (d) => /on :\d+/.test(d) && r()));
  try {
    ok((await fetch(`${base}/healthz`).then(j)).ok === true, 'healthz');
    const home = await fetch(`${base}/`);
    ok(home.status === 200 && /Content-Security-Policy/i.test([...home.headers.keys()].join(' ')), 'web UI served with CSP');
    ok((await fetch(`${base}/web/../server/server.js`)).status === 404, 'no path traversal');
    ok((await fetch(`${base}/api/overview`)).status === 401, 'overview needs auth');

    const now = Date.now();
    const p = {
      accounts: [{ id: 'a1', label: 'Work', color: '#3987e5', email: 'w@example.com', ord: 0 }],
      snapshots: [[now, 'a1', 'w@example.com', 'org-1', 'Org', 'Max 5x', '{}']],
      windows: [[now, 'a1', 'five_hour', '5-hour', 42, new Date(now + 3600e3).toISOString(), null, null], [now, 'a1', 'seven_day', '7-day', 12, new Date(now + 5 * 864e5).toISOString(), null, null]],
      events: [[now, 'a1', 'Work', 'permission', 'Claude needs your permission', 'repo', 's1', '{}', null]],
      tokens: [['m1', now - 60e3, 's1', '/x/repo', 'claude-opus-5-5', 10, 2000, 0, 5000, 90000, 0, 0]],
      sessions: [{ session_id: 's1', account_id: 'a1', account: 'Work', started_at: now - 120e3, cwd: '/x/repo' }],
    };
    ok((await fetch(`${base}/api/ingest`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(p) })).status === 401, 'ingest needs the token');
    ok((await fetch(`${base}/api/ingest`, { method: 'POST', headers: { ...auth, 'content-type': 'application/json' }, body: JSON.stringify(p) })).status === 200, 'ingest accepted');
    await fetch(`${base}/api/ingest`, { method: 'POST', headers: { ...auth, 'content-type': 'application/json' }, body: JSON.stringify(p) });

    const ov = await fetch(`${base}/api/overview`, { headers: auth }).then(j);
    ok(ov.accounts.length === 1 && ov.accounts[0].windows.length === 2, 'overview has the account and its windows');
    ok(ov.events.length === 1, 're-sent rows are not duplicated');

    const login = await fetch(`${base}/login`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ token: TOKEN }) });
    const cookie = (login.headers.get('set-cookie') || '').split(';')[0];
    ok(login.status === 200 && /HttpOnly/.test(login.headers.get('set-cookie')), 'sign-in sets an HttpOnly cookie');
    ok((await fetch(`${base}/api/overview`, { headers: { cookie } })).status === 200, 'cookie reads the overview');
    ok((await fetch(`${base}/api/ingest`, { method: 'POST', headers: { cookie }, body: '{}' })).status === 401, 'cookie cannot write');
    ok((await fetch(`${base}/login`, { method: 'POST', body: JSON.stringify({ token: 'wrong' }) })).status === 401, 'wrong token rejected');

    const an = await fetch(`${base}/api/analytics?from=1&tz=0`, { headers: auth }).then(j);
    ok(an.tokens && an.tokens.totals.cost > 0, `analytics prices ingested tokens ($${an.tokens && an.tokens.totals.cost.toFixed(4)})`);

    const h = await fetch(`${base}/hook?email=w%40example.com`, { method: 'POST', headers: { ...auth, 'content-type': 'application/json' }, body: JSON.stringify({ hook_event_name: 'Notification', notification_type: 'idle_prompt', message: 'waiting', session_id: 's2' }) });
    const ov2 = await fetch(`${base}/api/overview`, { headers: auth }).then(j);
    ok(h.status === 204 && ov2.events.some((e) => e.kind === 'idle' && e.account === 'Work'), 'hook maps the email to the account');
  } finally {
    srv.kill('SIGTERM');
    await new Promise((r) => srv.on('exit', r));
    fs.rmSync(data, { recursive: true, force: true });
  }
})().catch((e) => { console.error(e); process.exit(1); });
