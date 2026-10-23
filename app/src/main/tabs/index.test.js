import { test } from 'node:test';
import assert from 'node:assert/strict';
import path from 'node:path';

import { CF_MAX_RETRIES, CF_RETRY_AFTER_MS, create, LAST_URL_DEBOUNCE_MS, LOAD_STAGGER_MS } from './index.js';
import { fakeElectron } from '../../../test/fakes/electron.js';
import { fakeClock } from '../../../test/fakes/claude/clock.js';
import { fakeIpc, fakeSecurity, fakeWindow, memStore, newBus, silentLog, tabRecord } from '../../../test/fakes/claude/harness.js';

const A = 't_000000000000000a';
const B = 't_000000000000000b';
const C = '9c1e5f0a'; // a tab id from earlier versions (8 hex)

function rig({ tabs = [], userData = '/ud', platform = 'linux', fsOverride, seats = [] } = {}) {
  const f = fakeElectron({ platform, paths: { userData } });
  const clock = fakeClock();
  const store = memStore(tabs);
  const bus = newBus();
  const ipc = fakeIpc();
  const security = fakeSecurity();
  const window = fakeWindow();
  const rm = [];
  const fs = fsOverride ?? { rm: async (p, o) => void rm.push([p, o]) };
  let n = 0;
  const api = create({
    electron: f.electron, security, window, store, bus, ipc, log: silentLog, claudeOrigin: 'https://claude.ai', fs, userData, seats: () => seats,
    timers: { setTimeout: clock.setTimeout, clearTimeout: clock.clearTimeout }, platform, chromeVersion: '130.0.6723.0',
    random: () => Buffer.from(String(++n).padStart(16, '0'), 'hex').subarray(0, 8),
  });
  const events = [];
  bus.on('tabs', (p) => events.push(p));
  const loads = () => f.callsOf('webContents.loadURL');
  return { f, clock, store, bus, ipc, security, window, api, events, rm, loads };
}

test('start: one view per stored tab in partition persist:tab-<id>, the platform UA, guarded, staggered loads', async () => {
  const r = rig({ tabs: [tabRecord(A, { last_url: 'https://claude.ai/chat/x' }), tabRecord(B), tabRecord(C, { last_url: 'https://evil.example/' })] });
  await r.api.start();
  assert.deepEqual([...r.f.electron.session._all.keys()].sort(), [`persist:tab-${A}`, `persist:tab-${B}`, `persist:tab-${C}`].sort());
  for (const id of [A, B, C]) {
    const ses = r.f.electron.session._all.get(`persist:tab-${id}`);
    assert.match(ses.userAgent, /^Mozilla\/5\.0 \(X11; Linux x86_64\) AppleWebKit\/537\.36 \(KHTML, like Gecko\) Chrome\/130\.0\.0\.0 Safari\/537\.36$/);
    assert.doesNotMatch(ses.userAgent, /Electron/);
    assert.equal(r.api.view(id).webContents.userAgent, ses.userAgent);
    assert.equal(r.window.views.get(id).kind, 'tab');
  }
  assert.deepEqual(r.security.calls.guardSession.map((c) => c[1]), ['tab', 'tab', 'tab']);
  assert.deepEqual(r.security.calls.guardView.map((c) => c[1]), ['tab', 'tab', 'tab']);
  assert.deepEqual(r.security.calls.webPreferences.map((c) => c[0]), ['tab', 'tab', 'tab']);
  // The real security.webPreferences takes the partition (not a session object), and the view's
  // session is the same one the tab guarded.
  assert.equal(r.api.view(A).options.webPreferences.partition, `persist:tab-${A}`);
  assert.equal(r.api.view(A).webContents.session, r.f.electron.session._all.get(`persist:tab-${A}`));
  // staggered by 1.5 s; lastUrl restored when it is a claude.ai URL, else the origin
  await r.clock.advance(1);
  assert.deepEqual(r.loads().map((l) => l[1]), ['https://claude.ai/chat/x']);
  await r.clock.advance(LOAD_STAGGER_MS);
  assert.equal(r.loads().length, 2);
  await r.clock.advance(LOAD_STAGGER_MS);
  assert.deepEqual(r.loads().map((l) => l[1]), ['https://claude.ai/chat/x', 'https://claude.ai/new', 'https://claude.ai/']);
});

