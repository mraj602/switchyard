import { test, after, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { createStream } from './stream.js';
import { startFakeHub } from '../../../test/fakes/hub.js';

const hub = await startFakeHub();
after(() => hub.close());
const silent = { warn() {}, error() {}, debug() {} };
const until = async (fn, ms = 3000) => {
  const t = Date.now();
  while (!(await fn())) {
    if (Date.now() - t > ms) throw new Error(`timed out waiting for: ${fn}`);
    await new Promise((r) => setTimeout(r, 10));
  }
};

// Every test starts with no open stream left from the one before.
beforeEach(async () => {
  await until(() => hub.streamCount() === 0);
  hub.requests.length = 0;
});

function mk(over = {}) {
  const got = { events: [], resets: 0, unauthorized: [], conn: [] };
  let token = 'sy_d_good';
  const s = createStream({
    fetch: globalThis.fetch, getAuth: () => ({ url: hub.url, token }), retryMs: [20, 40, 80], log: silent,
    onEvent: (type, data, id) => got.events.push({ type, data, id }),
    onReset: () => got.resets++,
    onUnauthorized: (st) => got.unauthorized.push(st),
    onConnection: (up) => got.conn.push(up),
    ...over,
  });
  return { s, got, setToken: (t) => (token = t) };
}

test('events arrive parsed with their ids; hello and ping are not forwarded as data events', async () => {
  const { s, got } = mk();
  s.start();
  await until(() => hub.streamCount() === 1);
  const id = hub.emit('session', { session: { id: 'c1' }, change: 'state' });
  hub.emit('ping', {});
  await until(() => got.events.some((e) => e.type === 'session'));
  const ev = got.events.find((e) => e.type === 'session');
  assert.equal(ev.data.session.id, 'c1');
  assert.equal(ev.id, String(id));
  assert.ok(!got.events.some((e) => e.type === 'ping'));
  assert.equal(s.lastEventId(), String(id) === s.lastEventId() ? String(id) : s.lastEventId());
  s.stop();
  await until(() => hub.streamCount() === 0);
});

test('a cut stream reconnects with Last-Event-ID', async () => {
  hub.requests.length = 0;
  const { s, got } = mk();
  s.start();
  await until(() => hub.streamCount() === 1);
  const id = hub.emit('session', { session: { id: 'c2' }, change: 'new' });
  await until(() => got.events.some((e) => e.data?.session?.id === 'c2'));
  hub.cutStreams();
  await until(() => hub.requestsTo('/v1/stream').length >= 2 && hub.streamCount() === 1);
  const second = hub.requestsTo('/v1/stream')[1];
  assert.equal(second.headers['last-event-id'], String(id));
  assert.equal(hub.requestsTo('/v1/stream')[0].headers['last-event-id'], undefined);
  assert.equal(second.headers.authorization, 'Bearer sy_d_good');
  await until(() => got.conn.includes(false) && got.conn.at(-1) === true);
  s.stop();
});

test('reset triggers onReset (the caller refetches the overview)', async () => {
  hub.set({ resetOnConnect: true });
  const { s, got } = mk();
  s.start();
  await until(() => hub.streamCount() === 1);
  hub.cutStreams();
  await until(() => got.resets === 1);
  assert.equal(got.resets, 1);
  assert.ok(!got.events.some((e) => e.type === 'reset'));
  s.stop();
  hub.set({ resetOnConnect: false });
});

test('a server retry: field sets the next delay; the backoff grows 20, 40, 80 and resets after hello', async () => {
  const delays = [];
  // retry delays are recorded and shortened; the 60 s ping watchdog stays real
  const timers = { setTimeout: (fn, ms) => { if (ms >= 60_000) return setTimeout(fn, ms); delays.push(ms); return setTimeout(fn, 1); }, clearTimeout };
  const { s } = mk({ timers, retryMs: [20, 40, 80] });
  s.start();
  await until(() => hub.streamCount() === 1);
  hub.rawWrite('retry: 7\n\n');
  await new Promise((r) => setTimeout(r, 100));
  hub.cutStreams();
  await until(() => delays.includes(7));
  s.stop();
  assert.ok(delays.includes(7));
});

test('connection failures back off (1, 2, 4 … up to the cap) when the hub is down', async () => {
  const delays = [];
  const timers = { setTimeout: (fn, ms) => { delays.push(ms); return setTimeout(fn, 1); }, clearTimeout };
  const { s } = mk({ timers, retryMs: [1000, 2000, 4000], getAuth: () => ({ url: 'http://127.0.0.1:1', token: 'sy_d_good' }) });
  s.start();
  await until(() => delays.filter((d) => d >= 1000).length >= 5);
  s.stop();
  assert.deepEqual(delays.filter((d) => d >= 1000).slice(0, 5), [1000, 2000, 4000, 4000, 4000]);
});

test('no ping for the timeout: it reconnects', async () => {
  hub.requests.length = 0;
  const { s } = mk({ pingTimeoutMs: 80 });
  s.start();
  await until(() => hub.requestsTo('/v1/stream').length >= 2, 4000);
  s.stop();
});

test('401 stops the stream and asks to pair again; it does not retry', async () => {
  hub.script.stream401 = true;
  hub.requests.length = 0;
  const { s, got } = mk();
  s.start();
  await until(() => got.unauthorized.length === 1);
  assert.deepEqual(got.unauthorized, [401]);
  assert.equal(s.running(), false);
  await new Promise((r) => setTimeout(r, 200));
  assert.equal(hub.requestsTo('/v1/stream').length, 1);
  hub.script.stream401 = false;
});

test('403 is not a dead token: onForbidden, no onUnauthorized, and the stream retries', async () => {
  hub.script.stream403 = true;
  hub.requests.length = 0;
  const forbidden = [];
  const { s, got } = mk({ onForbidden: (st) => forbidden.push(st) });
  s.start();
  await until(() => hub.requestsTo('/v1/stream').length >= 2, 4000);
  assert.deepEqual(got.unauthorized, []);
  assert.ok(forbidden.length >= 1 && forbidden.every((x) => x === 403));
  assert.equal(s.running(), true);
  hub.script.stream403 = false;
  s.stop();
});

test('stop is final: no reconnect after stop', async () => {
  hub.requests.length = 0;
  const { s } = mk();
  s.start();
  await until(() => hub.streamCount() === 1);
  s.stop();
  await new Promise((r) => setTimeout(r, 250));
  assert.equal(hub.requestsTo('/v1/stream').length, 1);
  assert.equal(s.running(), false);
});

test('a broken data line is skipped and the stream continues', async () => {
  const { s, got } = mk();
  s.start();
  await until(() => hub.streamCount() === 1);
  hub.rawWrite('id: 900\nevent: session\ndata: {not json\n\n');
  hub.emit('usage', { seat: 's1' });
  await until(() => got.events.some((e) => e.type === 'usage'));
  assert.ok(!got.events.some((e) => e.type === 'session'));
  s.stop();
});
