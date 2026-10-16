// Direct mode (no `sy`, or `sy` not connected to a hub): the app itself remembers the hub URL
// and its own machine id, checks a hub address with GET /health, claims a pairing code with
// POST /v1/pair/claim (the call `sy desktop token --code-stdin` makes: internal/desktop/desktop.go)
// and, while there is no sy, uploads usage records with POST /v1/ingest and the desktop token
// (the batch `sy flush` sends: internal/client/client.go Ingest). Records are completed the way
// `sy outbox append` completes them (v 1, id as a ULID, ts) and queued in direct-outbox.ndjson
// next to direct.json ({machine, url}; no secret: the token is in the token store).

import crypto from 'node:crypto';
import { FILE_MODE } from '../../shared/constants.js';
import { normalizeHubUrl } from './token.js';
import { hubUrl, isLoopbackHost } from './client.js';

const MACHINE_RE = /^[0-9A-Za-z_-]{16,64}$/;
const CROCKFORD = '0123456789ABCDEFGHJKMNPQRSTVWXYZ';
const QUEUE_MAX = 2000;
const BATCH_MAX = 200;
const FLUSH_DEBOUNCE_MS = 2000;
const FLUSH_RETRY_MS = 60_000;
const TIMEOUT_MS = 10_000;
const REPLY_MAX = 1024 * 1024;

/** A record id as `sy` makes it: a ULID (48 bits of ms, 80 random bits, Crockford base32). */
export function newRecordId(nowMs = Date.now(), rnd = crypto.randomBytes(10)) {
  let ms = BigInt(nowMs);
  let out = '';
  for (let i = 0; i < 10; i++) {
    out = CROCKFORD[Number(ms & 31n)] + out;
    ms >>= 5n;
  }
  let acc = 0;
  let bits = 0;
  for (const b of rnd) {
    acc = (acc << 8) | b;
    bits += 8;
    while (bits >= 5) {
      bits -= 5;
      out += CROCKFORD[(acc >> bits) & 31];
    }
    acc &= (1 << bits) - 1;
  }
  return out;
}

/** What `sy outbox append` fills in when a writer left it out: v 1, id, ts (UTC, milliseconds). */
export function completeRecord(r, nowMs = Date.now()) {
  return { ...r, v: r.v || 1, id: r.id || newRecordId(nowMs), ts: r.ts || new Date(nowMs).toISOString() };
}

/** Plain http is fine on this computer, a private network, Tailscale or a single-label (MagicDNS) name. */
export function isLocalNetworkHost(h) {
  const host = String(h).replace(/^\[|\]$/g, '').toLowerCase();
  if (isLoopbackHost(host)) return true;
  if (/^10(\.\d{1,3}){3}$/.test(host) || /^192\.168(\.\d{1,3}){2}$/.test(host) || /^169\.254(\.\d{1,3}){2}$/.test(host)) return true;
  const m = /^172\.(\d{1,3})(\.\d{1,3}){2}$/.exec(host);
  if (m && Number(m[1]) >= 16 && Number(m[1]) <= 31) return true;
  const t = /^100\.(\d{1,3})(\.\d{1,3}){2}$/.exec(host);
  if (t && Number(t[1]) >= 64 && Number(t[1]) <= 127) return true;
  if (/^f[cd][0-9a-f]{2}:/.test(host) || /^fe80:/.test(host)) return true;
  return host.endsWith('.ts.net') || host.endsWith('.local') || (!host.includes('.') && !host.includes(':'));
}

/**
 * Check what the person typed as a hub address.
 * @returns {{ok: true, url: string}|{ok: false, error: string}}
 */
