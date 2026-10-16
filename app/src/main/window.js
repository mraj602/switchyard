// The one BrowserWindow (1440×940, min 720×500) and the view layout: top bar 48 px, strip
// 40 px under an account tab, the active view fills the rest, the others setVisible(false).
//
// Owned by WP-A. Contract: docs/spec/desktop.md C8 layout.
//
// The shell is the window's own webContents and fills the whole window; it draws the top bar,
// the strip and Settings. Account tabs and the hub view are WebContentsViews stacked over it
// (win.contentView children). Only the selected one is visible, placed below the top bar (and
// below the strip for an account tab). 'settings' shows the shell alone. create() makes the
// window, guards it (security.guardView / guardSession, kind 'shell') and loads app://shell/.
// It shows the window on 'ready-to-show' unless startHidden.
//
// Additions beyond the wave 0 stub: onSelect(fn) → off (the hub module creates its view when
// 'hub' is first selected; state.js pushes the new view), layout() and bounds() for tests.

import { SHELL_URL, VIEW_SHELL, VIEW_TAB, VIEW_HUB } from '../shared/constants.js';

export const WINDOW = Object.freeze({ WIDTH: 1440, HEIGHT: 940, MIN_WIDTH: 720, MIN_HEIGHT: 500 });
export const TOP_BAR_PX = 48;
export const STRIP_PX = 40;
/** Window background before the shell paints (docs/design/screens Desk*: page background). */
export const BACKGROUND = Object.freeze({ light: '#F3F4F1', dark: '#17191B' });

/**
 * @param {{electron: object, security: object, bus?: object, preloadPath: string, startHidden: boolean,
 *          backgroundColor?: string, log?: object, title?: string, icon?: string}} deps
 *   icon: the app icon's path (assets/icon.png); not set on macOS (the bundle has it)
 * @returns {{
 *   win: object,
 *   shell: object,
 *   addView(id: string, view: object, kind: 'tab'|'hub'): void,
 *   removeView(id: string): void,
 *   select(id: 'hub'|'settings'|string): void,
 *   selected(): string,
 *   showWindow(): void,
 *   isShowing(id: string): boolean,
 *   onSelect(fn: (id: string) => void): () => void,
 *   layout(): void,
 * }}
 */
export function create(deps) {
  const { electron, security } = deps;
  const log = deps.log ?? console;
  const dark = !!electron.nativeTheme?.shouldUseDarkColors;
  const backgroundColor = deps.backgroundColor ?? (dark ? BACKGROUND.dark : BACKGROUND.light);

  const win = new electron.BrowserWindow({
    width: WINDOW.WIDTH,
    height: WINDOW.HEIGHT,
    minWidth: WINDOW.MIN_WIDTH,
    minHeight: WINDOW.MIN_HEIGHT,
    show: false,
    backgroundColor,
    title: deps.title ?? 'Switchyard',
    // Linux and Windows: the menu (Switchyard, Edit, View) stays one Alt press away instead of a
    // second bar above the app's own top bar (desktop UI audit). macOS keeps its menu bar.
    autoHideMenuBar: true,
    // The window icon (X11 _NET_WM_ICON, the Windows taskbar). On Wayland the shell finds the
    // icon through the app_id, which package.json desktopName sets (switchyard-desktop).
    ...(deps.icon ? { icon: deps.icon } : {}),
    webPreferences: security.webPreferences(VIEW_SHELL, { preload: deps.preloadPath }),
  });
  const shell = win.webContents;
  security.guardView(shell, VIEW_SHELL);
  security.guardSession(shell.session, VIEW_SHELL);

  /** id → {view, kind} */
  const views = new Map();
  /** Views kept out of sight although selected (the hub board while the app shows its own state panel). */
  const hiddenViews = new Set();
  let current = 'hub';
  const selectListeners = new Set();

  function layout() {
    if (win.isDestroyed?.()) return;
    const { width, height } = win.getContentBounds();
    for (const [id, { view, kind }] of views) {
      const active = id === current;
      if (active) {
        const top = kind === VIEW_TAB ? TOP_BAR_PX + STRIP_PX : TOP_BAR_PX;
        view.setBounds({ x: 0, y: top, width: Math.max(0, width), height: Math.max(0, height - top) });
      }
      view.setVisible(active && !hiddenViews.has(id));
    }
  }

  for (const ev of ['resize', 'maximize', 'unmaximize', 'enter-full-screen', 'leave-full-screen', 'restore']) win.on(ev, layout);
  win.once('ready-to-show', () => {
    if (!deps.startHidden) win.show();
  });

  function addView(id, view, kind) {
    if (kind !== VIEW_TAB && kind !== VIEW_HUB) throw new Error(`window.addView: bad kind ${kind}`);
    if (typeof id !== 'string' || !id || id === 'settings') throw new Error('window.addView: bad id');
    if (views.has(id)) throw new Error(`window.addView: ${id} already added`);
    if (kind === VIEW_HUB && id !== 'hub') throw new Error("window.addView: the hub view's id is 'hub'");
    if (kind === VIEW_TAB && id === 'hub') throw new Error("window.addView: 'hub' is the hub view's id");
    if (view.setBackgroundColor) view.setBackgroundColor(backgroundColor);
    view.setVisible(false);
    win.contentView.addChildView(view);
    views.set(id, { view, kind });
    layout();
  }

  function removeView(id) {
    const entry = views.get(id);
    if (!entry) return;
    views.delete(id);
    win.contentView.removeChildView(entry.view);
    if (current === id) select(firstTabId() ?? 'hub');
  }

  function setViewHidden(id, hidden) {
    if (hidden) hiddenViews.add(id);
    else hiddenViews.delete(id);
    layout();
  }

  function firstTabId() {
    for (const [id, { kind }] of views) if (kind === VIEW_TAB) return id;
    return null;
  }

  function select(id) {
    if (typeof id !== 'string' || !id) throw new Error('window.select: bad id');
    const changed = id !== current;
    current = id;
    layout();
    const entry = views.get(id);
    try {
      if (entry) entry.view.webContents?.focus?.();
      else shell.focus?.();
    } catch {
      // a view going away
    }
    if (changed) {
      for (const fn of [...selectListeners]) {
        try {
          fn(id);
        } catch (err) {
          log.error?.('window: onSelect listener failed:', err);
        }
      }
    }
  }

  function showWindow() {
    if (win.isDestroyed?.()) return;
    if (win.isMinimized()) win.restore();
    win.show();
    win.focus();
  }

  function isShowing(id) {
    if (win.isDestroyed?.()) return false;
    return win.isVisible() && win.isFocused() && !win.isMinimized() && current === id;
  }

  function onSelect(fn) {
    if (typeof fn !== 'function') throw new TypeError('window.onSelect needs a function');
    const entry = (id) => fn(id);
    selectListeners.add(entry);
    return () => selectListeners.delete(entry);
  }

  Promise.resolve()
    .then(() => win.loadURL(SHELL_URL))
    .catch((err) => log.error?.(`window: loading the shell failed: ${err?.message ?? err}`));

  return Object.freeze({
    win,
    shell,
    addView,
    removeView,
    setViewHidden,
    select,
    selected: () => current,
    showWindow,
    isShowing,
    onSelect,
    layout,
  });
}
