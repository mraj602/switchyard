import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import fsp from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { create, pairError, FORBIDDEN_ERROR } from './index.js';
import { createBus } from '../bus.js';
import { fakeElectron } from '../../../test/fakes/electron.js';
import { startFakeHub } from '../../../test/fakes/hub.js';

const silent = { warn() {}, error() {}, debug() {}, info() {} };
const until = async (fn, ms = 3000) => {
  const t = Date.now();
  while (!(await fn())) {
    if (Date.now() - t > ms) throw new Error(`timed out waiting for: ${fn}`);
    await new Promise((r) => setTimeout(r, 10));
  }
};
const SEAT = { id: 'seat_01HZZZZZZZZZZZZZZZZZZZZZZZ', account: 'acc-1', org: 'org-1', label: 'work' };
const TOKEN = 'sy_d_good';

function info(hubUrl, over = {}) {
  return {
    ok: true, v: 1, sy: '0.6.0', machine: { id: 'm_1', name: 'lap top' },
    hub: { url: hubUrl, connected: true, reachable: true, version: '0.6.0', local: true, dial: null, service: 'running', ...(over.hub ?? {}) },
    admin: 'local', outbox: { pending: 0, last_error: null }, ...Object.fromEntries(Object.entries(over).filter(([k]) => k !== 'hub')),
  };
}

async function harness(opts = {}) {
  const hub = await startFakeHub(opts.hubData);
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'sy-hubtest-'));
  const tokenFile = path.join(dir, 'hub-token.bin');
  const bus = createBus({ onError: (e) => { throw e; } });
  const handlers = {};
  const winCalls = [];
  const guards = [];
  const opened = [];
  const guardedSessions = [];
  // the fake session's fetch: real HTTP to the fake hub, with the partition's own cookie jar
  const sessionFetch = async (partition, url, init) => {
    const jar = fake.electron.session.fromPartition(partition).cookies.jar;
    const cookie = [...jar.values()].map((c) => `${c.name}=${c.value}`).join('; ');
    const res = await fetch(url, { ...init, headers: { ...(init?.headers ?? {}), ...(cookie ? { cookie } : {}) } });
    const set = res.headers.get('set-cookie');
    if (set) {
      const [nv] = set.split(';');
      const [name, value] = nv.split('=');
      await fake.electron.session.fromPartition(partition).cookies.set({ url, name, value });
    }
    return res;
  };
  const fake = fakeElectron({ sessionFetch, safeStorageKey: opts.key });
  const sy = {
    state: { info: info(hub.url), token: { ok: true, device: 'dev_1', token: TOKEN, scopes: ['write', 'read'], hub: { url: hub.url } }, owner: { ok: true, url: `${hub.url}/#/owner?s=one-time` } },
    calls: [],
    async info() { this.calls.push(['info']); return this.state.info; },
    async token(o) { this.calls.push(['token', o]); return this.state.token; },
    async ownerLink() { this.calls.push(['owner']); return this.state.owner; },
    async service(a) { this.calls.push(['service', a]); return this.state.service ?? { ok: true, service: 'running', unit: 'u', health: true }; },
    async accountsList() { return { ok: true, accounts: [{ folder: '~/.claude' }] }; },
    async share(f, s) { this.calls.push(['share', f, s]); return { ok: true }; },
  };
  const security = {
    webPreferences: (kind, extra) => ({ kind, ...extra }),
    guardView: (wc, kind, o) => guards.push({ wc, kind, o }),
    guardSession: (s, kind) => guardedSessions.push({ s, kind }),
    openExternalSafe: async (u) => { opened.push(u); return true; },
  };
  const window = {
    addView: (id, view, kind) => winCalls.push(['addView', id, kind, view]),
    removeView: (id) => winCalls.push(['removeView', id]),
    select: (id) => winCalls.push(['select', id]),
  };
  const slow = { setTimeout: (fn, ms) => setTimeout(fn, ms <= 1000 ? 15 : ms), clearTimeout };
  const links = [];
  const h = create({
    electron: fake.electron, sy, security, window, bus, ipc: { register: (c, fn) => (handlers[c] = fn) }, fs: opts.fs ?? fsp, path, tokenFile,
    fetch: globalThis.fetch, log: silent, timers: slow, platform: 'linux',
    openSessionLink: (url, seat) => { links.push({ url, seat }); return opts.openResult ?? true; },
  });
  const events = { hub: [], needs_you: [], alert: [] };
  for (const k of Object.keys(events)) bus.on(k, (p) => events[k].push(p));
  const close = async () => { await h.stop(); await hub.close(); };
  return { hub, h, sy, fake, bus, handlers, winCalls, guards, opened, links, events, tokenFile, dir, security, close, sessionFetch, guardedSessions };
}

