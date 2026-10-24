// User data folder (C1). Runs before 'ready': sets app.setPath('userData') to
// <appData>/watchyard-desktop (or WY_APP_USER_DATA, absolute, only when !app.isPackaged, read
// through test-hooks.js). The folder name stays as it is so existing installs keep their data.
// After a rename, the folder of the old name is moved to the new one on the first start that
// finds only the old one (docs/spec/rename.md 2.9); if the move fails the old folder is used.
// The hub view's partition folder (Partitions/sy-hub) moves the same way, so its board and owner
// cookies survive; if that move fails the hub view signs in again (desktop.md C7).
//
// Electron would otherwise derive the folder from productName ('Watchyard').
//
// Contract: docs/spec/desktop.md C1.

import { hostname } from 'node:os';
import { USER_DATA_DIRNAME, LEGACY_USER_DATA_DIRNAME, APP_STATE_FILE, HUB_TOKEN_FILE, HUB_PARTITION, LEGACY_HUB_PARTITION } from '../shared/constants.js';

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
  if (deps.fsSync && !inUse(dir, deps.fsSync)) legacyMove(partitionDir(dir, HUB_PARTITION, path), partitionDir(dir, LEGACY_HUB_PARTITION, path), deps.fsSync);
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

/** Folder Electron keeps a `persist:<name>` partition in. */
export function partitionDir(userData, partition, path) {
  return path.join(userData, 'Partitions', partition.replace(/^persist:/, ''));
}

/**
 * The user-data folder to use: dir, after moving the folder from before a rename (legacy) to it
 * when only legacy exists. A failed move keeps legacy, so nothing is lost.
 * @param {string} dir
 * @param {string} legacy
 * @param {{existsSync: Function, renameSync: Function}|undefined} fsSync
 * @returns {string}
 */
export function legacyMove(dir, legacy, fsSync, live = inUse) {
  if (!fsSync || legacy === dir || fsSync.existsSync(dir) || !fsSync.existsSync(legacy)) return dir;
  // The old app still running holds the folder: use it as it is this time, move it on a later start.
  if (live(legacy, fsSync)) return legacy;
  try {
    fsSync.renameSync(legacy, dir);
    return dir;
  } catch {
    return legacy;
  }
}

/**
 * Whether a running app holds the user-data folder: Electron's `SingletonLock` there (Linux,
 * macOS) is a symlink to `<hostname>-<pid>`, and that process is alive on this host. Windows
 * has no such link; there it answers false.
 * @param {string} folder
 * @param {{readlinkSync?: Function}} fsSync
 * @param {{hostname?: string, alive?: (pid: number) => boolean}} [opts]
 */
export function inUse(folder, fsSync, opts = {}) {
  let target;
  try {
    target = fsSync.readlinkSync?.(`${folder}/SingletonLock`);
  } catch {
    return false;
  }
  const m = typeof target === 'string' ? /^(.*)-(\d+)$/.exec(target) : null;
  if (!m) return false;
  const host = opts.hostname ?? hostname();
  if (m[1] !== host) return false;
  const alive = opts.alive ?? ((pid) => {
    try {
      process.kill(pid, 0);
      return true;
    } catch (err) {
      return err?.code === 'EPERM';
    }
  });
  return alive(Number(m[2]));
}
