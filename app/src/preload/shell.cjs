'use strict';
// The shell's preload (docs/spec/desktop.md C3). Sandboxed preloads cannot require relative
// files, so this file is self-contained: it repeats the channel names of src/shared/ipc.js,
// and shell.test.js checks that the two agree. It exposes `window.sy` with exactly one
// function per invoke channel and `on(event, cb)` for the push channels. It never exposes
// ipcRenderer, event objects or require. Only the shell gets a preload; account tabs and the
// hub view get none.

const { contextBridge, ipcRenderer } = require('electron');

// window.sy function name → invoke channel. Keep in table order of shared/ipc.js.
const INVOKE = {
  state: 'state:get',
  selectView: 'view:select',
  hubRoute: 'hub:route',
  addTab: 'tab:add',
  updateTab: 'tab:update',
  removeTab: 'tab:remove',
  reorderTabs: 'tab:reorder',
  reloadTab: 'tab:reload',
  homeTab: 'tab:home',
  openTabExternal: 'tab:external',
  acceptWarning: 'usage:accept-warning',
  setReading: 'usage:set-reading',
  setSend: 'usage:set-send',
  resumeReading: 'usage:resume',
  refreshUsage: 'usage:refresh',
  pair: 'hub:pair',
  ownerSignIn: 'hub:owner-sign-in',
  ownerSignOut: 'hub:owner-sign-out',
  hubConnect: 'hub:connect',
  hubUnpair: 'hub:unpair',
  hubRetry: 'hub:retry',
  hubService: 'hub:service',
  updateSeat: 'seat:update',
  localAccounts: 'accounts:local',
  shareAccount: 'accounts:share',
  setSettings: 'settings:set',
};

// Channels that take no argument: the function passes none, whatever it is called with.
const NO_ARGS = new Set(['state:get', 'hub:owner-sign-out', 'hub:retry', 'accounts:local']);

// Push channels the shell may subscribe to.
const PUSH = new Set(['state', 'toast']);

const api = {};
for (const [fn, channel] of Object.entries(INVOKE)) {
  api[fn] = NO_ARGS.has(channel)
    ? () => ipcRenderer.invoke(channel)
    : (args) => ipcRenderer.invoke(channel, args);
}

/**
 * on(event, cb): cb gets the payload only (never the IPC event). Returns a function that
 * removes the listener.
 */
api.on = (event, cb) => {
  if (!PUSH.has(event)) throw new Error(`sy.on: unknown event ${String(event).slice(0, 40)}`);
  if (typeof cb !== 'function') throw new TypeError('sy.on: callback must be a function');
  const listener = (_event, payload) => cb(payload);
  ipcRenderer.on(event, listener);
  return () => ipcRenderer.removeListener(event, listener);
};

contextBridge.exposeInMainWorld('sy', Object.freeze(api));