const lastHub = (t) => t.events.hub.at(-1);

test('no sy: the status says so and nothing else happens', async () => {
  const t = await harness();
  t.sy.state.info = { ok: false, exit: -1, error: 'sy not found', code: 'no_sy' };
  await t.h.start();
  assert.equal(lastHub(t).status, 'no_sy');
  assert.equal(lastHub(t).connected, false);
  assert.equal((await t.handlers['hub:pair']({})).ok, false);
  assert.equal(t.hub.requests.length, 0);
  await t.close();
});

test('sy not connected to a hub, and forward mode (dial) are refused', async () => {
  const t = await harness();
  t.sy.state.info = info(null, { hub: { connected: false, url: null, reachable: false } });
  await t.h.start();
  assert.equal(lastHub(t).status, 'not_connected');
  t.sy.state.info = info(t.hub.url, { hub: { dial: 'ssh oci' } });
  await t.h.stop();
  const t2 = await harness();
  t2.sy.state.info = info(t2.hub.url, { hub: { dial: 'ssh oci' } });
  await t2.h.start();
  assert.equal(lastHub(t2).status, 'dial');
  assert.match(lastHub(t2).error, /forward mode is not supported/);
  assert.equal(t2.hub.requests.length, 0, 'no token, no stream');
  await t2.close();
  await t.close();
});

test('pairing: one click through sy, token stored encrypted, stream and overview start', async () => {
  const t = await harness({ hubData: { overview: { seats: [SEAT], needs_you: [{ id: 'c1', state: 'asked', seat_label: 'work', project: 'api', needs_you: true }], machines: [], counts: { needs_you: 1 } }, accounts: { accounts: [SEAT] } } });
  await t.h.start();
  assert.equal(lastHub(t).status, 'needs_pairing');
  assert.equal(lastHub(t).paired, false);
  const r = await t.handlers['hub:pair']({});
  assert.deepEqual(r, { ok: true });
  const tokenCall = t.sy.calls.find((c) => c[0] === 'token');
  assert.equal(tokenCall[1].code, undefined);
  assert.equal(tokenCall[1].name, 'Switchyard-desktop-laptop');
  assert.match(tokenCall[1].name, /^[A-Za-z0-9._-]{1,40}$/, 'no spaces: it must pass an ssh admin channel');
  await until(() => lastHub(t).status === 'ok' && lastHub(t).needs_you === 1);
  assert.equal(lastHub(t).paired, true);
  assert.equal(lastHub(t).owner, false);
  assert.deepEqual(t.events.needs_you.at(-1), { count: 1, sessions: [{ id: 'c1', route: '#/sessions/c1', title: 'work · api · asked' }] });
  assert.deepEqual(t.h.seats().map((s) => s.id), [SEAT.id]);
  await until(() => t.hub.streamCount() === 1);
  assert.ok(!fs.readFileSync(t.tokenFile).includes(TOKEN), 'the token file holds no plain token');
  // the state never carries the token
  assert.ok(!JSON.stringify(t.events.hub).includes(TOKEN));
  for (const req of t.hub.requests) if (req.path !== '/health') assert.equal(req.headers.authorization, `Bearer ${TOKEN}`);
  await t.close();
});

