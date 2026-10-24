// Tray (C9): a monochrome chevron glyph (assets/tray; dark ink for light bars, light ink for
// dark bars, picked by nativeTheme and re-picked when it changes; a template image on macOS),
// platform branches, and the menu. "Needs you" is a small dot on the glyph; the count is in
// the tooltip and the first menu line. Also: macOS dock badge, Windows overlay dot, Linux
// app.setBadgeCount.
import nodePath from 'node:path';
import { fileURLToPath } from 'node:url';

import { badgeText, normalizeCount } from '../shared/badge.js';
import { TRAY_SESSIONS_MAX } from '../shared/constants.js';

const DEFAULT_ASSETS = fileURLToPath(new URL('../../assets', import.meta.url));
/** "2 sessions need you", "1 session needs you"; '' for none. */
export function needsYouText(count) {
  return count > 0 ? `${count} session${count === 1 ? '' : 's'} need${count === 1 ? 's' : ''} you` : '';
}

/** The glyph size per platform; every size 16/22/24/32 (with @2x) is shipped in assets/tray. */
const TRAY_SIZE = { linux: 22, win32: 16, darwin: 16 };

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

  const dark = () => !!electron.nativeTheme?.shouldUseDarkColors;

  // Folder of the glyph ink: 'light' (white) for dark bars. Linux panels (GNOME, KDE, most others) are
  // dark even under a light system theme, so Linux is always light unless WATCHYARD_TRAY_ICON=dark|light
  // says otherwise; Windows follows the theme.
  function inkFolder() {
    if (platform === 'linux') {
      const env = deps.env ?? process.env;
      // The variable's name from before a rename still works (docs/spec/rename.md 2.9).
      const legacy = env.SWITCHYARD_TRAY_ICON; // rename:keep
      const o = String(env.WATCHYARD_TRAY_ICON ?? legacy ?? '').trim().toLowerCase();
      if (o === 'dark' || o === 'light') return o;
      return 'light';
    }
    return dark() ? 'light' : 'dark';
  }

  // dot: the needs-you state. On macOS the template image adapts to the bar by itself; elsewhere
  // a dark bar (dark theme) gets the light glyph and a light bar the dark glyph.
  function image(dot) {
    if (platform === 'darwin') {
      const img = electron.nativeImage.createFromPath(path.join(assets, dot ? 'trayDotTemplate.png' : 'trayTemplate.png'));
      img.setTemplateImage?.(true);
      return img;
    }
    const size = TRAY_SIZE[platform] ?? 22;
    return electron.nativeImage.createFromPath(path.join(assets, inkFolder(), `${dot ? 'tray-dot' : 'tray'}-${size}.png`));
  }

  const overlay = () => electron.nativeImage.createFromPath(path.join(assets, 'overlay-dot.png'));

  function show() {
    window.showWindow();
  }

  function openRoute(route) {
    show();
    Promise.resolve(hub.route(route)).catch((err) => log.error('tray: route failed:', err));
  }

  function buildMenu() {
    const items = [{ label: 'Open Watchyard', click: show }, { type: 'separator' }];
    if (count > 0) items.push({ label: needsYouText(count), enabled: false });
    if (sessions.length === 0 && count === 0) items.push({ label: 'Nothing needs you', enabled: false });
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
    tray.setImage(image(count > 0));
    tray.setToolTip(count > 0 ? needsYouText(count) : 'Watchyard');
    tray.setContextMenu(buildMenu());
    const { app } = electron;
    if (platform === 'darwin') {
      app.dock?.setBadge(badgeText(count));
    } else if (platform === 'win32') {
      const win = window.win;
      if (win && !win.isDestroyed?.()) win.setOverlayIcon(count > 0 ? overlay() : null, needsYouText(count));
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
        tray = new electron.Tray(image(false));
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
      electron.nativeTheme?.on?.('updated', applyBadge);
      return true;
    },
    stop() {
      if (off) off();
      off = null;
      electron.nativeTheme?.removeListener?.('updated', applyBadge);
      if (tray && !tray.isDestroyed?.()) tray.destroy();
      tray = null;
    },
    available() {
      return tray !== null && !tray.isDestroyed?.();
    },
    count: () => count,
    /** The icon file name in use (a dot variant while something needs you); for the smoke test. */
    iconName: () => (platform === 'darwin' ? `${count > 0 ? 'trayDotTemplate' : 'trayTemplate'}.png` : `${count > 0 ? 'tray-dot' : 'tray'}-${TRAY_SIZE[platform] ?? 22}.png`),
    buildMenu,
  };
}
