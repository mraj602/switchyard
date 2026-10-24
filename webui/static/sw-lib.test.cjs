// node --test webui/static/*.test.cjs   (Node 20+, no packages)
// Tests of the service worker's browser-free logic (sw-lib.js) and of sw.js's event wiring
// with stubbed globals.

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const read = (f) => fs.readFileSync(path.join(__dirname, f), 'utf8');

function loadLib() {
  const ctx = vm.createContext({ URL, console });
  vm.runInContext(read('sw-lib.js'), ctx, { filename: 'sw-lib.js' });
  return ctx.SyLib;
}
const L = loadLib();
// Objects made inside the vm have another Object prototype; compare them as plain JSON.
const deq = (a, b) => assert.deepEqual(JSON.parse(JSON.stringify(a === undefined ? null : a)), JSON.parse(JSON.stringify(b)));

const payload = (over) => ({
  v: 1, type: 'alert', alert: 'al_1', trigger: 'asked', title: 'work · api · asked a question',
  body: 'Should I?', tag: 'sy-s-1', url: '/#/sessions/1?alert=al_1', remote_url: 'https://claude.ai/code/session_x',
  tap: 'auto', session: '1', needs_you: 3, actions: { snooze: 'a1.aaa.bbb', seen: 'a1.ccc.ddd' }, private: false,
  ts: '2026-10-12T14:04:01.000Z', ...over,
});

test('chooseTarget: tap behaviour', () => {
  deq(L.chooseTarget(payload({ tap: 'claude' }), true), { kind: 'claude', url: 'https://claude.ai/code/session_x' });
  deq(L.chooseTarget(payload({ tap: 'session' }), false), { kind: 'app', url: '/#/sessions/1?alert=al_1' });
  assert.equal(L.chooseTarget(payload({ tap: 'auto' }), true).kind, 'app');
  assert.equal(L.chooseTarget(payload({ tap: 'auto' }), false).kind, 'claude');
  // No Remote Control link: always the app.
  assert.equal(L.chooseTarget(payload({ tap: 'claude', remote_url: null }), false).kind, 'app');
  assert.equal(L.chooseTarget(payload({ tap: 'auto', remote_url: undefined }), false).kind, 'app');
});

test('chooseTarget refuses non-https links and non-relative deep links', () => {
  assert.equal(L.chooseTarget(payload({ tap: 'claude', remote_url: 'javascript:alert(1)' }), true).kind, 'app');
  assert.equal(L.chooseTarget(payload({ tap: 'claude', remote_url: 'http://evil.example/' }), true).kind, 'app');
  assert.equal(L.chooseTarget(payload({ url: '//evil.example/x', tap: 'session' }), true).url, '/#/');
  assert.equal(L.chooseTarget(payload({ url: 'https://evil.example/', tap: 'session' }), true).url, '/#/');
});

test('deep links with a backslash are refused (browsers read "/\\" as "//")', () => {
  for (const url of ['/\\evil.example/x', '/#/a\\b', '\\\\evil.example']) {
    assert.equal(L.chooseTarget(payload({ url, tap: 'session' }), true).url, '/#/', url);
    assert.equal(L.parsePayload(JSON.stringify(payload({ url }))).url, '/#/', url);
  }
});

test('needsProbe only for auto with a Remote Control link', () => {
  assert.equal(L.needsProbe(payload({ tap: 'auto' })), true);
  assert.equal(L.needsProbe(payload({ tap: 'claude' })), false);
  assert.equal(L.needsProbe(payload({ tap: 'auto', remote_url: null })), false);
});

test('parsePayload falls back to a generic notice', () => {
  for (const bad of ['', 'not json', '[]', 'null', '"x"', '42']) {
    const p = L.parsePayload(bad);
    assert.equal(p.title, 'Watchyard · something needs you', bad);
    assert.equal(p.url, '/#/');
    assert.equal(p.actions, null);
  }
  assert.equal(L.parsePayload(undefined).title, 'Watchyard · something needs you');
});

test('parsePayload keeps a good payload and repairs bad fields', () => {
  const good = L.parsePayload(JSON.stringify(payload()));
  assert.equal(good.title, 'work · api · asked a question');
  assert.equal(good.tag, 'sy-s-1');
  assert.equal(good.needs_you, 3);
  const bad = L.parsePayload(JSON.stringify({ title: '', body: 5, tag: '', url: 'http://x', tap: 'bogus', actions: 'x' }));
  assert.equal(bad.title, 'Watchyard · something needs you');
  assert.equal(bad.body, '');
  assert.equal(bad.url, '/#/');
  assert.equal(bad.tap, 'auto');
  assert.equal(bad.actions, null);
});