test('pairing with a code passes it to sy (which sends it on stdin); a refusal gets a plain message', async () => {
  const t = await harness();
  await t.h.start();
  t.sy.state.token = { ok: false, exit: 3, error: 'code expired' };
  const r = await t.handlers['hub:pair']({ code: '123-456' });
  assert.equal(r.ok, false);
  assert.match(r.error, /refused or has expired/);
  assert.equal(t.sy.calls.find((c) => c[0] === 'token')[1].code, '123-456');
  t.sy.state.token = { ok: false, exit: 4, error: 'no admin' };
  assert.match((await t.handlers['hub:pair']({})).error, /type a code from the board/i);
  t.sy.state.token = { ok: false, exit: 5, error: 'x' };
  assert.match((await t.handlers['hub:pair']({ code: '123456' })).error, /not reachable/);
  assert.equal(lastHub(t).paired, false);
  await t.close();
});

test('a stored token is used at start; the same file with another hub URL is dropped', async () => {
  const t = await harness();
  await t.h.start();
  await t.handlers['hub:pair']({});
  await t.h.stop();
  assert.ok(fs.existsSync(t.tokenFile));

  // same URL, new run: paired without asking
  const bus2 = createBus();
  const evs = [];
  bus2.on('hub', (p) => evs.push(p));
  const mk = (url) => {
    const sy2 = { ...t.sy, info: async () => info(url) };
    return create({
      electron: t.fake.electron, sy: sy2, security: t.security, window: { addView() {}, removeView() {}, select() {} }, bus: bus2,
      ipc: { register() {} }, fs: fsp, path, tokenFile: t.tokenFile, fetch: globalThis.fetch, log: silent, openSessionLink: () => true,
    });
  };
  const same = mk(t.hub.url);
  await same.start();
  await until(() => evs.at(-1)?.status === 'ok');
  assert.equal(evs.at(-1).paired, true);
  await same.stop();

  // another hub URL: the token is dropped and pairing starts again
  const other = mk('https://hub.example.com');
  await other.start();
  assert.equal(evs.at(-1).status, 'needs_pairing');
  assert.equal(evs.at(-1).paired, false);
  assert.equal(fs.existsSync(t.tokenFile), false, 'the file is gone');
  await other.stop();
  await t.close();
});

test('a token file made with another safeStorage key is kept (not read) and pairing is asked for', async () => {
  const a = await harness({ key: 'k1' });
  await a.h.start();
  await a.handlers['hub:pair']({});
  await a.h.stop();
  const bus2 = createBus();
  const evs = [];
  bus2.on('hub', (p) => evs.push(p));
  const otherFake = fakeElectron({ safeStorageKey: 'k2' });
  const h2 = create({
    electron: otherFake.electron, sy: a.sy, security: a.security, window: { addView() {}, removeView() {}, select() {} }, bus: bus2,
    ipc: { register() {} }, fs: fsp, path, tokenFile: a.tokenFile, fetch: globalThis.fetch, log: silent, openSessionLink: () => true,
  });
  await h2.start();
  assert.equal(evs.at(-1).status, 'needs_pairing');
  assert.ok(fs.existsSync(a.tokenFile), 'kept: the keyring may only be locked');
  await h2.stop();
  await a.close();
});

test('session and usage events refetch the overview (debounced); machine events do not', async () => {
  const t = await harness();
  await t.h.start();
  await t.handlers['hub:pair']({});
  await until(() => t.hub.streamCount() === 1 && lastHub(t).status === 'ok');
  const base = t.hub.requestsTo('/v1/overview').length;
  t.hub.set({ overview: { seats: [], needs_you: [{ id: 'c9', state: 'working', needs_you: true }], machines: [], counts: { needs_you: 4 } } });
  t.hub.emit('machine', { machine: {} });
  t.hub.emit('session', { session: { id: 'c9' }, change: 'state' });
  t.hub.emit('session', { session: { id: 'c9' }, change: 'update' });
  t.hub.emit('usage', { seat: 's' });
  await until(() => lastHub(t).needs_you === 4);
  assert.equal(t.hub.requestsTo('/v1/overview').length, base + 1, 'one refetch for the burst');
  await t.close();
});

test('reset refetches the overview', async () => {
  const t = await harness();
  await t.h.start();
  await t.handlers['hub:pair']({});
  await until(() => t.hub.streamCount() === 1);
  const base = t.hub.requestsTo('/v1/overview').length;
  t.hub.rawWrite('id: 77\nevent: reset\ndata: {}\n\n');
  await until(() => t.hub.requestsTo('/v1/overview').length > base);
  await t.close();
});

