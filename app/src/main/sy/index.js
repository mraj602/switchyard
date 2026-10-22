// Running sy (C11): resolution, execFile with the cleaned environment, timeouts and
// caps, and the argv allow-list (main/sy/commands.js, the only builder). Every result of a
// desktop command is the JSON of C4; a non-zero exit is {ok: false, exit, error}.
//
// Contract: docs/spec/desktop.md C4, C11. Every method resolves; none rejects.
// A success carries `ok: true` next to the fields of the C4 JSON, so callers check `ok` first.

import { FLUSH_DEBOUNCE_MS } from '../../shared/constants.js';
import { build } from './commands.js';
import { createRunner } from './runner.js';
import { resolve } from './resolve.js';

export { build } from './commands.js';

const NO_SY = Object.freeze({ ok: false, exit: -1, stdout: '', error: 'sy not found: install it or run the Switchyard setup', code: 'no_sy' });
const TOKEN_CODE_RE = /^[A-Za-z0-9_.-]{1,200}$/;

/**
 * @param {{env: Record<string, string|undefined>, fs: object, path: object, platform: string,
 *          resourcesPath: string, minSy: string, log: object, syBin?: string|null,
 *          execFile?: Function, timers?: object}} deps
 *   syBin: SY_BIN, only when unpackaged (test-hooks.js). execFile: a test seam; by default this
 *   module imports node:child_process itself (the lint allows it only under main/sy/).
 * @returns {{
 *   info(): Promise<object>,                       // sy desktop info --json (C4), or {ok: false, …}
 *   append(records: object[]): Promise<{ok: boolean, ids?: string[], error?: string}>,
 *                                                  // sy outbox append; ids.length === records.length when ok
 *   flushSoon(): void,                             // sy flush --quiet, debounced 2 s, coalesced
 *   token(opts?: {code?: string, name: string}): Promise<object>,   // code travels on stdin only
 *   ownerLink(): Promise<object>,
 *   service(action: 'status'|'start'|'stop'): Promise<object>,
 *   accountsList(): Promise<object>,
 *   share(folder: string, share: string): Promise<object>,
 *   machineId(): Promise<string|null>,             // sy machine show --json, cached
 *   run(purpose: string, params?: object, stdin?: string): Promise<{ok: boolean, exit: number,
 *       stdout: string, error?: string}>,          // the one runner; purpose names a row of C11
 *   resolved(): Promise<object|null>,              // {bin, source, version} (tests, diagnostics)
 *   flushNow(): Promise<void>,                     // run a pending flush at once (before quit)
 * }}
 */
export function create(deps) {
  const log = deps.log ?? console;
  const timers = deps.timers ?? { setTimeout, clearTimeout };
  const runner = createRunner({ execFile: deps.execFile, env: deps.env, platform: deps.platform });
  let found = null; // a successful resolution is kept; a failed one is retried on the next call
  let resolving = null;
  let cachedMachine = null;

  const rawVersion = async (bin) => {
    const r = await runner(bin, build('version').argv, { timeout: build('version').timeout });
    return r.ok ? r.stdout : null;
  };

  function resolved() {
    if (found) return Promise.resolve(found);
    resolving ??= resolve({
      env: deps.env, fs: deps.fs, path: deps.path, platform: deps.platform, resourcesPath: deps.resourcesPath,
      minSy: deps.minSy, syBin: deps.syBin ?? null, version: rawVersion,
    })
      .then((r) => {
        found = r;
        return r;
      })
      .finally(() => {
        resolving = null;
      });
    return resolving;
  }

  async function run(purpose, params = {}, stdin = undefined) {
    const spec = build(purpose, params); // throws for anything off the allow-list
    if (spec.stdin !== (stdin !== undefined)) throw new Error(`sy: ${purpose} ${spec.stdin ? 'needs' : 'takes no'} stdin`);
    const r = await resolved();
    if (!r) return { ...NO_SY };
    return runner(r.bin, spec.argv, { stdin, timeout: spec.timeout });
  }

  /** run() + JSON parse of stdout; failures keep the {ok: false, exit, error} shape. */
  async function json(purpose, params, stdin) {
    const r = await run(purpose, params, stdin);
    if (!r.ok) return { ok: false, exit: r.exit, error: r.error, ...(r.code ? { code: r.code } : {}) };
    let v;
    try {
      v = JSON.parse(r.stdout);
    } catch {
      return { ok: false, exit: 0, error: `sy ${purpose}: reply is not JSON` };
    }
    if (v === null || typeof v !== 'object') return { ok: false, exit: 0, error: `sy ${purpose}: unexpected reply` };
    return Array.isArray(v) ? { ok: true, accounts: v } : { ok: true, ...v };
  }

  // ---- flush: debounced 2 s, coalesced across tabs, never two at once

  let flushTimer = null;
  let flushing = null;
  let again = false;
  async function doFlush() {
    if (flushing) {
      again = true;
      return flushing;
    }
    flushing = (async () => {
      try {
        const r = await run('flush');
        if (!r.ok) log.warn?.(`sy flush failed: ${r.error}`);
      } catch (err) {
        log.warn?.(`sy flush failed: ${err?.message ?? err}`);
      }
    })();
    await flushing;
    flushing = null;
    if (again) {
      again = false;
      flushSoon();
    }
  }
  function flushSoon() {
    if (flushTimer) return; // coalesced: one pending timer covers every caller
    flushTimer = timers.setTimeout(() => {
      flushTimer = null;
      doFlush();
    }, FLUSH_DEBOUNCE_MS);
    flushTimer?.unref?.();
  }

  return {
    info: () => json('info'),
    async append(records) {
      if (!Array.isArray(records) || records.some((r) => typeof r !== 'object' || r === null || Array.isArray(r))) {
        return { ok: false, error: 'append: records must be an array of objects', retry: false };
      }
      if (records.length === 0) return { ok: true, ids: [] };
      const r = await run('append', {}, records.map((x) => JSON.stringify(x)).join('\n') + '\n');
      if (!r.ok) return { ok: false, error: r.error, exit: r.exit };
      const ids = r.stdout.split(/\r?\n/).map((l) => l.trim()).filter(Boolean);
      if (ids.length !== records.length) {
        // The outbox may hold some of them: not retried, so nothing is duplicated.
        return { ok: false, error: `append: ${ids.length} ids for ${records.length} records`, retry: false, ids };
      }
      return { ok: true, ids };
    },
    flushSoon,
    flushNow: async () => {
      if (flushTimer) {
        timers.clearTimeout(flushTimer);
        flushTimer = null;
        await doFlush();
      } else if (flushing) await flushing;
    },
    async token(opts = {}) {
      const code = opts.code;
      if (code !== undefined && (typeof code !== 'string' || !TOKEN_CODE_RE.test(code))) {
        return { ok: false, exit: 2, error: 'that code is not valid' };
      }
      const params = { name: opts.name ?? 'Switchyard-desktop', code_stdin: code !== undefined };
      const r = await json('token', params, code !== undefined ? `${code}\n` : undefined);
      return r;
    },
    ownerLink: () => json('owner'),
    service: (action) => json('service', { action }),
    accountsList: () => json('accounts'),
    share: (folder, share) => json('share', { folder, share }),
    async machineId() {
      if (cachedMachine) return cachedMachine;
      const r = await json('machine');
      const id = r.ok ? r.id ?? r.machine?.id ?? null : null;
      if (typeof id === 'string' && id) cachedMachine = id;
      return cachedMachine;
    },
    run,
    resolved,
  };
}
