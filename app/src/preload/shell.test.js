import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import vm from 'node:vm';

import { CHANNELS, FUNCTIONS, INVOKE_CHANNELS, PUSH_CHANNELS } from '../shared/ipc.js';
import { fakeElectron } from '../../test/fakes/electron.js';

const file = fileURLToPath(new URL('./shell.cjs', import.meta.url));
const source = readFileSync(file, 'utf8');

// Runs the preload as a sandboxed preload would: CommonJS, `require` that knows only
// 'electron' (the renderer-side fake), no Node globals.
function loadPreload() {
  const fake = fakeElectron();
  const required = [];
  const sandbox = {
    require: (name) => {
      required.push(name);
      if (name !== 'electron') throw new Error(`preload may not require ${name}`);
      return { contextBridge: fake.electron.contextBridge, ipcRenderer: fake.electron.ipcRenderer };
    },
    module: { exports: {} },
    console,
  };
  sandbox.exports = sandbox.module.exports;
  vm.runInNewContext(source, sandbox, { filename: file });
  return { fake, required, wy: fake.exposed.wy };
}

test('window.wy has exactly one function per invoke channel, plus on', () => {
  const { wy, required, fake } = loadPreload();
  assert.deepEqual(required, ['electron']);
  assert.deepEqual(fake.callsOf('contextBridge.exposeInMainWorld'), [['wy']]);
  assert.ok(Object.isFrozen(wy));
  assert.deepEqual(Object.keys(wy).sort(), [...Object.keys(FUNCTIONS), 'on'].sort());
  for (const v of Object.values(wy)) assert.equal(typeof v, 'function');
  assert.equal(wy.ipcRenderer, undefined);
  assert.equal(wy.require, undefined);
});

test('each function invokes its own channel with its argument, or none', async () => {
  const { wy, fake } = loadPreload();
  for (const channel of INVOKE_CHANNELS) {
    const fn = CHANNELS[channel].fn;
    const before = fake.callsOf('ipcRenderer.invoke').length;
    const arg = { probe: channel };
    await wy[fn](arg, 'extra');
    const got = fake.callsOf('ipcRenderer.invoke').slice(before);
    assert.equal(got.length, 1, fn);
    if (CHANNELS[channel].args === null) assert.deepEqual(got[0], [channel], `${fn} must pass no argument`);
    else assert.deepEqual(got[0], [channel, arg], `${fn} passes exactly its one argument`);
  }
  assert.equal(fake.callsOf('ipcRenderer.send').length, 0);
  assert.equal(fake.callsOf('ipcRenderer.sendSync').length, 0);
});

test('on() takes only push channels and hands the payload, never the event', () => {
  const { wy, fake } = loadPreload();
  const ipc = fake.electron.ipcRenderer;
  for (const ev of PUSH_CHANNELS) {
    const got = [];
    const off = wy.on(ev, (...a) => got.push(a));
    ipc._fromMain(ev, { n: 1 });
    off();
    ipc._fromMain(ev, { n: 2 });
    assert.deepEqual(got, [[{ n: 1 }]], ev);
    assert.equal(ipc.listenerCount(ev), 0, `${ev}: listener removed`);
  }
  assert.throws(() => wy.on('tab:add', () => {}));
  assert.throws(() => wy.on('ipc-message', () => {}));
  assert.throws(() => wy.on('state', 'not a function'));
});

test('the preload is self-contained and names the same channels as shared/ipc.js', () => {
  const requires = [...source.matchAll(/require\(([^)]*)\)/g)].map((m) => m[1]);
  assert.deepEqual(requires, ["'electron'"]);
  const named = new Set([...source.matchAll(/'([a-z]+:[a-z-]+)'/g)].map((m) => m[1]));
  assert.deepEqual([...named].sort(), [...INVOKE_CHANNELS].sort());
  assert.doesNotMatch(source, /exposeInMainWorld\([^)]*ipcRenderer/);
});