test('alert fired: the alert and its session are fetched and published; other changes are not', async () => {
  const t = await harness({ hubData: { alerts: { al_1: { id: 'al_1', status: 'fired', trigger: 'asked', session: 'c1' } }, sessionById: { c1: { id: 'c1', state: 'asked', question: 'proceed?' } } } });
  await t.h.start();
  await t.handlers['hub:pair']({});
  await until(() => t.hub.streamCount() === 1);
  t.hub.emit('alert', { change: 'created', alert: { id: 'al_1', status: 'pending' } });
  t.hub.emit('alert', { change: 'fired', alert: { id: 'al_1', status: 'fired' }, session: 'c1' });
  await until(() => t.events.alert.length === 1);
  assert.equal(t.events.alert[0].alert.id, 'al_1');
  assert.equal(t.events.alert[0].session.question, 'proceed?');
  await new Promise((r) => setTimeout(r, 100));
  assert.equal(t.events.alert.length, 1);
  assert.equal(await t.h.alert('nope'), null);
  assert.equal(await t.h.alert('bad/../id'), null);
  assert.equal((await t.h.session('c1')).id, 'c1');
  await t.close();
});

test('a 401 on the stream drops the token and asks to pair again', async () => {
  const t = await harness();
  await t.h.start();
  await t.handlers['hub:pair']({});
  await until(() => t.hub.streamCount() === 1);
  assert.ok(fs.existsSync(t.tokenFile));
  t.hub.script.stream401 = true;
  t.hub.cutStreams();
  await until(() => lastHub(t).status === 'needs_pairing');
  assert.equal(lastHub(t).paired, false);
  assert.equal(fs.existsSync(t.tokenFile), false);
  await t.close();
});

test('a 403 (stream or overview) is shown as an error; the token is kept', async () => {
  const t = await harness();
  await t.h.start();
  await t.handlers['hub:pair']({});
  await until(() => t.hub.streamCount() === 1);
  t.hub.script.stream403 = true;
  t.hub.cutStreams();
  await until(() => lastHub(t).error === FORBIDDEN_ERROR);
  assert.equal(lastHub(t).paired, true);
  assert.ok(fs.existsSync(t.tokenFile), 'the token file is kept');
  t.hub.script.stream403 = false;
  await until(() => t.hub.streamCount() === 1);
  // the overview answers 403 after a session event: still an error, still paired
  t.hub.script.overview = 403;
  const before = t.hub.requestsTo('/v1/overview').length;
  t.hub.emit('session', { session: { id: 'c1' }, change: 'state' });
  await until(() => t.hub.requestsTo('/v1/overview').length > before);
  await new Promise((r) => setTimeout(r, 50));
  assert.equal(lastHub(t).error, FORBIDDEN_ERROR);
  assert.equal(lastHub(t).paired, true);
  assert.ok(fs.existsSync(t.tokenFile));
  // a good overview clears it
  t.hub.script.overview = 200;
  t.hub.emit('session', { session: { id: 'c1' }, change: 'state' });
  await until(() => lastHub(t).error === null);
  assert.equal(lastHub(t).status, 'ok');
  await t.close();
});

test('an info poll that read the token file before a pairing does not undo the pairing', async () => {
  // The first readFile after `gate` is armed reads at call time (no file yet) but answers late.
  let gate = null;
  const fs2 = Object.create(fsp);
  fs2.readFile = (...a) => {
    const r = fsp.readFile(...a).then((v) => ({ v }), (e) => ({ e }));
    if (!gate) return r.then((x) => (x.e ? Promise.reject(x.e) : x.v));
    const g = gate;
    gate = null;
    return g.then(() => r).then((x) => (x.e ? Promise.reject(x.e) : x.v));
  };
  const t = await harness({ fs: fs2 });
  try {
    await racePairing(t, (g) => (gate = g), () => gate);
  } finally {
    await t.close();
  }
});

