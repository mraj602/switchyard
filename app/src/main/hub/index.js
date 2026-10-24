// Hub connection and view (C6, C7): the desktop token (safeStorage, bound to the hub URL),
// the client, the stream, overview and needs-you count, the hub WebContentsView in
// persist:sy-hub with the cookie login, owner sign-in, hub:route, the claude.ai handoff,
// seat:update, the certificate pin of an https hub (pin.js), the update notice's release link,
// and the hub:* and accounts:* handlers.
//
// Contract: docs/spec/desktop.md C6, C7. The bus carries the results:
//   hub        HubState + {storage_backend, error}  after info, pairing, stream or overview changes
//   needs_you  {count, sessions}                    when the needs-you picture changes
//   alert      {alert, session}                     for a stream `alert` with change "fired"
// Note: `window` is read from deps at call time (boot passes a getter: the window is created
// after the hub), so it is never destructured here.

import { HUB_ROUTE_PATTERN } from '../../shared/ipc.js';
import {
  CLAUDE_SESSION_LINK_PATTERN, NEEDS_YOU_DEBOUNCE_MS, TRAY_SESSIONS_MAX,
} from '../../shared/constants.js';
import { createTokenStore, normalizeHubUrl } from './token.js';
import { createClient, hubUrlAllowed } from './client.js';
import { createStream } from './stream.js';
import { createHubView } from './view.js';
import { createDirect, checkAddress } from './direct.js';
import { createPinnedFetch, installVerifyProc, probe as probeCertificate, pinOrigin, normalizeFingerprint } from './pin.js';
import { CLIENT_NAME, HUB_PARTITION } from '../../shared/constants.js';
import { updateNotice, releaseUrl } from '../../shared/version.js';
import { isLoopbackHost } from './client.js';

const ROUTE_RE = new RegExp(HUB_ROUTE_PATTERN);
const SESSION_LINK_RE = new RegExp(CLAUDE_SESSION_LINK_PATTERN);
const ID_RE = /^[A-Za-z0-9_-]{1,64}$/;
const INFO_POLL_MS = 300_000;
const INFO_POLL_BAD_MS = 30_000;
/** The hub error shown for a 403 on a desktop-token request (only a 401 means the token is dead). */
export const FORBIDDEN_ERROR = 'The hub refused a request of this app (403 forbidden).';

/** How often a certificate failure may trigger another probe of the hub. */
const CERT_RECHECK_MS = 10_000;
const UNTRUSTED_TEXT = "This hub's certificate is not signed by an authority this computer trusts (for example a self-signed certificate). Compare its fingerprint with the one on the hub's computer before you trust it.";
const CHANGED_TEXT = "This hub's certificate changed since you trusted it. If you did not replace it, someone may be intercepting the connection. Compare the new fingerprint with the one on the hub's computer before you trust it.";

/** Plain http to a host that is not this computer: the token crosses the network unencrypted. */
export function insecureHubUrl(url) {
  try {
    const u = new URL(url);
    return u.protocol === 'http:' && !isLoopbackHost(u.hostname);
  } catch {
    return false;
  }
}

/** What the shell shows about a certificate (no secrets: a public certificate's facts). */
function certInfo(url, p, pinned) {
  return {
    url, origin: pinOrigin(url), fingerprint: p.fingerprint, pinned: pinned ?? null, changed: !!pinned, trusted: p.trusted === true,
    reason: p.reason ?? null, subject: p.subject ?? null, issuer: p.issuer ?? null, valid_to: p.valid_to ?? null,
  };
}

/** The messages the shell shows for a failed `sy desktop token`, by exit code (C4). */
export function pairError(r, hadCode) {
  switch (r.exit) {
    case 2:
    case 3:
      return 'That code was refused or has expired. Make a new one and try again.';
    case 4:
      return hadCode
        ? 'Pairing is not possible here. Run sy setup first.'
        : 'Pairing without a code needs the hub on this computer or a working admin channel (machines.hub_admin). Type a code from the board (More, Devices, Add desktop app) or from sy hub pair --kind desktop.';
    case 5:
      return 'The hub is not reachable.';
    default:
      // 1: the admin channel exists but its command failed; sy's message says why.
      return r.error ? `Pairing failed: ${r.error}` : 'Pairing failed.';
  }
}

