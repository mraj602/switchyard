// Copy-only import from the prototype's user-data folder (import:prototype and
// --import-prototype[=<dir>] [--dry-run]). Refuses while the prototype runs; never writes its
// folder: it reads accounts.json and copies Partitions/acct-<id>/ to this app's
// Partitions/tab-<id>/ (docs/phases/6.md WP-F, docs/spec/desktop.md C1).

import { PROTOTYPE_DIRNAME, TAB_ID_RE, TAB_NAME_MAX, READING_NEVER_ASKED, FLAG_IMPORT_PROTOTYPE, FLAG_DRY_RUN, CLAUDE_ORIGIN } from '../shared/constants.js';

/** Folders never copied (caches are rebuilt by Chromium). */
export const SKIP_DIRS = Object.freeze(['Cache', 'Code Cache', 'GPUCache']);
const PROTOTYPE_ID_RE = /^[0-9a-f]{8}$/;

/** Error with a machine-readable `code`: 'running', 'not_found', 'bad_accounts', 'same_folder'. */
export class ImportError extends Error {
  constructor(code, message) {
    super(message);
    this.name = 'ImportError';
    this.code = code;
  }
}

/**
 * Parses `--import-prototype[=<dir>]` and `--dry-run` from argv.
 * @param {string[]} argv
 * @returns {{dir?: string, dry_run: boolean}|null}  null when the flag is absent
 */
export function parseArgs(argv) {
  let found = false;
  let dir;
  let dry = false;
  for (const a of argv) {
    if (a === FLAG_IMPORT_PROTOTYPE) found = true;
    else if (a.startsWith(FLAG_IMPORT_PROTOTYPE + '=')) {
      found = true;
      dir = a.slice(FLAG_IMPORT_PROTOTYPE.length + 1) || undefined;
    } else if (a === FLAG_DRY_RUN) dry = true;
  }
  return found ? { ...(dir ? { dir } : {}), dry_run: dry } : null;
}

/**
 * @param {{fs: typeof import('node:fs/promises'), path: object, platform: string, appData: string,
 *          userData: string, tabs?: object, store: object, hostname: string,
 *          isPidAlive: (pid: number) => boolean, log: object}} deps
 *   `tabs.closeViews(ids)` is called when present, before a copy, for tab ids that have views.
 * @returns {{run(opts: {dir?: string, dry_run: boolean}): Promise<object>}}   // ImportReport (shared/ipc.js)
 */
