// Hub connection and view (C6, C7): the desktop token (safeStorage, bound to the hub URL),
// the client, the stream, overview and needs-you count, the hub WebContentsView in
// persist:sy-hub with the cookie login, owner sign-in, hub:route, the claude.ai handoff,
// seat:update, and the hub:* and accounts:* handlers.
//
// Owned by WP-C. Contract: docs/spec/desktop.md C6, C7. The bus carries the results:
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

const ROUTE_RE = new RegExp(HUB_ROUTE_PATTERN);
const SESSION_LINK_RE = new RegExp(CLAUDE_SESSION_LINK_PATTERN);
const ID_RE = /^[A-Za-z0-9_-]{1,64}$/;
const INFO_POLL_MS = 300_000;
const INFO_POLL_BAD_MS = 30_000;
/** The hub error shown for a 403 on a desktop-token request (only a 401 means the token is dead). */
export const FORBIDDEN_ERROR = 'The hub refused a request of this app (403 forbidden).';

/** The messages the shell shows for a failed `sy desktop token`, by exit code (C4). */
export function pairError(r, hadCode) {
  switch (r.exit) {
    case 2:
    case 3:
      return 'That code was refused or has expired. Make a new one and try again.';
    case 4:
      return hadCode
        ? 'Pairing is not possible here. Run sy setup first.'
        : 'Pairing without a code needs the hub on this computer or a working admin channel (machines.hub_admin). Type a code from the board (Devices, Add device, Desktop) or from sy hub pair --kind desktop.';
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
  let lastNeedsYou = '';
  let machineName = '';

  const emitHub = () => bus.emit('hub', { ...state });
  const set = (patch) => {
    let changed = false;
    for (const [k, v] of Object.entries(patch)) {
      if (state[k] !== v) {
        state[k] = v;
        changed = true;
      }
    }
    if (changed) emitHub();
  };

  const client = createClient({ fetch: deps.fetch, getAuth: () => auth, reportedBySy: () => reportedBySy });

  const hubView = createHubView({
    electron: deps.electron, security: deps.security,
    get window() {
      return deps.window;
    },
    log,
    base: () => state.url,
    auth: () => auth,
    onSessionLink: (url) => handoff(url),
    onOwner: (owner) => set({ owner }),
    onLoginRefused: (status) => handleUnauthorized(status),
  });

  // ---- status

  let mode = 'no_sy'; // 'no_sy' | 'dial' | null: what sy reported before any hub logic applies

  function statusFor() {
    if (mode) return mode;
    if (!state.connected || !state.url) return 'not_connected';
    if (!auth) return 'needs_pairing';
    if (!state.reachable) return 'unreachable';
    return 'ok';
  }

  // ---- info

  async function refreshInfo() {
    if (stopped) return;
    const info = await sy.info();
    if (stopped) return;
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
    machineName = String(info.machine?.name ?? '');
    const rawUrl = typeof hub.url === 'string' ? hub.url : null;
    const url = rawUrl && hubUrlAllowed(rawUrl, true) ? normalizeHubUrl(rawUrl) : null;
    if (url !== state.url) dropHub(); // another hub: the token, the view and the stream are no longer valid
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
      url, connected: !!hub.connected && !!url, reachable: !!hub.reachable, paired: !!auth, version: hub.version ?? null,
      local: !!hub.local, service: typeof hub.service === 'string' ? hub.service : null, admin, error: null,
    });
    set({ status: statusFor() });
    if (auth && state.connected) {
      startStream();
      if (state.reachable) refreshOverview();
    } else stopStream();
  }

  function dropHub() {
    setAuth(null);
    seats = [];
    stopStream();
    hubView.reset();
    state.needs_you = 0;
    lastNeedsYou = '';
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
    }, state.status === 'ok' ? INFO_POLL_MS : INFO_POLL_BAD_MS);
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
    await tokens.clear();
    await hubView.signOut();
    set({ paired: false, status: 'needs_pairing', owner: false });
  }

  // ---- stream and overview

  const stream = createStream({
    fetch: deps.fetch,
    getAuth: () => auth,
    reportedBySy: () => reportedBySy,
    timers,
    log,
    ...(deps.streamOptions ?? {}),
    onEvent: (type, data) => {
      if (type === 'session' || type === 'usage') scheduleRefresh();
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
    inflight ??= (async () => {
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
    if (!opened) await deps.security.openExternalSafe(url);
  }

  // ---- handlers

  const ready = () => state.connected && state.url && state.status !== 'dial' && state.status !== 'no_sy';

  async function pair(args = {}) {
    await refreshInfo();
    if (state.status === 'no_sy') return { ok: false, error: 'sy was not found. Install it, then run sy setup.' };
    if (state.status === 'dial') return { ok: false, error: state.error };
    if (!ready()) return { ok: false, error: 'sy is not connected to a hub. Run sy setup.' };
    const code = typeof args.code === 'string' && args.code !== '' ? args.code : undefined;
    const safeName = machineName.replace(/[^A-Za-z0-9._-]/g, '').slice(0, 20);
    // No spaces (TOKEN_NAME_RE): through an ssh admin channel the name is a remote word.
    const r = await sy.token({ code, name: safeName ? `Switchyard-desktop-${safeName}` : 'Switchyard-desktop' });
    if (!r.ok) return { ok: false, error: pairError(r, code !== undefined) };
    if (typeof r.token !== 'string' || !r.token || typeof r.device !== 'string') return { ok: false, error: 'The hub answered without a token.' };
    setAuth({ url: state.url, device: r.device, token: r.token });
    r.token = '';
    let stored = true;
    try {
      stored = await tokens.save(auth);
    } catch (err) {
      stored = false;
      log.warn?.(`hub: cannot store the token: ${err?.message ?? err}`);
    }
    set({ paired: true, storage_backend: tokens.backend(), status: statusFor() });
    await hubView.signOut(); // a new device, a new board session
    startStream();
    refreshOverview();
    if (hubView.view()) hubView.load(hubView.currentRoute());
    return stored ? { ok: true } : { ok: true, stored: false, warning: 'No secure storage is available, so the token is kept for this run only.' };
  }

  async function ownerSignIn() {
    if (!state.url) await refreshInfo();
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
    if (auth && !state.owner) await hubView.sessionLogin().catch(() => {});
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
  ipc.register('hub:owner-sign-in', () => ownerSignIn());
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
    state: () => ({ ...state }),
    async stop() {
      started = false;
      stopped = true;
      if (infoTimer) timers.clearTimeout(infoTimer);
      if (refreshTimer) timers.clearTimeout(refreshTimer);
      infoTimer = refreshTimer = null;
      stopStream();
    },
  };
}
