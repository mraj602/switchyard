// User data folder (C1). Runs before 'ready': sets app.setPath('userData') to
// <appData>/switchyard-desktop (or SY_APP_USER_DATA, absolute, only when !app.isPackaged, read
// through test-hooks.js). The folder name stays as it is so existing installs keep their data.
//
// Electron would otherwise derive the folder from productName ('Switchyard').
//
// Contract: docs/spec/desktop.md C1.

import { USER_DATA_DIRNAME, APP_STATE_FILE, HUB_TOKEN_FILE } from '../shared/constants.js';

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
  checkUserData(dir, { path });
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
 * Throws when dir is not an acceptable user-data folder: missing or relative.
 * @param {string} dir
 * @param {{path?: typeof import('node:path')}} [opts]
 */
export function checkUserData(dir, opts = {}) {
  const path = opts.path;
  if (typeof dir !== 'string' || dir.length === 0) throw new Error('paths: user-data folder missing');
  const abs = path ? path.isAbsolute(dir) : dir.startsWith('/') || /^[A-Za-z]:[\\/]/.test(dir);
  if (!abs) throw new Error(`paths: user-data folder must be absolute: ${dir}`);
}
