// The usage reader (C5): the one place that runs script inside a claude.ai tab (the security
// lint allows executeJavaScript only here). It reads the same endpoints claude.ai's Settings →
// Usage page uses, from inside that tab's own session, from an isolated world (1001). It reads
// numbers only: no chats, no cookies. Undocumented endpoints: they may change without notice.
// Owned by WP-B.

import { ACTIVE_ORG_COOKIE, READER_MAX_BYTES, READER_WORLD_ID, STOP_STATUSES, UUID_PATTERN } from '../../shared/constants.js';
import { ACCOUNT_KEYS, ORG_KEYS, pickKeys } from '../../shared/record.js';

const UUID_RE = new RegExp(UUID_PATTERN);

/**
 * The script. It returns the raw objects of C5:
 * {orgs: [{org (the six keys), usage | {__status}}] | {__status}, account: {four keys} | {__status},
 *  fetch_ms}. An HTTP error becomes {__status}; a network error or a reply that is not JSON
 * becomes {__status: 0}. So the script itself never throws once it runs: a throw from
 * executeJavaScript… means the world could not run it, and no request was made.
 */
export const READER_SCRIPT = `(async () => {
  const t0 = Date.now();
  const j = async (u) => {
    try {
      const r = await fetch(u, { credentials: 'include', headers: { accept: 'application/json' } });
      if (!r.ok) return { __status: r.status };
      return await r.json();
    } catch (e) {
      return { __status: 0 };
    }
  };
  const ORG = ${JSON.stringify(ORG_KEYS)};
  const ACCT = ${JSON.stringify(ACCOUNT_KEYS)};
  const pick = (o, keys) => { const out = {}; for (const k of keys) if (o && typeof o === 'object' && k in o) out[k] = o[k]; return out; };
  const uuidRe = /${UUID_PATTERN}/;
  const orgs = await j('/api/organizations');
  if (!Array.isArray(orgs)) return { orgs, account: null, fetch_ms: Date.now() - t0 };
  const out = [];
  for (const o of orgs) {
    const org = pick(o, ORG);
    const usage = typeof org.uuid === 'string' && uuidRe.test(org.uuid)
      ? await j('/api/organizations/' + org.uuid + '/usage')
      : { __status: 0 };
    out.push({ org, usage });
  }
  const a = await j('/api/account');
  return { orgs: out, account: a && !a.__status ? pick(a, ACCT) : a, fetch_ms: Date.now() - t0 };
})()`;

const withTimeout = (p, ms, what) =>
  new Promise((resolve, reject) => {
    const h = setTimeout(() => reject(Object.assign(new Error(`${what} timed out`), { readerTimeout: true })), ms);
    h.unref?.();
    p.then(
      (v) => {
        clearTimeout(h);
        resolve(v);
      },
      (e) => {
        clearTimeout(h);
        reject(e);
      },
    );
  });

/**
 * Run the script in the tab. One retry in the main world if the isolated world throws (the
 * world could not run the script: the script itself never throws, so no request was made and
 * the retry does not double them), logged at debug level as `reader: "main"`. A timeout is
 * not retried: the first script may still be running its requests. Fills `active_org` from
 * the `lastActiveOrg` cookie of the tab's own session. Throws when both worlds throw.
 *
 * The main world is the page's own: its result is used only if its account uuid is the tab's
 * known seat account (`expectAccount`). When the seat is not known yet (a tab's first read),
 * the check is skipped and the result is taken as the isolated world's would be: the first
 * read is what sets the seat, so there is nothing to compare with.
 * @param {object} wc the tab's webContents
 * @param {{claudeOrigin: string, log?: object, timeoutMs?: number, expectAccount?: string|null}} opts
 */
