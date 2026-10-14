import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as fs from 'node:fs/promises';
import * as path from 'node:path';
import { tmpdir } from 'node:os';

import { create, emptyState } from './store.js';

const quiet = { warn() {} };

async function tmp(t) {
  const dir = await fs.mkdtemp(path.join(tmpdir(), 'sy-store-'));
  t.after(() => fs.rm(dir, { recursive: true, force: true }));
  return { dir, file: path.join(dir, 'ud', 'app-state.json') };
}

const TAB = { id: 't_9c1e5f0a7b2d4e68', name: 'work', last_url: 'https://claude.ai/new', reading: 'on', warning_accepted: 1, send_to_hub: true, stopped: null, seat: null };

test('first start: empty state, nothing written until the first update', async (t) => {
  const { file } = await tmp(t);
  const s = create({ fs, path, file, log: quiet });
  const { state, recovered } = await s.load();
  assert.deepEqual(state, emptyState());
  assert.equal(recovered, null);
  await assert.rejects(fs.stat(file), { code: 'ENOENT' });
  assert.deepEqual(emptyState(), { v: 1, tabs: [], settings: { keep_running: true, notifications: true, start_hidden: false, close_notice_shown: false } });
});

test('update writes atomically with mode 0600 and leaves no temp files', async (t) => {
  const { file } = await tmp(t);
  const s = create({ fs, path, file, log: quiet });
  await s.load();
  const out = await s.update((d) => d.tabs.push({ ...TAB }));
  assert.deepEqual(out.tabs, [TAB]);
  const disk = JSON.parse(await fs.readFile(file, 'utf8'));
  assert.deepEqual(disk, out);
  if (process.platform !== 'win32') assert.equal((await fs.stat(file)).mode & 0o777, 0o600);
  assert.deepEqual(await fs.readdir(path.dirname(file)), ['app-state.json']);
});

test('get returns a copy; update mutates a copy', async (t) => {
  const { file } = await tmp(t);
  const s = create({ fs, path, file, log: quiet });
  await s.load();
  s.get().tabs.push({ id: 'x' });
  assert.deepEqual(s.get().tabs, []);
  await assert.rejects(s.update((d) => {
    d.tabs.push({ ...TAB });
    throw new Error('changed my mind');
  }), /changed my mind/);
  assert.deepEqual(s.get().tabs, [], 'a failed update changes nothing');
  await assert.rejects(s.update((d) => d.tabs.push({ id: 'bad id' })), /bad tab/);
  await assert.rejects(s.update((d) => (d.tabs = 'x')), /not an array/);
  assert.deepEqual(s.get().tabs, []);
});

test('updates are serialised in call order', async (t) => {
  const { file } = await tmp(t);
  const s = create({ fs, path, file, log: quiet });
  await s.load();
  const order = [];
  const ps = [];
  for (let i = 0; i < 20; i++) {
    ps.push(s.update(async (d) => {
      await new Promise((r) => setTimeout(r, (20 - i) % 3));
      order.push(i);
      d.counter = (d.counter ?? 0) + 1;
    }));
  }
  ps.push(s.update(() => {
    throw new Error('one bad apple');
  }).catch(() => 'rejected'));
  ps.push(s.update((d) => (d.after = true)));
  const results = await Promise.all(ps);
  await s.flush();
  assert.deepEqual(order, [...Array(20).keys()]);
  assert.equal(results.at(-2), 'rejected');
  const disk = JSON.parse(await fs.readFile(file, 'utf8'));
  assert.equal(disk.counter, 20);
  assert.equal(disk.after, true);
});