test('the user agent names the real platform', async () => {
  const mac = rig({ tabs: [tabRecord(A)], platform: 'darwin' });
  await mac.api.start();
  assert.match(mac.f.electron.session._all.get(`persist:tab-${A}`).userAgent, /\(Macintosh; Intel Mac OS X 10_15_7\)/);
  const win = rig({ tabs: [tabRecord(A)], platform: 'win32' });
  await win.api.start();
  assert.match(win.f.electron.session._all.get(`persist:tab-${A}`).userAgent, /\(Windows NT 10\.0; Win64; x64\)/);
});

test('list(): TabInfo with live facts', async () => {
  const r = rig({ tabs: [tabRecord(A, { reading: 'on', warning_accepted: 1, seat: { account: 'a', org: 'o' } })] });
  await r.api.start();
  assert.deepEqual(r.api.list(), [{
    id: A, name: A, reading: 'on', warning_accepted: 1, send_to_hub: true, stopped: null, seat: { account: 'a', org: 'o' }, expect: null,
    last_url: 'https://claude.ai/new', signed_in: false, loaded: false,
  }]);
  const wc = r.api.view(A).webContents;
  wc._setURL('https://claude.ai/chat/1');
  wc.emit('did-finish-load');
  assert.equal(r.api.list()[0].signed_in, true);
  assert.equal(r.api.list()[0].loaded, true);
  assert.equal(r.events.at(-1).tabs[0].loaded, true, 'announced on the bus');
  wc._setURL('https://claude.ai/login');
  assert.equal(r.api.list()[0].signed_in, false);
});

test('lastUrl is remembered (debounced) for claude.ai URLs only, and never /logout', async () => {
  const r = rig({ tabs: [tabRecord(A)] });
  await r.api.start();
  const wc = r.api.view(A).webContents;
  wc.emit('did-navigate-in-page', {}, 'https://claude.ai/chat/1');
  wc.emit('did-navigate-in-page', {}, 'https://claude.ai/chat/2');
  wc.emit('did-navigate', {}, 'https://example.com/x');
  assert.equal(r.store.get().tabs[0].last_url, 'https://claude.ai/new', 'not yet');
  await r.clock.advance(LAST_URL_DEBOUNCE_MS + 10);
  assert.equal(r.store.get().tabs[0].last_url, 'https://claude.ai/chat/2');
  wc.emit('did-navigate', {}, 'https://claude.ai/logout');
  await r.clock.advance(LAST_URL_DEBOUNCE_MS + 10);
  assert.equal(r.store.get().tabs[0].last_url, 'https://claude.ai/chat/2');
  // a pending write is flushed on stop
  wc.emit('did-navigate-in-page', {}, 'https://claude.ai/chat/3');
  await r.api.stop();
  assert.equal(r.store.get().tabs[0].last_url, 'https://claude.ai/chat/3');
});

test('Cloudflare: a hidden tab stuck on "Just a moment" reloads after 25 s, at most twice, never the visible one', async () => {
  const r = rig({ tabs: [tabRecord(A), tabRecord(B)] });
  await r.api.start();
  const wa = r.api.view(A).webContents;
  const wb = r.api.view(B).webContents;
  wa.getTitle = () => 'Just a moment...';
  wb.getTitle = () => 'Just a moment...';
  r.window.selectedId = B; // B is visible
  const reloads = (wc) => r.f.callsOf('webContents.reload').filter((c) => c[0] === wc.id).length;
  for (let i = 0; i < 4; i++) {
    wa.emit('did-finish-load');
    wb.emit('did-finish-load');
    await r.clock.advance(CF_RETRY_AFTER_MS - 1);
    assert.equal(reloads(wa), Math.min(i, CF_MAX_RETRIES), 'not before 25 s');
    await r.clock.advance(2);
  }
  assert.equal(reloads(wa), 2);
  assert.equal(reloads(wb), 0);
  // a clean title resets the budget
  wa.getTitle = () => 'Claude';
  wa.emit('did-finish-load');
  await r.clock.advance(CF_RETRY_AFTER_MS + 1);
  wa.getTitle = () => 'Just a moment...';
  wa.emit('did-finish-load');
  await r.clock.advance(CF_RETRY_AFTER_MS + 1);
  assert.equal(reloads(wa), 3);
});

