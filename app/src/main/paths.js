// User data folder (C1). Runs before 'ready': sets app.setPath('userData') to
// <appData>/watchyard-desktop (or WY_APP_USER_DATA, absolute, only when !app.isPackaged, read
// through test-hooks.js). The folder name stays as it is so existing installs keep their data.
// After a rename, the folder of the old name is moved to the new one on the first start that
// finds only the old one (docs/spec/rename.md 2.9); if the move fails the old folder is used.
//
// Electron would otherwise derive the folder from productName ('Watchyard').
//
// Contract: docs/spec/desktop.md C1.

import { USER_DATA_DIRNAME, LEGACY_USER_DATA_DIRNAME, APP_STATE_FILE, HUB_TOKEN_FILE } from '../shared/constants.js';

/**
 * @param {{app: Electron.App, path: typeof import('node:path'), env: Record<string, string|undefined>,
 *          testHooks?: object|null, fsSync?: {existsSync: Function, renameSync: Function}}} deps
 *   testHooks: main/test-hooks.js exports, null when packaged. fsSync: node:fs, for the move of
 *   the folder from before a rename (absent: no move).
 * @returns {{userData: string, appStateFile: string, hubTokenFile: string}}
 */
export function create(deps) {
  const { app, path, env } = deps;
  if (app.isReady()) throw new Error('paths: the user-data folder must be set before ready');
  const appData = app.getPath('appData');
  let dir = path.join(appData, USER_DATA_DIRNAME);
  let override = false;
  if (!app.isPackaged && deps.testHooks?.userData) {
    const given = deps.testHooks.userData(env ?? {});
    if (given) {
      if (!path.isAbsolute(given)) throw new Error('paths: WY_APP_USER_DATA must be an absolute path');
      dir = path.resolve(given);
      override = true;
    }
  }
  if (!override) dir = legacyMove(dir, path.join(appData, LEGACY_USER_DATA_DIRNAME), deps.fsSync);
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

/**
 * The user-data folder to use: dir, after moving the folder from before a rename (legacy) to it
 * when only legacy exists. A failed move keeps legacy, so nothing is lost.
 * @param {string} dir
 * @param {string} legacy
 * @param {{existsSync: Function, renameSync: Function}|undefined} fsSync
 * @returns {string}
 */
export function legacyMove(dir, legacy, fsSync) {
  if (!fsSync || legacy === dir || fsSync.existsSync(dir) || !fsSync.existsSync(legacy)) return dir;
  try {
    fsSync.renameSync(legacy, dir);
    return dir;
  } catch {
    return legacy;
  }
}