export async function readTab(wc, opts) {
  const timeoutMs = opts.timeoutMs ?? 60_000;
  let raw;
  try {
    raw = await withTimeout(wc.executeJavaScriptInIsolatedWorld(READER_WORLD_ID, [{ code: READER_SCRIPT }]), timeoutMs, 'reader');
  } catch (err) {
    if (err?.readerTimeout) throw err;
    opts.log?.debug?.('usage: isolated world failed, retrying in the main world:', err?.message ?? err);
    raw = await withTimeout(wc.executeJavaScript(READER_SCRIPT), timeoutMs, 'reader');
    opts.log?.debug?.('usage: reader: "main"');
    const expect = typeof opts.expectAccount === 'string' && opts.expectAccount !== '' ? opts.expectAccount : null;
    if (expect && raw?.account?.uuid !== expect) {
      throw new Error('the main-world read is for another account than the tab\'s seat; ignored');
    }
  }
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return raw;
  let cookies = [];
  try {
    cookies = await wc.session.cookies.get({ url: opts.claudeOrigin, name: ACTIVE_ORG_COOKIE });
  } catch {
    cookies = [];
  }
  const v = cookies[0]?.value;
  return { ...raw, active_org: typeof v === 'string' && UUID_RE.test(v) ? v : null };
}

const isStatusObj = (v) => v && typeof v === 'object' && !Array.isArray(v) && Number.isInteger(v.__status);
const isObj = (v) => v && typeof v === 'object' && !Array.isArray(v);

/**
 * Check a reader result in main before use: shapes, total size (≤ 512 KiB), uuids.
 * Returns {kind: 'ok', orgs, account, activeOrg, fetchMs}, or {kind: 'stop', status, message}
 * for a 401, 403 or 429 on any call, or {kind: 'retry', message} for anything else wrong.
 * The `orgs[].org` and `account` carry only their listed keys; `usage` is kept whole.
 */
export function checkResult(raw) {
  if (!isObj(raw)) return { kind: 'retry', message: 'no data' };
  let size;
  try {
    size = Buffer.byteLength(JSON.stringify(raw));
  } catch {
    return { kind: 'retry', message: 'bad shape' };
  }
  if (size > READER_MAX_BYTES) return { kind: 'retry', message: 'result too large' };

  const statuses = [];
  const note = (v) => {
    if (isStatusObj(v)) statuses.push(v.__status);
  };
  note(raw.orgs);
  if (Array.isArray(raw.orgs)) for (const o of raw.orgs) note(o?.usage);
  note(raw.account);
  const stop = statuses.find((s) => STOP_STATUSES.includes(s));
  if (stop !== undefined) return { kind: 'stop', status: stop, message: stopMessage(stop) };
  if (statuses.length) return { kind: 'retry', message: statuses[0] === 0 ? 'network error' : `HTTP ${statuses[0]}` };

  if (!Array.isArray(raw.orgs) || raw.orgs.length === 0) return { kind: 'retry', message: 'no organizations' };
  const orgs = [];
  for (const entry of raw.orgs) {
    if (!isObj(entry) || !isObj(entry.org) || !isObj(entry.usage)) return { kind: 'retry', message: 'bad shape' };
    if (typeof entry.org.uuid !== 'string' || !UUID_RE.test(entry.org.uuid)) return { kind: 'retry', message: 'bad org uuid' };
    orgs.push({ org: pickKeys(entry.org, ORG_KEYS), usage: entry.usage });
  }
  if (!isObj(raw.account) || typeof raw.account.uuid !== 'string' || !UUID_RE.test(raw.account.uuid)) {
    return { kind: 'retry', message: 'bad account' };
  }
  const active = typeof raw.active_org === 'string' && UUID_RE.test(raw.active_org) ? raw.active_org : null;
  const fetchMs = Number.isInteger(raw.fetch_ms) && raw.fetch_ms >= 0 ? raw.fetch_ms : null;
  return { kind: 'ok', orgs, account: pickKeys(raw.account, ACCOUNT_KEYS), activeOrg: active, fetchMs };
}

/** Text of the error record and the strip for a stop status. */
export function stopMessage(status) {
  if (status === 429) return 'rate limited';
  if (status === 401 || status === 403) return 'signed out';
  return `HTTP ${status}`;
}