test('tab:add makes a t_ + 16 hex id, a view, a stored record and announces it', async () => {
  const r = rig();
  await r.api.start();
  const { id } = await r.ipc.call('tab:add', { name: '  work ' });
  assert.match(id, /^t_[0-9a-f]{16}$/);
  const t = r.store.get().tabs[0];
  assert.deepEqual(t, { id, name: 'work', last_url: 'https://claude.ai/new', reading: 'never_asked', warning_accepted: 0, send_to_hub: true, stopped: null, seat: null });
  assert.ok(r.api.view(id));
  assert.ok(r.f.electron.session._all.has(`persist:tab-${id}`));
  assert.equal(r.events.at(-1).tabs[0].id, id);
  await r.clock.advance(1);
  assert.equal(r.loads()[0][1], 'https://claude.ai/new');
  await assert.rejects(r.ipc.call('tab:add', { name: '   ' }), /name/);
});

test('real random ids are 16 lowercase hex', async () => {
  const f = fakeElectron();
  const ipc = fakeIpc();
  const api = create({ electron: f.electron, security: fakeSecurity(), window: fakeWindow(), store: memStore(), bus: newBus(), ipc, log: silentLog, fs: {}, userData: '/ud' });
  await api.start();
  const ids = new Set();
  for (let i = 0; i < 20; i++) ids.add((await ipc.call('tab:add', { name: `t${i}` })).id);
  assert.equal(ids.size, 20);
  for (const id of ids) assert.match(id, /^t_[0-9a-f]{16}$/);
  await api.stop();
});

test('tab:update renames; unknown ids are refused', async () => {
  const r = rig({ tabs: [tabRecord(A)] });
  await r.api.start();
  await r.ipc.call('tab:update', { id: A, name: 'home' });
  assert.equal(r.store.get().tabs[0].name, 'home');
  await r.ipc.call('tab:update', { id: A });
  await assert.rejects(r.ipc.call('tab:update', { id: B, name: 'x' }), /no such tab/);
});

test('tab:reorder keeps unknown ids out and unlisted tabs last', async () => {
  const r = rig({ tabs: [tabRecord(A), tabRecord(B), tabRecord(C)] });
  await r.api.start();
  await r.ipc.call('tab:reorder', { ids: [C, 't_ffffffffffffffff', A] });
  assert.deepEqual(r.store.get().tabs.map((t) => t.id), [C, A, B]);
  assert.deepEqual(r.api.list().map((t) => t.id), [C, A, B]);
});

test('tab:reorder persists the order in the store, notifies once, and a re-listed full order is a no-op change', async () => {
  const r = rig({ tabs: [tabRecord(A), tabRecord(B), tabRecord(C)] });
  await r.api.start();
  const before = r.events.length;
  await r.ipc.call('tab:reorder', { ids: [B, C, A] });
  assert.deepEqual(r.store.get().tabs.map((t) => t.id), [B, C, A], 'the stored order is the display order');
  assert.equal(r.events.length, before + 1);
  assert.deepEqual(r.events.at(-1).tabs.map((t) => t.id), [B, C, A]);
  await r.ipc.call('tab:reorder', { ids: [B, C, A] });
  assert.deepEqual(r.store.get().tabs.map((t) => t.id), [B, C, A]);
  // a partial list moves only the listed tabs to the front; none is lost
  await r.ipc.call('tab:reorder', { ids: [A] });
  assert.deepEqual(r.store.get().tabs.map((t) => t.id), [A, B, C]);
});

test('tab:reload, tab:home, tab:external', async () => {
  const r = rig({ tabs: [tabRecord(A)] });
  await r.api.start();
  const wc = r.api.view(A).webContents;
  wc._setURL('https://claude.ai/chat/9');
  await r.ipc.call('tab:reload', { id: A });
  assert.equal(r.f.callsOf('webContents.reload').length, 1);
  await r.ipc.call('tab:home', { id: A });
  assert.equal(r.loads().at(-1)[1], 'https://claude.ai/new');
  wc._setURL('https://claude.ai/chat/9');
  await r.ipc.call('tab:external', { id: A });
  assert.deepEqual(r.security.calls.openExternalSafe, ['https://claude.ai/chat/9']);
  await assert.rejects(r.ipc.call('tab:reload', { id: B }), /no such tab/);
});

