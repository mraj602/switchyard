import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';

import { create } from './index.js';
import { fakeElectron } from '../../../test/fakes/electron.js';
import { fakeBrowser, signInFake } from '../../../test/fakes/claude/browser.js';
import { fakeClock } from '../../../test/fakes/claude/clock.js';
import { fakeIpc, fakeSy, memStore, newBus, silentLog, sleep, tabRecord, waitFor } from '../../../test/fakes/claude/harness.js';
import { startFakeClaude, uuidOf } from '../../../test/fakes/claude/server.js';
import { TAB_ID_RE, WARNING_VERSION } from '../../shared/constants.js';

const TAB = 't_9c1e5f0a7b2d4e68';
const TAB2 = 't_0000000000000002';
let server;
before(async () => {
  server = await startFakeClaude({ orgs: 2 });
});
after(() => server.close());

/** Everything usage/ needs, wired to the fake claude.ai. */
async function rig({ tabs = [tabRecord(TAB)], url = '/new', loggedIn = true, cadenceMs } = {}) {
  await fetch(`${server.origin}/_fake/script`, { method: 'POST', body: '{"reset":true}' });
  const cookies = loggedIn ? await signInFake(server.origin) : {};
  const browser = fakeBrowser({ origin: server.origin, cookies });
  const f = fakeElectron({ executeJavaScript: browser.exec });
  const views = new Map();
  for (const t of tabs) {
    const view = new f.electron.WebContentsView({ webPreferences: { partition: `persist:tab-${t.id}` } });
    view.webContents._setURL(`${server.origin}${url}`);
    for (const [name, value] of Object.entries(cookies)) await view.webContents.session.cookies.set({ url: server.origin, name, value });
    views.set(t.id, view);
  }
  const store = memStore(tabs);
  const bus = newBus();
  const sy = fakeSy();
  const ipc = fakeIpc();
  const clock = fakeClock();
  const tabsApi = {
    list: () => (store.get().tabs ?? []).map((t) => ({ ...t, signed_in: true, loaded: true })),
    view: (id) => views.get(id),
  };
  const usage = create({ tabs: tabsApi, sy, store, bus, ipc, log: silentLog, clientVersion: '0.6.0', clock, random: () => 0.5, claudeOrigin: server.origin, cadenceMs });
  const events = [];
  bus.on('usage', (p) => events.push(p));
  const tabEvents = [];
  bus.on('tabs', (p) => tabEvents.push(p));
  usage.start();
  const tab = (id = TAB) => store.get().tabs.find((t) => t.id === id);
  const last = (id = TAB) => events.filter((e) => e.id === id).at(-1);
  const accept = async (id = TAB) => {
    await ipc.call('usage:accept-warning', { id, version: WARNING_VERSION });
    await waitFor(() => last(id)?.last_read || last(id)?.read_state === 'stopped', 3000, 'first read');
  };
  const stats = async () => (await fetch(`${server.origin}/_fake/stats`)).json();
  return { usage, store, bus, sy, ipc, clock, views, browser, events, tabEvents, tab, last, accept, stats, f };
}

const script = (path, status, extra = {}) => fetch(`${server.origin}/_fake/script`, { method: 'POST', body: JSON.stringify({ path, status, ...extra }) });

test('no read until the warning is accepted: zero requests', async () => {
  const r = await rig();
  await r.ipc.call('usage:refresh', {});
  await r.clock.advance(40 * 60_000);
  assert.equal((await r.stats()).total, 0);
  assert.equal(r.sy.appended.length, 0);
  assert.equal(r.last().read_state, 'off', 'never_asked shows as off');
});

