// The hub view (C7): a WebContentsView in the session `persist:sy-hub`, created when first
// needed and kept afterwards. The board is signed in by main: before the first load (when the
// partition holds no read cookie) and whenever the view lands on `#/login` or `#/pair`, main posts
// the desktop token to `<hub>/v1/session/login` through that partition's own session, so the
// cookie lands there and the token never enters the page. The login only ever targets the hub's
// origin. Owner sign-in loads the one-time link of `sy desktop owner-link`.

import { HUB_PARTITION } from '../../shared/constants.js';
import { hubUrl } from './client.js';

const LOGIN_ROUTE_RE = /^#\/(login|pair)(?:[/?]|$)/;
const COOKIE = 'sy_session';
const HUB_VIEW_ID = 'hub';
const LOGIN_WINDOW_MS = 30_000;
const LOGIN_MAX_IN_WINDOW = 2;

/**
 * @param {{electron: object, security: object, window: object, log?: object,
 *          base: () => string|null,                       // the hub URL (normalized) or null
 *          auth: () => ({url: string, token: string}|null),
 *          onSessionLink: (url: string) => void,
 *          onOwner: (owner: boolean, until?: string|null) => void,
 *          onLoginRefused: (status: number) => void,       // 401 on login: the token is revoked
 *          userAgentSuffix?: string,                        // ' SwitchyardDesktop/<version>': the board can tell it is in the app
 *          onBlocked?: () => void,                          // the board kept asking for a login main could not give
 *          now?: () => number, timeoutMs?: number}} deps
 */
