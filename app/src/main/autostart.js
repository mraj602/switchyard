// Run at login (C1): Linux ~/.config/autostart/switchyard-desktop.desktop and
// ~/.local/share/applications/switchyard-desktop.desktop; macOS and Windows
// app.setLoginItemSettings with --background. Never reads or writes switchyard.desktop.
import { DESKTOP_ENTRY, FLAG_BACKGROUND, PROTOTYPE_DESKTOP_ENTRY, TEST_ENV } from '../shared/constants.js';

/**
 * @param {{electron: object, fs: typeof import('node:fs/promises'), path: object, platform: string, home: string,
 *          execPath: string, appImage?: string|null, appPath?: string, isPackaged?: boolean,
 *          env?: Record<string,string|undefined>, iconName?: string}} deps
 *   fs: the promise API (mkdir, writeFile, rm, access). appImage: $APPIMAGE, used instead of
 *   execPath (which sits in a temporary mount). appPath + !isPackaged: a development run
 *   (`electron <appPath>`). env: only SY_APP_USER_DATA is read, and only when !isPackaged.
 * @returns {{get(): Promise<boolean>, set(on: boolean): Promise<void>, desktopEntry(background: boolean): string,
 *            files(): {autostart: string, launcher: string}}}
 */
export function create(deps) {
  const { electron, fs, path, platform, home, execPath } = deps;
  const { app } = electron;
  const isPackaged = deps.isPackaged ?? app.isPackaged;
  const iconName = deps.iconName ?? 'switchyard-desktop';

  const files = () => ({
    autostart: path.join(home, '.config', 'autostart', DESKTOP_ENTRY),
    launcher: path.join(home, '.local', 'share', 'applications', DESKTOP_ENTRY),
  });
  if (DESKTOP_ENTRY === PROTOTYPE_DESKTOP_ENTRY) throw new Error('autostart: refusing the prototype entry name');

  // Quoting as the legacy prototype's desktopEntry, plus `%` (a field code in Exec).
  const q = (x) => `"${String(x).replace(/(["\\`$])/g, '\\$1').replace(/%/g, '%%')}"`;

  function desktopEntry(background) {
    const userData = !isPackaged && deps.env?.[TEST_ENV.USER_DATA] ? path.resolve(deps.env[TEST_ENV.USER_DATA]) : null;
    const env = userData ? `env ${TEST_ENV.USER_DATA}=${q(userData)} ` : '';
    const bin = deps.appImage || execPath;
    const exec = isPackaged || deps.appImage ? q(bin) : `${q(bin)} ${q(deps.appPath ?? app.getAppPath())}`;
    return [
      '[Desktop Entry]',
      'Type=Application',
      'Name=Switchyard Desktop',
      'Comment=Usage, limits and alerts for several Claude accounts (unofficial)',
      `Exec=${env}${exec}${background ? ` ${FLAG_BACKGROUND}` : ''}`,
      `Icon=${iconName}`,
      'Terminal=false',
      'Categories=Utility;Development;',
      'StartupWMClass=switchyard-desktop',
      ...(background ? ['X-GNOME-Autostart-enabled=true', 'X-GNOME-Autostart-Delay=10'] : []),
      '',
    ].join('\n');
  }

  return {
    desktopEntry,
    files,
    async get() {
      if (platform === 'linux') {
        try {
          await fs.access(files().autostart);
          return true;
        } catch {
          return false;
        }
      }
      return Boolean(app.getLoginItemSettings({ args: [FLAG_BACKGROUND] }).openAtLogin);
    },
    async set(on) {
      if (platform === 'linux') {
        const f = files();
        if (on) {
          await fs.mkdir(path.dirname(f.autostart), { recursive: true });
          await fs.writeFile(f.autostart, desktopEntry(true), { mode: 0o644 });
          await fs.mkdir(path.dirname(f.launcher), { recursive: true });
          await fs.writeFile(f.launcher, desktopEntry(false), { mode: 0o644 }); // so it opens from the app grid
        } else {
          await fs.rm(f.autostart, { force: true });
        }
        return;
      }
      const item = { openAtLogin: Boolean(on), args: [FLAG_BACKGROUND] };
      if (platform === 'darwin') item.openAsHidden = true;
      app.setLoginItemSettings(item);
    },
  };
}