test('accepting the warning turns reading on and reads at once; one record per org', async () => {
  const r = await rig();
  await r.accept();
  assert.equal(r.tab().reading, 'on');
  assert.equal(r.tab().warning_accepted, WARNING_VERSION);
  assert.equal(r.sy.batches.length, 1);
  assert.equal(r.sy.appended.length, 2);
  assert.equal(r.sy.flushes, 1);
  const [a, b] = r.sy.appended;
  assert.equal(a.kind, 'web_usage');
  assert.equal(a.machine, r.sy.machine);
  assert.equal(a.client, 'switchyard-desktop/0.6.0');
  assert.match(a.ts, /^\d{4}-\d\d-\d\dT/);
  assert.equal(a.payload.tab_id, TAB);
  assert.match(a.payload.tab_id, TAB_ID_RE);
  assert.deepEqual([a.seat.org, b.seat.org], [uuidOf('org', 1), uuidOf('org', 2)]);
  assert.equal(a.seat.src, 'claude_ai');
  assert.equal(a.seat.email, 'a@example.com');
  assert.equal(a.payload.active, true, 'lastActiveOrg is org 1');
  assert.equal(b.payload.active, false);
  assert.equal(JSON.stringify(a.payload.usage), JSON.stringify(JSON.parse(server.fixtureText(0))), 'usage kept whole');
  assert.equal(JSON.stringify(b.payload.usage), JSON.stringify(JSON.parse(server.fixtureText(1))));
  assert.equal('v' in a || 'id' in a, false, 'id and v are sy\'s');
  // the strip and the stored seat
  const ev = r.last();
  assert.equal(ev.read_state, 'idle');
  assert.equal(ev.windows_source, 'local');
  assert.ok(ev.windows.length >= 2);
  assert.equal(ev.windows[0].key, 'five_hour');
  assert.equal(ev.last_read, a.ts);
  assert.deepEqual(r.tab().seat, { account: uuidOf('acct', 1), org: uuidOf('org', 1) });
});

test('a tab that is not on a claude.ai app page is not read', async () => {
  const r = await rig({ url: '/login', loggedIn: false });
  await r.ipc.call('usage:accept-warning', { id: TAB, version: WARNING_VERSION });
  await r.usage.refresh();
  await r.clock.advance(20 * 60_000);
  assert.equal((await r.stats()).total, 0);
  assert.equal(r.sy.appended.length, 0);
});

test('periodic reads every 5 minutes (fake clock), floor and jitter from the scheduler', async () => {
  const r = await rig();
  await r.accept();
  assert.equal(r.sy.batches.length, 1);
  await r.clock.advance(5 * 60_000);
  await waitFor(() => r.sy.batches.length === 2, 3000, 'second batch');
  await r.clock.advance(5 * 60_000);
  await waitFor(() => r.sy.batches.length === 3, 3000, 'third batch');
});

test('turning reading off during a delayed read: zero appends, no more requests', async () => {
  const r = await rig();
  await script('usage', 200, { delay: 400 });
  await r.ipc.call('usage:accept-warning', { id: TAB, version: WARNING_VERSION });
  await waitFor(async () => (await r.stats()).paths.usage >= 1, 3000, 'read in flight');
  assert.equal(r.last().read_state, 'reading');
  await r.ipc.call('usage:set-reading', { id: TAB, on: false });
  assert.equal(r.tab().reading, 'off');
  assert.equal(r.last().read_state, 'off');
  await sleep(900); // the delayed read finishes meanwhile
  assert.equal(r.sy.appended.length, 0, 'the result was dropped before the outbox');
  assert.equal(r.sy.flushes, 0);
  assert.equal(r.last().last_read, null, 'and not shown in the strip');
  const before = (await r.stats()).total;
  await r.clock.advance(3 * 60 * 60_000);
  await r.usage.refresh();
  assert.equal((await r.stats()).total, before, 'nothing more read while off');
  assert.equal(r.clock.pending(), 0, 'no timers');
});

test('429: stops with exactly one error record per seat, then no requests until Resume', async () => {
  const r = await rig();
  await r.accept(); // learns the two seats
  r.sy.batches.length = 0;
  r.sy.appended.length = 0;
  await script('usage', 429);
  await r.clock.advance(5 * 60_000);
  await waitFor(() => r.tab().stopped, 3000, 'stopped');
  assert.equal(r.tab().stopped.status, 429);
  assert.match(r.tab().stopped.at, /^\d{4}-/);
  assert.equal(r.sy.batches.length, 1);
  assert.equal(r.sy.appended.length, 2, 'one per known seat');
  for (const e of r.sy.appended) {
    assert.deepEqual(e.payload.error, { status: 429, message: 'rate limited' });
    assert.equal('usage' in e.payload, false);
  }
  assert.deepEqual(r.sy.appended.map((e) => e.seat.org), [uuidOf('org', 1), uuidOf('org', 2)]);
  assert.equal(r.last().read_state, 'stopped');
  assert.deepEqual(r.last().stopped, r.tab().stopped);
  const reqs = (await r.stats()).total;
  await r.clock.advance(6 * 60 * 60_000);
  await r.usage.refresh();
  assert.equal((await r.stats()).total, reqs, 'no further requests');
  assert.equal(r.sy.appended.length, 2, 'no further records');

  // Resume reads again.
  await r.ipc.call('usage:resume', { id: TAB });
  await waitFor(() => r.sy.appended.length === 4, 3000, 'resumed read');
  assert.equal(r.tab().stopped, null);
  assert.equal(r.last().read_state, 'idle');
  assert.ok(r.sy.appended.at(-1).payload.usage);
});