async function racePairing(t, arm, armed) {
  await t.h.start();
  assert.equal(lastHub(t).status, 'needs_pairing');
  let release;
  arm(new Promise((r) => (release = r)));
  // an info refresh (as the poll or hub:service makes) is now parked on the token read
  await t.handlers['hub:service']({ action: 'start' });
  await until(() => armed() === null);
  const r = await t.handlers['hub:pair']({});
  assert.equal(r.ok, true);
  assert.equal(lastHub(t).paired, true);
  release();
  await new Promise((res) => setTimeout(res, 100));
  assert.equal(t.h.state().paired, true, 'the stale (empty) read must not drop the fresh token');
  await until(() => t.h.state().status === 'ok');
}

test('after stop, an info refresh (a late poll or hub:service) starts no stream and asks sy nothing', async () => {
  const t = await harness();
  await t.h.start();
  await t.handlers['hub:pair']({});
  await until(() => t.hub.streamCount() === 1);
  await t.h.stop();
  await until(() => t.hub.streamCount() === 0);
  const infos = t.sy.calls.filter((c) => c[0] === 'info').length;
  await t.handlers['hub:service']({ action: 'start' });
  await new Promise((r) => setTimeout(r, 100));
  assert.equal(t.hub.streamCount(), 0, 'the stream stays down');
  assert.equal(t.sy.calls.filter((c) => c[0] === 'info').length, infos, 'no sy desktop info after stop');
  await t.close();
});

// ---- the hub view

test('route: the view is made in persist:sy-hub, guarded, signed in through the partition, then loaded', async () => {
  const t = await harness();
  await t.h.start();
  await t.handlers['hub:pair']({});
  await t.handlers['hub:route']({ route: '#/sessions/c1' });
  const add = t.winCalls.find((c) => c[0] === 'addView');
  assert.deepEqual(add.slice(0, 3), ['addView', 'hub', 'hub']);
  const view = add[3];
  assert.equal(view.options.webPreferences.partition, 'persist:sy-hub');
  assert.equal(view.options.webPreferences.kind, 'hub');
  assert.equal(t.guards.length, 1);
  assert.equal(t.guards[0].kind, 'hub');
  assert.equal(t.guards[0].o.hubOrigin, new URL(t.hub.url).origin);
  assert.deepEqual(t.guardedSessions.map((g) => g.kind), ['hub']);
  const fetches = t.fake.callsOf('session.fetch');
  assert.equal(fetches.length, 1);
  assert.equal(fetches[0][0], 'persist:sy-hub');
  assert.equal(fetches[0][1], `${t.hub.url}/v1/session/login`);
  assert.deepEqual(JSON.parse(fetches[0][2].body), { token: TOKEN });
  assert.deepEqual(t.fake.callsOf('webContents.loadURL').at(-1).slice(1, 2), [`${t.hub.url}/#/sessions/c1`]);
  assert.deepEqual(t.winCalls.at(-1), ['select', 'hub']);
  assert.equal(t.h.view(), view);
  // the token never reaches the page: no script is run in it
  assert.equal(t.fake.callsOf('webContents.executeJavaScript').length, 0);

  // a second route needs no second login (the cookie is there) and reuses the view
  await t.handlers['hub:route']({ route: '#/alerts' });
  assert.equal(t.fake.callsOf('session.fetch').filter((c) => c[1].endsWith('/v1/session/login')).length, 1);
  assert.equal(t.winCalls.filter((c) => c[0] === 'addView').length, 1);
  await t.close();
});

test('re-login when the board lands on #/login or #/pair, for the hub origin only', async () => {
  const t = await harness();
  await t.h.start();
  await t.handlers['hub:pair']({});
  await t.handlers['hub:route']({ route: '#/' });
  const wc = t.h.view().webContents;
  const logins = () => t.fake.callsOf('session.fetch').filter((c) => c[1].endsWith('/v1/session/login')).length;
  assert.equal(logins(), 1);
  // a foreign origin with a login route never gets a cookie login
  wc.emit('did-navigate-in-page', {}, 'https://evil.example/#/login');
  wc.emit('did-navigate', {}, 'https://evil.example/#/pair');
  await new Promise((r) => setTimeout(r, 50));
  assert.equal(logins(), 1);
  // the hub's own #/login re-logs in
  wc.emit('did-navigate-in-page', {}, `${t.hub.url}/#/login`);
  await until(() => logins() === 2);
  // and not more than twice in a window (no loop)
  wc.emit('did-navigate-in-page', {}, `${t.hub.url}/#/pair`);
  wc.emit('did-navigate-in-page', {}, `${t.hub.url}/#/login`);
  await new Promise((r) => setTimeout(r, 100));
  assert.ok(logins() <= 3);
  for (const c of t.fake.callsOf('session.fetch')) assert.ok(c[1].startsWith(t.hub.url), `session.fetch only to the hub: ${c[1]}`);
  await t.close();
});

