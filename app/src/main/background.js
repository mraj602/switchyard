// Background life cycle (C1, C9): single-instance lock, --background, a second launch
// shows the window or applies --autostart=on|off, close-to-tray with the one-time notice, and the
// before-quit flush. Also owns `settings:set` (settings(), setSettings(patch)) because it is
// the module that holds the autostart, the store's `settings` and the `settings` bus event.
import { FLAG_AUTOSTART, FLAG_BACKGROUND } from '../shared/constants.js';

export const NOTICE_TITLE = 'Switchyard is still running';
export const NOTICE_WITH_TRAY = 'Closing the window keeps Switchyard running in the tray, so alerts and usage keep coming. Use the tray icon to open it again, or choose Quit. You can change this in Settings (Keep running).';
export const NOTICE_NO_TRAY = 'There is no tray icon on this desktop, so Switchyard keeps running with no window. Start Switchyard again to show it, or turn off Keep running in Settings so closing the window quits.';

/** @param {string[]} argv @returns {{background: boolean, autostart: boolean|null}} */
export function parseArgv(argv) {
  const out = { background: false, autostart: null };
  for (const a of argv ?? []) {
    if (a === FLAG_BACKGROUND) out.background = true;
    else if (a === `${FLAG_AUTOSTART}=on`) out.autostart = true;
    else if (a === `${FLAG_AUTOSTART}=off`) out.autostart = false;
  }
  return out;
}

const BOOL_KEYS = ['keep_running', 'notifications', 'start_hidden'];

/**
 * @param {{electron: object, window: object, store: object, autostart: object, tray: object,
 *          argv: string[], onQuit: () => Promise<void>, log: object, bus?: object, platform?: string}} deps
 *   onQuit: index.js's shutdown (stop stream, flush cookies, wait for the last append)
 *   bus: for the `settings` event
 * @returns {{
 *   claim(): boolean,        // requestSingleInstanceLock + second-instance wiring; false = quit now
 *   startHidden(): boolean,  // only --background (the login entry); never a stored setting
 *   start(): void,           // close-to-tray, before-quit
 *   settings(): Promise<object>,                // Settings (shared/ipc.js), autostart read from the OS
 *   setSettings(patch: object): Promise<object>,  // settings:set
 *   applied(): Promise<void>,  // resolves once a --autostart=on|off of this launch is applied
 *   quitting(): boolean,
 * }}
 *   When this launch itself carries --autostart=on|off, claim() applies it, exits the process
 *   (app.exit(0)) once it is written and returns false: the caller just stops starting.
 */
export function create(deps) {
  const { electron, window, store, autostart, tray, onQuit, log, bus } = deps;
  const { app } = electron;
  const platform = deps.platform ?? process.platform;
  const own = parseArgv(deps.argv);
  let quitting = false;
  let appliedP = Promise.resolve();
  let started = false;

  async function settings() {
    const s = store.get().settings ?? {};
    let on = false;
    try {
      on = await autostart.get();
    } catch (err) {
      log.warn?.('background: autostart read failed:', err?.message ?? err);
    }
    let backend = null;
    if (platform === 'linux') {
      try {
        backend = electron.safeStorage?.getSelectedStorageBackend?.() ?? null;
      } catch {
        backend = null;
      }
    }
    return {
      keep_running: s.keep_running !== false,
      notifications: s.notifications !== false,
      start_hidden: s.start_hidden === true,
      autostart: on,
      close_notice_shown: s.close_notice_shown === true,
      storage_backend: backend,
    };
  }

  async function publish() {
    const s = await settings();
    bus?.emit('settings', s);
    return s;
  }

  async function setSettings(patch = {}) {
    const keys = BOOL_KEYS.filter((k) => typeof patch[k] === 'boolean');
    if (keys.length > 0) {
      await store.update((draft) => {
        draft.settings = draft.settings ?? {};
        for (const k of keys) draft.settings[k] = patch[k];
      });
    }
    if (typeof patch.autostart === 'boolean') await autostart.set(patch.autostart);
    return publish();
  }

  async function applyAutostart(on) {
    try {
      await autostart.set(on);
    } catch (err) {
      log.error('background: --autostart failed:', err);
    }
  }

  function keepRunning() {
    return store.get().settings?.keep_running !== false;
  }

  async function closeNotice() {
    if (store.get().settings?.close_notice_shown) return;
    const body = tray?.available?.() ? NOTICE_WITH_TRAY : NOTICE_NO_TRAY;
    try {
      await store.update((d) => {
        d.settings = d.settings ?? {};
        d.settings.close_notice_shown = true;
      });
      if (electron.Notification?.isSupported?.()) new electron.Notification({ title: NOTICE_TITLE, body }).show();
      else await electron.dialog.showMessageBox({ type: 'info', message: NOTICE_TITLE, detail: body, buttons: ['OK'] });
      await publish();
    } catch (err) {
      log.warn?.('background: close notice failed:', err?.message ?? err);
    }
  }

  function onSecondInstance(_event, argv2) {
    const a = parseArgv(argv2);
    if (a.autostart !== null) {
      applyAutostart(a.autostart).then(() => publish()).catch(() => {});
      return; // applies and exits: no window
    }
    if (a.background) return; // a login launch while already running: nothing to show
    window.showWindow();
  }

  function beginQuit() {
    if (quitting) return false;
    quitting = true;
    return true;
  }

  return {
    claim() {
      if (!app.requestSingleInstanceLock()) return false;
      app.on('second-instance', onSecondInstance);
      if (own.autostart !== null) {
        appliedP = applyAutostart(own.autostart).then(() => app.exit(0));
        return false;
      }
      return true;
    },
    startHidden() {
      // A normal launch (app menu, AppImage) always shows the window. A stored 'start hidden'
      // setting used to hide those too, which left the user with a tray icon only.
      return own.background;
    },
    start() {
      if (started) return;
      started = true;
      const win = window.win;
      win?.on('close', (event) => {
        if (quitting) return;
        if (keepRunning()) {
          event.preventDefault();
          win.hide();
          closeNotice();
        } else {
          // Not keeping running: closing the window is quitting. before-quit runs the flush.
          event.preventDefault();
          app.quit();
        }
      });
      app.on('before-quit', (event) => {
        if (quitting) return;
        beginQuit();
        event.preventDefault();
        Promise.resolve(onQuit())
          .catch((err) => log.error('background: shutdown failed:', err))
          .finally(() => app.quit());
      });
      app.on('activate', () => window.showWindow());
      publish().catch(() => {});
    },
    settings,
    setSettings,
    applied: () => appliedP,
    quitting: () => quitting,
  };
}