test('tab:remove clears the partition, closes the view, deletes the folder after, and selects the hub', async () => {
  const r = rig({ tabs: [tabRecord(A), tabRecord(B)] });
  await r.api.start();
  const view = r.api.view(A);
  const ses = r.f.electron.session._all.get(`persist:tab-${A}`);
  await ses.cookies.set({ url: 'https://claude.ai', name: 'sessionKey', value: 'x' });
  r.window.selectedId = A;
  await r.ipc.call('tab:remove', { id: A });
  assert.deepEqual(r.store.get().tabs.map((t) => t.id), [B]);
  assert.equal(r.api.view(A), undefined);
  assert.equal(r.window.views.has(A), false);
  assert.equal(r.window.selected(), 'hub');
  assert.equal(view.webContents.destroyed, true, 'the view is closed');
  assert.equal(r.f.callsOf('session.clearStorageData').length, 1);
  assert.equal(ses.cookies.jar.size, 0);
  assert.deepEqual(r.rm, [[path.resolve('/ud', 'Partitions', `tab-${A}`), { recursive: true, force: true }]]);
  assert.deepEqual(r.events.at(-1).tabs.map((t) => t.id), [B]);
  // order: closed, then cleared, then the folder removed
  const order = r.f.calls.map((c) => c.api).filter((a) => ['webContents.close', 'session.clearStorageData'].includes(a));
  assert.deepEqual(order, ['webContents.close', 'session.clearStorageData']);
});

test('tab:remove never deletes a folder outside <userData>/Partitions/tab-<id>', async () => {
  const r = rig({ tabs: [tabRecord(A)] });
  await r.api.start();
  r.f.electron.session._all.get(`persist:tab-${A}`).getStoragePath = () => '/home/me/.config/switchyard/Partitions/acct-1';
  await r.ipc.call('tab:remove', { id: A });
  assert.deepEqual(r.rm, []);
});

test('tab:remove of a tab with no view yet (imported before the restart) still deletes its folder', async () => {
  const r = rig({ tabs: [tabRecord(A)] });
  await r.api.start();
  // the importer adds a tab to the store; its view is made only at the next start
  await r.store.update((d) => d.tabs.push(tabRecord(C)));
  assert.equal(r.api.view(C), undefined);
  await r.ipc.call('tab:remove', { id: C });
  assert.deepEqual(r.store.get().tabs.map((t) => t.id), [A]);
  assert.deepEqual(r.rm, [[path.resolve('/ud', 'Partitions', `tab-${C}`), { recursive: true, force: true }]]);
});

const SEAT6 = { id: 'seat_01M4SH4F8KV20NK22FQCTR0RR6', account: 'a6', org: 'o6', label: 'claude-6' };
const SEAT7 = { id: 'seat_01M4SH4F8KV20NK22FQCTR0RR7', account: 'a7', org: 'o7', label: 'claude-7' };
const LINK = 'https://claude.ai/code/session_01AbC';