test('login refused with 401: the token is dropped', async () => {
  const t = await harness();
  await t.h.start();
  await t.handlers['hub:pair']({});
  t.hub.script.login = 401;
  await t.handlers['hub:route']({ route: '#/' });
  await until(() => lastHub(t).status === 'needs_pairing');
  assert.equal(fs.existsSync(t.tokenFile), false);
  await t.close();
});

test('bad routes are refused and the hub URL is the only target', async () => {
  const t = await harness();
  await t.h.start();
  await t.handlers['hub:pair']({});
  for (const bad of ['', '/x', 'https://evil.example/', '#x', '#/a b', '#/a\nb', `#/${'a'.repeat(300)}`, null, 5]) await t.h.route(bad);
  assert.equal(t.winCalls.filter((c) => c[0] === 'addView').length, 0);
  assert.equal(t.fake.callsOf('webContents.loadURL').length, 0);
  await t.close();
});

test('owner sign-in loads the one-time link; the owner flag follows /v1/me', async () => {
  const t = await harness();
  await t.h.start();
  await t.handlers['hub:pair']({});
  await t.handlers['hub:route']({ route: '#/' }); // signs the partition in, as the owner link would
  const r = await t.handlers['hub:owner-sign-in']();
  assert.deepEqual(r, { ok: true });
  assert.equal(t.fake.callsOf('webContents.loadURL').at(-1)[1], `${t.hub.url}/#/owner?s=one-time`);
  assert.deepEqual(t.winCalls.at(-1), ['select', 'hub']);
  t.hub.set({ me: { device: { id: 'd' }, owner: true, hub: {} } });
  t.h.view().webContents.emit('did-finish-load');
  await until(() => lastHub(t).owner === true);
  await t.close();
});

test('owner sign-in: a link for another origin is never opened; failures are explained', async () => {
  const t = await harness();
  await t.h.start();
  t.sy.state.owner = { ok: true, url: 'https://evil.example/#/owner?s=x' };
  const r = await t.handlers['hub:owner-sign-in']();
  assert.equal(r.ok, false);
  assert.equal(t.fake.callsOf('webContents.loadURL').length, 0);
  t.sy.state.owner = { ok: false, exit: 4, error: 'no admin' };
  assert.match((await t.handlers['hub:owner-sign-in']()).error, /admin channel/);
  await t.close();
});

test('seat:update goes through the owner cookie (PATCH), never the desktop token', async () => {
  const t = await harness();
  await t.h.start();
  await t.handlers['hub:pair']({});
  const denied = await t.handlers['seat:update']({ seat: SEAT.id, label: 'home', colour: '#aabbcc' });
  assert.equal(denied.ok, false);
  assert.match(denied.error, /owner/i);
  t.hub.set({ me: { device: { id: 'd' }, owner: true, hub: {} } });
  const ok = await t.handlers['seat:update']({ seat: SEAT.id, label: 'home', colour: null });
  assert.deepEqual(ok, { ok: true });
  assert.deepEqual(t.hub.log.at(-1), { patch: SEAT.id, body: { label: 'home', colour: null } });
  const patch = t.hub.requests.filter((r) => r.method === 'PATCH').at(-1);
  assert.equal(patch.headers.authorization, undefined, 'no bearer token on the owner call');
  assert.ok(patch.headers.cookie.includes('sy_session'));
  assert.equal((await t.handlers['seat:update']({ seat: SEAT.id })).ok, false);
  await t.close();
});

// ---- the claude.ai handoff

