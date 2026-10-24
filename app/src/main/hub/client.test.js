import { test, after } from 'node:test';
import assert from 'node:assert/strict';
import { createClient, hubUrl, hubUrlAllowed, isLoopbackHost } from './client.js';
import { startFakeHub } from '../../../test/fakes/hub.js';

const hub = await startFakeHub({ overview: { seats: [], needs_you: [], machines: [], counts: { needs_you: 2 } } });
after(() => hub.close());
const mk = (over = {}) => createClient({ fetch: globalThis.fetch, getAuth: () => ({ url: hub.url, token: 'sy_d_good' }), ...over });

test('GET with a bearer token and JSON parsing', async () => {
  const r = await mk().get('/v1/overview');
  assert.equal(r.ok, true);
  assert.equal(r.data.counts.needs_you, 2);
  const req = hub.requestsTo('/v1/overview').at(-1);
  assert.equal(req.headers.authorization, 'Bearer sy_d_good');
});

test('errors carry status and message, 401 is reported', async () => {
  const bad = await mk({ getAuth: () => ({ url: hub.url, token: 'wrong' }) }).get('/v1/overview');
  assert.equal(bad.ok, false);
  assert.equal(bad.status, 401);
  assert.equal(bad.error, 'unauthorized');
  const missing = await mk().get('/v1/sessions/nope');
  assert.equal(missing.status, 404);
});

test('no auth: nothing is sent', async () => {
  const before = hub.requests.length;
  const r = await mk({ getAuth: () => null }).get('/v1/overview');
  assert.equal(r.ok, false);
  assert.equal(hub.requests.length, before);
});

test('redirects are not followed', async () => {
  const r = await mk().get('/v1/redirect');
  assert.equal(r.ok, false);
  assert.equal(r.status, 302);
});

test('a reply over the cap is refused', async () => {
  const c = createClient({ fetch: globalThis.fetch, getAuth: () => ({ url: hub.url, token: 'sy_d_good' }), maxBytes: 1024 });
  const small = await c.request('GET', '/v1/overview');
  assert.equal(small.ok, true);
  const big = await createClient({ fetch: async (u, i) => globalThis.fetch(u.replace('/v1/nothing', '/big'), i), getAuth: () => ({ url: hub.url, token: 'sy_d_good' }), maxBytes: 1024 * 1024 }).get('/v1/nothing');
  assert.equal(big.ok, false);
  assert.match(big.error, /too large/);
});

test('the timeout aborts a hub that does not answer', async () => {
  const never = (_u, init) => new Promise((_res, rej) => init.signal.addEventListener('abort', () => rej(Object.assign(new Error('aborted'), { name: 'TimeoutError' }))));
  const r = await mk({ fetch: never, timeoutMs: 30 }).get('/v1/overview');
  assert.equal(r.ok, false);
  assert.match(r.error, /in time/);
});

test('the token only goes to the hub origin: paths cannot leave it', () => {
  assert.equal(hubUrl('https://hub.example.com', '/v1/overview'), 'https://hub.example.com/v1/overview');
  assert.equal(hubUrl('https://hub.example.com/wy', '/v1/overview'), 'https://hub.example.com/wy/v1/overview');
  assert.equal(hubUrl('https://hub.example.com', '//evil.example/v1/x'), null);
  assert.equal(hubUrl('https://hub.example.com', 'https://evil.example/v1/x'), null);
  assert.equal(hubUrl('https://hub.example.com', '/other'), null);
  assert.equal(hubUrl('https://hub.example.com', '/v1/a', { x: '1' }), 'https://hub.example.com/v1/a?x=1');
  assert.equal(hubUrl('nope', '/v1/a'), null);
});

test('plain http: loopback, or a URL wy reported', async () => {
  assert.equal(hubUrlAllowed('http://127.0.0.1:8787'), true);
  assert.equal(hubUrlAllowed('http://localhost:8787'), true);
  assert.equal(hubUrlAllowed('http://[::1]:8787'), true);
  assert.equal(hubUrlAllowed('http://192.168.1.5:8787'), false);
  assert.equal(hubUrlAllowed('http://192.168.1.5:8787', true), true);
  assert.equal(hubUrlAllowed('https://hub.example.com'), true);
  assert.equal(hubUrlAllowed('file:///etc/passwd', true), false);
  assert.equal(isLoopbackHost('127.9.9.9'), true);
  assert.equal(isLoopbackHost('127.0.0.1.evil.com'), false);
  let called = false;
  const c = createClient({ fetch: async () => { called = true; return new Response('{}'); }, getAuth: () => ({ url: 'http://192.168.1.5:8787', token: 't' }) });
  assert.equal((await c.get('/v1/overview')).ok, false);
  assert.equal(called, false, 'nothing is sent over plain http to a LAN host');
  const lan = createClient({ fetch: async () => new Response('{"ok":1}'), getAuth: () => ({ url: 'http://192.168.1.5:8787', token: 't' }), reportedBySy: () => true });
  assert.equal((await lan.get('/v1/overview')).ok, true);
});
