import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import { createHash } from 'node:crypto';
import { create, parseArgs, ImportError } from './import-prototype.js';

/** Hash of a tree: relative paths, kinds, modes and file contents. */
async function treeHash(root, skip = []) {
  const h = createHash('sha256');
  const walk = async (dir, rel) => {
    for (const e of (await fs.readdir(dir, { withFileTypes: true })).sort((a, b) => (a.name < b.name ? -1 : 1))) {
      if (skip.includes(e.name)) continue;
      const p = path.join(dir, e.name);
      const r = path.posix.join(rel, e.name);
      if (e.isSymbolicLink()) h.update(`L ${r} ${await fs.readlink(p)}\n`);
      else if (e.isDirectory()) {
        h.update(`D ${r}\n`);
        await walk(p, r);
      } else {
        const st = await fs.stat(p);
        h.update(`F ${r} ${st.mode & 0o777} ${st.size}\n`);
        h.update(await fs.readFile(p));
      }
    }
  };
  await walk(root, '');
  return h.digest('hex');
}

/** A fixture with the prototype's data layout (never the real one). */
async function fixture() {
  const tmp = await fs.mkdtemp(path.join(os.tmpdir(), 'sy-import-'));
  const appData = path.join(tmp, 'appData');
  const proto = path.join(appData, 'switchyard');
  const userData = path.join(appData, 'switchyard-desktop');
  await fs.mkdir(userData, { recursive: true });
  const accounts = [
    { id: 'aaaa1111', label: 'work', color: '#199e70', track: true, lastUrl: 'https://claude.ai/chat/abc' },
    { id: 'bbbb2222', label: 'a very long label that exceeds the limit', color: '#e66767', lastUrl: 'https://evil.example/x' },
    { id: 'cccc3333', label: 'nopart' },
    { id: 'NOTHEX', label: 'bad' },
  ];
  await fs.mkdir(proto, { recursive: true });
  await fs.writeFile(path.join(proto, 'accounts.json'), JSON.stringify(accounts));
  await fs.writeFile(path.join(proto, 'config.json'), '{"hookToken":"x"}');
  for (const id of ['aaaa1111', 'bbbb2222']) {
    const p = path.join(proto, 'Partitions', `acct-${id}`);
    await fs.mkdir(path.join(p, 'Network'), { recursive: true });
    await fs.mkdir(path.join(p, 'Cache'), { recursive: true });
    await fs.mkdir(path.join(p, 'Code Cache', 'js'), { recursive: true });
    await fs.mkdir(path.join(p, 'GPUCache'), { recursive: true });
    await fs.writeFile(path.join(p, 'Network', 'Cookies'), `cookies-${id}`);
    await fs.writeFile(path.join(p, 'Local Storage'), `ls-${id}`, { mode: 0o600 });
    await fs.writeFile(path.join(p, 'Cache', 'blob'), 'cache');
    await fs.writeFile(path.join(p, 'Code Cache', 'js', 'x'), 'cc');
    await fs.writeFile(path.join(p, 'GPUCache', 'y'), 'gc');
  }
  return { tmp, appData, proto, userData };
}

function mkStore(tabs = []) {
  let state = { v: 1, tabs };
  return {
    get: () => structuredClone(state),
    async update(fn) {
      const d = structuredClone(state);
      fn(d);
      state = d;
      return d;
    },
  };
}

function mk(fx, over = {}) {
  const store = over.store || mkStore();
  const imp = create({
    fs,
    path,
    platform: 'linux',
    appData: fx.appData,
    userData: fx.userData,
    store,
    hostname: 'host1',
    isPidAlive: () => false,
    log: { info() {}, warn() {} },
    ...over,
  });
  return { imp, store };
}

test('copy is exact (minus caches), source untouched, tabs added', async () => {
  const fx = await fixture();
  const before = await treeHash(fx.proto);
  const { imp, store } = mk(fx);
  const rep = await imp.run({ dry_run: false });
  assert.deepEqual(rep.copied, ['aaaa1111', 'bbbb2222']);
  assert.deepEqual(rep.skipped, [
    { id: 'cccc3333', reason: 'no_partition' },
    { id: 'NOTHEX', reason: 'bad_id' },
  ]);
  assert.ok(rep.bytes > 0);
  assert.equal(await treeHash(fx.proto), before, 'prototype folder unchanged');
  for (const id of rep.copied) {
    const src = path.join(fx.proto, 'Partitions', `acct-${id}`);
    const dst = path.join(fx.userData, 'Partitions', `tab-${id}`);
    assert.equal(await treeHash(dst), await treeHash(src, ['Cache', 'Code Cache', 'GPUCache']));
  }
  const tabs = store.get().tabs;
  assert.equal(tabs.length, 2);
  assert.equal(tabs[0].name, 'work');
  assert.equal(tabs[0].last_url, 'https://claude.ai/chat/abc');
  assert.equal(tabs[0].reading, 'never_asked');
  assert.equal(tabs[0].warning_accepted, 0);
  assert.equal(tabs[1].name.length, 24);
  assert.equal(tabs[1].last_url, 'https://claude.ai/');
});

