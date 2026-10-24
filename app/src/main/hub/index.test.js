import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import fsp from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { create, cliVersion, pairError, FORBIDDEN_ERROR } from './index.js';
import { createBus } from '../bus.js';
import { fakeElectron } from '../../../test/fakes/electron.js';
import { startFakeHub } from '../../../test/fakes/hub.js';
import { haveOpenssl, makeCert, startTlsFront } from '../../../test/fakes/tls.js';
import { probe as probeCertificate } from './pin.js';

const silent = { warn() {}, error() {}, debug() {}, info() {} };
const until = async (fn, ms = 3000) => {
  const t = Date.now();
  while (!(await fn())) {
    if (Date.now() - t > ms) throw new Error(`timed out waiting for: ${fn}`);
    await new Promise((r) => setTimeout(r, 10));
  }
};
const SEAT = { id: 'seat_01HZZZZZZZZZZZZZZZZZZZZZZZ', account: 'acc-1', org: 'org-1', label: 'work' };
const TOKEN = 'wy_d_good';

function info(hubUrl, over = {}) {
  return {
    ok: true, v: 1, wy: '0.6.0', machine: { id: 'm_1', name: 'lap top' },
    hub: { url: hubUrl, connected: true, reachable: true, version: '0.6.0', local: true, dial: null, service: 'running', ...(over.hub ?? {}) },
    admin: 'local', outbox: { pending: 0, last_error: null }, ...Object.fromEntries(Object.entries(over).filter(([k]) => k !== 'hub')),
  };
}

async function harness(opts = {}) {
  const hub = await startFakeHub(opts.hubData);
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'wy-hubtest-'));
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
  const wy = {
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
    setViewHidden: (id, hidden) => winCalls.push(['hidden', id, hidden]),
  };
  const slow = { setTimeout: (fn, ms) => setTimeout(fn, ms <= 1000 ? 15 : ms), clearTimeout };
  const links = [];
  const h = create({
    electron: fake.electron, wy, security, window, bus, ipc: { register: (c, fn) => (handlers[c] = fn) }, fs: opts.fs ?? fsp, path, tokenFile,
    fetch: globalThis.fetch, log: silent, timers: slow, platform: 'linux', clientVersion: opts.clientVersion,
    probeCertificate: opts.probe ?? (async () => ({ ok: false, error: 'no TLS probe in this test' })),
    openSessionLink: (url, seat) => { links.push({ url, seat }); return opts.openResult ?? true; },
  });
  const events = { hub: [], needs_you: [], alert: [] };
  for (const k of Object.keys(events)) bus.on(k, (p) => events[k].push(p));
  const close = async () => { await h.stop(); await hub.close(); };
  return { hub, h, wy, fake, bus, handlers, winCalls, guards, opened, links, events, tokenFile, dir, security, close, sessionFetch, guardedSessions };
}

const lastHub = (t) => t.events.hub.at(-1);

test('no wy: the status says so and nothing else happens', async () => {
  const t = await harness();
  t.wy.state.info = { ok: false, exit: -1, error: 'wy not found', code: 'no_wy' };
  await t.h.start();
  assert.equal(lastHub(t).status, 'no_wy');
  assert.equal(lastHub(t).connected, false);
  assert.equal((await t.handlers['hub:pair']({})).ok, false);
  assert.equal(t.hub.requests.length, 0);
  await t.close();
});

test('wy not connected to a hub, and forward mode (dial) are refused', async () => {
  const t = await harness();
  t.wy.state.info = info(null, { hub: { connected: false, url: null, reachable: false } });
  await t.h.start();
  assert.equal(lastHub(t).status, 'not_connected');
  t.wy.state.info = info(t.hub.url, { hub: { dial: 'ssh gpu-box' } });
  await t.h.stop();
  const t2 = await harness();
  t2.wy.state.info = info(t2.hub.url, { hub: { dial: 'ssh gpu-box' } });
  await t2.h.start();
  assert.equal(lastHub(t2).status, 'dial');
  assert.match(lastHub(t2).error, /forward mode is not supported/);
  assert.equal(t2.hub.requests.length, 0, 'no token, no stream');
  await t2.close();
  await t.close();
});

