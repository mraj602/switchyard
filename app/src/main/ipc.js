// IPC (C3): registers every channel of shared/ipc.js with ipcMain.handle, checks the
// sender (the shell's webContents, main frame on app://shell/) and the arguments (shared/ipc.js
// validate) before calling the owner's handler. Registering a channel not in the table throws.
// Handlers are registered by their owning module through register(); push goes through send().
//
// Owned by WP-A. Contract: docs/spec/desktop.md C3.
//
// Notes for the other modules:
// - A handler gets the validated, cleaned argument object only (never the IPC event). An
//   object channel whose keys are all optional (usage:refresh, hub:pair, settings:set) gets
//   {} when the shell passes nothing.
// - The lookup of the handler happens per call, so start() may run before every owner has
//   registered; a call to a channel nobody registered rejects with 'not available'.
// - send('toast') before the shell has loaded is queued (at most TOAST_QUEUE_MAX) and
//   delivered after its did-finish-load; send('state') before then is dropped (the shell
//   pulls state:get when it starts).

import { CHANNELS, INVOKE, PUSH, INVOKE_CHANNELS, validate, IpcArgError } from '../shared/ipc.js';
import { isShellUrl } from '../shared/urls.js';

export const TOAST_QUEUE_MAX = 20;

/**
 * @param {{electron: object, log: object, getShell: () => object|null}} deps
 *   getShell: the shell's webContents (from window.js), or null before the window exists
 * @returns {{
 *   register(channel: string, fn: (args: any) => any): void,
 *   send(channel: 'state'|'toast', payload: object): void,
 *   start(): void,
 *   has(channel: string): boolean,
 *   watchShell(webContents: object): void,
 * }}
 */
export function create(deps) {
  const { electron } = deps;
  const log = deps.log ?? console;
  const getShell = deps.getShell ?? (() => null);
  const handlers = new Map();
  const refusedSenders = new Set();
  let started = false;
  const toastQueue = [];
  const watched = new WeakSet();
  const loaded = new WeakSet();

  function register(channel, fn) {
    const ch = Object.hasOwn(CHANNELS, channel) ? CHANNELS[channel] : null;
    if (!ch) throw new Error(`ipc.register: ${JSON.stringify(channel)} is not in the IPC table`);
    if (ch.kind !== INVOKE) throw new Error(`ipc.register: ${channel} is a push channel`);
    if (typeof fn !== 'function') throw new TypeError(`ipc.register: handler of ${channel} must be a function`);
    if (handlers.has(channel)) throw new Error(`ipc.register: ${channel} is already registered`);
    handlers.set(channel, fn);
  }

  /** True when event comes from the shell's main frame on app://shell/. */
  function fromShell(event) {
    const shell = getShell();
    if (!shell || shell.isDestroyed?.()) return false;
    const sender = event?.sender;
    if (!sender || sender !== shell) return false;
    const frame = event.senderFrame;
    const main = shell.mainFrame;
    if (!frame || !main) return false;
    if (frame !== main && (frame.processId !== main.processId || frame.routingId !== main.routingId)) return false;
    if (frame.parent) return false;
    return isShellUrl(frame.url);
  }

  function refuse(channel, event) {
    const id = event?.sender?.id ?? 'unknown';
    if (!refusedSenders.has(id)) {
      refusedSenders.add(id);
      const url = String(event?.senderFrame?.url ?? '').slice(0, 120);
      log.warn?.(`ipc: refused ${channel} from webContents ${id} (${url || 'no frame'})`);
    }
    return new Error('ipc: refused');
  }

  async function dispatch(channel, event, args) {
    if (!fromShell(event)) throw refuse(channel, event);
    // The shell is running script, so queued toasts can go (after this reply).
    const shell = getShell();
    watch(shell);
    if (toastQueue.length) setTimeout(() => flushToasts(shell), 0);
    const ch = CHANNELS[channel];
    let input = args;
    if (input === undefined && ch.args?.type === 'object' && ch.args.required.length === 0) input = {};
    let clean;
    try {
      clean = validate(channel, input);
    } catch (err) {
      if (err instanceof IpcArgError) throw new Error(err.message);
      throw err;
    }
    const fn = handlers.get(channel);
    if (!fn) throw new Error(`ipc: ${channel} is not available`);
    return clean === undefined ? fn() : fn(clean);
  }

  function start() {
    if (started) return;
    started = true;
    for (const channel of INVOKE_CHANNELS) {
      electron.ipcMain.handle(channel, (event, args) => dispatch(channel, event, args));
    }
  }

  function watch(shell) {
    if (watched.has(shell)) return;
    watched.add(shell);
    shell.on?.('did-start-loading', () => loaded.delete(shell));
    shell.on?.('did-finish-load', () => {
      loaded.add(shell);
      flushToasts(shell);
    });
  }

  function isLoaded(shell) {
    if (loaded.has(shell)) return true;
    return isShellUrl(shell.getURL?.() ?? '') && shell.isLoading?.() === false;
  }

  function flushToasts(shell) {
    while (toastQueue.length && !shell.isDestroyed?.()) shell.send('toast', toastQueue.shift());
  }

  function send(channel, payload) {
    const ch = Object.hasOwn(CHANNELS, channel) ? CHANNELS[channel] : null;
    if (!ch || ch.kind !== PUSH) throw new Error(`ipc.send: ${JSON.stringify(channel)} is not a push channel`);
    const clean = validate(channel, payload);
    const shell = getShell();
    if (shell && !shell.isDestroyed?.()) watch(shell);
    const ready = shell && !shell.isDestroyed?.() && isLoaded(shell);
    if (channel === 'toast') {
      if (ready) {
        flushToasts(shell);
        shell.send(channel, clean);
      } else {
        if (toastQueue.length >= TOAST_QUEUE_MAX) toastQueue.shift();
        toastQueue.push(clean);
      }
      return;
    }
    if (ready) shell.send(channel, clean);
  }

  /** Watch the shell's loads (queued toasts go out after did-finish-load); state.js calls it. */
  function watchShell(shell) {
    if (shell) watch(shell);
  }

  return Object.freeze({ register, send, start, watchShell, has: (channel) => handlers.has(channel) });
}