test('unknown keys are kept at every level; settings get defaults; v is kept', async (t) => {
  const { file } = await tmp(t);
  await fs.mkdir(path.dirname(file), { recursive: true });
  const stored = { v: 1, future: { a: 1 }, tabs: [{ ...TAB, later: 'x' }], settings: { keep_running: false, newer: 2 } };
  await fs.writeFile(file, JSON.stringify(stored));
  const s = create({ fs, path, file, log: quiet });
  const { state } = await s.load();
  assert.deepEqual(state.future, { a: 1 });
  assert.equal(state.tabs[0].later, 'x');
  assert.deepEqual(state.settings, { keep_running: false, notifications: true, start_hidden: false, close_notice_shown: false, newer: 2 });
  await s.update((d) => (d.settings.notifications = false));
  const disk = JSON.parse(await fs.readFile(file, 'utf8'));
  assert.deepEqual(disk.future, { a: 1 });
  assert.equal(disk.tabs[0].later, 'x');
  assert.equal(disk.settings.newer, 2);
  assert.equal(disk.v, 1);
});

test('bad tab entries are dropped on load; imported 8-hex ids are kept', async (t) => {
  const { file } = await tmp(t);
  await fs.mkdir(path.dirname(file), { recursive: true });
  const tabs = [{ ...TAB }, { ...TAB }, { id: 'a1b2c3d4', name: 'imported' }, { id: '../x' }, 'str', null];
  await fs.writeFile(file, JSON.stringify({ v: 1, tabs }));
  const { state } = await create({ fs, path, file, log: quiet }).load();
  assert.deepEqual(state.tabs.map((x) => x.id), [TAB.id, 'a1b2c3d4']);
});

for (const [name, text] of [
  ['not JSON', '{"v": 1, "tabs": ['],
  ['not an object', '[1, 2]'],
  ['tabs not an array', '{"v": 1, "tabs": {}}'],
  ['settings not an object', '{"v": 1, "settings": 3}'],
  ['an unknown version', '{"v": 2, "tabs": []}'],
  ['no version', '{"tabs": []}'],
]) {
  test(`a corrupt file (${name}) is moved aside and the state starts fresh with a toast`, async (t) => {
    const { file } = await tmp(t);
    await fs.mkdir(path.dirname(file), { recursive: true });
    await fs.writeFile(file, text);
    const toasts = [];
    const now = () => Date.UTC(2026, 9, 13, 10, 15, 0, 123);
    const s = create({ fs, path, file, log: quiet, now, toast: (level, msg) => toasts.push([level, msg]) });
    const { state, recovered } = await s.load();
    assert.deepEqual(state, emptyState());
    assert.equal(recovered, `${file}.bad-2026-10-13T10-15-00-123Z`);
    assert.equal(await fs.readFile(recovered, 'utf8'), text, 'the old file is kept as it was');
    await assert.rejects(fs.stat(file), { code: 'ENOENT' });
    assert.equal(toasts.length, 1);
    assert.equal(toasts[0][0], 'warn');
    assert.match(toasts[0][1], /app-state\.json\.bad-/);
    await s.update((d) => d.tabs.push({ ...TAB }));
    assert.equal(JSON.parse(await fs.readFile(file, 'utf8')).tabs.length, 1);
  });
}

test('update before load is refused', async (t) => {
  const { file } = await tmp(t);
  const s = create({ fs, path, file, log: quiet });
  await assert.rejects(s.update(() => {}), /before load/);
});

test('a failed write leaves the old file and no temp file', async (t) => {
  const { file } = await tmp(t);
  const s = create({ fs, path, file, log: quiet });
  await s.load();
  await s.update((d) => (d.n = 1));
  const failing = { ...fs, rename: async () => { throw Object.assign(new Error('disk full'), { code: 'ENOSPC' }); } };
  const s2 = create({ fs: failing, path, file, log: quiet });
  await s2.load();
  await assert.rejects(s2.update((d) => (d.n = 2)), /disk full/);
  assert.equal(s2.get().n, 1);
  assert.equal(JSON.parse(await fs.readFile(file, 'utf8')).n, 1);
  assert.deepEqual(await fs.readdir(path.dirname(file)), ['app-state.json']);
});
