// Tray (C9): icon with the needs-you badge (assets/tray, shared/badge.js), platform
// branches, and the menu. Badge: macOS tray title + dock badge, Windows overlay icon,
// Linux app.setBadgeCount; the tray image itself carries the count everywhere.
import nodePath from 'node:path';
import { fileURLToPath } from 'node:url';

import { badgeFile, badgeText, normalizeCount } from '../shared/badge.js';
import { TRAY_SESSIONS_MAX } from '../shared/constants.js';

const DEFAULT_ASSETS = fileURLToPath(new URL('../../assets', import.meta.url));
export const PAUSE_CONFIRM_TEXT = 'Pause reading on every account tab? Usage will stop updating until you turn reading on again.';

/**
 * @param {{electron: object, bus: object, window: object, usage: object, hub: object, platform: string,
 *          assetsDir?: string, confirm?: (text: string) => Promise<boolean>, path?: object, log?: object}} deps
 *   assetsDir: the folder that holds `tray/` (default: app/assets next to src/)
 *   confirm: default is a native message box
 * @returns {{start(): boolean, stop(): void, available(): boolean, count(): number, buildMenu(): object}}
 *   start() false = no tray here
 */
export function create(deps) {
  const { electron, bus, window, usage, hub, platform } = deps;
  const path = deps.path ?? nodePath;
  const log = deps.log ?? console;
  const assets = path.join(deps.assetsDir ?? DEFAULT_ASSETS, 'tray');
  const confirm =
    deps.confirm ??
    (async (text) => {
      const { response } = await electron.dialog.showMessageBox({
        type: 'question', buttons: ['Pause all', 'Cancel'], defaultId: 1, cancelId: 1, message: text,
      });
      return response === 0;
    });

  let tray = null;
  let off = null;
  let count = 0;
  let sessions = [];

  const image = (n) => electron.nativeImage.createFromPath(path.join(assets, badgeFile(n)));

  function show() {
    window.showWindow();
  }

  function openRoute(route) {
    show();
    Promise.resolve(hub.route(route)).catch((err) => log.error('tray: route failed:', err));
  }

  function buildMenu() {
    const items = [{ label: 'Open Switchyard', click: show }, { type: 'separator' }];
    if (sessions.length === 0) items.push({ label: 'Nothing needs you', enabled: false });
    for (const s of sessions.slice(0, TRAY_SESSIONS_MAX)) items.push({ label: String(s.title), click: () => openRoute(s.route) });
    items.push(
      { type: 'separator' },
      { label: 'Refresh usage', click: () => Promise.resolve(usage.refresh()).catch((err) => log.error('tray: refresh failed:', err)) },
      {
        label: 'Pause all reading',
        click: async () => {
          try {
            if (await confirm(PAUSE_CONFIRM_TEXT)) await usage.pauseAll();
          } catch (err) {
            log.error('tray: pause failed:', err);
          }
        },
      },
      { type: 'separator' },
      { label: 'Quit', click: () => electron.app.quit() },
    );
    return electron.Menu.buildFromTemplate(items);
  }

  function applyBadge() {
    if (!tray) return;
    tray.setImage(image(count));
    tray.setToolTip(count > 0 ? `Switchyard: ${count} need${count === 1 ? 's' : ''} you` : 'Switchyard');
    tray.setContextMenu(buildMenu());
    const { app } = electron;
    if (platform === 'darwin') {
      tray.setTitle(badgeText(count));
      app.dock?.setBadge(badgeText(count));
    } else if (platform === 'win32') {
      const win = window.win;
      if (win && !win.isDestroyed?.()) win.setOverlayIcon(count > 0 ? image(count) : null, count > 0 ? `${count} need you` : '');
    } else {
      app.setBadgeCount(count);
    }
  }

  function onNeedsYou(payload) {
    count = normalizeCount(payload?.count);
    sessions = Array.isArray(payload?.sessions) ? payload.sessions : [];
    applyBadge();
  }

  return {
    start() {
      if (tray) return true;
      try {
        tray = new electron.Tray(image(0));
      } catch (err) {
        log.warn?.('tray: not available here:', err?.message ?? err);
        tray = null;
        return false;
      }
      if (platform !== 'darwin') {
        tray.on('click', show);
        tray.on('double-click', show);
      }
      const last = bus.last?.('needs_you');
      if (last) onNeedsYou(last);
      else applyBadge();
      off = bus.on('needs_you', onNeedsYou);
      return true;
    },
    stop() {
      if (off) off();
      off = null;
      if (tray && !tray.isDestroyed?.()) tray.destroy();
      tray = null;
    },
    available() {
      return tray !== null && !tray.isDestroyed?.();
    },
    count: () => count,
    buildMenu,
  };
}