test('openLink: the tab of the seat\'s account, else a tab made for it, else a new tab for it; never another account\'s', async () => {
  const r = rig({ tabs: [tabRecord(A, { seat: { account: 'a1', org: 'o1' } }), tabRecord(B, { seat: { account: 'a2', org: 'o2' } })], seats: [SEAT6, SEAT7] });
  await r.api.start();
  assert.equal(await r.api.openLink('https://example.com/', null), false, 'not a session link');
  assert.equal(await r.api.openLink('https://claude.ai/code/session_01AbC?x=1', null), false);
  // seat match (by account), even if the tab is not signed in
  assert.equal(await r.api.openLink(LINK, { account: 'a2', org: 'o2' }), true);
  assert.equal(r.window.selected(), B);
  assert.equal(r.window.shown, 1);
  assert.equal(r.loads().at(-1)[1], LINK);
  // a seat with no tab, with other tabs signed in: a new tab for that account, not the signed-in one
  r.api.view(B).webContents._setURL('https://claude.ai/chat/1');
  const n0 = r.loads().length;
  assert.equal(await r.api.openLink(LINK, { id: SEAT7.id, account: 'a7', org: 'o7' }), true);
  const made = r.store.get().tabs.at(-1);
  assert.equal(r.store.get().tabs.length, 3);
  assert.equal(made.name, 'claude-7');
  assert.deepEqual(made.expect, { seat: SEAT7.id, account: 'a7', org: 'o7', label: 'claude-7' });
  assert.equal(made.last_url, 'https://claude.ai/code/session_01AbC', 'the link is where the new tab starts');
  assert.equal(r.window.selected(), made.id);
  await r.clock.advance(1);
  assert.equal(r.loads().slice(n0).filter((l) => l[1] === LINK).length, 1, 'the link loaded once, in the new tab');
  assert.equal(r.loads().at(-1)[1], LINK);
  // the same seat again: the tab made for it is used, no second tab
  assert.equal(await r.api.openLink(LINK, { id: SEAT7.id, account: 'a7', org: 'o7' }), true);
  assert.equal(r.store.get().tabs.length, 3);
  assert.equal(r.window.selected(), made.id);
  // a tab signed in as the account wins over a tab made for it
  await r.store.update((d) => {
    d.tabs.find((t) => t.id === A).seat = { account: 'a7', org: 'o7' };
  });
  assert.equal(await r.api.openLink(LINK, { id: SEAT7.id, account: 'a7', org: 'o7' }), true);
  assert.equal(r.window.selected(), A);
  // no seat at all: the old fallback, the first signed-in tab; none signed in: false
  r.api.view(A).webContents._setURL('https://claude.ai/login');
  assert.equal(await r.api.openLink(LINK, null), true);
  assert.equal(r.window.selected(), B);
  for (const t of r.store.get().tabs) r.api.view(t.id).webContents._setURL('https://claude.ai/login');
  assert.equal(await r.api.openLink(LINK, null), false);
});


test('openLink quiet: a link a page handed over selects the tab but never raises the window', async () => {
  const r = rig({ tabs: [tabRecord(A, { seat: { account: 'a1', org: 'o1' } }), tabRecord(B, { seat: { account: 'a2', org: 'o2' } })], seats: [SEAT6, SEAT7] });
  await r.api.start();
  assert.equal(await r.api.openLink(LINK, { account: 'a2', org: 'o2' }, { quiet: true }), true);
  assert.equal(r.window.selected(), B);
  assert.equal(r.window.shown, 0, 'the window was not shown or focused');
});

test('adoptTab: a tab made for claude-6, signed in as claude-7, becomes claude-7\'s; claude-6 is offered a new tab', async () => {
  const expect6 = { seat: SEAT6.id, account: 'a6', org: 'o6', label: 'claude-6' };
  const r = rig({ tabs: [tabRecord(A, { name: 'claude-6', expect: expect6, seat: { account: 'a7', org: 'o7' } })], seats: [SEAT6, SEAT7] });
  await r.api.start();
  const out = await r.ipc.call('tab:adopt', { id: A });
  assert.deepEqual(out, { name: 'claude-7', label: 'claude-7', offer: { seat: SEAT6.id, label: 'claude-6' } });
  const t = r.store.get().tabs[0];
  assert.equal(t.name, 'claude-7');
  assert.deepEqual(t.expect, { seat: SEAT7.id, account: 'a7', org: 'o7', label: 'claude-7' });
  assert.equal(r.events.at(-1).tabs[0].name, 'claude-7', 'announced on the bus');
  // the existing add-for-account flow then makes claude-6 a fresh tab
  const made = await r.ipc.call('tab:add', { name: 'claude-6', seat: SEAT6.id });
  assert.notEqual(made.id, A);
  // no offer when another tab already stands for the account that lost this one
  const r2 = rig({ tabs: [tabRecord(A, { expect: expect6, seat: { account: 'a7', org: 'o7' } }), tabRecord(B, { seat: { account: 'a6', org: 'o6' } })], seats: [SEAT6, SEAT7] });
  await r2.api.start();
  assert.equal((await r2.ipc.call('tab:adopt', { id: A })).offer, null);
});

