import { test } from 'node:test';
import assert from 'node:assert/strict';
import fsp from 'node:fs/promises';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { checkAddress, isLocalNetworkHost, newRecordId, completeRecord, createDirect } from './direct.js';

const silent = { warn() {}, error() {}, debug() {}, info() {} };

test('checkAddress: https always, http only on this computer or a local network', () => {
  assert.deepEqual(checkAddress('https://hub.example.com/'), { ok: true, url: 'https://hub.example.com' });
  assert.deepEqual(checkAddress('hub.example.com'), { ok: true, url: 'https://hub.example.com' }, 'no scheme means https');
  for (const ok of ['http://127.0.0.1:8787', 'http://localhost:8787', 'http://192.168.1.20:8787', 'http://10.0.0.5', 'http://172.20.1.1', 'http://100.101.102.103', 'http://myhub.tailnet.ts.net', 'http://nas', 'http://nas.local:8787']) {
    assert.equal(checkAddress(ok).ok, true, ok);
  }
  for (const bad of ['http://hub.example.com', 'http://172.32.0.1', 'http://8.8.8.8', 'http://100.128.0.1']) {
    const r = checkAddress(bad);
    assert.equal(r.ok, false, bad);
    assert.match(r.error, /https/);
  }
  assert.equal(checkAddress('').ok, false);
  assert.equal(checkAddress('ftp://hub.example.com').ok, false);
  assert.equal(checkAddress('https://user:pw@hub.example.com').ok, false);
  assert.equal(isLocalNetworkHost('[::1]'), true);
});

test('record ids are ULIDs in time order; completeRecord fills v, id and ts only when missing', () => {
  const a = newRecordId(1_700_000_000_000);
  const b = newRecordId(1_700_000_000_001);
  assert.match(a, /^[0-9A-HJKMNP-TV-Z]{26}$/);
  assert.ok(a.slice(0, 10) < b.slice(0, 10));
  const r = completeRecord({ kind: 'web_usage', machine: 'm', payload: {} }, 1_700_000_000_000);
  assert.equal(r.v, 1);
  assert.equal(r.ts, '2023-11-14T22:13:20.000Z');
  assert.match(r.id, /^[0-9A-HJKMNP-TV-Z]{26}$/);
  assert.deepEqual(completeRecord({ v: 1, id: 'X', ts: 'T', kind: 'k' }), { v: 1, id: 'X', ts: 'T', kind: 'k' });
});

function setup({ fetch, auth = { url: 'https://hub.example.com', token: 'sy_d_x' }, active = true, onUnauthorized } = {}) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'sy-direct-'));
  const calls = [];
  const f = fetch ?? (async (url, init) => {
    calls.push([url, init]);
    return new Response(JSON.stringify({ accepted: [], duplicates: [], rejected: [] }), { status: 200 });
  });
  const d = createDirect({
    fs: fsp, path, file: path.join(dir, 'direct.json'), fetch: f, log: silent, clientName: 'switchyard-desktop', version: '9.9.9',
    timers: { setTimeout: () => ({ unref() {} }), clearTimeout() {} }, getAuth: () => auth, active: () => active, onUnauthorized,
  });
  return { d, dir, calls };
}

test('the machine id and the hub address survive a restart; the id is made once', async () => {
  const { d, dir } = setup();
  const m = await d.machineId();
  assert.match(m, /^[0-9A-Za-z_-]{16,64}$/);
  await d.setUrl('https://hub.example.com/');
  const d2 = createDirect({ fs: fsp, path, file: path.join(dir, 'direct.json'), fetch: async () => null, log: silent, clientName: 'x', version: '1', getAuth: () => null, active: () => false });
  assert.equal(await d2.machineId(), m);
  assert.equal(d2.url(), 'https://hub.example.com');
  assert.equal((fs.statSync(path.join(dir, 'direct.json')).mode & 0o777), 0o600);
});

