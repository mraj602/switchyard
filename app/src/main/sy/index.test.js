import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import fsp from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { create } from './index.js';
import { cleanEnv } from './env.js';
import { parseVersion, versionAtLeast } from './resolve.js';
import { syExecFile, writeFakeSy } from '../../../test/fakes/hub.js';

const tmp = () => fs.mkdtempSync(path.join(os.tmpdir(), 'sy-wpc-'));
const silent = { warn() {}, error() {}, debug() {}, info() {} };
const INFO = JSON.stringify({ v: 1, sy: '0.6.0', machine: { id: 'm_1', name: 'laptop' }, hub: { url: 'http://127.0.0.1:8787', connected: true, reachable: true, version: '0.6.0', local: true, dial: null, service: 'running' }, admin: 'local', outbox: { pending: 0, last_error: null } });

function make(dir, over = {}) {
  const fake = writeFakeSy(dir, { '--version': { stdout: 'sy 0.6.1\n' }, ...(over.map ?? {}) });
  const sy = create({
    env: { HOME: dir, PATH: '/nonexistent', ...(over.env ?? {}) }, fs: fsp, path, platform: process.platform,
    resourcesPath: over.resourcesPath ?? '', minSy: '0.6.0', log: silent, syBin: over.syBin === undefined ? fake.bin : over.syBin,
    timers: over.timers, execFile: syExecFile,
  });
  return { fake, sy };
}

test('version parsing and comparison', () => {
  assert.deepEqual(parseVersion('sy version 0.6.0 (abc)'), [0, 6, 0]);
  assert.equal(parseVersion('nope'), null);
  assert.ok(versionAtLeast([0, 6, 0], [0, 6, 0]) && versionAtLeast([1, 0, 0], [0, 9, 9]) && !versionAtLeast([0, 5, 9], [0, 6, 0]));
});

test('info: parses the C4 JSON and adds ok', async () => {
  const dir = tmp();
  const { sy, fake } = make(dir, { map: { 'desktop info --json': { stdout: INFO } } });
  const info = await sy.info();
  assert.equal(info.ok, true);
  assert.equal(info.hub.url, 'http://127.0.0.1:8787');
  assert.equal(info.machine.id, 'm_1');
  assert.deepEqual(fake.calls().at(-1).argv, ['desktop', 'info', '--json']);
});

test('a non-zero exit becomes {ok: false, exit, error: first stderr line}', async () => {
  const { sy } = make(tmp(), { map: { 'desktop owner-link --json': { stderr: '\nsy: no admin channel\nsecond line\n', exit: 4 } } });
  assert.deepEqual(await sy.ownerLink(), { ok: false, exit: 4, error: 'sy: no admin channel' });
});

test('a reply that is not JSON is an error, not a crash', async () => {
  const { sy } = make(tmp(), { map: { 'desktop info --json': { stdout: 'not json' } } });
  const r = await sy.info();
  assert.equal(r.ok, false);
  assert.match(r.error, /not JSON/);
});

test('the environment is cleaned: no ELECTRON_*, NODE_*, SY_APP_*; HOME, PATH, SY_*, XDG kept', async () => {
  const dir = tmp();
  const { sy, fake } = make(dir, {
    map: { 'desktop info --json': { stdout: INFO } },
    env: { ELECTRON_RUN_AS_NODE: '1', ELECTRON_FOO: 'x', NODE_OPTIONS: '--inspect', NODE_PATH: '/x', SY_APP_USER_DATA: '/u', SY_DATA: '/d', XDG_CONFIG_HOME: '/c', LANG: 'C' },
  });
  await sy.info();
  const env = fake.calls().at(-1).env;
  for (const k of ['ELECTRON_RUN_AS_NODE', 'ELECTRON_FOO', 'NODE_OPTIONS', 'NODE_PATH', 'SY_APP_USER_DATA']) assert.equal(env[k], undefined, k);
  assert.equal(env.HOME, dir);
  assert.equal(env.PATH, '/nonexistent');
  assert.equal(env.SY_DATA, '/d');
  assert.equal(env.XDG_CONFIG_HOME, '/c');
  assert.equal(env.LANG, 'C');
  assert.deepEqual(cleanEnv({ Node_Options: 'x', Path: 'p', SY_APP_X: '1', UserProfile: 'u' }, 'win32'), { Path: 'p', UserProfile: 'u' });
});

