import { test } from 'node:test';
import assert from 'node:assert/strict';

import { create, TOAST_QUEUE_MAX } from './ipc.js';
import { fakeElectron } from '../../test/fakes/electron.js';
import { INVOKE_CHANNELS } from '../shared/ipc.js';

const TAB_ID = 't_9c1e5f0a7b2d4e68';

function setup() {
  const fake = fakeElectron();
  const e = fake.electron;
  const warnings = [];
  const shellWin = new e.BrowserWindow({});
  const shell = shellWin.webContents;
  shell._setURL('app://shell/index.html');
  let current = shell;
  const ipc = create({ electron: e, log: { warn: (m) => warnings.push(m) }, getShell: () => current });
  ipc.start();
  return { fake, e, ipc, shell, warnings, setShell: (s) => (current = s) };
}

test('start registers every invoke channel with ipcMain.handle, once, and never ipcMain.on', () => {
  const { fake, ipc } = setup();
  ipc.start();
  assert.deepEqual(fake.callsOf('ipcMain.handle').map((c) => c[0]), [...INVOKE_CHANNELS]);
  assert.equal(fake.callsOf('ipcMain.on').length, 0);
});

test('register: unknown, push, duplicate and non-function handlers throw', () => {
  const { ipc } = setup();
  assert.throws(() => ipc.register('tab:explode', () => {}), /not in the IPC table/);
  assert.throws(() => ipc.register('state', () => {}), /push channel/);
  assert.throws(() => ipc.register('__proto__', () => {}), /not in the IPC table/);
  assert.throws(() => ipc.register('tab:add', 'x'), /function/);
  ipc.register('tab:add', () => ({ id: TAB_ID }));
  assert.throws(() => ipc.register('tab:add', () => {}), /already registered/);
  assert.ok(ipc.has('tab:add'));
});

test('a call from the shell reaches the handler with the cleaned arguments only', async () => {
  const { e, ipc, shell } = setup();
  const seen = [];
  ipc.register('tab:add', (args, ...rest) => {
    seen.push([args, rest.length]);
    return { id: TAB_ID };
  });
  assert.deepEqual(await e.ipcMain._invoke('tab:add', shell, { name: 'work' }), { id: TAB_ID });
  assert.deepEqual(seen, [[{ name: 'work' }, 0]]);
});

test('channels without arguments get none; optional-object channels get {} when called bare', async () => {
  const { e, ipc, shell } = setup();
  let got = 'unset';
  ipc.register('state:get', (...a) => ((got = a), { ok: 1 }));
  assert.deepEqual(await e.ipcMain._invoke('state:get', shell), { ok: 1 });
  assert.deepEqual(got, []);
  await assert.rejects(e.ipcMain._invoke('state:get', shell, { x: 1 }), /takes no arguments/);
  ipc.register('usage:refresh', (a) => a);
  assert.deepEqual(await e.ipcMain._invoke('usage:refresh', shell), {});
  assert.deepEqual(await e.ipcMain._invoke('usage:refresh', shell, { id: TAB_ID }), { id: TAB_ID });
  // tab:add requires a name, so a bare call is refused.
  ipc.register('tab:add', () => 'x');
  await assert.rejects(e.ipcMain._invoke('tab:add', shell), /must be an object/);
});

test('bad arguments are refused before the handler runs', async () => {
  const { e, ipc, shell } = setup();
  let calls = 0;
  for (const ch of ['tab:add', 'tab:remove', 'tab:reorder', 'hub:route', 'view:select', 'hub:pair']) ipc.register(ch, () => calls++);
  const bad = [
    ['tab:add', { name: '' }],
    ['tab:add', { name: 'x'.repeat(25) }],
    ['tab:add', { name: 'ok', extra: 1 }],
    ['tab:add', []],
    ['tab:add', 'work'],
    ['tab:remove', { id: '../../etc' }],
    ['tab:remove', { id: 't_9C1E5F0A7B2D4E68' }],
    ['tab:reorder', { ids: [TAB_ID, TAB_ID] }],
    ['hub:route', { route: 'https://evil/' }],
    ['hub:route', { route: '#/a b' }],
    ['view:select', { view: 'devtools' }],
    ['hub:pair', { code: '12345' }],
  ];
  for (const [ch, args] of bad) await assert.rejects(e.ipcMain._invoke(ch, shell, args), Error, `${ch} ${JSON.stringify(args)}`);
  const evil = Object.create({ name: 'inherited' });
  await assert.rejects(e.ipcMain._invoke('tab:add', shell, evil), /plain object/);
  assert.equal(calls, 0);
});