test('pairing: one click through wy, token stored encrypted, stream and overview start', async () => {
  const t = await harness({ hubData: { overview: { seats: [SEAT], needs_you: [{ id: 'c1', state: 'asked', seat_label: 'work', project: 'api', needs_you: true }], machines: [], counts: { needs_you: 1 } }, accounts: { accounts: [SEAT] } } });
  await t.h.start();
  assert.equal(lastHub(t).status, 'needs_pairing');
  assert.equal(lastHub(t).paired, false);
  const r = await t.handlers['hub:pair']({});
  assert.deepEqual(r, { ok: true });
  const tokenCall = t.wy.calls.find((c) => c[0] === 'token');
  assert.equal(tokenCall[1].code, undefined);
  assert.equal(tokenCall[1].name, 'Watchyard-desktop-laptop');
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

test('pairing with a code passes it to wy (which sends it on stdin); a refusal gets a plain message', async () => {
  const t = await harness();
  await t.h.start();
  t.wy.state.token = { ok: false, exit: 3, error: 'code expired' };
  const r = await t.handlers['hub:pair']({ code: '123-456' });
  assert.equal(r.ok, false);
  assert.match(r.error, /refused or has expired/);
  assert.equal(t.wy.calls.find((c) => c[0] === 'token')[1].code, '123-456');
  t.wy.state.token = { ok: false, exit: 4, error: 'no admin' };
  assert.match((await t.handlers['hub:pair']({})).error, /type a code from the board/i);
  t.wy.state.token = { ok: false, exit: 5, error: 'x' };
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
    const sy2 = { ...t.wy, info: async () => info(url) };
    return create({
      electron: t.fake.electron, wy: sy2, security: t.security, window: { addView() {}, removeView() {}, select() {} }, bus: bus2,
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
    electron: otherFake.electron, wy: a.wy, security: a.security, window: { addView() {}, removeView() {}, select() {} }, bus: bus2,
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

test('a seats event (the owner reordered the accounts) refetches the accounts', async () => {
  const t = await harness();
  await t.h.start();
  await t.handlers['hub:pair']({});
  await until(() => t.hub.streamCount() === 1 && lastHub(t).status === 'ok');
  const base = t.hub.requestsTo('/v1/accounts').length;
  t.hub.emit('seats', { order: ['s2', 's1'] });
  await until(() => t.hub.requestsTo('/v1/accounts').length > base);
  await t.close();
});

test('reset refetches the overview', async () => {
  const t = await harness();
  await t.h.start();
  await t.handlers['hub:pair']({});
  await until(() => t.hub.streamCount() === 1 && lastHub(t).status === 'ok');
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

test('after stop, an info refresh (a late poll or hub:service) starts no stream and asks wy nothing', async () => {
  const t = await harness();
  await t.h.start();
  await t.handlers['hub:pair']({});
  await until(() => t.hub.streamCount() === 1);
  await t.h.stop();
  await until(() => t.hub.streamCount() === 0);
  const infos = t.wy.calls.filter((c) => c[0] === 'info').length;
  await t.handlers['hub:service']({ action: 'start' });
  await new Promise((r) => setTimeout(r, 100));
  assert.equal(t.hub.streamCount(), 0, 'the stream stays down');
  assert.equal(t.wy.calls.filter((c) => c[0] === 'info').length, infos, 'no wy desktop info after stop');
  await t.close();
});

// ---- the hub view

test('route: the view is made in persist:wy-hub, guarded, signed in through the partition, then loaded', async () => {
  const t = await harness();
  await t.h.start();
  await t.handlers['hub:pair']({});
  await t.handlers['hub:route']({ route: '#/sessions/c1' });
  const add = t.winCalls.find((c) => c[0] === 'addView');
  assert.deepEqual(add.slice(0, 3), ['addView', 'hub', 'hub']);
  const view = add[3];
  assert.equal(view.options.webPreferences.partition, 'persist:wy-hub');
  assert.equal(view.options.webPreferences.kind, 'hub');
  assert.equal(t.guards.length, 1);
  assert.equal(t.guards[0].kind, 'hub');
  assert.equal(t.guards[0].o.hubOrigin, new URL(t.hub.url).origin);
  assert.deepEqual(t.guardedSessions.map((g) => g.kind), ['hub']);
  const fetches = t.fake.callsOf('session.fetch');
  assert.equal(fetches.length, 1);
  assert.equal(fetches[0][0], 'persist:wy-hub');
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
  t.wy.state.owner = { ok: true, url: 'https://evil.example/#/owner?s=x' };
  const r = await t.handlers['hub:owner-sign-in']();
  assert.equal(r.ok, false);
  assert.equal(t.fake.callsOf('webContents.loadURL').length, 0);
  t.wy.state.owner = { ok: false, exit: 4, error: 'no admin' };
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
  assert.ok(patch.headers.cookie.includes('wy_session'));
  assert.equal((await t.handlers['seat:update']({ seat: SEAT.id })).ok, false);
  await t.close();
});

test('one board session: no new login while the cookie is there, and the cookie is flushed to disk', async () => {
  const t = await harness();
  await t.h.start();
  await t.handlers['hub:pair']({});
  await t.handlers['hub:route']({ route: '#/' });
  const logins = () => t.fake.callsOf('session.fetch').filter((c) => c[1].endsWith('/v1/session/login')).length;
  assert.equal(logins(), 1);
  const flushes = () => t.fake.callsOf('cookies.flushStore').filter((c) => c[0] === 'persist:wy-hub').length;
  assert.ok(flushes() >= 1, 'the new cookie is written at once');
  // a seat edit without owner rights used to log in again (a new board session each time)
  await t.handlers['seat:update']({ seat: SEAT.id, label: 'x' });
  await t.handlers['seat:update']({ seat: SEAT.id, label: 'y' });
  assert.equal(logins(), 1);
  const before = flushes();
  await t.h.stop();
  assert.equal(flushes(), before + 1, 'stop flushes the hub cookie');
  await t.close();
});

// ---- the board's own login page is never shown in the app

const hiddenNow = (t) => t.winCalls.filter((c) => c[0] === 'hidden').at(-1)?.[2] === true;
const loginCount = (t) => t.fake.callsOf('session.fetch').filter((c) => c[1].endsWith('/v1/session/login')).length;

test('a dead token (401 on the stream): the board is hidden, needs_pairing with "removed", then pairing shows it again', async () => {
  const t = await harness();
  await t.h.start();
  await t.handlers['hub:pair']({});
  await t.handlers['hub:route']({ route: '#/' });
  assert.equal(lastHub(t).removed, false);
  t.hub.script.stream401 = true;
  t.hub.cutStreams();
  await until(() => lastHub(t).status === 'needs_pairing');
  assert.equal(lastHub(t).removed, true);
  assert.equal(lastHub(t).paired, false);
  assert.equal(hiddenNow(t), true, 'the board view is hidden');
  assert.equal(fs.existsSync(t.tokenFile), false);
  t.hub.script.stream401 = false;
  t.wy.state.token = { ok: true, device: 'dev_2', token: TOKEN, scopes: ['write', 'read'], hub: { url: t.hub.url } }; // pair() blanks the one it was given
  assert.deepEqual(await t.handlers['hub:pair']({}), { ok: true });
  assert.equal(lastHub(t).removed, false);
  assert.equal(hiddenNow(t), false, 'paired again: the board shows');
  await t.close();
});

test('login refused with 401 while the board lands on #/login: dead token, board hidden, removed', async () => {
  const t = await harness();
  await t.h.start();
  await t.handlers['hub:pair']({});
  await t.handlers['hub:route']({ route: '#/' });
  t.hub.script.login = 401;
  t.h.view().webContents.emit('did-navigate-in-page', {}, `${t.hub.url}/#/login`);
  await until(() => lastHub(t).status === 'needs_pairing');
  assert.equal(lastHub(t).removed, true);
  assert.equal(hiddenNow(t), true);
  await t.close();
});

test('the board cookie revoked but the token good: a stale cookie in the jar is not trusted, main logs in silently', async () => {
  const t = await harness();
  await t.h.start();
  await t.handlers['hub:pair']({});
  // a revoked board session: the cookie is still there, the hub no longer knows it
  await t.fake.electron.session.fromPartition('persist:wy-hub').cookies.set({ url: t.hub.url, name: 'wy_session', value: 'revoked' });
  const before = loginCount(t);
  await t.handlers['hub:route']({ route: '#/' });
  assert.equal(loginCount(t), before + 1, 'logged in again although a cookie was present');
  assert.equal(hiddenNow(t), false);
  assert.equal(lastHub(t).status, 'ok');
  assert.equal(lastHub(t).board_blocked, false);
  await t.close();
});

test('the board lands on #/login later (no cookie any more): relogin runs, the page is hidden meanwhile and shown after', async () => {
  const t = await harness();
  await t.h.start();
  await t.handlers['hub:pair']({});
  await t.handlers['hub:route']({ route: '#/' });
  const before = loginCount(t);
  t.h.view().webContents._setURL(`${t.hub.url}/#/login`);
  t.h.view().webContents.emit('did-finish-load'); // no did-navigate event on this path
  await until(() => loginCount(t) === before + 1);
  await until(() => hiddenNow(t) === false);
  assert.equal(lastHub(t).board_blocked, false);
  await t.close();
});

test('still on #/login after the relogin (rate limit) or a failed login: the board stays hidden and the state says so', async () => {
  const t = await harness();
  await t.h.start();
  await t.handlers['hub:pair']({});
  await t.handlers['hub:route']({ route: '#/' });
  const wc = t.h.view().webContents;
  for (let i = 0; i < 4; i++) {
    wc.emit('did-navigate-in-page', {}, `${t.hub.url}/#/login`);
    await new Promise((r) => setTimeout(r, 30));
  }
  await until(() => lastHub(t).board_blocked === true);
  assert.equal(hiddenNow(t), true);
  assert.equal(lastHub(t).status, 'ok');
  assert.equal(lastHub(t).paired, true, 'a rate limit is not a dead token');
  await t.close();
  const u = await harness();
  await u.h.start();
  await u.handlers['hub:pair']({});
  await u.handlers['hub:route']({ route: '#/' });
  u.hub.script.login = 500;
  u.h.view().webContents.emit('did-navigate-in-page', {}, `${u.hub.url}/#/pair`);
  await until(() => lastHub(u).board_blocked === true);
  assert.equal(hiddenNow(u), true);
  assert.equal(lastHub(u).paired, true);
  await u.close();
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

test('no matching seat: openLink gets null (the old first-signed-in-tab fallback); no tab: the system browser', async () => {
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
  t.wy.state.service = { ok: false, exit: 4, error: 'service not installed' };
  const bad = await t.handlers['hub:service']({ action: 'start' });
  assert.equal(bad.ok, false);
  assert.equal(bad.service, null);
  assert.deepEqual(await t.handlers['accounts:local'](), { ok: true, accounts: [{ folder: '~/.claude' }] });
  assert.deepEqual(await t.handlers['accounts:share']({ folder: '~/.claude-2', share: 'skills' }), { ok: true });
  await t.close();
});

test('every handler of the WP is registered', async () => {
  const t = await harness();
  assert.deepEqual(Object.keys(t.handlers).sort(), ['accounts:local', 'accounts:share', 'app:open-release', 'hub:connect', 'hub:owner-sign-in', 'hub:owner-sign-out', 'hub:pair', 'hub:retry', 'hub:route', 'hub:service', 'hub:trust-cert', 'hub:unpair', 'seat:update']);
  await t.close();
});

test('a URL change at a later info drops the view and the stream', async () => {
  const t = await harness();
  await t.h.start();
  await t.handlers['hub:pair']({});
  await t.handlers['hub:route']({ route: '#/' });
  assert.ok(t.h.view());
  t.wy.state.info = info('https://other.example.com');
  await t.handlers['hub:pair']({}).catch(() => {});
  assert.equal(t.h.view(), null);
  assert.ok(t.winCalls.some((c) => c[0] === 'removeView'));
  await t.close();
});

test('pairError: exit 1 shows wy\'s own message; exit 4 without a code is not called "no admin channel"', () => {
  assert.equal(pairError({ exit: 1, error: '--hub-admin ssh gpu-box wy: exec: "ssh": not found' }, false), 'Pairing failed: --hub-admin ssh gpu-box wy: exec: "ssh": not found');
  assert.equal(pairError({ exit: 1 }, false), 'Pairing failed.');
  assert.match(pairError({ exit: 4 }, false), /machines\.hub_admin/);
  assert.doesNotMatch(pairError({ exit: 4 }, false), /no admin channel/i);
  assert.match(pairError({ exit: 3 }, true), /refused or has expired/);
  assert.equal(pairError({ exit: 5 }, false), 'The hub is not reachable.');
});

// ---- direct mode (no wy): the app connects, pairs, uploads and unpairs by itself

const NO_WY = { ok: false, code: 'no_wy', error: 'wy was not found' };
const directHarness = async (opts) => {
  const t = await harness(opts);
  t.wy.state.info = NO_WY;
  return t;
};
const readJson = (file) => JSON.parse(fs.readFileSync(file, 'utf8'));

test('direct: without wy the Hub tab says no hub; Connect checks the address, remembers it and pairs with the code', async () => {
  const t = await directHarness();
  await t.h.start();
  assert.equal(lastHub(t).status, 'no_wy');
  const r = await t.handlers['hub:connect']({ url: t.hub.url, code: '123-456' });
  assert.deepEqual(r, { ok: true });
  assert.equal(lastHub(t).status, 'ok');
  assert.equal(lastHub(t).direct, true);
  assert.equal(lastHub(t).paired, true);
  assert.equal(lastHub(t).url, t.hub.url);
  // POST /v1/pair/claim: the same body wy sends, with the app's own machine id
  const [claim] = t.hub.requestsTo('/v1/pair/claim');
  const body = JSON.parse(claim.body);
  assert.equal(body.code, '123456');
  assert.equal(body.expect_kind, 'desktop');
  assert.match(body.machine, /^[0-9A-Za-z_-]{16,64}$/);
  assert.match(body.name, /^Watchyard-desktop/);
  assert.equal(claim.headers.authorization, undefined, 'no token yet');
  // the token is stored like today, the address and the machine id in direct.json
  assert.ok(fs.existsSync(t.tokenFile));
  const saved = readJson(path.join(t.dir, 'direct.json'));
  assert.equal(saved.url, t.hub.url);
  assert.equal(saved.machine, body.machine);
  assert.equal(t.wy.calls.filter((c) => c[0] === 'token').length, 0, 'wy was not asked');
  await until(() => t.hub.streamCount() === 1);
  await t.close();
});

test('direct: Connect without a code only remembers the address; pairing then needs a code', async () => {
  const t = await directHarness();
  await t.h.start();
  assert.deepEqual(await t.handlers['hub:connect']({ url: t.hub.url }), { ok: true });
  assert.equal(lastHub(t).status, 'needs_pairing');
  assert.equal(lastHub(t).paired, false);
  assert.match((await t.handlers['hub:pair']({})).error, /pairing code/i);
  t.hub.script.claim = 400;
  assert.match((await t.handlers['hub:pair']({ code: '111-222' })).error, /refused or has expired/);
  t.hub.script.claim = 429;
  assert.match((await t.handlers['hub:pair']({ code: '111-222' })).error, /Too many/);
  t.hub.script.claim = 200;
  assert.deepEqual(await t.handlers['hub:pair']({ code: '111-222' }), { ok: true });
  assert.equal(lastHub(t).status, 'ok');
  await t.close();
});

test('direct: a bad address is refused before anything is saved', async () => {
  const t = await directHarness();
  await t.h.start();
  assert.match((await t.handlers['hub:connect']({ url: 'http://hub.example.com' })).error, /https/);
  assert.match((await t.handlers['hub:connect']({ url: 'http://127.0.0.1:9' })).error, /No hub answered/);
  assert.equal(fs.existsSync(path.join(t.dir, 'direct.json')) && readJson(path.join(t.dir, 'direct.json')).url, null);
  assert.equal(lastHub(t).status, 'no_wy');
  await t.close();
});

test('with wy connected to a hub, Connect is refused and wy keeps doing the pairing', async () => {
  const t = await harness();
  await t.h.start();
  assert.match((await t.handlers['hub:connect']({ url: t.hub.url })).error, /wy on this computer is already connected/);
  assert.deepEqual(await t.handlers['hub:pair']({}), { ok: true });
  assert.equal(lastHub(t).direct, false);
  assert.equal(t.hub.requestsTo('/v1/pair/claim').length, 0);
  await t.close();
});

test('direct: usage records are uploaded to /v1/ingest with the desktop token, completed like wy outbox append', async () => {
  const t = await directHarness();
  await t.h.start();
  await t.handlers['hub:connect']({ url: t.hub.url, code: '123456' });
  const machine = await t.h.direct.machineId();
  const rec = { kind: 'web_usage', ts: '2026-10-16T10:00:00.000Z', machine, seat: { account: 'a', org: 'o', src: 'claude_ai' }, client: 'watchyard-desktop/0.6.0', payload: { tab_id: 't' } };
  const res = await t.h.direct.append([rec, { ...rec, ts: undefined }]);
  assert.equal(res.ok, true);
  assert.equal(res.ids.length, 2);
  await t.h.direct.flushNow();
  const [ing] = t.hub.requestsTo('/v1/ingest');
  assert.equal(ing.headers.authorization, `Bearer ${TOKEN}`);
  const b = JSON.parse(ing.body);
  assert.equal(b.v, 1);
  assert.match(b.client, /^watchyard-desktop\//);
  assert.equal(b.records.length, 2);
  assert.match(b.records[0].id, /^[0-9A-HJKMNP-TV-Z]{26}$/);
  assert.equal(b.records[0].v, 1);
  assert.equal(b.records[0].kind, 'web_usage');
  assert.match(b.records[1].ts, /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$/);
  assert.equal(t.h.direct.queued(), 0);
  await t.close();
});

test('direct: a 401 on the upload drops the token like any dead token', async () => {
  const t = await directHarness();
  await t.h.start();
  await t.handlers['hub:connect']({ url: t.hub.url, code: '123456' });
  await t.h.direct.append([{ kind: 'web_usage', machine: 'm', payload: {} }]);
  t.hub.script.revokedSelf = true; // the device was removed on the hub
  await t.h.direct.flushNow();
  await until(() => lastHub(t).status === 'needs_pairing');
  assert.equal(lastHub(t).removed, true);
  assert.equal(t.h.direct.queued(), 1, 'the record stays queued for later');
  await t.close();
});

test('unpair: the app revokes its own device, forgets the token, hides the board and goes back to the pair card', async () => {
  const t = await harness();
  await t.h.start();
  await t.handlers['hub:pair']({});
  await t.handlers['hub:route']({ route: '#/' });
  assert.equal(fs.existsSync(t.tokenFile), true);
  const r = await t.handlers['hub:unpair']({});
  assert.deepEqual(r, { ok: true, revoked: true });
  assert.equal(t.hub.requestsTo('/v1/devices/self').filter((q) => q.method === 'DELETE').length, 1);
  assert.equal(t.hub.script.revokedSelf, true);
  assert.equal(fs.existsSync(t.tokenFile), false);
  assert.equal(lastHub(t).status, 'needs_pairing');
  assert.equal(lastHub(t).paired, false);
  assert.equal(lastHub(t).removed, false, 'a deliberate unpair is not a removal');
  assert.equal(hiddenNow(t), true);
  assert.ok(t.fake.callsOf('session.clearStorageData').length >= 1, 'cookies cleared');
  await t.close();
});

test('unpair with the hub unreachable still drops everything locally and says what is left to do', async () => {
  const t = await harness();
  await t.h.start();
  await t.handlers['hub:pair']({});
  await t.hub.close();
  const r = await t.handlers['hub:unpair']({});
  assert.equal(r.ok, true);
  assert.equal(r.revoked, false);
  assert.match(r.warning, /Devices/);
  assert.equal(fs.existsSync(t.tokenFile), false);
  await t.h.stop();
});

test('direct unpair with forget drops the remembered address too: back to the Connect card', async () => {
  const t = await directHarness();
  await t.h.start();
  await t.handlers['hub:connect']({ url: t.hub.url, code: '123456' });
  await t.handlers['hub:unpair']({ forget: true });
  assert.equal(readJson(path.join(t.dir, 'direct.json')).url, null);
  assert.equal(lastHub(t).status, 'no_wy');
  await t.close();
});

test('owner link pasted into the app: only a link to the paired hub is opened', async () => {
  const t = await harness();
  await t.h.start();
  await t.handlers['hub:pair']({});
  await t.handlers['hub:route']({ route: '#/' });
  const before = t.fake.callsOf('webContents.loadURL').length;
  assert.match((await t.handlers['hub:owner-sign-in']({ link: 'https://evil.example/#/owner?s=x' })).error, /another address/);
  assert.match((await t.handlers['hub:owner-sign-in']({ link: 'not a link' })).error, /not a link/);
  assert.equal(t.fake.callsOf('webContents.loadURL').length, before);
  const link = `${t.hub.url}/#/owner?s=pasted`;
  assert.deepEqual(await t.handlers['hub:owner-sign-in']({ link }), { ok: true });
  assert.equal(t.fake.callsOf('webContents.loadURL').at(-1)[1], link);
  assert.equal(t.wy.calls.filter((c) => c[0] === 'owner').length, 0, 'wy was not asked');
  await t.close();
});

test('owner sign-in without wy asks for a pasted link instead of calling wy', async () => {
  const t = await directHarness();
  await t.h.start();
  await t.handlers['hub:connect']({ url: t.hub.url, code: '123456' });
  assert.match((await t.handlers['hub:owner-sign-in']({})).error, /Paste an owner link/);
  await t.close();
});

test('the owner pill data: owner and until come from /v1/me; sign out of owner returns to read-only', async () => {
  const t = await harness();
  await t.h.start();
  await t.handlers['hub:pair']({});
  await t.handlers['hub:route']({ route: '#/' });
  t.hub.set({ me: { device: { id: 'd', expires_at: '2026-11-14T10:00:00Z' }, owner: true, hub: {} } });
  t.h.view().webContents.emit('did-finish-load');
  await until(() => lastHub(t).owner === true);
  assert.equal(lastHub(t).owner_until, '2026-11-14T10:00:00Z');
  t.hub.set({ me: { device: { id: 'd' }, owner: false, hub: {} } });
  assert.deepEqual(await t.handlers['hub:owner-sign-out'](), { ok: true });
  assert.equal(t.hub.requestsTo('/v1/session/logout').length, 1);
  await until(() => lastHub(t).owner === false);
  assert.equal(lastHub(t).owner_until, null);
  await t.close();
});

test('the hub view announces the app in its user agent', async () => {
  const t = await harness();
  await t.h.start();
  await t.handlers['hub:pair']({});
  await t.handlers['hub:route']({ route: '#/' });
  const ua = t.fake.callsOf('webContents.setUserAgent').map((c) => c[1]).find((u) => /WatchyardDesktop/.test(u));
  assert.match(ua, / WatchyardDesktop(\/|$)/);
  await t.close();
});

test('one failed health check does not flip a reachable hub to Unreachable; two in a row do, and a good one recovers', async () => {
  const t = await harness();
  await t.h.start();
  await t.handlers['hub:pair']({});
  await until(() => lastHub(t).status === 'ok');
  const down = info(t.hub.url, { hub: { reachable: false, version: null } });
  const up = info(t.hub.url);
  t.wy.state.info = down;
  await t.handlers['hub:retry']();
  assert.equal(lastHub(t).status, 'ok', 'first failure: the board stays');
  assert.equal(lastHub(t).reachable, true);
  await t.handlers['hub:retry']();
  assert.equal(lastHub(t).status, 'unreachable', 'second failure in a row');
  t.wy.state.info = up;
  await t.handlers['hub:retry']();
  assert.equal(lastHub(t).status, 'ok');
  t.wy.state.info = down;
  await t.handlers['hub:retry']();
  t.wy.state.info = up;
  await t.handlers['hub:retry']();
  t.wy.state.info = down;
  await t.handlers['hub:retry']();
  assert.equal(lastHub(t).status, 'ok', 'failures that are not consecutive never count');
  await t.close();
});

test('a hub never seen reachable is reported unreachable at once', async () => {
  const t = await harness();
  t.wy.state.info = info(t.hub.url, { hub: { reachable: false, version: null } });
  await t.h.start();
  await t.handlers['hub:pair']({}).catch(() => {});
  assert.equal(lastHub(t).reachable, false);
  await t.close();
});

// ---- the certificate of an https hub (C6): trust on first use, pinned, refused when it changes

const NO_OPENSSL = haveOpenssl() ? false : 'the openssl CLI is not installed';
const certDir = fs.mkdtempSync(path.join(os.tmpdir(), 'wy-hubcert-'));
const certs = NO_OPENSSL ? {} : { a: makeCert(certDir, 'a'), b: makeCert(certDir, 'b') };

test('direct: a self-signed https hub is pinned once the person confirms its fingerprint; a changed certificate is refused until re-pinned', { skip: NO_OPENSSL }, async () => {
  const t = await directHarness({ probe: probeCertificate });
  const front = await startTlsFront({ target: t.hub.url, ...certs.a });
  const directFile = path.join(t.dir, 'direct.json');
  await t.h.start();
  const r = await t.handlers['hub:connect']({ url: front.url, code: '123-456' });
  assert.equal(r.ok, false);
  assert.match(r.error, /not signed by an authority this computer trusts/);
  assert.equal(r.cert.fingerprint, certs.a.fingerprint);
  assert.equal(r.cert.changed, false);
  assert.equal(r.cert.origin, front.url);
  assert.equal(front.requests.length, 0, 'nothing was sent before the person decided');
  assert.equal(fs.existsSync(directFile) ? readJson(directFile).url : null, null);

  assert.match((await t.handlers['hub:trust-cert']({ url: front.url, fingerprint: certs.b.fingerprint })).error, /different certificate/);
  assert.match((await t.handlers['hub:trust-cert']({ url: front.url, fingerprint: 'nope' })).error, /not a SHA-256/);
  assert.match((await t.handlers['hub:trust-cert']({ url: t.hub.url, fingerprint: certs.a.fingerprint })).error, /Only an https address/);
  assert.deepEqual(await t.handlers['hub:trust-cert']({ url: front.url, fingerprint: certs.a.fingerprint.toLowerCase() }), { ok: true, pinned: true });
  assert.equal(readJson(directFile).pins[front.url].sha256, certs.a.fingerprint, 'kept with the hub config');

  assert.deepEqual(await t.handlers['hub:connect']({ url: front.url, code: '123-456' }), { ok: true });
  assert.equal(lastHub(t).status, 'ok');
  assert.equal(lastHub(t).pinned, true);
  assert.equal(lastHub(t).insecure, false);
  await until(() => t.hub.streamCount() === 1);
  assert.ok(front.requests.some((q) => q.url === '/v1/stream' && q.headers.authorization === 'Bearer wy_d_good'), 'the token goes over the pinned TLS');

  // The certificate changes: nothing is sent, the state says so and the board is hidden.
  front.use(certs.b);
  t.hub.cutStreams();
  const sent = front.requests.length;
  await t.handlers['hub:retry']();
  await until(() => lastHub(t).cert?.changed === true);
  assert.equal(lastHub(t).cert.fingerprint, certs.b.fingerprint);
  assert.equal(lastHub(t).cert.pinned, certs.a.fingerprint);
  assert.equal(lastHub(t).status, 'unreachable');
  assert.equal(front.requests.length, sent, 'no request crossed the changed certificate');

  // Re-pin after comparing the new fingerprint.
  assert.deepEqual(await t.handlers['hub:trust-cert']({ url: front.url, fingerprint: certs.b.fingerprint }), { ok: true, pinned: true });
  assert.equal(lastHub(t).cert, null);
  assert.equal(lastHub(t).status, 'ok');
  assert.equal(readJson(directFile).pins[front.url].sha256, certs.b.fingerprint);

  // Forgetting the hub forgets its certificate.
  await t.handlers['hub:unpair']({ forget: true });
  assert.equal(readJson(directFile).pins[front.url], undefined);
  await front.close();
  await t.close();
});

test('plain http to another computer is flagged insecure; loopback and https are not', async () => {
  const t = await harness();
  t.wy.state.info = info('http://192.168.1.5:8787', { hub: { reachable: false } });
  await t.h.start();
  assert.equal(lastHub(t).insecure, true);
  t.wy.state.info = info(t.hub.url);
  await t.handlers['hub:retry']();
  assert.equal(lastHub(t).insecure, false, 'loopback');
  t.wy.state.info = info('https://hub.example.com', { hub: { reachable: false } });
  await t.handlers['hub:retry']();
  assert.equal(lastHub(t).insecure, false);
  await t.close();
});

test('versions: wy’s is reported, and the release link opens the newer version’s page through openExternalSafe', async () => {
  const t = await harness({ clientVersion: '0.5.0' });
  t.wy.state.info = info(t.hub.url, { wy: '0.6.1' });
  await t.h.start();
  assert.equal(lastHub(t).wy_version, '0.6.1');
  assert.deepEqual(await t.handlers['app:open-release'](), { ok: true });
  assert.deepEqual(t.opened, ['https://github.com/mraj602/watchyard/releases/tag/v0.6.1']);
  await t.close();
  const same = await harness({ clientVersion: '0.6.0' });
  await same.h.start();
  await same.handlers['app:open-release']();
  assert.deepEqual(same.opened, ['https://github.com/mraj602/watchyard/releases']);
  await same.close();
});

test('cliVersion reads wy', () => {
  assert.equal(cliVersion({ wy: '1.0.0' }), '1.0.0');
  assert.equal(cliVersion({ other: '0.9.0' }), null);
  assert.equal(cliVersion({}), null);
  assert.equal(cliVersion(null), null);
  assert.equal(cliVersion({ wy: 'x'.repeat(100) }).length, 64);
});