test('claim posts what sy desktop token posts, and maps the failures to words', async () => {
  let reply = { status: 200, body: { device: 'dev_1', token: 'sy_d_new', kind: 'desktop', scopes: ['read', 'write'] } };
  const seen = [];
  const { d } = setup({
    fetch: async (url, init) => {
      seen.push([url, init]);
      return new Response(JSON.stringify(reply.body), { status: reply.status });
    },
  });
  const ok = await d.claim({ url: 'https://hub.example.com', code: '123-456', name: 'Switchyard-desktop-box' });
  assert.deepEqual(ok, { ok: true, device: 'dev_1', token: 'sy_d_new' });
  assert.equal(seen[0][0], 'https://hub.example.com/v1/pair/claim');
  assert.equal(seen[0][1].method, 'POST');
  assert.equal(seen[0][1].headers.authorization, undefined);
  const b = JSON.parse(seen[0][1].body);
  assert.deepEqual({ ...b, machine: 'x' }, { code: '123456', name: 'Switchyard-desktop-box', machine: 'x', expect_kind: 'desktop', client: 'switchyard-desktop/9.9.9' });
  // a long secret (not digits) goes as `secret`
  await d.claim({ url: 'https://hub.example.com', code: 'abcdefghijkl', name: 'n' });
  assert.equal(JSON.parse(seen[1][1].body).secret, 'abcdefghijkl');
  reply = { status: 400, body: { error: { message: 'bad' } } };
  assert.match((await d.claim({ url: 'https://hub.example.com', code: '111222', name: 'n' })).error, /refused or has expired/);
  reply = { status: 429, body: {} };
  assert.match((await d.claim({ url: 'https://hub.example.com', code: '111222', name: 'n' })).error, /Too many/);
  reply = { status: 503, body: {} };
  assert.match((await d.claim({ url: 'https://hub.example.com', code: '111222', name: 'n' })).error, /not reachable/);
  reply = { status: 200, body: { device: 'dev_2', token: 'sy_o_x', kind: 'phone' } };
  assert.match((await d.claim({ url: 'https://hub.example.com', code: '111222', name: 'n' })).error, /not a desktop/);
});

test('the outbox: records are queued on disk, sent as one batch with the token, removed when accepted', async () => {
  const { d, dir, calls } = setup();
  await d.append([{ kind: 'web_usage', machine: 'm', payload: { a: 1 } }]);
  assert.ok(fs.existsSync(path.join(dir, 'direct-outbox.ndjson')));
  assert.equal(d.queued(), 1);
  await d.flushNow();
  assert.equal(calls.length, 1);
  assert.equal(calls[0][0], 'https://hub.example.com/v1/ingest');
  assert.equal(calls[0][1].headers.authorization, 'Bearer sy_d_x');
  const body = JSON.parse(calls[0][1].body);
  assert.equal(body.v, 1);
  assert.equal(body.client, 'switchyard-desktop/9.9.9');
  assert.equal(body.records[0].v, 1);
  assert.equal(d.queued(), 0);
  assert.equal(fs.existsSync(path.join(dir, 'direct-outbox.ndjson')), false);
});

test('the outbox: kept on a network failure or a 503, dropped for a batch the hub refuses, nothing sent when not active', async () => {
  let status = 0;
  const { d } = setup({ fetch: async () => { if (status === 0) throw new Error('offline'); return new Response('{}', { status }); } });
  await d.append([{ kind: 'k', machine: 'm', payload: {} }]);
  await d.flushNow();
  assert.equal(d.queued(), 1, 'offline: kept');
  status = 503;
  await d.flushNow();
  assert.equal(d.queued(), 1, '503: kept');
  status = 400;
  await d.flushNow();
  assert.equal(d.queued(), 0, '400: a bad batch must not block the rest');
  const idle = setup({ active: false });
  await idle.d.append([{ kind: 'k', machine: 'm', payload: {} }]);
  await idle.d.flushNow();
  assert.equal(idle.calls.length, 0, 'sy mode: the direct outbox sends nothing');
  assert.equal(idle.d.queued(), 1);
});

test('a 401 on the upload reports a dead token and keeps the records', async () => {
  const seen = [];
  const { d } = setup({ fetch: async () => new Response('{}', { status: 401 }), onUnauthorized: (s) => seen.push(s) });
  await d.append([{ kind: 'k', machine: 'm', payload: {} }]);
  await d.flushNow();
  assert.deepEqual(seen, [401]);
  assert.equal(d.queued(), 1);
});

test('check: the address is validated, then /health must answer like a hub', async () => {
  const answers = { 'https://hub.example.com/health': { status: 200, body: { ok: true, version: '0.6.1' } }, 'https://other.example.com/health': { status: 200, body: { hello: 1 } } };
  const { d } = setup({
    fetch: async (url) => {
      const a = answers[url];
      if (!a) throw new Error('ECONNREFUSED');
      return new Response(JSON.stringify(a.body), { status: a.status });
    },
  });
  assert.deepEqual(await d.check('hub.example.com'), { ok: true, url: 'https://hub.example.com', version: '0.6.1' });
  assert.match((await d.check('https://other.example.com')).error, /does not look like|No hub answered/);
  assert.match((await d.check('https://gone.example.com')).error, /No hub answered/);
  assert.match((await d.check('http://hub.example.com')).error, /https/);
});
