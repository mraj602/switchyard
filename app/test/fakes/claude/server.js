// An HTTP fake of claude.ai for unit, smoke and e2e tests (docs/spec/desktop.md C5). It never
// talks to the real claude.ai. Point the app at it with SY_APP_TEST_CLAUDE_ORIGIN=<origin>.
//
//   const fake = await startFakeClaude({ orgs: 2 });
//   fake.origin            // http://127.0.0.1:<port>
//   await fake.close();
//
// Pages: GET /login (form: email → POST /login sets the `sessionKey` cookie and redirects to /new),
// GET / and /new (the app page: title "Claude", buttons that change the URL in-page with
// history.pushState; redirects to /login without a session), GET /logout (clears the cookie).
// API (401 without a session): GET /api/organizations (one or two orgs), GET
// /api/organizations/{uuid}/usage (testdata/usage/response-0N.json, in turn), GET /api/account.
// Cookies: `sessionKey` (the session) and `lastActiveOrg` (the first org's uuid, or the second
// with { active: 1 }).
// Control: GET /_fake/stats → {total, paths: {<key>: n}, sessions: {<sessionKey>: {<key>: n}}}
// POST /_fake/script {path: 'organizations'|'usage'|'account', status?, delay?, times?} queues
// responses for the next requests of that path (FIFO); POST /_fake/script {reset: true} clears
// the queue and the stats. Path keys: organizations, usage, account.

import http from 'node:http';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
export const USAGE_DIR = path.resolve(here, '../../../../testdata/usage');

/** Deterministic uuids for a session and org index (36 chars, lowercase hex and dashes). */
export const uuidOf = (kind, n) => {
  const k = { org: 'a', acct: 'b' }[kind] ?? 'c';
  const tail = String(n).padStart(12, '0');
  return `${k.repeat(8)}-${k.repeat(4)}-4${k.repeat(3)}-8${k.repeat(3)}-${tail}`;
};

const PAGE = (title, body, script = '') =>
  `<!doctype html><html><head><meta charset="utf-8"><title>${title}</title></head><body>${body}<script>${script}</script></body></html>`;

/**
 * @param {{port?: number, orgs?: 1|2, active?: 0|1, personalFree?: boolean, fixtures?: string[], email?: string}} [opts]
 */
