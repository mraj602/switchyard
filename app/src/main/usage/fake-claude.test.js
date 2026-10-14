// Tests of the fake claude.ai itself (test/fakes/claude/), so the smoke and e2e tests of
// wave 2 can trust it.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';

import { startFakeClaude, uuidOf } from '../../../test/fakes/claude/server.js';

let fake;
before(async () => {
  fake = await startFakeClaude({ orgs: 2, active: 1 });
});
after(() => fake.close());

const post = (p, body) => fetch(`${fake.origin}${p}`, { method: 'POST', body: JSON.stringify(body), headers: { 'content-type': 'application/json' } });
const stats = async () => (await fetch(`${fake.origin}/_fake/stats`)).json();

async function login() {
  const r = await fetch(`${fake.origin}/login`, { method: 'POST', redirect: 'manual', body: 'email=a' });
  assert.equal(r.status, 302);
  assert.equal(r.headers.get('location'), '/new');
  const cookies = r.headers.getSetCookie().map((c) => c.split(';')[0]);
  return { cookie: cookies.join('; '), cookies, session: cookies[0].split('=')[1] };
}

test('login page, form login, app page, logout', async () => {
  const page = await (await fetch(`${fake.origin}/login`)).text();
  assert.match(page, /<title>Fake Claude - Sign in<\/title>/);
  assert.match(page, /<form method="post" action="\/login">/);
  const noSession = await fetch(`${fake.origin}/new`, { redirect: 'manual' });
  assert.equal(noSession.status, 302);
  assert.equal(noSession.headers.get('location'), '/login');
  const { cookie, cookies } = await login();
  assert.match(cookies[0], /^sessionKey=sk-/);
  assert.equal(cookies[1], `lastActiveOrg=${uuidOf('org', 2)}`);
  const app = await fetch(`${fake.origin}/new`, { headers: { cookie } });
  assert.equal(app.status, 200);
  const html = await app.text();
  assert.match(html, /<title>Claude<\/title>/);
  assert.match(html, /history\.pushState/, 'in-page navigation');
  const out = await fetch(`${fake.origin}/logout`, { redirect: 'manual', headers: { cookie } });
  assert.match(out.headers.getSetCookie()[0], /sessionKey=; .*Max-Age=0/);
});

test('api: 401 without a session; organizations, usage in turn, account', async () => {
  await post('/_fake/script', { reset: true });
  assert.equal((await fetch(`${fake.origin}/api/organizations`)).status, 401);
  const { cookie, session } = await login();
  const h = { headers: { cookie } };
  const orgs = await (await fetch(`${fake.origin}/api/organizations`, h)).json();
  assert.equal(orgs.length, 2);
  assert.deepEqual(orgs.map((o) => o.uuid), [uuidOf('org', 1), uuidOf('org', 2)]);
  for (const o of orgs) assert.ok(/^[0-9a-f-]{36}$/.test(o.uuid));
  const u1 = await (await fetch(`${fake.origin}/api/organizations/${orgs[0].uuid}/usage`, h)).text();
  const u2 = await (await fetch(`${fake.origin}/api/organizations/${orgs[1].uuid}/usage`, h)).text();
  assert.equal(u1, fake.fixtureText(0));
  assert.equal(u2, fake.fixtureText(1));
  assert.equal((await fetch(`${fake.origin}/api/organizations/${uuidOf('org', 9)}/usage`, h)).status, 404);
  const acct = await (await fetch(`${fake.origin}/api/account`, h)).json();
  assert.equal(acct.email_address, 'a@example.com');
  const s = await stats();
  assert.equal(s.paths.usage, 3, 'counted per path (the 404 too)');
  assert.deepEqual(s.sessions[session], { organizations: 1, usage: 3, account: 1 });
  assert.equal(s.sessions['(none)'].organizations, 1);
});

test('script: the next status per path, delay, times; reset clears', async () => {
  await post('/_fake/script', { reset: true });
  const { cookie } = await login();
  const h = { headers: { cookie } };
  await post('/_fake/script', { path: 'usage', status: 429, times: 2 });
  await post('/_fake/script', { path: 'account', status: 500 });
  await post('/_fake/script', { path: 'organizations', delay: 150 });
  const t0 = Date.now();
  assert.equal((await fetch(`${fake.origin}/api/organizations`, h)).status, 200);
  assert.ok(Date.now() - t0 >= 140, 'delayed');
  const org = uuidOf('org', 1);
  assert.equal((await fetch(`${fake.origin}/api/organizations/${org}/usage`, h)).status, 429);
  assert.equal((await fetch(`${fake.origin}/api/organizations/${org}/usage`, h)).status, 429);
  assert.equal((await fetch(`${fake.origin}/api/organizations/${org}/usage`, h)).status, 200);
  assert.equal((await fetch(`${fake.origin}/api/account`, h)).status, 500);
  assert.equal((await fetch(`${fake.origin}/api/account`, h)).status, 200);
  assert.equal((await post('/_fake/script', { path: 'nope' })).status, 400);
  await post('/_fake/script', { path: 'usage', status: 401 });
  await post('/_fake/script', { reset: true });
  assert.equal((await fetch(`${fake.origin}/api/organizations/${org}/usage`, h)).status, 200);
  assert.equal((await stats()).total, 1);
});

test('one org, and the first org active by default', async () => {
  const one = await startFakeClaude();
  try {
    const r = await fetch(`${one.origin}/login`, { method: 'POST', redirect: 'manual', body: 'x' });
    const cookies = r.headers.getSetCookie().map((c) => c.split(';')[0]);
    assert.equal(cookies[1], `lastActiveOrg=${uuidOf('org', 1)}`);
    const orgs = await (await fetch(`${one.origin}/api/organizations`, { headers: { cookie: cookies.join('; ') } })).json();
    assert.equal(orgs.length, 1);
  } finally {
    await one.close();
  }
});