test('append: JSON lines on stdin, one id per record, ids counted', async () => {
  const dir = tmp();
  const { sy, fake } = make(dir, { map: { 'outbox append': { stdout: 'r_1\nr_2\n' } } });
  const records = [{ kind: 'web_usage', payload: { a: 1 } }, { kind: 'web_usage', payload: { b: 'multi\nline' } }];
  const r = await sy.append(records);
  assert.deepEqual(r, { ok: true, ids: ['r_1', 'r_2'] });
  const call = fake.calls().at(-1);
  assert.deepEqual(call.argv, ['outbox', 'append']);
  const lines = call.stdin.trimEnd().split('\n');
  assert.equal(lines.length, 2);
  assert.deepEqual(lines.map((l) => JSON.parse(l)), records);

  fake.script({ 'outbox append': { stdout: 'r_1\n' } });
  const bad = await sy.append(records);
  assert.equal(bad.ok, false);
  assert.equal(bad.retry, false);
  assert.match(bad.error, /1 ids for 2 records/);

  fake.script({ 'outbox append': { stderr: 'sy outbox append: record 1: kind\n', exit: 1 } });
  const failed = await sy.append(records);
  assert.equal(failed.ok, false);
  assert.match(failed.error, /record 1/);

  assert.deepEqual(await sy.append([]), { ok: true, ids: [] });
  assert.equal((await sy.append('x')).ok, false);
});

test('token: the code travels on stdin only, never in argv; the result is the C4 JSON', async () => {
  const dir = tmp();
  const TOKEN = { device: 'dev_1', token: 'sy_d_secret', scopes: ['write', 'read'], hub: { url: 'http://127.0.0.1:8787' } };
  const { sy, fake } = make(dir, {
    map: {
      'desktop token --json --name Switchyard-desktop --code-stdin': { stdout: JSON.stringify(TOKEN) },
      'desktop token --json --name Switchyard-desktop': { stdout: JSON.stringify(TOKEN) },
    },
  });
  const withCode = await sy.token({ code: '123-456', name: 'Switchyard-desktop' });
  assert.equal(withCode.ok, true);
  assert.equal(withCode.token, 'sy_d_secret');
  const c1 = fake.calls().at(-1);
  assert.equal(c1.stdin, '123-456\n');
  assert.ok(c1.argv.includes('--code-stdin'));
  assert.ok(!c1.argv.some((a) => a.includes('123')));

  const noCode = await sy.token({ name: 'Switchyard-desktop' });
  assert.equal(noCode.ok, true);
  const c2 = fake.calls().at(-1);
  assert.equal(c2.stdin, '');
  assert.ok(!c2.argv.includes('--code-stdin'));

  // no secret in any argv of any call
  for (const c of fake.calls()) assert.ok(!c.argv.join(' ').includes('sy_d_'));

  const badCode = await sy.token({ code: '12 3;rm', name: 'x' });
  assert.equal(badCode.ok, false);
  assert.equal(fake.calls().length, 2, 'a bad code never reaches sy');
});

test('refused by the hub: exit 3 is kept', async () => {
  const { sy } = make(tmp(), { map: { 'desktop token --json --name n --code-stdin': { stderr: 'code expired\n', exit: 3 } } });
  const r = await sy.token({ code: '123456', name: 'n' });
  assert.deepEqual(r, { ok: false, exit: 3, error: 'code expired' });
});

test('service, accounts and share go through their table rows', async () => {
  const { sy, fake } = make(tmp(), {
    map: {
      'desktop hub start --json': { stdout: '{"service":"running","unit":"u","health":true}' },
      'account list --json': { stdout: '[{"folder":"~/.claude"}]' },
      'account share ~/.claude-2 --share skills --yes --json': { stdout: '{"ok":true}' },
    },
  });
  assert.deepEqual(await sy.service('start'), { ok: true, service: 'running', unit: 'u', health: true });
  assert.deepEqual(await sy.accountsList(), { ok: true, accounts: [{ folder: '~/.claude' }] });
  assert.equal((await sy.share('~/.claude-2', 'skills')).ok, true);
  assert.equal(fake.calls().length, 3);
  await assert.rejects(() => sy.run('share', { folder: 'x', share: 'A' }));
  await assert.rejects(() => sy.run('info', {}, 'stdin where none is allowed'));
  await assert.rejects(() => sy.run('append', {}));
});

test('machineId: cached after the first success', async () => {
  const { sy, fake } = make(tmp(), { map: { 'machine show --json': { stdout: '{"id":"m_9","name":"x","created":"t"}' } } });
  assert.equal(await sy.machineId(), 'm_9');
  assert.equal(await sy.machineId(), 'm_9');
  assert.equal(fake.calls().filter((c) => c.argv[0] === 'machine').length, 1);
});

test('flushSoon: debounced 2 s and coalesced across callers', async () => {
  const pending = [];
  const timers = { setTimeout: (fn, ms) => { const t = { fn, ms }; pending.push(t); return t; }, clearTimeout: (t) => { pending.splice(pending.indexOf(t), 1); } };
  const { sy, fake } = make(tmp(), { timers });
  sy.flushSoon();
  sy.flushSoon();
  sy.flushSoon();
  assert.equal(pending.length, 1);
  assert.equal(pending[0].ms, 2000);
  assert.equal(fake.calls().length, 0);
  pending.shift().fn();
  await new Promise((r) => setTimeout(r, 300));
  assert.deepEqual(fake.calls().map((c) => c.argv), [['flush', '--quiet']]);
  sy.flushSoon();
  assert.equal(pending.length, 1, 'a new flush can be scheduled afterwards');
});

