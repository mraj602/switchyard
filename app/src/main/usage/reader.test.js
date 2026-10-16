import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';

import { checkResult, READER_SCRIPT, readTab, stopMessage } from './reader.js';
import { fakeElectron } from '../../../test/fakes/electron.js';
import { fakeBrowser, signInFake } from '../../../test/fakes/claude/browser.js';
import { startFakeClaude, uuidOf } from '../../../test/fakes/claude/server.js';
import { ACCOUNT_KEYS, ORG_KEYS } from '../../shared/record.js';
import { READER_MAX_BYTES } from '../../shared/constants.js';

let fake;
before(async () => {
  fake = await startFakeClaude({ orgs: 2, active: 1 });
});
after(() => fake.close());

async function tab(cookies, over = {}) {
  const browser = fakeBrowser({ origin: fake.origin, cookies });
  const f = fakeElectron({ executeJavaScript: over.exec ?? browser.exec });
  const view = new f.electron.WebContentsView({ webPreferences: { partition: 'persist:tab-t_00' } });
  const wc = view.webContents;
  for (const [name, value] of Object.entries(cookies)) await wc.session.cookies.set({ url: fake.origin, name, value });
  return { wc, browser, f };
}

test('the script reads the three endpoints and returns the raw objects', async () => {
  const cookies = await signInFake(fake.origin);
  const { wc, browser } = await tab(cookies);
  const raw = await readTab(wc, { claudeOrigin: fake.origin });
  assert.deepEqual(browser.calls.map((c) => c.world), [1001], 'the isolated world, once');
  assert.equal(raw.orgs.length, 2);
  for (const o of raw.orgs) {
    assert.deepEqual(Object.keys(o.org).sort(), [...ORG_KEYS].sort());
    assert.equal(typeof o.usage.five_hour, 'object');
  }
  assert.deepEqual(Object.keys(raw.account).sort(), [...ACCOUNT_KEYS].sort());
  assert.equal(raw.active_org, uuidOf('org', 2), 'from the lastActiveOrg cookie of the tab\'s own session');
  assert.ok(Number.isInteger(raw.fetch_ms));
  const res = checkResult(raw);
  assert.equal(res.kind, 'ok');
  assert.equal(res.activeOrg, uuidOf('org', 2));
  assert.equal(res.fetchMs, raw.fetch_ms);
  // The usage is whole: byte-equal to what the fake served.
  assert.equal(JSON.stringify(res.orgs[0].usage), JSON.stringify(JSON.parse(fake.fixtureText(fake.stats.paths.usage - 2))));
});