test('a claude.ai session link goes to the tab with the matching seat', async () => {
  const link = 'https://claude.ai/code/session_ABC123';
  const t = await harness({
    hubData: {
      accounts: { accounts: [SEAT, { id: 'seat_01HYYYYYYYYYYYYYYYYYYYYYYY', account: 'acc-2', org: 'org-2' }] },
      sessionById: { c1: { id: 'c1', seat: SEAT.id, remote_url: link } },
      sessions: { sessions: [{ id: 'c1', seat: SEAT.id, remote_url: link }], next: null },
    },
  });
  await t.h.start();
  await t.handlers['hub:pair']({});
  await until(() => t.h.seats().length === 2);
  await t.handlers['hub:route']({ route: '#/sessions/c1' });
  const onLink = t.guards[0].o.onSessionLink;
  await onLink(link);
  await until(() => t.links.length === 1);
  assert.deepEqual(t.links[0], { url: link, seat: { id: SEAT.id, account: 'acc-1', org: 'org-1' } });
  assert.deepEqual(t.opened, []);

  // from a screen that is not the session's: found through the session list
  t.fake.callsOf('webContents.loadURL');
  t.h.view().webContents._setURL(`${t.hub.url}/#/alerts`);
  await onLink(link);
  await until(() => t.links.length === 2);
  assert.equal(t.links[1].seat.id, SEAT.id);
  await t.close();
});

test('no matching seat: openLink gets null (it picks the first signed-in tab); no tab: the system browser', async () => {
  const link = 'https://claude.ai/code/session_ZZZ';
  const t = await harness({ openResult: false });
  await t.h.start();
  await t.handlers['hub:pair']({});
  await t.handlers['hub:route']({ route: '#/alerts' });
  await t.guards[0].o.onSessionLink(link);
  await until(() => t.opened.length === 1);
  assert.deepEqual(t.links[0], { url: link, seat: null });
  assert.deepEqual(t.opened, [link]);
  // anything that is not a session link is ignored
  await t.guards[0].o.onSessionLink('https://claude.ai/settings');
  await t.guards[0].o.onSessionLink('https://evil.example/code/session_ABC');
  assert.equal(t.links.length, 1);
  await t.close();
});

// ---- other handlers

test('hub:service, accounts:local and accounts:share', async () => {
  const t = await harness();
  await t.h.start();
  assert.deepEqual(await t.handlers['hub:service']({ action: 'stop' }), { ok: true, service: 'running', health: true });
  t.sy.state.service = { ok: false, exit: 4, error: 'service not installed' };
  const bad = await t.handlers['hub:service']({ action: 'start' });
  assert.equal(bad.ok, false);
  assert.equal(bad.service, null);
  assert.deepEqual(await t.handlers['accounts:local'](), { ok: true, accounts: [{ folder: '~/.claude' }] });
  assert.deepEqual(await t.handlers['accounts:share']({ folder: '~/.claude-2', share: 'skills' }), { ok: true });
  await t.close();
});

test('every handler of the WP is registered', async () => {
  const t = await harness();
  assert.deepEqual(Object.keys(t.handlers).sort(), ['accounts:local', 'accounts:share', 'hub:owner-sign-in', 'hub:pair', 'hub:route', 'hub:service', 'seat:update']);
  await t.close();
});

test('a URL change at a later info drops the view and the stream', async () => {
  const t = await harness();
  await t.h.start();
  await t.handlers['hub:pair']({});
  await t.handlers['hub:route']({ route: '#/' });
  assert.ok(t.h.view());
  t.sy.state.info = info('https://other.example.com');
  await t.handlers['hub:pair']({}).catch(() => {});
  assert.equal(t.h.view(), null);
  assert.ok(t.winCalls.some((c) => c[0] === 'removeView'));
  await t.close();
});

test('pairError: exit 1 shows sy\'s own message; exit 4 without a code is not called "no admin channel"', () => {
  assert.equal(pairError({ exit: 1, error: '--hub-admin ssh oci sy: exec: "ssh": not found' }, false), 'Pairing failed: --hub-admin ssh oci sy: exec: "ssh": not found');
  assert.equal(pairError({ exit: 1 }, false), 'Pairing failed.');
  assert.match(pairError({ exit: 4 }, false), /machines\.hub_admin/);
  assert.doesNotMatch(pairError({ exit: 4 }, false), /no admin channel/i);
  assert.match(pairError({ exit: 3 }, true), /refused or has expired/);
  assert.equal(pairError({ exit: 5 }, false), 'The hub is not reachable.');
});