test('resolution: SY_BIN, then PATH or ~/.local/bin if new enough, else the bundled copy', async () => {
  // SY_BIN wins and is not version checked
  const a = tmp();
  const { sy: viaEnv, fake } = make(a, { map: { '--version': { stdout: 'sy 0.0.1' } } });
  assert.equal((await viaEnv.resolved()).source, 'env');
  assert.equal(fake.calls().length, 0);

  // PATH: too old, so the bundled copy
  const b = tmp();
  const old = writeFakeSy(path.join(b, 'bin'), { '--version': { stdout: 'sy 0.5.9' } });
  const bundled = writeFakeSy(path.join(b, 'res', 'sy'), { '--version': { stdout: 'x' } });
  const sy1 = create({ env: { HOME: b, PATH: path.dirname(old.bin) }, fs: fsp, path, platform: process.platform, execFile: syExecFile, resourcesPath: path.join(b, 'res'), minSy: '0.6.0', log: silent });
  const r1 = await sy1.resolved();
  assert.equal(r1.source, 'bundled');
  assert.equal(r1.bin, bundled.bin);

  // PATH: new enough wins over bundled
  old.script({ '--version': { stdout: 'sy 0.6.0' } });
  const sy2 = create({ env: { HOME: b, PATH: path.dirname(old.bin) }, fs: fsp, path, platform: process.platform, execFile: syExecFile, resourcesPath: path.join(b, 'res'), minSy: '0.6.0', log: silent });
  assert.equal((await sy2.resolved()).source, 'path');

  // ~/.local/bin after PATH
  const c = tmp();
  writeFakeSy(path.join(c, '.local', 'bin'), { '--version': { stdout: 'sy 0.7.0' } });
  const sy3 = create({ env: { HOME: c, PATH: '/nonexistent' }, fs: fsp, path, platform: process.platform, execFile: syExecFile, resourcesPath: '', minSy: '0.6.0', log: silent });
  assert.equal((await sy3.resolved()).source, 'local');

  // nothing at all: every call says so, without throwing
  const sy4 = create({ env: { HOME: tmp(), PATH: '/nonexistent' }, fs: fsp, path, platform: process.platform, execFile: syExecFile, resourcesPath: '/nonexistent', minSy: '0.6.0', log: silent });
  assert.equal(await sy4.resolved(), null);
  const r = await sy4.info();
  assert.equal(r.ok, false);
  assert.equal(r.code, 'no_sy');
});

test('execFile seam: ENOENT, timeouts and the stdout cap are reported, not thrown', async () => {
  const mk = (err, out = '', errOut = '') => create({
    env: {}, fs: fsp, path, platform: 'linux', resourcesPath: '', minSy: '0.6.0', log: silent, syBin: process.execPath,
    execFile: (_bin, _argv, opts, cb) => {
      assert.equal(opts.shell, false);
      assert.equal(opts.maxBuffer, 1024 * 1024);
      queueMicrotask(() => cb(err, out, errOut));
      return { stdin: { on() {}, end() {} } };
    },
  });
  const enoent = Object.assign(new Error('x'), { code: 'ENOENT' });
  assert.equal((await mk(enoent).info()).error, 'sy not found');
  const killed = Object.assign(new Error('x'), { killed: true, signal: 'SIGTERM' });
  const t = await mk(killed).info();
  assert.equal(t.ok, false);
  assert.match(t.error, /timed out/);
  const big = Object.assign(new Error('x'), { code: 'ERR_CHILD_PROCESS_STDIO_MAXBUFFER' });
  assert.match((await mk(big).info()).error, /too large/);
});

test('timeouts per purpose reach execFile', async () => {
  const seen = {};
  const sy = create({
    env: {}, fs: fsp, path, platform: 'linux', resourcesPath: '', minSy: '0.6.0', log: silent, syBin: process.execPath,
    execFile: (_b, argv, opts, cb) => {
      seen[argv.join(' ')] = opts.timeout;
      queueMicrotask(() => cb(null, '{}', ''));
      return { stdin: { on() {}, end() {} } };
    },
  });
  await sy.info();
  await sy.ownerLink();
  await sy.token({ name: 'n' });
  await sy.run('flush');
  assert.equal(seen['desktop info --json'], 15_000);
  assert.equal(seen['desktop owner-link --json'], 60_000);
  assert.equal(seen['desktop token --json --name n'], 60_000);
  assert.equal(seen['flush --quiet'], 60_000);
});