test('the script reads numbers only: no cookie, no chat endpoint', () => {
  assert.doesNotMatch(READER_SCRIPT, /document\.cookie|localStorage|\/chat_conversations|\/messages/);
  assert.deepEqual([...READER_SCRIPT.matchAll(/'(\/api\/[^']*)'/g)].map((m) => m[1]), ['/api/organizations', '/api/organizations/', '/api/account']);
});

test('active_org is null with no cookie', async () => {
  const cookies = await signInFake(fake.origin);
  const { wc } = await tab({ sessionKey: cookies.sessionKey });
  const raw = await readTab(wc, { claudeOrigin: fake.origin });
  assert.equal(raw.active_org, null);
});

test('an isolated world that throws is retried once in the main world', async () => {
  const cookies = await signInFake(fake.origin);
  const browser = fakeBrowser({ origin: fake.origin, cookies });
  let isolatedCalls = 0;
  const exec = async (wc, code, world) => {
    if (world === 1001) {
      isolatedCalls += 1;
      throw new Error('world gone');
    }
    return browser.exec(wc, code, world);
  };
  const { wc } = await tab(cookies, { exec });
  const debug = [];
  const raw = await readTab(wc, { claudeOrigin: fake.origin, log: { debug: (...a) => debug.push(a.join(' ')) } });
  assert.equal(isolatedCalls, 1);
  assert.deepEqual(browser.calls.map((c) => c.world), ['main']);
  assert.equal(checkResult(raw).kind, 'ok');
  assert.ok(debug.some((d) => d.includes('reader: "main"')));
});

test('both worlds throwing is an error for the caller (it backs off)', async () => {
  const { wc } = await tab({}, {
    exec: async () => {
      throw new Error('fetch failed');
    },
  });
  await assert.rejects(readTab(wc, { claudeOrigin: fake.origin }), /fetch failed/);
});

test('a read that never answers times out', async () => {
  const { wc } = await tab({}, { exec: () => new Promise(() => {}) });
  await assert.rejects(readTab(wc, { claudeOrigin: fake.origin, timeoutMs: 20 }), /timed out/);
});

test('a timeout in the isolated world is not retried in the main world (its requests may still run)', async () => {
  const worlds = [];
  const { wc } = await tab({}, {
    exec: (_wc, _code, world) => {
      worlds.push(world ?? 'main');
      return new Promise(() => {});
    },
  });
  await assert.rejects(readTab(wc, { claudeOrigin: fake.origin, timeoutMs: 20 }), /timed out/);
  assert.deepEqual(worlds, [1001]);
});

test('a network error inside the script is a result (no throw), so the reads are never doubled', async () => {
  const before = fake.stats.paths.usage;
  const browser = fakeBrowser({ origin: 'http://127.0.0.1:9', cookies: {} }); // nothing listens there
  const worlds = [];
  const { wc } = await tab({}, {
    exec: (w, code, world) => {
      worlds.push(world ?? 'main');
      return browser.exec(w, code, world);
    },
  });
  const raw = await readTab(wc, { claudeOrigin: fake.origin });
  assert.deepEqual(worlds, [1001], 'no main-world retry');
  assert.deepEqual(checkResult(raw), { kind: 'retry', message: 'network error' });
  assert.equal(fake.stats.paths.usage, before);
});

test('a main-world result must be for the tab\'s known seat account; with no seat yet it is taken', async () => {
  const cookies = await signInFake(fake.origin);
  const browser = fakeBrowser({ origin: fake.origin, cookies });
  const exec = async (wc, code, world) => {
    if (world === 1001) throw new Error('world gone');
    return browser.exec(wc, code, world);
  };
  const { wc } = await tab(cookies, { exec });
  const first = await readTab(wc, { claudeOrigin: fake.origin, expectAccount: null });
  assert.equal(checkResult(first).kind, 'ok', 'seat unknown: the check is skipped');
  const same = await readTab(wc, { claudeOrigin: fake.origin, expectAccount: first.account.uuid });
  assert.equal(same.account.uuid, first.account.uuid);
  await assert.rejects(readTab(wc, { claudeOrigin: fake.origin, expectAccount: uuidOf('acct', 9) }), /another account/);
  // the isolated world's result is not checked against the seat (the page cannot touch it)
  const { wc: wc2 } = await tab(cookies);
  assert.equal(checkResult(await readTab(wc2, { claudeOrigin: fake.origin, expectAccount: uuidOf('acct', 9) })).kind, 'ok');
});

const A = uuidOf('org', 1);
const B = uuidOf('org', 2);
const acct = { uuid: uuidOf('acct', 1), email_address: 'a@example.com' };
const ok = () => ({ orgs: [{ org: { uuid: A, name: 'x' }, usage: { five_hour: null } }], account: acct, active_org: A, fetch_ms: 5 });

test('checkResult: ok keeps usage whole and picks the listed keys', () => {
  const raw = ok();
  raw.orgs[0].org.extra = 1;
  raw.account = { ...acct, phone: 'x' };
  const r = checkResult(raw);
  assert.equal(r.kind, 'ok');
  assert.deepEqual(r.orgs[0].org, { uuid: A, name: 'x' });
  assert.deepEqual(r.account, acct);
  assert.equal(r.orgs[0].usage, raw.orgs[0].usage);
});

test('checkResult: 401, 403 (when nothing is left to read) and 429 stop; other statuses retry', () => {
  for (const status of [401, 403, 429]) {
    const a = ok();
    a.orgs[0].usage = { __status: status };
    assert.deepEqual(checkResult(a), { kind: 'stop', status, message: stopMessage(status) });
    assert.equal(checkResult({ orgs: { __status: status }, account: null }).status, status);
    assert.equal(checkResult({ ...ok(), account: { __status: status } }).status, status);
  }
  for (const status of [500, 502, 404, 0]) {
    const a = ok();
    a.orgs[0].usage = { __status: status };
    assert.equal(checkResult(a).kind, 'retry', String(status));
  }
  // a stop wins over a retry
  const two = { orgs: [{ org: { uuid: A }, usage: { __status: 500 } }, { org: { uuid: B }, usage: { __status: 429 } }], account: acct };
  assert.equal(checkResult(two).kind, 'stop');
});

test('checkResult: bad shapes retry', () => {
  for (const bad of [
    null, 'x', [], {}, { orgs: [], account: acct }, { orgs: 'x', account: acct },
    { orgs: [{ org: { uuid: 'not-a-uuid' }, usage: {} }], account: acct },
    { orgs: [{ org: {}, usage: {} }], account: acct },
    { orgs: [{ org: { uuid: A }, usage: [] }], account: acct },
    { orgs: [{ org: { uuid: A }, usage: {} }], account: { uuid: 'nope' } },
    { orgs: [{ org: { uuid: A }, usage: {} }], account: null },
  ]) {
    assert.equal(checkResult(bad).kind, 'retry', JSON.stringify(bad));
  }
});

test('checkResult: more than 512 KiB retries', () => {
  const raw = ok();
  raw.orgs[0].usage = { blob: 'x'.repeat(READER_MAX_BYTES) };
  assert.equal(checkResult(raw).kind, 'retry');
  raw.orgs[0].usage = { blob: 'x'.repeat(READER_MAX_BYTES - 2000) };
  assert.equal(checkResult(raw).kind, 'ok');
});

test('checkResult: active_org must look like a uuid', () => {
  assert.equal(checkResult({ ...ok(), active_org: 'x; drop' }).activeOrg, null);
  assert.equal(checkResult({ ...ok(), active_org: B }).activeOrg, B);
});

test('stopMessage', () => {
  assert.equal(stopMessage(429), 'rate limited');
  assert.equal(stopMessage(401), 'signed out');
  assert.equal(stopMessage(403), 'usage not available for this account');
});

const WIN = { five_hour: { utilization: 10, resets_at: '2026-01-01T00:00:00Z' } };

test('checkResult: personal Max account (one org, usage served) reads', () => {
  const r = checkResult({ orgs: [{ org: { uuid: A, raven_type: null }, usage: WIN }], account: acct, active_org: A });
  assert.equal(r.kind, 'ok');
  assert.equal(r.orgs.length, 1);
});

test('checkResult: Team member, two orgs, one 403s on usage: the other is read', () => {
  const raw = { orgs: [{ org: { uuid: A, name: 'personal' }, usage: { __status: 403 } }, { org: { uuid: B, raven_type: 'team' }, usage: WIN }], account: acct, active_org: A };
  const r = checkResult(raw);
  assert.equal(r.kind, 'ok');
  assert.deepEqual(r.orgs.map((o) => o.org.uuid), [B]);
  // either order
  raw.orgs.reverse();
  assert.deepEqual(checkResult(raw).orgs.map((o) => o.org.uuid), [B]);
});

test('checkResult: every org 403s on usage: stop as "not available", not signed out', () => {
  const raw = { orgs: [{ org: { uuid: A }, usage: { __status: 403 } }, { org: { uuid: B }, usage: { __status: 403 } }], account: acct };
  assert.deepEqual(checkResult(raw), { kind: 'stop', status: 403, message: 'usage not available for this account' });
});

test('checkResult: a real sign-out (401 anywhere) stops as signed out, even beside a 403', () => {
  const raw = { orgs: [{ org: { uuid: A }, usage: { __status: 403 } }, { org: { uuid: B }, usage: { __status: 401 } }], account: acct };
  assert.deepEqual(checkResult(raw), { kind: 'stop', status: 401, message: 'signed out' });
  assert.deepEqual(checkResult({ orgs: { __status: 401 }, account: null }), { kind: 'stop', status: 401, message: 'signed out' });
});

test('the script maps a redirect to /login onto a 401', () => {
  assert.match(READER_SCRIPT, /r\.redirected/);
  assert.match(READER_SCRIPT, /__status: 401/);
});