export function createHubView(deps) {
  const { electron } = deps;
  const log = deps.log ?? console;
  const now = deps.now ?? Date.now;
  const timeoutMs = deps.timeoutMs ?? 10_000;

  let view = null;
  let viewBase = null; // the hub URL the view was made for
  let pendingRoute = '';
  let loginTimes = [];
  let ownerTimer = null;
  let suppressed = false; // the board is kept out of sight (its login page, a dead token)
  let relogging = false;

  const session = () => electron.session.fromPartition(HUB_PARTITION);
  const originOf = (u) => {
    try {
      return new URL(u).origin;
    } catch {
      return null;
    }
  };
  const target = (base, route = '') => `${base}/${route}`;

  /** A fetch through the hub partition's session (its cookie), to the hub's own origin only. */
  async function sessionFetch(method, path, body) {
    const base = deps.base();
    const url = base && hubUrl(base, path);
    if (!url) return { ok: false, status: 0, error: 'no hub' };
    try {
      const res = await session().fetch(url, {
        method,
        headers: body === undefined ? { accept: 'application/json' } : { 'content-type': 'application/json', accept: 'application/json' },
        body: body === undefined ? undefined : JSON.stringify(body),
        credentials: 'include',
        redirect: 'manual',
        signal: AbortSignal.timeout(timeoutMs),
      });
      let data = null;
      if (res.status !== 204) {
        const text = await res.text();
        if (text.length > 2 * 1024 * 1024) return { ok: false, status: res.status, error: 'reply too large' };
        try {
          data = text ? JSON.parse(text) : null;
        } catch {
          data = null;
        }
      }
      const ok = res.status >= 200 && res.status < 300;
      const msg = data?.error?.message ?? data?.message;
      return { ok, status: res.status, data, ...(ok ? {} : { error: typeof msg === 'string' ? msg.slice(0, 200) : `hub answered ${res.status}` }) };
    } catch (err) {
      return { ok: false, status: 0, error: String(err?.message ?? err).split('\n')[0].slice(0, 200) };
    }
  }

  /** POST /v1/session/login with the desktop token (never in the page). */
  async function sessionLogin() {
    const auth = deps.auth();
    const base = deps.base();
    if (!auth || !base) return { ok: false, status: 0, error: 'not paired' };
    const url = hubUrl(base, '/v1/session/login');
    if (!url || originOf(url) !== originOf(base)) return { ok: false, status: 0, error: 'bad hub URL' };
    try {
      const res = await session().fetch(url, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ token: auth.token }),
        credentials: 'include',
        redirect: 'error',
        signal: AbortSignal.timeout(timeoutMs),
      });
      if (res.status === 204 || res.status === 200) {
        // Write the new cookie to disk now: a cookie lost at a crash or a kill means a new
        // login at the next start (the hub then replaces the old board session, auth/session.go).
        await flushCookies();
        return { ok: true, status: res.status };
      }
      if (res.status === 401) deps.onLoginRefused(401);
      return { ok: false, status: res.status, error: res.status === 429 ? 'the hub is rate limiting logins' : `login answered ${res.status}` };
    } catch (err) {
      return { ok: false, status: 0, error: String(err?.message ?? err).split('\n')[0].slice(0, 200) };
    }
  }

  async function flushCookies() {
    try {
      await session().cookies.flushStore?.();
    } catch (err) {
      log.debug?.(`hub view: cookie flush: ${err?.message ?? err}`);
    }
  }

  async function hasCookie() {
    const base = deps.base();
    if (!base) return false;
    try {
      const list = await session().cookies.get({ url: `${base}/`, name: COOKIE });
      return list.some((c) => !c.expirationDate || c.expirationDate * 1000 > now());
    } catch {
      return false;
    }
  }

  async function refreshOwner() {
    const r = await sessionFetch('GET', '/v1/me');
    if (r.ok) {
      const owner = r.data?.owner === true;
      const until = typeof r.data?.device?.expires_at === 'string' ? r.data.device.expires_at : null; // an owner session ends (30 days)
      deps.onOwner(owner, owner ? until : null);
    }
    else if (r.status === 401 || r.status === 403) deps.onOwner(false);
  }

  function scheduleOwnerCheck() {
    if (ownerTimer) clearTimeout(ownerTimer);
    ownerTimer = setTimeout(() => {
      ownerTimer = null;
      refreshOwner();
    }, 500);
    ownerTimer.unref?.();
  }

  /** Error code of a failed loadURL; never its message, which holds the URL (an owner link's secret). */
  const loadErrorCode = (err) => {
    const code = typeof err?.code === 'string' && /^[A-Z0-9_]{1,64}$/.test(err.code) ? err.code : null;
    const errno = Number.isInteger(err?.errno) ? err.errno : null;
    return code ?? (errno !== null ? `errno ${errno}` : 'error');
  };

  /** Load url in the view (the view at call time; nothing when it is gone). */
  async function navigate(url) {
    const v = view;
    if (!v) return;
    try {
      await v.webContents.loadURL(url);
    } catch (err) {
      // ERR_ABORTED is what a redirect to the same document or a hash change reports
      log.debug?.(`hub view: loadURL failed: ${loadErrorCode(err)}`);
    }
  }

  /** Hide or show the board over the shell: the board's own login page is never shown in the app. */
  function suppress(on) {
    suppressed = on === true;
    if (view) deps.window.setViewHidden(HUB_VIEW_ID, suppressed);
  }

  /** The board cannot be signed in by main: hide it and tell the owner of this module. */
  function blocked(why) {
    log.warn?.(`hub view: ${why}`);
    suppress(true);
    deps.onBlocked?.();
  }

  async function relogin() {
    if (relogging) return;
    relogging = true;
    try {
      await reloginOnce();
    } finally {
      relogging = false;
    }
  }

  async function reloginOnce() {
    const base = deps.base();
    if (!base || !deps.auth()) return blocked('the board asks for a login and this app has no token');
    suppress(true); // never let the login page show while main signs in
    const t = now();
    loginTimes = loginTimes.filter((x) => t - x < LOGIN_WINDOW_MS);
    if (loginTimes.length >= LOGIN_MAX_IN_WINDOW) return blocked('not signing in again, the board keeps asking for a login');
    loginTimes.push(t);
    const r = await sessionLogin();
    const v = view;
    if (!v) return;
    if (!r.ok) {
      if (r.status !== 401) blocked(`the board login failed (${r.status || r.error})`); // 401: handled as a dead token
      return;
    }
    const was = v.webContents.getURL();
    const to = target(base, pendingRoute);
    await navigate(to);
    // the view may have been dropped (reset) while it loaded
    if (view === v && was.split('#')[0] === to.split('#')[0]) v.webContents.reload?.();
    if (view === v) suppress(false);
  }

  function onNavigated(url) {
    const base = deps.base();
    if (!base || originOf(url) !== originOf(base)) return; // a login is never made for another origin
    let hash = '';
    try {
      hash = new URL(url).hash;
    } catch {
      return;
    }
    if (LOGIN_ROUTE_RE.test(hash)) relogin();
  }

  function create() {
    const base = deps.base();
    if (!base) return null;
    const v = new electron.WebContentsView({ webPreferences: deps.security.webPreferences('hub', { partition: HUB_PARTITION }) });
    deps.security.guardSession(session(), 'hub');
    deps.security.guardView(v.webContents, 'hub', {
      hubOrigin: originOf(base),
      onSessionLink: (url) => deps.onSessionLink(url),
    });
    v.webContents.on('did-navigate', (_e, url) => onNavigated(url));
    v.webContents.on('did-navigate-in-page', (_e, url) => {
      onNavigated(url);
      // The board's #/owner route swaps the link in-page and then moves on in-page, with no
      // new load: check the session's owner flag again (debounced).
      scheduleOwnerCheck();
    });
    v.webContents.on('did-finish-load', () => {
      onNavigated(v.webContents.getURL()); // a login route reached by a path that fired no navigation event
      scheduleOwnerCheck();
    });
    // The board can tell it runs inside the app (before it knows anything about the session).
    try {
      const wc = v.webContents;
      const ua = wc.getUserAgent?.();
      if (ua && deps.userAgentSuffix && !ua.includes(deps.userAgentSuffix)) wc.setUserAgent(`${ua} ${deps.userAgentSuffix}`);
    } catch (err) {
      log.warn?.(`hub view: user agent: ${err?.message ?? err}`);
    }
    deps.window.addView(HUB_VIEW_ID, v, 'hub');
    if (suppressed) deps.window.setViewHidden(HUB_VIEW_ID, true);
    view = v;
    viewBase = base;
    return v;
  }

  return {
    view: () => view,
    /** The view, created (not loaded) when missing; null without a hub URL. */
    ensure() {
      if (view && viewBase !== deps.base()) this.reset();
      return view ?? create();
    },
    /** Load `<hub>/<route>` (route like `#/sessions/x`, or ''), signing the partition in first when needed. */
    async load(route = '') {
      const v = this.ensure();
      if (!v) return false;
      pendingRoute = route;
      // A cookie that is still in the jar may be revoked (the board session was removed): ask
      // the hub before trusting it, so the board never opens on its own login page.
      if (deps.auth() && (!(await hasCookie()) || (await sessionFetch('GET', '/v1/me')).status === 401)) {
        loginTimes.push(now());
        const r = await sessionLogin();
        if (!r.ok) {
          // 401: the token is dead (onLoginRefused took it); else the board cannot be signed in now
          if (r.status !== 401) blocked(`the board login failed (${r.status || r.error})`);
        } else suppress(false);
      }
      await navigate(target(deps.base(), route));
      return true;
    },
    /** Load a one-time owner link; only the hub's own origin is accepted. */
    async loadOwnerLink(link) {
      const base = deps.base();
      if (!base || originOf(link) !== originOf(base)) return false;
      const v = this.ensure();
      if (!v) return false;
      pendingRoute = '';
      await navigate(link);
      return true;
    },
    sessionFetch,
    sessionLogin,
    hasCookie,
    flushCookies,
    refreshOwner,
    /** The route in the view's address now, e.g. `#/sessions/abc`, or ''. */
    currentRoute() {
      try {
        return view ? new URL(view.webContents.getURL()).hash : '';
      } catch {
        return '';
      }
    },
    suppress,
    /** Drop the view (the hub URL changed). */
    reset() {
      if (ownerTimer) clearTimeout(ownerTimer);
      ownerTimer = null;
      if (!view) return;
      try {
        deps.window.removeView('hub');
      } catch (err) {
        log.warn?.(`hub view: removeView: ${err?.message ?? err}`);
      }
      try {
        view.webContents.close?.();
      } catch {
        // already gone
      }
      view = null;
      viewBase = null;
      suppressed = false;
    },
    /** Leave the owner session: tell the hub, drop the cookie, and sign in again as this device (read-only). */
    async ownerSignOut() {
      await sessionFetch('POST', '/v1/session/logout', {});
      await this.signOut();
      if (view) await this.load(this.currentRoute());
    },
    /** Forget the partition's cookie (the token was revoked or replaced). */
    async signOut() {
      try {
        await session().clearStorageData?.({ storages: ['cookies'] });
      } catch {
        // nothing to clear
      }
      deps.onOwner(false);
    },
  };
}