export function checkAddress(raw) {
  let text = String(raw ?? '').trim();
  if (!text) return { ok: false, error: 'Enter the hub address, for example https://hub.example.com.' };
  if (!/^[a-z][a-z0-9+.-]*:\/\//i.test(text)) text = `https://${text}`;
  const url = normalizeHubUrl(text);
  if (!url) return { ok: false, error: 'That is not a hub address. Use the form https://hub.example.com.' };
  const u = new URL(url);
  if (u.protocol === 'http:' && !isLocalNetworkHost(u.hostname)) {
    return { ok: false, error: 'This address uses http. Use https, or an address on this computer or your local network.' };
  }
  return { ok: true, url };
}

/**
 * @param {{fs: object, path: object, file: string, fetch: typeof fetch, log?: object, clientName: string,
 *          version: string, timers?: object, getAuth: () => ({url: string, token: string}|null),
 *          onUnauthorized?: (status: number) => void, active: () => boolean}} deps
 *   fs: node:fs/promises. active(): the app is in direct mode (no sy hub), so records go here.
 */
export function createDirect(deps) {
  const { fs, path, file } = deps;
  const log = deps.log ?? console;
  const timers = deps.timers ?? { setTimeout, clearTimeout };
  const queueFile = path.join(path.dirname(file), 'direct-outbox.ndjson');
  const client = `${deps.clientName}/${deps.version}`;
  let data = { machine: null, url: null };
  let loaded = false;
  let queue = []; // complete records, oldest first
  let flushTimer = null;
  let flushing = null;

  async function writeAtomic(target, text) {
    const tmp = `${target}.tmp-${process.pid}`;
    await fs.writeFile(tmp, text, { mode: FILE_MODE });
    await fs.rename(tmp, target);
  }

  async function save() {
    try {
      await fs.mkdir(path.dirname(file), { recursive: true });
      await writeAtomic(file, JSON.stringify(data));
    } catch (err) {
      log.warn?.(`hub direct: cannot save ${path.basename(file)}: ${err?.code ?? err?.message ?? err}`);
    }
  }

  async function saveQueue() {
    try {
      if (!queue.length) await fs.rm(queueFile, { force: true });
      else await writeAtomic(queueFile, `${queue.map((r) => JSON.stringify(r)).join('\n')}\n`);
    } catch (err) {
      log.warn?.(`hub direct: cannot save the outbox: ${err?.code ?? err?.message ?? err}`);
    }
  }

  async function load() {
    if (loaded) return;
    loaded = true;
    try {
      const j = JSON.parse(await fs.readFile(file, 'utf8'));
      if (j && typeof j === 'object') {
        if (typeof j.machine === 'string' && MACHINE_RE.test(j.machine)) data.machine = j.machine;
        if (typeof j.url === 'string' && normalizeHubUrl(j.url)) data.url = normalizeHubUrl(j.url);
      }
    } catch {
      // first run, or a damaged file: a new machine id below
    }
    if (!data.machine) {
      data.machine = `desktop-${crypto.randomBytes(12).toString('base64url')}`;
      await save();
    }
    try {
      queue = (await fs.readFile(queueFile, 'utf8')).split('\n').filter(Boolean).map((l) => JSON.parse(l)).slice(-QUEUE_MAX);
    } catch {
      queue = [];
    }
  }

  async function request(method, base, pathname, { body, token } = {}) {
    const url = hubUrl(base, pathname);
    if (!url) return { ok: false, status: 0, error: 'bad hub address' };
    try {
      const res = await deps.fetch(url, {
        method,
        headers: {
          accept: 'application/json',
          ...(body === undefined ? {} : { 'content-type': 'application/json' }),
          ...(token ? { authorization: `Bearer ${token}` } : {}),
        },
        body: body === undefined ? undefined : JSON.stringify(body),
        redirect: 'manual',
        signal: AbortSignal.timeout(TIMEOUT_MS),
      });
      const text = (await res.text()).slice(0, REPLY_MAX);
      let json = null;
      try {
        json = text ? JSON.parse(text) : null;
      } catch {
        json = null;
      }
      const msg = json?.error?.message ?? json?.message ?? (typeof json?.error === 'string' ? json.error : null);
      return { ok: res.status >= 200 && res.status < 300, status: res.status, data: json, ...(typeof msg === 'string' ? { error: msg.slice(0, 200) } : {}) };
    } catch (err) {
      const timedOut = err?.name === 'TimeoutError' || err?.name === 'AbortError';
      return { ok: false, status: 0, error: timedOut ? 'the hub did not answer in time' : String(err?.message ?? err).split('\n')[0].slice(0, 200) };
    }
  }

  /** GET /health of a hub whose address was checked: {ok, version}. */
  async function health(url) {
    try {
      const res = await deps.fetch(new URL('health', `${url}/`).toString(), { redirect: 'manual', signal: AbortSignal.timeout(TIMEOUT_MS), headers: { accept: 'application/json' } });
      const j = JSON.parse((await res.text()).slice(0, REPLY_MAX));
      return { ok: res.status === 200 && j?.ok === true, version: typeof j?.version === 'string' ? j.version.slice(0, 64) : null };
    } catch {
      return { ok: false, version: null };
    }
  }

  /** A typed address: checked for form and scheme, then for a hub that answers /health. */
  async function check(raw) {
    const c = checkAddress(raw);
    if (!c.ok) return c;
    const h = await health(c.url);
    if (!h.ok) return { ok: false, error: `No hub answered at ${c.url}. Check the address and that the hub is running.` };
    return { ok: true, url: c.url, version: h.version };
  }

  /**
   * POST /v1/pair/claim like `sy desktop token --code-stdin`.
   * @returns {Promise<{ok: true, device: string, token: string}|{ok: false, error: string, status?: number}>}
   */
  async function claim({ url, code, name }) {
    await load();
    const digits = /^\d{3}-?\d{3}$/.test(code ?? '');
    const r = await request('POST', url, '/v1/pair/claim', {
      body: { ...(digits ? { code: String(code).replace('-', '') } : { secret: code }), name, machine: data.machine, expect_kind: 'desktop', client },
    });
    if (r.ok && typeof r.data?.token === 'string' && typeof r.data?.device === 'string') {
      if (r.data.kind && r.data.kind !== 'desktop') return { ok: false, error: `The code made a ${r.data.kind} device, not a desktop. Get a code from More, Devices, Add desktop app.` };
      return { ok: true, device: r.data.device, token: r.data.token };
    }
    if (r.ok) return { ok: false, error: 'The hub answered without a token.' };
    if (r.status === 429) return { ok: false, status: 429, error: 'Too many tries. Wait a minute, then try again.' };
    if (r.status === 0 || r.status >= 500) return { ok: false, status: r.status, error: 'The hub is not reachable.' };
    return { ok: false, status: r.status, error: 'That code was refused or has expired. Make a new one and try again.' };
  }

  // ---- usage records

  const dropBatch = async (batch) => {
    const gone = new Set(batch.map((x) => x.id));
    queue = queue.filter((x) => !gone.has(x.id));
    await saveQueue();
  };

  async function flushOnce() {
    const auth = deps.getAuth();
    if (!deps.active() || !auth || !queue.length) return;
    const batch = queue.slice(0, BATCH_MAX);
    const r = await request('POST', auth.url, '/v1/ingest', { body: { v: 1, client, records: batch }, token: auth.token });
    if (r.status === 401) {
      deps.onUnauthorized?.(401);
      return;
    }
    if (r.ok) {
      await dropBatch(batch);
      if (queue.length) schedule(FLUSH_DEBOUNCE_MS);
      return;
    }
    if (r.status >= 400 && r.status < 500 && r.status !== 429 && r.status !== 403) {
      // a batch the hub refuses for good must not block every later record
      log.warn?.(`hub direct: ingest refused the batch (${r.status}); dropping ${batch.length} records`);
      await dropBatch(batch);
      return;
    }
    schedule(FLUSH_RETRY_MS);
  }

  function schedule(ms) {
    if (flushTimer || !loaded) return;
    flushTimer = timers.setTimeout(() => {
      flushTimer = null;
      flushNow().catch(() => {});
    }, ms);
    flushTimer?.unref?.();
  }

  async function flushNow() {
    if (flushTimer) {
      timers.clearTimeout(flushTimer);
      flushTimer = null;
    }
    if (flushing) return flushing;
    flushing = flushOnce().catch((err) => log.warn?.(`hub direct: flush failed: ${err?.message ?? err}`)).finally(() => {
      flushing = null;
    });
    return flushing;
  }

  return {
    load,
    url: () => data.url,
    async setUrl(url) {
      await load();
      data = { ...data, url: url ? normalizeHubUrl(url) : null };
      await save();
    },
    check,
    health,
    claim,
    async machineId() {
      await load();
      return data.machine;
    },
    /** Same shape as sy.append: {ok, ids}. */
    async append(records) {
      await load();
      const done = records.map((r) => completeRecord(r));
      queue.push(...done);
      if (queue.length > QUEUE_MAX) queue = queue.slice(-QUEUE_MAX);
      await saveQueue();
      return { ok: true, ids: done.map((r) => r.id) };
    },
    flushSoon: () => schedule(FLUSH_DEBOUNCE_MS),
    flushNow,
    queued: () => queue.length,
  };
}