test('a second run copies nothing', async () => {
  const fx = await fixture();
  const { imp, store } = mk(fx);
  await imp.run({ dry_run: false });
  const again = await imp.run({ dry_run: false });
  assert.deepEqual(again.copied, []);
  assert.deepEqual(
    again.skipped.filter((s) => s.reason === 'already_present').map((s) => s.id),
    ['aaaa1111', 'bbbb2222'],
  );
  assert.equal(store.get().tabs.length, 2);
});

test('dry run writes nothing but reports the same set', async () => {
  const fx = await fixture();
  const beforeUser = await treeHash(fx.userData);
  const beforeProto = await treeHash(fx.proto);
  const { imp, store } = mk(fx);
  const dry = await imp.run({ dry_run: true });
  assert.equal(dry.dry_run, true);
  assert.deepEqual(dry.copied, ['aaaa1111', 'bbbb2222']);
  assert.equal(await treeHash(fx.userData), beforeUser);
  assert.equal(await treeHash(fx.proto), beforeProto);
  assert.equal(store.get().tabs.length, 0);
  const real = await mk(fx).imp.run({ dry_run: false });
  assert.equal(real.bytes, dry.bytes);
});

test('a live SingletonLock on this host refuses; a stale or foreign one does not', async () => {
  const fx = await fixture();
  await fs.symlink('host1-4242', path.join(fx.proto, 'SingletonLock'));
  const { imp, store } = mk(fx, { isPidAlive: (pid) => pid === 4242 });
  await assert.rejects(imp.run({ dry_run: false }), (e) => e instanceof ImportError && e.code === 'running');
  assert.equal(store.get().tabs.length, 0);
  await assert.rejects(fs.stat(path.join(fx.userData, 'Partitions')));
  // dead pid
  assert.equal((await mk(fx, { isPidAlive: () => false }).imp.run({ dry_run: true })).copied.length, 2);
  // other host
  assert.equal((await mk(fx, { hostname: 'other', isPidAlive: () => true }).imp.run({ dry_run: true })).copied.length, 2);
});

test('windows: an unopenable lockfile refuses', async () => {
  const fx = await fixture();
  await fs.writeFile(path.join(fx.proto, 'lockfile'), '');
  const fakeFs = { ...fs };
  fakeFs.open = async () => {
    throw Object.assign(new Error('busy'), { code: 'EBUSY' });
  };
  const { imp } = mk(fx, { platform: 'win32', fs: fakeFs });
  await assert.rejects(imp.run({ dry_run: true }), (e) => e.code === 'running');
});

test('an existing tab id is skipped and its partition left alone', async () => {
  const fx = await fixture();
  const { imp, store } = mk(fx, { store: mkStore([{ id: 'aaaa1111', name: 'mine' }]) });
  const rep = await imp.run({ dry_run: false });
  assert.deepEqual(rep.copied, ['bbbb2222']);
  assert.ok(rep.skipped.some((s) => s.id === 'aaaa1111' && s.reason === 'already_present'));
  assert.equal(store.get().tabs.find((t) => t.id === 'aaaa1111').name, 'mine');
});

test('refuses its own folder, a missing folder and a bad accounts.json', async () => {
  const fx = await fixture();
  const { imp } = mk(fx);
  await assert.rejects(imp.run({ dir: fx.userData, dry_run: true }), (e) => e.code === 'same_folder');
  await assert.rejects(imp.run({ dir: path.join(fx.tmp, 'nope'), dry_run: true }), (e) => e.code === 'not_found');
  await fs.writeFile(path.join(fx.proto, 'accounts.json'), '{not json');
  await assert.rejects(imp.run({ dry_run: true }), (e) => e.code === 'bad_accounts');
});

test('parseArgs', () => {
  assert.equal(parseArgs(['--background']), null);
  assert.deepEqual(parseArgs(['--import-prototype']), { dry_run: false });
  assert.deepEqual(parseArgs(['--import-prototype=/x/y', '--dry-run']), { dir: '/x/y', dry_run: true });
});