test('notificationOptions: actions only when the payload has tokens', () => {
  const o = L.notificationOptions(L.parsePayload(JSON.stringify(payload())));
  assert.equal(o.tag, 'sy-s-1');
  assert.equal(o.renotify, true);
  assert.equal(o.timestamp, Date.parse('2026-10-12T14:04:01.000Z'));
  deq(o.actions.map((a) => a.action), ['snooze', 'seen']);
  assert.equal(o.icon, 'icons/icon-192.png');
  assert.equal(o.badge, 'icons/badge-96.png');
  assert.equal(o.data.alert, 'al_1');
  const t = L.notificationOptions(L.parsePayload(JSON.stringify(payload({ type: 'test', actions: undefined }))));
  deq(t.actions, []);
});

test('actionToken accepts only well-formed a1 tokens', () => {
  const p = payload();
  assert.equal(L.actionToken(p, 'snooze'), 'a1.aaa.bbb');
  assert.equal(L.actionToken(p, 'seen'), 'a1.ccc.ddd');
  assert.equal(L.actionToken(payload({ actions: { snooze: '../../x' } }), 'snooze'), null);
  assert.equal(L.actionToken(payload({ actions: null }), 'snooze'), null);
});

test('badgeCount', () => {
  assert.equal(L.badgeCount({ needs_you: 3 }), 3);
  assert.equal(L.badgeCount({ needs_you: 0 }), 0);
  assert.equal(L.badgeCount({}), 0);
  assert.equal(L.badgeCount({ needs_you: -1 }), 0);
});

test('isDataRequest matches exactly the cached read API', () => {
  const yes = ['/v1/me', '/v1/overview', '/v1/sessions', '/v1/sessions?include_ended=true&days=2', '/v1/sessions/abc-123',
    '/v1/accounts', '/v1/machines', '/v1/alerts?limit=50', '/v1/rules', '/v1/devices/self', '/v1/push/config'];
  const no = ['/v1/stream', '/v1/actions/a1.x.y', '/v1/sessions/abc/extra', '/v1/devices', '/v1/devices/dev_1', '/v1/pair/codes',
    '/v1/push/targets', '/v1/alerts/al_1', '/health', '/app.js', '/v1/session/login'];
  for (const u of yes) assert.equal(L.isDataRequest(`https://hub.example${u}`, 'GET'), true, u);
  for (const u of no) assert.equal(L.isDataRequest(`https://hub.example${u}`, 'GET'), false, u);
  assert.equal(L.isDataRequest('https://hub.example/v1/overview', 'POST'), false);
});

test('sessionsToEvict keeps the newest 20 session details only', () => {
  const urls = ['/v1/overview'];
  for (let i = 0; i < 25; i++) urls.push(`https://h/v1/sessions/s${i}`);
  urls.push('/v1/sessions');
  const gone = L.sessionsToEvict(urls);
  deq(gone, [0, 1, 2, 3, 4].map((i) => `https://h/v1/sessions/s${i}`));
  deq(L.sessionsToEvict(urls.slice(0, 10)), []);
});

test('cache names', () => {
  assert.equal(L.shellCacheName('abc'), 'sy-shell-abc');
  assert.equal(L.isCurrentCache('sy-shell-abc', 'abc'), true);
  assert.equal(L.isCurrentCache('sy-shell-old', 'abc'), false);
  assert.equal(L.isCurrentCache('sy-data', 'abc'), true);
  assert.equal(L.isNavigation('https://h/'), true);
  assert.equal(L.isNavigation('https://h/index.html'), true);
  assert.equal(L.isNavigation('https://h/app.js'), false);
});

test('hashOf and targetBody', () => {
  assert.equal(L.hashOf('/#/sessions/1?alert=a'), '#/sessions/1?alert=a');
  assert.equal(L.hashOf('/'), '#/');
  const body = L.targetBody({ toJSON: () => ({ endpoint: 'https://push.example/x', keys: { p256dh: 'P', auth: 'A' } }) }, 'iPhone');
  deq(body, { kind: 'webpush', endpoint: 'https://push.example/x', keys: { p256dh: 'P', auth: 'A' }, name: 'iPhone' });
});

// ---- sw.js with stubbed globals -----------------------------------------------------------

