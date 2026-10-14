import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import fsp from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { createTokenStore, normalizeHubUrl } from './token.js';
import { fakeElectron } from '../../../test/fakes/electron.js';

const silent = { warn() {}, error() {}, debug() {} };
const setup = (opts = {}) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'sy-tok-'));
  const file = path.join(dir, 'hub-token.bin');
  const fake = fakeElectron(opts);
  const store = createTokenStore({ safeStorage: fake.electron.safeStorage, fs: fsp, path, file, log: silent, platform: 'linux' });
  return { dir, file, fake, store };
};
const REC = { url: 'http://127.0.0.1:8787', device: 'dev_1', token: 'sy_d_secret_token_value' };

test('save then load: the file is encrypted, mode 0600, and holds no plain token', async () => {
  const { file, store } = setup();
  assert.equal(await store.save(REC), true);
  const raw = fs.readFileSync(file);
  assert.ok(!raw.includes('sy_d_secret_token_value'));
  assert.ok(!raw.includes('dev_1'));
  if (process.platform !== 'win32') assert.equal(fs.statSync(file).mode & 0o777, 0o600);
  assert.deepEqual(await store.load('http://127.0.0.1:8787/'), REC);
  assert.deepEqual(fs.readdirSync(path.dirname(file)), ['hub-token.bin'], 'no temp file left behind');
});

test('cannot be decrypted (another key, keyring locked): kept, warned once, read again later', async () => {
  const a = setup({ safeStorageKey: 'key-one' });
  await a.store.save(REC);
  const b = fakeElectron({ safeStorageKey: 'key-two' });
  const warns = [];
  const other = createTokenStore({ safeStorage: b.electron.safeStorage, fs: fsp, path, file: a.file, log: { ...silent, warn: (m) => warns.push(m) }, platform: 'linux' });
  assert.equal(await other.load(REC.url), null);
  assert.equal(await other.load(REC.url), null);
  assert.equal(fs.existsSync(a.file), true, 'the file is kept for the next try');
  assert.equal(warns.length, 1, 'warned once, not at every poll');
  // the keyring is back (the right key): the same file is read
  assert.deepEqual(await a.store.load(REC.url), REC);
});

test('decryption throws (keyring temporarily unavailable): the file is kept, then read', async () => {
  const { file, fake, store } = setup();
  await store.save(REC);
  const real = fake.electron.safeStorage.decryptString;
  fake.electron.safeStorage.decryptString = () => {
    throw new Error('Error while decrypting the ciphertext provided to safeStorage.decryptString.');
  };
  assert.equal(await store.load(REC.url), null);
  assert.equal(fs.existsSync(file), true);
  fake.electron.safeStorage.decryptString = real;
  assert.deepEqual(await store.load(REC.url), REC);
});

test('dropped when the hub URL changes', async () => {
  const { file, store } = setup();
  await store.save(REC);
  assert.equal(await store.load('https://hub.example.com'), null);
  assert.equal(fs.existsSync(file), false);
  await store.save(REC);
  assert.equal(await store.load(null), null);
  assert.equal(fs.existsSync(file), false);
});

test('the same hub under another spelling keeps the token', async () => {
  const { store } = setup();
  await store.save({ ...REC, url: 'HTTP://127.0.0.1:8787/' });
  assert.ok(await store.load('http://127.0.0.1:8787'));
});

test('no secure storage: nothing is written', async () => {
  const { file, store } = setup({ encryption: false });
  assert.equal(await store.save(REC), false);
  assert.equal(fs.existsSync(file), false);
});

test('a garbage or incomplete record is dropped', async () => {
  const { file, fake, store } = setup();
  fs.writeFileSync(file, fake.electron.safeStorage.encryptString(JSON.stringify({ url: REC.url })));
  assert.equal(await store.load(REC.url), null);
  assert.equal(fs.existsSync(file), false);
  fs.writeFileSync(file, fake.electron.safeStorage.encryptString('not json'));
  assert.equal(await store.load(REC.url), null);
  assert.equal(fs.existsSync(file), false, 'decrypted but not JSON: dropped');
  assert.equal(await store.load(REC.url), null, 'a missing file is fine');
});

test('backend and availability: basic_text is reported on Linux only', () => {
  const lin = setup({ storageBackend: 'basic_text' });
  assert.equal(lin.store.backend(), 'basic_text');
  assert.equal(lin.store.available(), true);
  const store = createTokenStore({ safeStorage: lin.fake.electron.safeStorage, fs: fsp, path, file: lin.file, platform: 'darwin' });
  assert.equal(store.backend(), null);
});

test('basic_text: plain-text encryption is allowed first, so the token is written (as real Electron needs)', async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'sy-tok-'));
  const file = path.join(dir, 'hub-token.bin');
  let plain = false;
  // Real Electron on basic_text: isEncryptionAvailable() is false until setUsePlainTextEncryption(true).
  const safeStorage = {
    getSelectedStorageBackend: () => 'basic_text',
    setUsePlainTextEncryption: (v) => (plain = v),
    isEncryptionAvailable: () => plain,
    encryptString: (s) => Buffer.from(`enc:${Buffer.from(s).toString('base64')}`),
    decryptString: (b) => Buffer.from(String(b).slice(4), 'base64').toString(),
  };
  const store = createTokenStore({ safeStorage, fs: fsp, path, file, log: silent, platform: 'linux' });
  assert.equal(await store.save(REC), true);
  assert.equal(plain, true);
  assert.deepEqual(await store.load(REC.url), REC);
  // A new run (the switch off again) still reads the file it wrote.
  plain = false;
  const decrypt = safeStorage.decryptString;
  safeStorage.decryptString = (b) => {
    if (!plain) throw new Error('Error while decrypting');
    return decrypt(b);
  };
  const again = createTokenStore({ safeStorage, fs: fsp, path, file, log: silent, platform: 'linux' });
  assert.deepEqual(await again.load(REC.url), REC);
  safeStorage.decryptString = decrypt;
  // Elsewhere the switch is never touched.
  plain = false;
  const other = createTokenStore({ safeStorage: { ...safeStorage, getSelectedStorageBackend: () => 'gnome_libsecret' }, fs: fsp, path, file, log: silent, platform: 'linux' });
  assert.equal(other.available(), false);
  assert.equal(plain, false);
});

test('normalizeHubUrl', () => {
  assert.equal(normalizeHubUrl('https://Hub.Example.com:443/'), 'https://hub.example.com');
  assert.equal(normalizeHubUrl('http://127.0.0.1:8787//'), 'http://127.0.0.1:8787');
  assert.equal(normalizeHubUrl('https://hub.example.com/sy/#x'), 'https://hub.example.com/sy');
  assert.equal(normalizeHubUrl('ftp://x'), null);
  assert.equal(normalizeHubUrl('https://u:p@x.com'), null);
  assert.equal(normalizeHubUrl('nope'), null);
});
