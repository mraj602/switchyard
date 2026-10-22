// User data folder (C1). Runs before 'ready': sets app.setPath('userData') to
// <appData>/switchyard-desktop (or SY_APP_USER_DATA, absolute, only when !app.isPackaged, read
// through test-hooks.js), and refuses any folder whose basename is 'switchyard' in any case, or
// that lies inside the legacy prototype's <appData>/switchyard folder.
//
// Electron would otherwise derive the folder from productName ('Switchyard'), which on a
// case-insensitive macOS volume is the legacy prototype's folder.
//
// Contract: docs/spec/desktop.md C1.

import { USER_DATA_DIRNAME, PROTOTYPE_DIRNAME, APP_STATE_FILE, HUB_TOKEN_FILE } from '../shared/constants.js';

/**
 * @param {{app: Electron.App, path: typeof import('node:path'), env: Record<string, string|undefined>,
 *          testHooks?: object|null}} deps   testHooks: main/test-hooks.js exports, null when packaged
 * @returns {{userData: string, appStateFile: string, hubTokenFile: string}}
 */
export function create(deps) {
  const { app, path, env } = deps;
  if (app.isReady()) throw new Error('paths: the user-data folder must be set before ready');
  const appData = app.getPath('appData');
  let dir = path.join(appData, USER_DATA_DIRNAME);
  if (!app.isPackaged && deps.testHooks?.userData) {
    const override = deps.testHooks.userData(env ?? {});
    if (override) {
      if (!path.isAbsolute(override)) throw new Error('paths: SY_APP_USER_DATA must be an absolute path');
      dir = path.resolve(override);
    }
  }
  checkUserData(dir, { path, appData });
  app.setPath('userData', dir);
  const got = app.getPath('userData');
  if (got !== dir) throw new Error(`paths: userData is ${got}, expected ${dir}`);
  return Object.freeze({
    userData: dir,
    appStateFile: path.join(dir, APP_STATE_FILE),
    hubTokenFile: path.join(dir, HUB_TOKEN_FILE),
  });
}

/**
 * Throws when dir is not an acceptable user-data folder: relative, basename 'switchyard' in
 * any case, or (when appData is given) inside <appData>/switchyard in any case.
 * @param {string} dir
 * @param {{path?: typeof import('node:path'), appData?: string}} [opts]
 */
export function checkUserData(dir, opts = {}) {
  const path = opts.path;
  if (typeof dir !== 'string' || dir.length === 0) throw new Error('paths: user-data folder missing');
  const abs = path ? path.isAbsolute(dir) : dir.startsWith('/') || /^[A-Za-z]:[\\/]/.test(dir);
  if (!abs) throw new Error(`paths: user-data folder must be absolute: ${dir}`);
  const parts = dir.split(/[\\/]+/).filter(Boolean);
  const base = parts.at(-1) ?? '';
  if (base.toLowerCase() === PROTOTYPE_DIRNAME) {
    throw new Error(`paths: refusing user-data folder ${dir}: it is the prototype's folder name (${PROTOTYPE_DIRNAME})`);
  }
  if (opts.appData) {
    const norm = (p) => (path ? path.resolve(p) : p).split(/[\\/]+/).filter(Boolean).map((s) => s.toLowerCase());
    const proto = [...norm(opts.appData), PROTOTYPE_DIRNAME];
    const d = norm(dir);
    if (d.length >= proto.length && proto.every((s, i) => d[i] === s)) {
      throw new Error(`paths: refusing user-data folder ${dir}: it is inside the prototype's folder`);
    }
  }
}