test('an IPC call from an account tab, the hub view, a subframe or another page is refused and logged once', async () => {
  const { e, ipc, shell, warnings } = setup();
  let calls = 0;
  ipc.register('tab:add', () => calls++);
  const tab = new e.WebContentsView({ webPreferences: { partition: `persist:tab-${TAB_ID}` } }).webContents;
  tab._setURL('https://claude.ai/new');
  await assert.rejects(e.ipcMain._invoke('tab:add', tab, { name: 'x' }), /refused/);
  await assert.rejects(e.ipcMain._invoke('tab:add', tab, { name: 'x' }), /refused/);
  assert.equal(warnings.filter((w) => w.includes(`webContents ${tab.id}`)).length, 1, 'logged once per sender');

  const hub = new e.WebContentsView({ webPreferences: { partition: 'persist:sy-hub' } }).webContents;
  hub._setURL('app://shell/index.html'); // even a hub view pretending to be the shell
  await assert.rejects(e.ipcMain._invoke('tab:add', hub, { name: 'x' }), /refused/);

  // The shell's webContents, but a subframe.
  const sub = { url: 'app://shell/index.html', routingId: 99, processId: shell.mainFrame.processId, parent: shell.mainFrame };
  await assert.rejects(e.ipcMain._invoke('tab:add', shell, { name: 'x' }, sub), /refused/);
  // The shell's webContents after it navigated somewhere else.
  shell._setURL('https://evil.example/');
  await assert.rejects(e.ipcMain._invoke('tab:add', shell, { name: 'x' }), /refused/);
  // Another frame of the same process that is not the main frame (e.g. a previous document).
  shell._setURL('app://shell/index.html');
  const stale = { url: 'app://shell/index.html', routingId: 7, processId: shell.mainFrame.processId };
  await assert.rejects(e.ipcMain._invoke('tab:add', shell, { name: 'x' }, stale), /refused/);
  assert.equal(calls, 0);
  assert.equal(await e.ipcMain._invoke('tab:add', shell, { name: 'x' }), 0);
});

test('before the window exists every call is refused', async () => {
  const { e, ipc, shell, setShell } = setup();
  ipc.register('tab:add', () => 1);
  setShell(null);
  await assert.rejects(e.ipcMain._invoke('tab:add', shell, { name: 'x' }), /refused/);
});

test('a channel nobody registered rejects as not available', async () => {
  const { e, shell } = setup();
  await assert.rejects(e.ipcMain._invoke('view:select', shell, { view: 'hub' }), /not available/);
});

test('handler errors reach the caller', async () => {
  const { e, ipc, shell } = setup();
  ipc.register('tab:add', async () => {
    throw new Error('name taken');
  });
  await assert.rejects(e.ipcMain._invoke('tab:add', shell, { name: 'x' }), /name taken/);
});

test('send: push channels only, checked payloads, to the shell only', () => {
  const { ipc, shell, e } = setup();
  const other = new e.WebContentsView({}).webContents;
  assert.throws(() => ipc.send('tab:add', {}), /not a push channel/);
  assert.throws(() => ipc.send('toast', { level: 'loud', text: 'x' }), /not an allowed value/);
  assert.throws(() => ipc.send('toast', { level: 'info', text: '' }), /shorter/);
  ipc.send('state', { tabs: [] });
  ipc.send('toast', { level: 'info', text: 'hello' });
  assert.deepEqual(shell.sent, [
    { channel: 'state', args: [{ tabs: [] }] },
    { channel: 'toast', args: [{ level: 'info', text: 'hello' }] },
  ]);
  assert.deepEqual(other.sent, []);
});

test('toasts before the shell has loaded are queued and delivered after did-finish-load; state is dropped', () => {
  const { e, ipc, setShell } = setup();
  setShell(null);
  ipc.send('toast', { level: 'warn', text: 'early' });
  ipc.send('state', { tabs: [] });
  const win = new e.BrowserWindow({});
  const shell = win.webContents;
  setShell(shell);
  shell.isLoading = () => true;
  ipc.watchShell(shell);
  ipc.send('toast', { level: 'info', text: 'second' });
  assert.deepEqual(shell.sent, []);
  for (let i = 0; i < TOAST_QUEUE_MAX + 5; i++) ipc.send('toast', { level: 'info', text: `n${i}` });
  shell._setURL('app://shell/index.html');
  shell.isLoading = () => false;
  shell.emit('did-finish-load');
  assert.equal(shell.sent.length, TOAST_QUEUE_MAX, 'the queue is capped');
  assert.ok(shell.sent.every((m) => m.channel === 'toast'));
  assert.equal(shell.sent.at(-1).args[0].text, `n${TOAST_QUEUE_MAX + 4}`, 'the newest are kept');
});