test('401 stops the same way, and 403 too', async () => {
  for (const status of [401, 403]) {
    const r = await rig();
    await r.accept();
    r.sy.appended.length = 0;
    await script('organizations', status);
    await r.clock.advance(5 * 60_000);
    await waitFor(() => r.tab().stopped, 3000, 'stopped');
    assert.equal(r.tab().stopped.status, status);
    assert.equal(r.sy.appended.length, 2);
    assert.equal(r.sy.appended[0].payload.error.message, 'signed out');
  }
});

test('a stop before any seat is known appends nothing; a restart uses the stored seat', async () => {
  const r = await rig();
  await script('account', 401);
  await r.ipc.call('usage:accept-warning', { id: TAB, version: WARNING_VERSION });
  await waitFor(() => r.tab().stopped, 3000, 'stopped');
  assert.equal(r.sy.appended.length, 0, 'unknown seat: none');
  assert.equal(r.last().read_state, 'stopped');

  const seat = { account: uuidOf('acct', 1), org: uuidOf('org', 1) };
  const r2 = await rig({ tabs: [tabRecord(TAB, { reading: 'on', warning_accepted: WARNING_VERSION, seat })] });
  await script('usage', 429);
  await r2.usage.refresh(TAB);
  await waitFor(() => r2.tab().stopped, 3000, 'stopped');
  assert.equal(r2.sy.appended.length, 1);
  assert.deepEqual(r2.sy.appended[0].seat, { account: seat.account, org: seat.org, src: 'claude_ai' });
});

test('a stopped tab stays stopped across a restart (reading never starts by itself)', async () => {
  const r = await rig({ tabs: [tabRecord(TAB, { reading: 'on', warning_accepted: WARNING_VERSION, stopped: { status: 429, at: '2026-10-11T10:00:00.000Z' } })] });
  assert.equal(r.last().read_state, 'stopped');
  await r.clock.advance(60 * 60_000);
  await r.usage.refresh();
  assert.equal((await r.stats()).total, 0);
});

test('send_to_hub off: the strip updates, nothing is appended', async () => {
  const r = await rig({ tabs: [tabRecord(TAB, { reading: 'on', warning_accepted: WARNING_VERSION, send_to_hub: false })] });
  await r.usage.refresh(TAB);
  await waitFor(() => r.last().last_read, 3000, 'read');
  assert.ok(r.last().windows.length);
  assert.equal(r.sy.batches.length, 0);
  assert.equal(r.sy.flushes, 0);
  // and the switch is stored
  await r.ipc.call('usage:set-send', { id: TAB, on: true });
  assert.equal(r.tab().send_to_hub, true);
});

test('a server error backs off: no records, state retrying with a retry time, one failure at a time', async () => {
  const r = await rig();
  await script('usage', 500);
  await r.ipc.call('usage:accept-warning', { id: TAB, version: WARNING_VERSION });
  await waitFor(() => r.last().read_state === 'retrying', 3000, 'retrying');
  const retryAt = Date.parse(r.last().retry_at);
  assert.ok(retryAt - r.clock.now() >= 5 * 60_000 && retryAt - r.clock.now() <= 5 * 60_000 + 15_000);
  assert.equal(r.sy.appended.length, 0);
  assert.equal(r.tab().stopped, null);
  await r.clock.advance(retryAt - r.clock.now());
  await waitFor(() => r.sy.appended.length === 2, 3000, 'recovered');
  assert.equal(r.last().read_state, 'idle');
  assert.equal(r.last().retry_at, null);
});

test('a network error (both worlds throw) backs off too', async () => {
  const r = await rig();
  r.f.electron.session.fromPartition(`persist:tab-${TAB}`); // keep the session
  const wc = r.views.get(TAB).webContents;
  wc.executeJavaScriptInIsolatedWorld = async () => {
    throw new Error('net::ERR_INTERNET_DISCONNECTED');
  };
  wc.executeJavaScript = async () => {
    throw new Error('net::ERR_INTERNET_DISCONNECTED');
  };
  await r.ipc.call('usage:accept-warning', { id: TAB, version: WARNING_VERSION });
  await waitFor(() => r.last().read_state === 'retrying', 3000, 'retrying');
  assert.equal(r.sy.appended.length, 0);
});