const sameLink = (a, b) => {
  const n = (u) => String(u ?? '').split(/[?#]/)[0].replace(/\/+$/, '');
  return n(a) !== '' && n(a) === n(b);
};

/**
 * @param {{electron: object, sy: object, security: object, window: object, bus: object, ipc: object,
 *          fs: object, path: object, tokenFile: string, fetch: typeof fetch, log: object,
 *          openSessionLink: (url: string, seat: object|null) => boolean, timers?: object,
 *          platform?: string, streamOptions?: object}} deps
 *   openSessionLink: tabs.openLink, late-bound by index.js (tabs start after hub)
 * @returns {{
 *   start(): Promise<void>,           // sy desktop info, token, stream
 *   route(route: string): Promise<void>,  // load <hub>/<route> in the hub view (hub:route, notifications, tray)
 *   view(): object|null,              // the hub WebContentsView once created
 *   ensureView(): Promise<object|null>, // create and load the hub view; window.select('hub') must call it
 *   alert(id: string): Promise<object|null>,     // GET /v1/alerts/{id}
 *   session(id: string): Promise<object|null>,   // GET /v1/sessions/{id}
 *   seats(): object[],                // the newest /v1/accounts seats (for tab labels and the strip)
 *   stop(): Promise<void>,            // stop the stream
 * }}
 */
export function create(deps) {
  const log = deps.log ?? console;
  const { bus, sy } = deps;
  const timers = deps.timers ?? { setTimeout, clearTimeout };
  const tokens = createTokenStore({
    safeStorage: deps.electron.safeStorage, fs: deps.fs, path: deps.path, file: deps.tokenFile, log, platform: deps.platform,
  });

  const state = {
    url: null, connected: false, reachable: false, paired: false, owner: false, version: null, local: false,
    service: null, admin: null, needs_you: 0, status: 'no_sy', storage_backend: tokens.backend(), error: null,
    removed: false, board_blocked: false, direct: false, owner_until: null, last_seen: null, machine: '',
    cert: null, insecure: false, pinned: false, sy_version: null,
  };
  let auth = null; // {url, device, token}: in memory, never in the state
  // Bumped at every change of auth (pairing, drop, 401): a token load that started before a
  // change must not overwrite it (an info poll racing a fresh pairing).
  let authGen = 0;
  const setAuth = (a) => {
    auth = a;
    authGen++;
  };
  let seats = [];
  let reportedBySy = false;
  let started = false;
  let stopped = false; // stop(): shutting down; no info refresh, stream or overview starts again
  let infoTimer = null;
  let refreshTimer = null;
  let inflight = null;
  let refetchAfter = false; // a refresh asked for while one runs: that one may have read older data
  let lastNeedsYou = '';
  let machineName = String(deps.hostname ?? '');

  const emitHub = () => bus.emit('hub', { ...state });
  const set = (patch) => {
    // When the hub is reached, or lost, it was last seen now.
    if (patch.reachable !== undefined && patch.reachable !== state.reachable) patch = { ...patch, last_seen: new Date().toISOString() };
    // Derived from the URL: plain http off this computer, or a pinned https certificate.
    const url = patch.url !== undefined ? patch.url : state.url;
    patch = { ...patch, insecure: insecureHubUrl(url), pinned: !!(pinOrigin(url) && direct.pinFor(pinOrigin(url))) };
    if (patch.cert !== undefined && JSON.stringify(patch.cert) === JSON.stringify(state.cert)) delete patch.cert;
    let changed = false;
    for (const [k, v] of Object.entries(patch)) {
      if (state[k] !== v) {
        state[k] = v;
        changed = true;
      }
    }
    if (changed) emitHub();
  };

  // The app dials the hub itself (direct) when there is no sy or sy has no hub: the address was
  // checked by direct.check (https, or a local network), so plain http is allowed like for sy.
  const allowHttp = () => reportedBySy || state.direct;
  // Every request of the app's own to the hub: a pinned https origin accepts exactly its pinned
  // certificate (pin.js), any other URL is the normal fetch with normal validation.
  const probe = deps.probeCertificate ?? probeCertificate;
  const hubFetch = createPinnedFetch({
    fetch: deps.fetch, getPin: (origin) => direct.pinFor(origin), onCertError: (origin) => certTrouble(origin), https: deps.https, tls: deps.tls,
  });
  const client = createClient({ fetch: hubFetch, getAuth: () => auth, reportedBySy: allowHttp });
  const direct = createDirect({
    fs: deps.fs, path: deps.path, file: deps.directFile ?? deps.path.join(deps.path.dirname(deps.tokenFile), 'direct.json'),
    fetch: hubFetch, log, clientName: CLIENT_NAME, version: deps.clientVersion ?? '0.0.0', timers,
    getAuth: () => auth, active: () => state.direct, onUnauthorized: (status) => handleUnauthorized(status),
  });

  const hubView = createHubView({
    electron: deps.electron, security: deps.security,
    get window() {
      return deps.window;
    },
    log,
    base: () => state.url,
    auth: () => auth,
    onSessionLink: (url) => handoff(url),
    onOwner: (owner, until) => set({ owner, owner_until: owner ? until ?? null : null }),
    onLoginRefused: (status) => handleUnauthorized(status),
    onBlocked: () => set({ board_blocked: true }),
    userAgentSuffix: deps.clientVersion ? `SwitchyardDesktop/${deps.clientVersion}` : 'SwitchyardDesktop',
  });
  // The hub view's partition takes exactly the pinned certificate of a pinned hub host (C6).
  // Only this partition: claude.ai tabs and the shell keep Chromium's own validation.
  try {
    installVerifyProc(deps.electron.session.fromPartition(HUB_PARTITION), {
      pins: () => direct.pins(), onMismatch: (origin) => certTrouble(origin), log,
    });
  } catch (err) {
    log.warn?.(`hub: cannot install the certificate check of the hub view: ${err?.message ?? err}`);
  }

  // ---- the hub's certificate (C6): untrusted or changed

  let certChecking = null;
  let lastCertCheck = 0;

  /** A certificate failure on a request: probe the hub (throttled) to tell the person what it presents. */
  function certTrouble(origin) {
    if (!state.url || pinOrigin(state.url) !== origin) return;
    if (certChecking || Date.now() - lastCertCheck < CERT_RECHECK_MS) return;
    checkCert().catch((err) => log.warn?.(`hub: certificate check failed: ${err?.message ?? err}`));
  }

  function checkCert() {
    if (certChecking) return certChecking;
    certChecking = (async () => {
      const url = state.url;
      const origin = pinOrigin(url);
      if (!origin) return setCert(null);
      const p = await probe(url);
      lastCertCheck = Date.now();
      if (url !== state.url || !p.ok) return undefined; // another hub now, or no handshake: nothing to say about a certificate
      const pinned = direct.pinFor(origin);
      if (pinned ? p.fingerprint === pinned : p.trusted) return setCert(null);
      log.warn?.(`hub: the certificate of ${origin} is ${pinned ? 'not the pinned one' : 'not trusted'} (${p.fingerprint})`);
      return setCert(certInfo(url, p, pinned));
    })().finally(() => {
      certChecking = null;
    });
    return certChecking;
  }

  /** Set or clear the certificate problem; while there is one, the stream stops and the board is hidden. */
  function setCert(cert) {
    const had = !!state.cert;
    set({ cert });
    if (cert) {
      stopStream();
      hubView.suppress(true);
    } else if (had && auth) hubView.suppress(false);
    set({ status: statusFor() });
  }

  // ---- status

  let mode = 'no_sy'; // 'no_sy' | 'dial' | null: what sy reported before any hub logic applies
  let unreachableStreak = 0;

  /**
   * A hub that was reachable must fail two info checks in a row before the app says Unreachable
   * (the hub is often a tiny VM that answers slowly now and then); the board stays meanwhile and
   * the next check comes sooner. A hub never seen reachable is reported at once.
   */
  function settleReachable(reachable, version) {
    if (reachable) {
      unreachableStreak = 0;
      return { reachable: true, version };
    }
    unreachableStreak++;
    if (state.reachable && unreachableStreak < 2) return { reachable: true, version: state.version };
    return { reachable: false, version };
  }

  function statusFor() {
    if (mode) return mode;
    if (!state.connected || !state.url) return 'not_connected';
    if (state.cert) return 'unreachable'; // the hub cannot be talked to until its certificate is trusted
    if (!auth) return 'needs_pairing';
    if (!state.reachable) return 'unreachable';
    return 'ok';
  }

  // ---- info

  async function refreshInfo() {
    if (stopped) return;
    const info = await sy.info();
    if (stopped) return;
    await direct.load();
    // sy (when there is one) wins; without sy, or with a sy that has no hub, the app uses the
    // hub address it remembers itself.
    const syHasHub = info.ok && typeof info.hub?.url === 'string' && info.hub.url !== '' && !!info.hub.connected;
    if (!syHasHub && direct.url()) return refreshDirect(info);
    state.direct = false;
    if (!info.ok) {
      mode = info.code === 'no_sy' ? 'no_sy' : null;
      dropHub();
      set({
        url: null, connected: false, reachable: false, paired: false, local: false, service: null, admin: null, version: null,
        error: mode === 'no_sy' ? 'sy was not found' : info.error ?? 'sy desktop info failed',
      });
      set({ status: statusFor() });
      return;
    }
    const hub = info.hub ?? {};
    reportedBySy = true;
    state.sy_version = typeof info.sy === 'string' ? info.sy.slice(0, 64) : null; // rename:keep: a wire key older releases send too (rename.md 2.12)
    machineName = String(info.machine?.name ?? '');
    state.machine = machineName;
    const rawUrl = typeof hub.url === 'string' ? hub.url : null;
    const url = rawUrl && hubUrlAllowed(rawUrl, true) ? normalizeHubUrl(rawUrl) : null;
    if (url !== state.url) dropHub(); // another hub: the token, the view and the stream are no longer valid
    if (state.cert && state.cert.url !== url) state.cert = null;
    const admin = typeof info.admin === 'string' ? info.admin : null;
    if (hub.dial) {
      mode = 'dial';
      stopStream();
      set({
        url, connected: !!hub.connected, reachable: false, paired: false, local: false, service: null, version: null, admin,
        error: 'forward mode is not supported in the desktop app',
      });
      set({ status: 'dial' });
      return;
    }
    mode = null;
    if (url && !auth) {
      const gen = authGen;
      const loaded = await tokens.load(url);
      if (stopped) return;
      if (gen === authGen) setAuth(loaded); // else a pairing (or a drop) came first: keep it
    }
    set({
      url, connected: !!hub.connected && !!url, ...settleReachable(!!hub.reachable, hub.version ?? null), paired: !!auth,
      local: !!hub.local, service: typeof hub.service === 'string' ? hub.service : null, admin, error: null,
    });
    set({ status: statusFor() });
    if (state.board_blocked && auth) retryBoard();
    if (auth && state.connected) {
      startStream();
      if (state.reachable) refreshOverview();
    } else stopStream();
  }

  /** A blocked board gets another try at each info refresh (a hub that was down, a rate limit that has passed). */
  function retryBoard() {
    set({ board_blocked: false });
    hubView.suppress(false);
    if (hubView.view()) hubView.load(hubView.currentRoute()).catch(() => {});
  }

  async function refreshDirect(info) {
    const url = direct.url();
    mode = null;
    state.direct = true;
    if (info?.ok) machineName = String(info.machine?.name ?? machineName);
    state.machine = machineName;
    state.sy_version = info?.ok && typeof info.sy === 'string' ? info.sy.slice(0, 64) : null; // rename:keep
    if (url !== state.url) dropHub();
    if (state.cert && state.cert.url !== url) state.cert = null;
    if (!auth) {
      const gen = authGen;
      const loaded = await tokens.load(url);
      if (stopped) return;
      if (gen === authGen) setAuth(loaded);
    }
    const h = await direct.health(url);
    if (stopped) return;
    if (!h.ok && pinOrigin(url)) await checkCert(); // an https hub that does not answer: its certificate may be why
    else if (h.ok && state.cert) setCert(null);
    if (stopped) return;
    set({
      url, connected: true, ...settleReachable(h.ok, h.version), paired: !!auth, local: false, service: null, admin: null,
      direct: true, error: null,
    });
    set({ status: statusFor() });
    if (state.board_blocked && auth) retryBoard();
    if (auth && state.reachable) {
      startStream();
      refreshOverview();
      direct.flushSoon();
    } else if (!auth) stopStream();
  }

  function dropHub() {
    setAuth(null);
    seats = [];
    stopStream();
    hubView.reset();
    state.needs_you = 0;
    state.removed = false;
    state.board_blocked = false;
    lastNeedsYou = '';
    unreachableStreak = 0;
  }

  function schedulePoll() {
    if (infoTimer) timers.clearTimeout(infoTimer);
    infoTimer = timers.setTimeout(async () => {
      infoTimer = null;
      try {
        await refreshInfo();
      } catch (err) {
        log.warn?.(`hub: info refresh failed: ${err?.message ?? err}`);
      }
      if (started) schedulePoll();
    }, state.status === 'ok' && unreachableStreak === 0 ? INFO_POLL_MS : INFO_POLL_BAD_MS);
    infoTimer?.unref?.();
  }

  // ---- 401: the token is no good. A 403 is an error to show (forbidden), not a dead token.

  function handleForbidden(what) {
    log.warn?.(`hub: ${what} answered 403 (forbidden)`);
    set({ error: FORBIDDEN_ERROR });
  }

  async function handleUnauthorized(status) {
    if (!auth) return;
    log.warn?.(`hub: token refused (${status}), pairing is needed again`);
    setAuth(null);
    stopStream();
    hubView.suppress(true); // the board would show its own login page: the shell's state panel shows instead
    await tokens.clear();
    await hubView.signOut();
    set({ paired: false, status: 'needs_pairing', owner: false, removed: true });
  }

  // ---- stream and overview

  const stream = createStream({
    fetch: hubFetch,
    getAuth: () => auth,
    reportedBySy: allowHttp,
    timers,
    log,
    ...(deps.streamOptions ?? {}),
    onEvent: (type, data) => {
      if (type === 'session' || type === 'usage' || type === 'seats') scheduleRefresh();
      else if (type === 'alert') onAlert(data);
    },
    onReset: () => {
      refreshOverview();
    },
    onUnauthorized: (status) => {
      handleUnauthorized(status);
    },
    onForbidden: () => handleForbidden('the stream'),
    onConnection: (up) => {
      if (!auth) return;
      if (up) {
        const was = state.status;
        set({ reachable: true });
        set({ status: statusFor() });
        if (was !== 'ok') refreshOverview();
      } else {
        set({ reachable: false });
        set({ status: statusFor() });
      }
    },
  });

  const startStream = () => {
    if (!stopped) stream.start();
  };
  const stopStream = () => stream.stop();

  function scheduleRefresh() {
    if (refreshTimer) return;
    refreshTimer = timers.setTimeout(() => {
      refreshTimer = null;
      refreshOverview();
    }, NEEDS_YOU_DEBOUNCE_MS);
    refreshTimer?.unref?.();
  }

  function sessionTitle(s) {
    const who = s.seat_label ?? s.machine_name ?? 'unattributed';
    const what = s.project ?? s.name ?? s.title ?? 'session';
    return `${who} · ${what} · ${s.detail ?? s.state ?? ''}`.replace(/ · $/, '');
  }

  function refreshOverview() {
    if (stopped) return Promise.resolve();
    if (inflight) {
      refetchAfter = true;
      return inflight;
    }
    inflight = (async () => {
      try {
        const [ov, acc] = await Promise.all([client.get('/v1/overview'), client.get('/v1/accounts')]);
        for (const r of [ov, acc]) {
          if (r.status === 401) return handleUnauthorized(r.status);
        }
        if (ov.status === 403) return handleForbidden('/v1/overview');
        if (acc.status === 403) handleForbidden('/v1/accounts');
        else if (ov.ok && state.error === FORBIDDEN_ERROR) set({ error: null });
        if (acc.ok && Array.isArray(acc.data?.accounts)) seats = acc.data.accounts;
        else if (ov.ok && Array.isArray(ov.data?.seats)) seats = ov.data.seats;
        if (!ov.ok) return;
        const o = ov.data ?? {};
        const count = Number.isInteger(o.counts?.needs_you) ? o.counts.needs_you : (o.needs_you ?? []).length;
        set({ needs_you: count, reachable: true });
        const list = (Array.isArray(o.needs_you) ? o.needs_you : [])
          .filter((s) => s && typeof s.id === 'string' && ID_RE.test(s.id))
          .slice(0, TRAY_SESSIONS_MAX)
          .map((s) => ({ id: s.id, route: `#/sessions/${s.id}`, title: sessionTitle(s) }));
        const payload = { count, sessions: list };
        const key = JSON.stringify(payload);
        if (key !== lastNeedsYou) {
          lastNeedsYou = key;
          bus.emit('needs_you', payload);
        }
        emitHub(); // the seats changed too: listeners read hub.seats()
        set({ status: statusFor() });
      } catch (err) {
        log.warn?.(`hub: overview failed: ${err?.message ?? err}`);
      } finally {
        inflight = null;
        if (refetchAfter) {
          refetchAfter = false;
          refreshOverview();
        }
      }
    })();
    return inflight;
  }

  async function getJson(path) {
    const r = await client.get(path);
    if (r.status === 401) handleUnauthorized(r.status);
    else if (r.status === 403) handleForbidden(path.split('?')[0]);
    return r.ok ? r.data : null;
  }
  const fetchAlert = (id) => (ID_RE.test(String(id)) ? getJson(`/v1/alerts/${encodeURIComponent(id)}`) : Promise.resolve(null));
  const fetchSession = (id) => (ID_RE.test(String(id)) ? getJson(`/v1/sessions/${encodeURIComponent(id)}`) : Promise.resolve(null));

  async function onAlert(data) {
    if (data?.change !== 'fired' || typeof data.alert?.id !== 'string') return;
    try {
      const alert = await fetchAlert(data.alert.id);
      if (!alert) return;
      const sid = alert.session ?? data.session ?? null;
      const session = sid ? await fetchSession(sid) : null;
      bus.emit('alert', { alert, session });
    } catch (err) {
      log.warn?.(`hub: alert ${data.alert.id}: ${err?.message ?? err}`);
    }
  }

  // ---- the claude.ai handoff (C7)

  async function seatForLink(url) {
    let seatId = null;
    const m = /^#\/sessions\/([^/?#]+)/.exec(hubView.currentRoute());
    if (m) {
      const s = await fetchSession(decodeURIComponent(m[1]));
      if (s && (!s.remote_url || sameLink(s.remote_url, url))) seatId = s.seat ?? null;
    }
    if (!seatId && auth) {
      const r = await client.get('/v1/sessions', { include_ended: true, limit: 200 });
      const hit = r.ok ? (r.data?.sessions ?? []).find((s) => sameLink(s.remote_url, url)) : null;
      seatId = hit?.seat ?? null;
    }
    const seat = seatId ? seats.find((x) => x.id === seatId) : null;
    return seat ? { id: seat.id, account: seat.account, org: seat.org } : null;
  }

  async function handoff(url) {
    if (typeof url !== 'string' || !SESSION_LINK_RE.test(url)) return;
    let seat = null;
    try {
      seat = await seatForLink(url);
    } catch (err) {
      log.warn?.(`hub: handoff lookup failed: ${err?.message ?? err}`);
    }
    let opened = false;
    try {
      opened = (await deps.openSessionLink(url, seat)) === true;
    } catch (err) {
      log.warn?.(`hub: handoff failed: ${err?.message ?? err}`);
    }
    if (!opened) await deps.security.openExternalSafe(url, { activate: false });
  }

  // ---- handlers

  const ready = () => state.connected && state.url && state.status !== 'dial' && state.status !== 'no_sy';

  async function pair(args = {}) {
    await refreshInfo();
    const code = typeof args.code === 'string' && args.code !== '' ? args.code : undefined;
    if (state.direct) {
      if (state.status === 'unreachable') return { ok: false, error: 'The hub is not reachable.' };
      if (code === undefined) return { ok: false, error: 'Type the pairing code. Get one on an owner device: More, Devices, Add desktop app.' };
      const r = await direct.claim({ url: state.url, code, name: tokenName() });
      if (!r.ok) return { ok: false, error: r.error };
      return finishPair(r.device, r.token);
    }
    if (state.status === 'no_sy') return { ok: false, error: 'sy was not found. Install it, then run sy setup.' };
    if (state.status === 'dial') return { ok: false, error: state.error };
    if (!ready()) return { ok: false, error: 'sy is not connected to a hub. Run sy setup.' };
    const r = await sy.token({ code, name: tokenName() });
    if (!r.ok) return { ok: false, error: pairError(r, code !== undefined) };
    if (typeof r.token !== 'string' || !r.token || typeof r.device !== 'string') return { ok: false, error: 'The hub answered without a token.' };
    const device = r.device;
    const token = r.token;
    r.token = '';
    return finishPair(device, token);
  }

  // No spaces (TOKEN_NAME_RE): through an ssh admin channel the name is a remote word.
  function tokenName() {
    const safeName = machineName.replace(/[^A-Za-z0-9._-]/g, '').slice(0, 20);
    return safeName ? `Switchyard-desktop-${safeName}` : 'Switchyard-desktop';
  }

  async function finishPair(device, token) {
    setAuth({ url: state.url, device, token });
    let stored = true;
    try {
      stored = await tokens.save(auth);
    } catch (err) {
      stored = false;
      log.warn?.(`hub: cannot store the token: ${err?.message ?? err}`);
    }
    set({ paired: true, removed: false, board_blocked: false, storage_backend: tokens.backend(), status: statusFor() });
    await hubView.signOut(); // a new device, a new board session
    hubView.suppress(false);
    startStream();
    refreshOverview();
    if (hubView.view()) hubView.load(hubView.currentRoute());
    return stored ? { ok: true } : { ok: true, stored: false, warning: 'No secure storage is available, so the token is kept for this run only.' };
  }

  /** A pasted owner link: only a link to the paired hub's own address is opened. */
  async function ownerFromLink(text) {
    if (!state.url || !auth) return { ok: false, error: 'Pair this computer first.' };
    let u;
    try {
      u = new URL(text);
    } catch {
      return { ok: false, error: 'That is not a link. Paste the whole owner link.' };
    }
    if (u.origin !== new URL(`${state.url}/`).origin) return { ok: false, error: `That link is for another address. Only a link to ${state.url} is accepted.` };
    if (!(await hubView.loadOwnerLink(u.toString()))) return { ok: false, error: `That link is for another address. Only a link to ${state.url} is accepted.` };
    deps.window.select('hub');
    return { ok: true };
  }

  async function ownerSignIn(args = {}) {
    if (typeof args?.link === 'string' && args.link.trim() !== '') return ownerFromLink(args.link.trim());
    if (!state.url) await refreshInfo();
    if (state.direct) return { ok: false, error: 'Paste an owner link: on an owner device open More, Devices, Make another device owner.' };
    if (!ready()) return { ok: false, error: 'sy is not connected to a hub. Run sy setup.' };
    const r = await sy.ownerLink();
    if (!r.ok) {
      return {
        ok: false,
        error: r.exit === 4
          ? 'Owner sign-in needs the hub on this computer or an admin channel (machines.hub_admin). Run sy setup.'
          : r.exit === 5 ? 'The hub is not reachable.' : r.error ? `Owner sign-in failed: ${r.error}` : 'Owner sign-in failed.',
      };
    }
    if (typeof r.url !== 'string' || !(await hubView.loadOwnerLink(r.url))) return { ok: false, error: 'The hub gave a link for another address; it was not opened.' };
    deps.window.select('hub');
    return { ok: true };
  }

  async function ownerSignOut() {
    if (!state.owner) return { ok: true };
    await hubView.ownerSignOut();
    return { ok: true };
  }

  /** Check a hub address, remember it (no sy needed) and pair with a code when one is given. */
  async function connect({ url, code, insecureLan } = {}) {
    await refreshInfo();
    if (state.connected && !state.direct) return { ok: false, error: `sy on this computer is already connected to ${state.url}. Use Pair this computer.` };
    // An https address whose certificate fails normal validation (or is not its pinned one)
    // stops here: the shell shows the fingerprint and asks the person to trust it (hub:trust-cert).
    const opts = { insecureLan: insecureLan === true };
    const a = checkAddress(url, opts);
    const origin = a.ok ? pinOrigin(a.url) : null;
    if (origin) {
      const p = await probe(a.url);
      const pinned = direct.pinFor(origin);
      if (p.ok && !(pinned ? p.fingerprint === pinned : p.trusted)) {
        return { ok: false, error: pinned ? CHANGED_TEXT : UNTRUSTED_TEXT, cert: certInfo(a.url, p, pinned) };
      }
    }
    const c = await direct.check(url, opts);
    if (!c.ok) return { ok: false, error: c.error };
    await direct.setUrl(c.url, opts);
    await refreshInfo();
    if (typeof code === 'string' && code !== '') return pair({ code });
    return { ok: true };
  }

  /**
   * Trust the certificate an https hub presents now, after the person compared its fingerprint
   * (C6). The hub is probed again and must still present exactly that certificate. A
   * certificate that passes normal validation needs no pin, so its pin is removed instead.
   */
  async function trustCert({ url, fingerprint } = {}) {
    const a = checkAddress(url);
    if (!a.ok) return { ok: false, error: a.error };
    const origin = pinOrigin(a.url);
    if (!origin) return { ok: false, error: 'Only an https address has a certificate to trust.' };
    const want = normalizeFingerprint(fingerprint);
    if (!want) return { ok: false, error: 'That is not a SHA-256 fingerprint.' };
    const p = await probe(a.url);
    if (!p.ok) return { ok: false, error: `No hub answered at ${a.url}.` };
    if (p.fingerprint !== want) {
      return { ok: false, error: 'The hub now presents a different certificate than the one you compared. Compare the new fingerprint.', cert: certInfo(a.url, p, direct.pinFor(origin)) };
    }
    await direct.setPin(origin, p.trusted ? null : want);
    log.info?.(p.trusted ? `hub: the certificate of ${origin} passes normal validation, no pin kept` : `hub: pinned the certificate of ${origin} (${want})`);
    lastCertCheck = 0;
    if (state.cert?.origin === origin) setCert(null);
    else set({});
    await refreshInfo().catch(() => {});
    if (auth && hubView.view()) hubView.load(hubView.currentRoute()).catch(() => {});
    return { ok: true, pinned: !p.trusted };
  }

  /** Open the release page of the update notice (or the release list) in the system browser. */
  async function openRelease() {
    const n = updateNotice({ app: deps.clientVersion, hub: state.version, sy: state.sy_version });
    const ok = await deps.security.openExternalSafe(n?.kind === 'update' ? n.url : releaseUrl(null));
    return { ok };
  }

  /** Revoke this app's own device on the hub (DELETE /v1/devices/self), forget the token and the board session. */
  async function unpair({ forget } = {}) {
    let revoked = false;
    let warning;
    if (auth) {
      const r = await client.request('DELETE', '/v1/devices/self');
      revoked = r.ok || r.status === 401 || r.status === 404; // 401: it was already removed there
      if (!revoked) warning = 'The hub could not be reached, so this computer may still be listed in Devices there. Remove it from that list.';
    }
    setAuth(null);
    stopStream();
    hubView.suppress(true);
    await tokens.clear();
    await hubView.signOut();
    if (forget) {
      const origin = pinOrigin(state.url);
      if (origin && direct.pinFor(origin)) await direct.setPin(origin, null); // forgetting a hub forgets its certificate
      await direct.setUrl(null);
      mode = null;
    }
    set({ paired: false, owner: false, removed: false, board_blocked: false, needs_you: 0 });
    await refreshInfo();
    return warning ? { ok: true, revoked, warning } : { ok: true, revoked };
  }

  async function route(route) {
    if (typeof route !== 'string' || route.length > 200 || !ROUTE_RE.test(route)) {
      log.warn?.('hub: refused a bad route');
      return;
    }
    if (!state.url) return;
    await hubView.load(route);
    deps.window.select('hub');
  }

  async function hubService({ action }) {
    const r = await sy.service(action);
    if (!r.ok) return { ok: false, error: r.error, service: null, health: false };
    set({ service: r.service ?? null });
    if (action !== 'status') refreshInfo().catch(() => {});
    return { ok: true, service: r.service, health: r.health === true };
  }

  async function updateSeat({ seat, label, colour }) {
    const body = {};
    if (label !== undefined) body.label = label;
    if (colour !== undefined) body.colour = colour;
    if (Object.keys(body).length === 0) return { ok: false, error: 'Nothing to change.' };
    if (!state.url) return { ok: false, error: 'Not connected to a hub.' };
    // A login only when the partition has no cookie: each login is a new board session.
    if (auth && !state.owner && !(await hubView.hasCookie())) await hubView.sessionLogin().catch(() => {});
    const r = await hubView.sessionFetch('PATCH', `/v1/accounts/${encodeURIComponent(seat)}`, body);
    if (r.ok) {
      refreshOverview();
      return { ok: true };
    }
    if (r.status === 401 || r.status === 403) return { ok: false, error: 'Sign in as owner first (Settings, Hub).' };
    return { ok: false, error: r.error || 'The hub refused the change.' };
  }

  async function localAccounts() {
    return sy.accountsList();
  }

  async function shareAccount({ folder, share }) {
    const r = await sy.share(folder, share);
    return r.ok ? { ok: true } : { ok: false, error: r.error };
  }

  const { ipc } = deps;
  ipc.register('hub:route', ({ route: r }) => route(r));
  ipc.register('hub:pair', (args) => pair(args ?? {}));
  ipc.register('hub:owner-sign-in', (args) => ownerSignIn(args ?? {}));
  ipc.register('hub:owner-sign-out', () => ownerSignOut());
  ipc.register('hub:connect', (args) => connect(args ?? {}));
  ipc.register('hub:unpair', (args) => unpair(args ?? {}));
  ipc.register('hub:trust-cert', (args) => trustCert(args ?? {}));
  ipc.register('app:open-release', () => openRelease());
  ipc.register('hub:retry', async () => {
    await refreshInfo();
    return { ok: true };
  });
  ipc.register('hub:service', (args) => hubService(args));
  ipc.register('seat:update', (args) => updateSeat(args));
  ipc.register('accounts:local', () => localAccounts());
  ipc.register('accounts:share', (args) => shareAccount(args));

  return {
    async start() {
      if (started) return;
      started = true;
      stopped = false;
      try {
        await refreshInfo();
      } catch (err) {
        log.warn?.(`hub: start: ${err?.message ?? err}`);
        set({ status: 'not_connected', error: 'sy desktop info failed' });
      }
      schedulePoll();
    },
    route,
    view: () => hubView.view(),
    async ensureView() {
      if (!state.url) return null;
      const fresh = !hubView.view();
      if (fresh) await hubView.load('');
      return hubView.view();
    },
    alert: fetchAlert,
    session: fetchSession,
    seats: () => seats.slice(),
    direct,
    state: () => ({ ...state }),
    async stop() {
      started = false;
      stopped = true;
      if (infoTimer) timers.clearTimeout(infoTimer);
      if (refreshTimer) timers.clearTimeout(refreshTimer);
      infoTimer = refreshTimer = null;
      stopStream();
      await Promise.race([direct.flushNow(), new Promise((r) => timers.setTimeout(r, 3000))]);
      await hubView.flushCookies(); // the board session survives the restart (no new login)
    },
  };
}