export async function startFakeClaude(opts = {}) {
  const orgCount = opts.orgs ?? 1;
  const activeIdx = opts.active ?? 0;
  const email = opts.email ?? 'a@example.com';
  const files =
    opts.fixtures ??
    fs
      .readdirSync(USAGE_DIR)
      .filter((f) => /^response-\d+\.json$/.test(f))
      .sort();
  const fixtures = files.map((f) => fs.readFileSync(path.join(USAGE_DIR, f), 'utf8'));
  let served = 0;

  const stats = { total: 0, paths: {}, sessions: {} };
  /** @type {Record<string, {status?: number, delay?: number}[]>} */
  const queues = {};
  const sessionsSeen = new Set();

  const cookieOf = (req, name) => {
    const m = (req.headers.cookie ?? '').split(/;\s*/).find((c) => c.startsWith(`${name}=`));
    return m ? decodeURIComponent(m.slice(name.length + 1)) : null;
  };
  const orgList = () =>
    Array.from({ length: orgCount }, (_, i) => ({
      uuid: uuidOf('org', i + 1),
      name: i === 0 ? `${email}'s Organization` : 'Team org',
      capabilities: i === 0 ? (opts.personalFree ? ['chat'] : ['chat', 'claude_max']) : ['chat', 'raven'],
      rate_limit_tier: i === 0 ? (opts.personalFree ? 'default' : 'default_claude_max_5x') : 'default_raven',
      billing_type: 'stripe_subscription',
      raven_type: i === 0 ? null : 'team',
      settings: { not_a_kept_key: true },
      api_disabled_reason: null,
    }));

  const json = (res, status, body) => {
    res.writeHead(status, { 'content-type': 'application/json' });
    res.end(JSON.stringify(body));
  };
  const readBody = (req) =>
    new Promise((resolve) => {
      let b = '';
      req.on('data', (c) => (b += c));
      req.on('end', () => resolve(b));
    });

  const server = http.createServer(async (req, res) => {
    const url = new URL(req.url, 'http://x');
    const p = url.pathname;
    const session = cookieOf(req, 'sessionKey');

    // ---- control
    if (p === '/_fake/stats') return json(res, 200, stats);
    if (p === '/_fake/script' && req.method === 'POST') {
      let body = {};
      try {
        body = JSON.parse(await readBody(req));
      } catch {
        return json(res, 400, { error: 'bad json' });
      }
      if (body.reset) {
        for (const k of Object.keys(queues)) delete queues[k];
        stats.total = 0;
        stats.paths = {};
        stats.sessions = {};
        served = 0;
        return json(res, 200, { ok: true });
      }
      if (!['organizations', 'usage', 'account'].includes(body.path)) return json(res, 400, { error: 'bad path' });
      for (let i = 0; i < (body.times ?? 1); i++) (queues[body.path] ??= []).push({ status: body.status, delay: body.delay });
      return json(res, 200, { ok: true, queued: queues[body.path].length });
    }

    // ---- pages
    if (p === '/login' && req.method === 'GET') {
      res.writeHead(200, { 'content-type': 'text/html' });
      return res.end(PAGE('Fake Claude - Sign in', '<form method="post" action="/login"><input name="email" value="a@example.com"><button id="go">Continue</button></form>'));
    }
    if (p === '/login' && req.method === 'POST') {
      await readBody(req);
      const key = `sk-${sessionsSeen.size + 1}-${Math.random().toString(16).slice(2, 8)}`;
      sessionsSeen.add(key);
      res.writeHead(302, {
        location: '/new',
        // Persistent, as claude.ai's: a session cookie would not survive a restart (smoke S7).
        'set-cookie': [`sessionKey=${key}; Path=/; HttpOnly; Max-Age=2592000`, `lastActiveOrg=${uuidOf('org', activeIdx + 1)}; Path=/; Max-Age=2592000`],
      });
      return res.end();
    }
    if (p === '/logout') {
      res.writeHead(302, { location: '/login', 'set-cookie': ['sessionKey=; Path=/; Max-Age=0'] });
      return res.end();
    }
    if (p === '/' || p === '/new' || p.startsWith('/chat/') || p.startsWith('/settings')) {
      if (!session) {
        res.writeHead(302, { location: '/login' });
        return res.end();
      }
      res.writeHead(200, { 'content-type': 'text/html' });
      return res.end(
        PAGE(
          'Claude',
          '<button id="nav">In-page navigation</button><a id="ext" href="https://example.com/">external</a>',
          "document.getElementById('nav').onclick=()=>history.pushState({},'','/chat/'+Date.now());",
        ),
      );
    }

    // ---- api
    const m = /^\/api\/(organizations(?:\/([0-9a-f-]{36})\/usage)?|account)$/.exec(p);
    if (!m) {
      res.writeHead(404);
      return res.end('not found');
    }
    const key = m[2] ? 'usage' : m[1] === 'account' ? 'account' : 'organizations';
    stats.total += 1;
    stats.paths[key] = (stats.paths[key] ?? 0) + 1;
    const sid = session ?? '(none)';
    stats.sessions[sid] ??= {};
    stats.sessions[sid][key] = (stats.sessions[sid][key] ?? 0) + 1;

    const scripted = queues[key]?.shift();
    if (scripted?.delay) await new Promise((r) => setTimeout(r, scripted.delay));
    if (scripted?.status && scripted.status !== 200) return json(res, scripted.status, { error: { type: 'scripted', status: scripted.status } });
    if (!session) return json(res, 401, { error: { type: 'authentication_error' } });

    if (key === 'organizations') return json(res, 200, orgList());
    if (key === 'account') {
      return json(res, 200, {
        uuid: uuidOf('acct', 1),
        email_address: email,
        full_name: 'Test Person',
        display_name: 'Test',
        phone_number: '+0000000000', // not a kept key
      });
    }
    if (!orgList().some((o) => o.uuid === m[2])) return json(res, 404, { error: 'no such org' });
    const body = fixtures[served % fixtures.length];
    served += 1;
    res.writeHead(200, { 'content-type': 'application/json' });
    return res.end(body);
  });

  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(opts.port ?? 0, '127.0.0.1', resolve);
  });
  const port = server.address().port;
  return {
    origin: `http://127.0.0.1:${port}`,
    port,
    stats,
    /** Queue the next response(s) of a path: {status?, delay?, times?}. */
    script(pathKey, s) {
      for (let i = 0; i < (s.times ?? 1); i++) (queues[pathKey] ??= []).push({ status: s.status, delay: s.delay });
    },
    orgs: orgList,
    /** The raw fixture text served for the nth usage request (0-based, cycling). */
    fixtureText: (n) => fixtures[n % fixtures.length],
    fixtureCount: fixtures.length,
    close: () =>
      new Promise((resolve) => {
        server.closeAllConnections?.();
        server.close(() => resolve());
      }),
  };
}