test('an append that returns the wrong number of ids is logged, not retried, and not flushed', async () => {
  const r = await rig();
  r.sy.appendResult = () => ({ ok: true, ids: ['only-one'] });
  await r.accept();
  assert.equal(r.sy.batches.length, 1);
  assert.equal(r.sy.flushes, 0);
  assert.equal(r.last().read_state, 'idle', 'not a backoff');
  await r.clock.advance(60_000);
  assert.equal(r.sy.batches.length, 1, 'not retried');
});

test('pauseAll sets every tab off and drops timers', async () => {
  const r = await rig({ tabs: [tabRecord(TAB, { reading: 'on', warning_accepted: 1 }), tabRecord(TAB2, { reading: 'on', warning_accepted: 1 })] });
  await r.usage.pauseAll();
  assert.deepEqual(r.store.get().tabs.map((t) => t.reading), ['off', 'off']);
  assert.equal(r.clock.pending(), 0);
  assert.equal(r.last(TAB).read_state, 'off');
  assert.equal(r.last(TAB2).read_state, 'off');
  assert.ok(r.tabEvents.length >= 1, 'announced on the tabs event');
});

test('set-reading on (after off) reads again; a raised warning version stops reading until re-accepted', async () => {
  const r = await rig({ tabs: [tabRecord(TAB, { reading: 'off', warning_accepted: WARNING_VERSION })] });
  await r.ipc.call('usage:set-reading', { id: TAB, on: true });
  await waitFor(() => r.sy.appended.length === 2, 3000, 'read');

  const old = await rig({ tabs: [tabRecord(TAB, { reading: 'on', warning_accepted: WARNING_VERSION - 1 })] });
  await old.usage.refresh();
  assert.equal((await old.stats()).total, 0, 'an old warning version does not read');
});

test('did-finish-load on the app page reads 1.5 s later; in-page navigation is debounced', async () => {
  const r = await rig({ tabs: [tabRecord(TAB, { reading: 'on', warning_accepted: WARNING_VERSION })] });
  const wc = r.views.get(TAB).webContents;
  wc.emit('did-finish-load');
  await r.clock.advance(1400);
  assert.equal(r.sy.batches.length, 0);
  await r.clock.advance(200);
  await waitFor(() => r.sy.batches.length === 1, 3000, 'open read');
  wc.emit('did-navigate-in-page', {}, `${server.origin}/chat/1`);
  await r.clock.advance(3100);
  assert.equal(r.sy.batches.length, 1, 'the last read is younger than 60 s');
  await r.clock.advance(61_000);
  wc.emit('did-navigate-in-page', {}, `${server.origin}/chat/2`);
  await r.clock.advance(3100);
  await waitFor(() => r.sy.batches.length === 2, 3000, 'nav read');
  // a load that lands on /login does not read
  wc._setURL(`${server.origin}/login`);
  wc.emit('did-finish-load');
  await r.clock.advance(2000);
  assert.equal(r.sy.batches.length, 2);
});

test('a tab removed from the list is forgotten: its timers die', async () => {
  const r = await rig({ tabs: [tabRecord(TAB, { reading: 'on', warning_accepted: WARNING_VERSION })] });
  assert.ok(r.clock.pending() >= 1);
  await r.store.update((d) => {
    d.tabs = [];
  });
  r.bus.emit('tabs', { tabs: [] });
  assert.equal(r.clock.pending(), 0);
});

test('stop waits for the last append and clears the timers', async () => {
  const r = await rig({ tabs: [tabRecord(TAB, { reading: 'on', warning_accepted: WARNING_VERSION })] });
  let release;
  r.sy.appendResult = (records) => new Promise((res) => (release = () => res({ ok: true, ids: records.map((_, i) => `i${i}`) })));
  const reading = r.usage.refresh(TAB);
  await waitFor(() => release, 3000, 'append pending');
  let stopped = false;
  const stopping = r.usage.stop().then(() => (stopped = true));
  await sleep(50);
  assert.equal(stopped, false, 'waiting for sy.append');
  release();
  await stopping;
  await reading;
  assert.equal(r.clock.pending(), 0);
});

