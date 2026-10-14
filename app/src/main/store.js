// app-state.json (C1): atomic write (temp file, fsync, rename, mode 0600), schema v,
// unknown keys kept, a corrupt file moved aside to app-state.json.bad-<ts> and started fresh
// (with a toast). Writes are serialised.
//
// Owned by WP-A. Contract: docs/spec/desktop.md C1. B owns what goes into tabs[], E owns
// settings; this module only keeps the file sound.
//
// - load(): a missing file is a first start (nothing is written until the first update). A
//   file that is not JSON, not an object, has tabs that is not an array, settings that is
//   not an object, or a `v` other than APP_STATE_V is moved aside (never deleted) and the
//   state starts fresh; `recovered` names where it went. Tab entries that are not objects or
//   whose id does not match the tab id pattern are dropped (and logged). Unknown keys, at the
//   top level, in settings and in each tab, are kept as they are.
// - update(mutate): mutate gets a deep copy; when it resolves, the copy is written (temp file
//   in the same folder, fsync, rename, then the folder fsynced where the OS allows) and
//   becomes the state. If mutate throws or the write fails, the state is unchanged and the
//   promise rejects; later updates still run. Updates run one at a time, in call order.

import { APP_STATE_V, FILE_MODE, TAB_ID_RE } from '../shared/constants.js';

const DEFAULT_SETTINGS = Object.freeze({ keep_running: true, notifications: true, start_hidden: false, close_notice_shown: false });

/** The state of a first start. */
export function emptyState() {
  return { v: APP_STATE_V, tabs: [], settings: { ...DEFAULT_SETTINGS } };
}

const isPlainObject = (v) => typeof v === 'object' && v !== null && !Array.isArray(v);

/**
 * Checks and normalises parsed JSON; returns {state, dropped} or throws for a file to move aside.
 * @param {unknown} data
 */
export function normalise(data) {
  if (!isPlainObject(data)) throw new Error('not an object');
  if (data.v !== APP_STATE_V) throw new Error(`unsupported v ${JSON.stringify(data.v)}`);
  if (data.tabs !== undefined && !Array.isArray(data.tabs)) throw new Error('tabs is not an array');
  if (data.settings !== undefined && !isPlainObject(data.settings)) throw new Error('settings is not an object');
  const dropped = [];
  const seen = new Set();
  const tabs = [];
  for (const t of data.tabs ?? []) {
    if (!isPlainObject(t) || typeof t.id !== 'string' || !TAB_ID_RE.test(t.id) || seen.has(t.id)) {
      dropped.push(isPlainObject(t) ? String(t.id).slice(0, 40) : typeof t);
      continue;
    }
    seen.add(t.id);
    tabs.push(t);
  }
  return { state: { ...data, v: APP_STATE_V, tabs, settings: { ...DEFAULT_SETTINGS, ...(data.settings ?? {}) } }, dropped };
}

const stamp = (ms) => new Date(ms).toISOString().replace(/[:.]/g, '-');

/**
 * @param {{fs: typeof import('node:fs/promises'), path: typeof import('node:path'), file: string,
 *          log: object, now?: () => number, toast?: (level: string, text: string) => void}} deps
 *   toast: optional; called once when a corrupt file was moved aside (WP-I may pass ipc.send).
 * @returns {{
 *   load(): Promise<{state: object, recovered: string|null}>,
 *   get(): object,
 *   update(mutate: (draft: object) => void|Promise<void>): Promise<object>,
 *   flush(): Promise<void>,
 * }}
 */
export function create(deps) {
  const { fs, path, file } = deps;
  const log = deps.log ?? console;
  const now = deps.now ?? Date.now;
  const dir = path.dirname(file);
  let state = emptyState();
  let loaded = false;
  let chain = Promise.resolve();
  let seq = 0;

  async function load() {
    let text = null;
    try {
      text = await fs.readFile(file, 'utf8');
    } catch (err) {
      if (err?.code !== 'ENOENT') throw err;
    }
    let recovered = null;
    if (text === null) {
      state = emptyState();
    } else {
      try {
        const { state: s, dropped } = normalise(JSON.parse(text));
        state = s;
        if (dropped.length) log.warn?.(`store: dropped ${dropped.length} bad tab entr${dropped.length === 1 ? 'y' : 'ies'}`);
      } catch (err) {
        recovered = `${file}.bad-${stamp(now())}`;
        await fs.rename(file, recovered);
        log.warn?.(`store: ${path.basename(file)} unreadable (${err.message}); moved to ${recovered}, starting fresh`);
        state = emptyState();
        deps.toast?.('warn', `The app's saved state could not be read, so it started fresh. The old file was kept as ${path.basename(recovered)}.`);
      }
    }
    loaded = true;
    return { state: structuredClone(state), recovered };
  }

  async function writeAtomic(next) {
    await fs.mkdir(dir, { recursive: true, mode: 0o700 });
    const tmp = `${file}.tmp-${process.pid}-${++seq}`;
    const json = `${JSON.stringify(next, null, 2)}\n`;
    let fh = null;
    try {
      fh = await fs.open(tmp, 'w', FILE_MODE);
      await fh.writeFile(json, 'utf8');
      await fh.sync();
      await fh.close();
      fh = null;
      await fs.chmod(tmp, FILE_MODE);
      await fs.rename(tmp, file);
    } catch (err) {
      if (fh) await fh.close().catch(() => {});
      await fs.rm(tmp, { force: true }).catch(() => {});
      throw err;
    }
    // Make the rename durable; not possible on every OS (Windows), so best effort.
    let dh = null;
    try {
      dh = await fs.open(dir, 'r');
      await dh.sync();
    } catch {
      // not supported here
    } finally {
      await dh?.close().catch(() => {});
    }
  }

  function update(mutate) {
    if (typeof mutate !== 'function') return Promise.reject(new TypeError('store.update needs a function'));
    const run = async () => {
      if (!loaded) throw new Error('store: update before load');
      const draft = structuredClone(state);
      await mutate(draft);
      const { state: next, dropped } = normalise({ ...draft, v: APP_STATE_V });
      if (dropped.length) throw new Error(`store: refusing to write bad tab entries (${dropped.join(', ')})`);
      await writeAtomic(next);
      state = next;
      return structuredClone(state);
    };
    const p = chain.then(run);
    chain = p.catch(() => {});
    return p;
  }

  return Object.freeze({
    load,
    get: () => structuredClone(state),
    update,
    flush: () => chain,
  });
}