test('adoptTab: an account the hub does not know clears expect and uses the reader\'s display name, else "account"', async () => {
  const expect6 = { seat: SEAT6.id, account: 'a6', org: 'o6', label: 'claude-6' };
  const r = rig({ tabs: [tabRecord(A, { name: 'claude-6', expect: expect6, seat: { account: 'zz', org: 'oz' }, account_name: 'Ada Lovelace' }), tabRecord(B, { name: 'b', expect: expect6, seat: { account: 'yy', org: 'oy' } })], seats: [SEAT6] });
  await r.api.start();
  const a = await r.ipc.call('tab:adopt', { id: A });
  assert.deepEqual([a.name, a.label], ['Ada Lovelace', null]);
  assert.equal('expect' in r.store.get().tabs[0], false);
  assert.equal((await r.ipc.call('tab:adopt', { id: B })).name, 'account');
  assert.equal(r.store.get().tabs[1].name, 'account');
  // matched by uuid, never by email: a seat with the same email but another account is not it
  const r3 = rig({ tabs: [tabRecord(A, { expect: expect6, seat: { account: 'zz', org: 'oz' } })], seats: [{ ...SEAT7, email: 'same@example.com' }, { ...SEAT6, email: 'same@example.com' }] });
  await r3.api.start();
  assert.equal((await r3.ipc.call('tab:adopt', { id: A })).label, null);
  // not signed in as anything yet: refused
  const r4 = rig({ tabs: [tabRecord(A, { expect: expect6 })], seats: [SEAT6] });
  await r4.api.start();
  await assert.rejects(r4.ipc.call('tab:adopt', { id: A }), /not signed in/);
  await assert.rejects(r4.ipc.call('tab:adopt', { id: B }), /no such tab/);
});

test('logoutTab loads claude.ai\'s logout in that tab', async () => {
  const r = rig({ tabs: [tabRecord(A)] });
  await r.api.start();
  await r.ipc.call('tab:logout', { id: A });
  assert.equal(r.loads().at(-1)[1], 'https://claude.ai/logout');
  await assert.rejects(r.ipc.call('tab:logout', { id: B }), /no such tab/);
});

test('addTab with a hub seat: named by the label, expects the seat, one tab per account; no such seat is refused', async () => {
  const r = rig({ tabs: [tabRecord(A, { seat: { account: 'a1', org: 'o1' } })], seats: [SEAT6, { id: 'seat_01M4SH4F8KV20NK22FQCTR0RR1', account: 'a1', org: 'o1', label: 'claude-1' }] });
  await r.api.start();
  const { id } = await r.ipc.call('tab:add', { name: 'whatever', seat: SEAT6.id });
  const t = r.store.get().tabs.find((x) => x.id === id);
  assert.equal(t.name, 'claude-6');
  assert.deepEqual(t.expect, { seat: SEAT6.id, account: 'a6', org: 'o6', label: 'claude-6' });
  assert.equal(r.api.list().find((x) => x.id === id).expect.account, 'a6');
  assert.equal((await r.ipc.call('tab:add', { name: 'x', seat: SEAT6.id })).id, id, 'no second tab for the account');
  assert.equal((await r.ipc.call('tab:add', { name: 'x', seat: 'seat_01M4SH4F8KV20NK22FQCTR0RR1' })).id, A, 'an account that has a signed-in tab');
  await assert.rejects(r.ipc.call('tab:add', { name: 'x', seat: SEAT7.id }), /no such hub account/);
  const blank = await r.ipc.call('tab:add', { name: ' work ' });
  assert.equal(r.store.get().tabs.find((x) => x.id === blank.id).expect, undefined);
  assert.equal(r.store.get().tabs.length, 3);
});

test('cookies are flushed every 30 s and on demand', async () => {
  const f = fakeElectron();
  const clock = fakeClock();
  const store = memStore([tabRecord(A), tabRecord(B)]);
  const api = create({
    electron: f.electron, security: fakeSecurity(), window: fakeWindow(), store, bus: newBus(), ipc: fakeIpc(), log: silentLog, fs: {}, userData: '/ud',
    timers: { setTimeout: clock.setTimeout, clearTimeout: clock.clearTimeout },
  });
  await api.start();
  await api.flushCookies();
  assert.equal(f.callsOf('cookies.flushStore').length, 2);
  await api.stop();
  assert.equal(f.callsOf('cookies.flushStore').length, 4, 'and before quitting');
});

test('handlers are registered once for exactly the tab:* channels', async () => {
  const r = rig();
  await r.api.start();
  await r.api.start();
  assert.deepEqual([...r.ipc.handlers.keys()].sort(), ['tab:add', 'tab:adopt', 'tab:external', 'tab:home', 'tab:logout', 'tab:reload', 'tab:remove', 'tab:reorder', 'tab:update']);
});