export function create(deps) {
  const { fs, path, platform, appData, userData, tabs, store, hostname, isPidAlive } = deps;
  const log = deps.log || { info() {}, warn() {} };

  const exists = async (p) => {
    try {
      await fs.lstat(p);
      return true;
    } catch (e) {
      if (e.code === 'ENOENT') return false;
      throw e;
    }
  };

  /** True when the prototype holds its singleton lock (so it is running). */
  async function prototypeRunning(dir) {
    if (platform === 'win32') {
      // The lockfile is held open by Chromium: opening it for writing fails while it runs.
      const p = path.join(dir, 'lockfile');
      if (!(await exists(p))) return false;
      try {
        const h = await fs.open(p, 'r+');
        await h.close();
        return false;
      } catch (e) {
        return e.code === 'EBUSY' || e.code === 'EPERM' || e.code === 'EACCES';
      }
    }
    let target;
    try {
      target = await fs.readlink(path.join(dir, 'SingletonLock'));
    } catch {
      return false; // no lock (or not a symlink): not running
    }
    const m = /^(.*)-(\d+)$/.exec(target);
    if (!m) return false;
    // A lock from another host (a synced folder) is stale here; a live pid on this host is not.
    return m[1] === hostname && isPidAlive(Number(m[2]));
  }

  /** Recursive copy skipping caches and anything that is not a plain file or folder. Returns bytes. */
  async function copyTree(src, dst, dry) {
    let bytes = 0;
    if (!dry) await fs.mkdir(dst, { recursive: true });
    for (const ent of await fs.readdir(src, { withFileTypes: true })) {
      const s = path.join(src, ent.name);
      const d = path.join(dst, ent.name);
      if (ent.isDirectory()) {
        if (SKIP_DIRS.includes(ent.name)) continue;
        bytes += await copyTree(s, d, dry);
      } else if (ent.isFile()) {
        const st = await fs.stat(s);
        bytes += st.size;
        if (!dry) {
          await fs.copyFile(s, d);
          await fs.chmod(d, st.mode & 0o777).catch(() => {});
        }
      }
    }
    return bytes;
  }

  async function run(opts = {}) {
    const dry = !!opts.dry_run;
    const dir = path.resolve(opts.dir || path.join(appData, PROTOTYPE_DIRNAME));
    if (dir === path.resolve(userData)) throw new ImportError('same_folder', "That is this app's own folder.");
    if (!(await exists(dir))) throw new ImportError('not_found', `No prototype data folder at ${dir}.`);
    if (await prototypeRunning(dir)) {
      throw new ImportError('running', 'The Switchyard prototype is running. Quit it, then import again.');
    }

    let accounts;
    try {
      accounts = JSON.parse(await fs.readFile(path.join(dir, 'accounts.json'), 'utf8'));
      if (!Array.isArray(accounts)) throw new Error('not an array');
    } catch (e) {
      throw new ImportError('bad_accounts', `Cannot read accounts.json in ${dir}: ${e.message}`);
    }

    const state = store.get();
    const have = new Set((state.tabs || []).map((t) => t.id));
    const report = { dry_run: dry, copied: [], skipped: [], bytes: 0 };
    const newTabs = [];
    const seen = new Set();

    for (const a of accounts) {
      const id = a && typeof a.id === 'string' ? a.id : '';
      if (!PROTOTYPE_ID_RE.test(id) || !TAB_ID_RE.test(id)) {
        report.skipped.push({ id, reason: 'bad_id' });
        continue;
      }
      if (have.has(id) || seen.has(id)) {
        report.skipped.push({ id, reason: 'already_present' });
        continue;
      }
      seen.add(id);
      const src = path.join(dir, 'Partitions', `acct-${id}`);
      const dst = path.join(userData, 'Partitions', `tab-${id}`);
      if (!(await exists(src))) {
        report.skipped.push({ id, reason: 'no_partition' });
        continue;
      }
      if (await exists(dst)) {
        report.skipped.push({ id, reason: 'partition_exists' });
        continue;
      }
      if (dry) {
        report.bytes += await copyTree(src, dst, true);
      } else {
        if (tabs && typeof tabs.closeViews === 'function') await tabs.closeViews([id]);
        // Copy to a temp name and rename, so a failed copy never leaves a half partition.
        const tmp = `${dst}.importing`;
        await fs.rm(tmp, { recursive: true, force: true });
        try {
          report.bytes += await copyTree(src, tmp, false);
          await fs.rename(tmp, dst);
        } catch (e) {
          await fs.rm(tmp, { recursive: true, force: true }).catch(() => {});
          log.warn(`import: ${id}: ${e.message}`);
          report.skipped.push({ id, reason: 'copy_failed' });
          continue;
        }
      }
      const label = typeof a.label === 'string' ? a.label.trim().slice(0, TAB_NAME_MAX) : '';
      const lastUrl = typeof a.lastUrl === 'string' && a.lastUrl.startsWith(CLAUDE_ORIGIN + '/') ? a.lastUrl : `${CLAUDE_ORIGIN}/`;
      newTabs.push({
        id,
        name: label || id,
        last_url: lastUrl,
        reading: READING_NEVER_ASKED,
        warning_accepted: 0,
        send_to_hub: true,
        stopped: null,
        seat: null,
      });
      report.copied.push(id);
    }

    if (!dry && newTabs.length) {
      await store.update((draft) => {
        draft.tabs = draft.tabs || [];
        for (const t of newTabs) if (!draft.tabs.some((x) => x.id === t.id)) draft.tabs.push(t);
      });
    }
    log.info(`import: ${dry ? 'dry run: ' : ''}${report.copied.length} copied, ${report.skipped.length} skipped`);
    return report;
  }

  return { run };
}