function loadWorker(extra = {}) {
  const handlers = {};
  const calls = { notifications: [], badges: [], windows: [], fetches: [], posted: [], closed: 0 };
  const self = {
    location: { origin: 'https://hub.example' },
    addEventListener: (t, f) => { handlers[t] = f; },
    registration: { showNotification: async (title, opts) => { calls.notifications.push({ title, opts }); } },
    navigator: { setAppBadge: async (n) => { calls.badges.push(n); } },
    clients: {
      claim: async () => {},
      matchAll: async () => extra.windows || [],
      openWindow: async (u) => { calls.windows.push(u); },
    },
    skipWaiting: () => { calls.skipped = true; },
  };
  const sandbox = Object.assign(self, {
    self, console, URL, Headers, Request: class { constructor(u, o) { this.url = u; Object.assign(this, o); } }, Response, AbortController, setTimeout, clearTimeout,
    caches: extra.caches || { open: async () => ({ addAll: async () => {} }), keys: async () => [], delete: async () => true, match: async () => undefined },
    fetch: async (u, o) => { calls.fetches.push([String(u), o]); return extra.fetchResult || { ok: true }; },
    importScripts: (f) => vm.runInContext(read(f), ctx, { filename: f }),
  });
  sandbox.self = sandbox;
  const ctx = vm.createContext(sandbox);
  vm.runInContext(read('sw.js'), ctx, { filename: 'sw.js' });
  const dispatch = async (type, ev) => {
    const waits = [];
    handlers[type]({ ...ev, waitUntil: (p) => waits.push(p), respondWith: (p) => waits.push(p) });
    await Promise.all(waits);
  };
  return { handlers, calls, dispatch };
}

test('sw.js registers every handler and does not skipWaiting by itself', async () => {
  const w = loadWorker();
  for (const t of ['install', 'activate', 'message', 'fetch', 'push', 'notificationclick', 'pushsubscriptionchange']) {
    assert.equal(typeof w.handlers[t], 'function', t);
  }
  await w.dispatch('install', {});
  assert.equal(w.calls.skipped, undefined);
  await w.dispatch('message', { data: { type: 'skip-waiting' } });
  assert.equal(w.calls.skipped, true);
});

test('push event shows the notification with actions and sets the badge', async () => {
  const w = loadWorker();
  await w.dispatch('push', { data: { text: () => JSON.stringify(payload()) } });
  assert.equal(w.calls.notifications.length, 1);
  const n = w.calls.notifications[0];
  assert.equal(n.title, 'work · api · asked a question');
  deq(n.opts.actions.map((a) => a.action), ['snooze', 'seen']);
  deq(w.calls.badges, [3]);
});

test('push event with an unreadable payload still shows a notification', async () => {
  const w = loadWorker();
  await w.dispatch('push', { data: { text: () => { throw new Error('boom'); } } });
  await w.dispatch('push', {});
  assert.equal(w.calls.notifications.length, 2);
  assert.equal(w.calls.notifications[0].title, 'Watchyard · something needs you');
  deq(w.calls.badges, []);
});

test('notificationclick: Snooze posts the action token and closes', async () => {
  const w = loadWorker();
  let closed = false;
  await w.dispatch('notificationclick', { action: 'snooze', notification: { data: payload(), close: () => { closed = true; } } });
  assert.equal(closed, true);
  assert.equal(w.calls.fetches.length, 1);
  assert.equal(w.calls.fetches[0][0], '/v1/actions/a1.aaa.bbb');
  assert.equal(w.calls.fetches[0][1].method, 'POST');
  assert.equal(w.calls.fetches[0][1].headers['Content-Type'], 'application/json');
});

test('notificationclick: body tap focuses an open window and routes it; else opens one', async () => {
  const posted = [];
  const win = { url: 'https://hub.example/#/', focus: async () => {}, postMessage: (m) => posted.push(m) };
  const w = loadWorker({ windows: [win] });
  await w.dispatch('notificationclick', { action: '', notification: { data: payload({ tap: 'session' }), close() {} } });
  deq(posted, [{ type: 'route', hash: '#/sessions/1?alert=al_1' }]);
  deq(w.calls.windows, []);

  const w2 = loadWorker();
  await w2.dispatch('notificationclick', { action: '', notification: { data: payload({ tap: 'claude' }), close() {} } });
  deq(w2.calls.windows, ['https://claude.ai/code/session_x']);
  const w3 = loadWorker();
  await w3.dispatch('notificationclick', { action: '', notification: { data: payload({ tap: 'session' }), close() {} } });
  deq(w3.calls.windows, ['/#/sessions/1?alert=al_1']);
});

test('notificationclick with tap auto probes /health and falls back to the Remote Control link', async () => {
  const w = loadWorker({ fetchResult: { ok: false } });
  await w.dispatch('notificationclick', { action: '', notification: { data: payload({ tap: 'auto' }), close() {} } });
  assert.equal(w.calls.fetches[0][0], '/health');
  deq(w.calls.windows, ['https://claude.ai/code/session_x']);
});

test('pushsubscriptionchange subscribes again and posts the target', async () => {
  const w = loadWorker();
  const sub = { toJSON: () => ({ endpoint: 'https://push.example/new', keys: { p256dh: 'P', auth: 'A' } }) };
  await w.dispatch('pushsubscriptionchange', { newSubscription: sub });
  const [url, init] = w.calls.fetches[0];
  assert.equal(url, '/v1/push/targets');
  assert.equal(init.method, 'POST');
  assert.equal(JSON.parse(init.body).endpoint, 'https://push.example/new');
});
