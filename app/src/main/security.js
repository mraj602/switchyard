// Security baseline (C2), applied before any window exists: app.enableSandbox(), the
// web-contents-created guards, the default deny window-open handler, per-view navigation and
// open policies (through shared/urls.js), per-session permission handlers, downloads and
// certificate errors.
//
// Contract: docs/spec/desktop.md C2.
//
// How the policies fit together:
// - Every webContents gets, when it is created, a deny-all window-open handler, a refusal of
//   <webview>, and navigation listeners that consult a per-webContents policy. With no policy
//   (a webContents nobody guarded) every renderer-started navigation is prevented.
// - guardView(wc, kind) installs the narrower policy of its view kind (shell, tab, hub).
// - guardSession(session, kind) installs the permission handlers and the download rule.
// - Certificate errors: there is deliberately no 'certificate-error' handler and no
//   setCertificateVerifyProc, so Chromium's default (refuse) always applies.
// - openExternalSafe is the only caller of shell.openExternal (the lint checks it).

import * as defaultUrls from '../shared/urls.js';
import { SAFE_WEB_PREFERENCES, PERMISSIONS, VIEW_SHELL, VIEW_TAB, VIEW_HUB, TAB_PARTITION_PREFIX, HUB_PARTITION, TAB_ID_RE } from '../shared/constants.js';

const KINDS = new Set([VIEW_SHELL, VIEW_TAB, VIEW_HUB]);

/** Extra webPreferences a caller may add; anything else (and any SAFE key) throws. */
const EXTRA_KEYS = new Set(['partition', 'preload', 'backgroundThrottling', 'zoomFactor', 'autoplayPolicy', 'safeDialogs', 'disableDialogs']);

const kindOf = (kind) => {
  if (!KINDS.has(kind)) throw new Error(`security: unknown view kind ${JSON.stringify(kind)}`);
  return kind;
};

/**
 * @param {{electron: object, urls?: object, log: object, claudeOrigin?: string|null,
 *          testHooks?: object|null, env?: object}} deps
 *   claudeOrigin: the test stand-in for claude.ai (unpackaged only); when absent it is read
 *   through testHooks.claudeOrigin(env) when both are given.
 * @returns {{
 *   applyGlobal(): void,
 *   webPreferences(kind: 'shell'|'tab'|'hub', extra?: object): object,
 *   guardView(webContents: object, kind: 'shell'|'tab'|'hub', opts?: {hubOrigin?: string|(() => string|null),
 *             onSessionLink?: (url: string) => void}): void,
 *   guardSession(session: object, kind: 'shell'|'tab'|'hub'): void,
 *   openExternalSafe(url: string): Promise<boolean>,
 *   claudeOrigin: string|null,
 * }}
 */
export function create(deps) {
  const { electron } = deps;
  const urls = deps.urls ?? defaultUrls;
  const log = deps.log ?? console;
  const packaged = !!electron.app?.isPackaged;
  let claudeOrigin = null;
  if (!packaged) {
    claudeOrigin = deps.claudeOrigin ?? (deps.testHooks?.claudeOrigin ? deps.testHooks.claudeOrigin(deps.env ?? {}) : null);
  }
  const urlOpts = Object.freeze({ claudeOrigin });

  /** webContents → {kind, opts} */
  const policies = new WeakMap();
  /** webContents with the base listeners. */
  const attached = new WeakSet();
  const guardedSessions = new WeakMap();
  let applied = false;

  const short = (u) => String(u).slice(0, 200);

  async function openExternalSafe(url) {
    if (!urls.isExternalAllowed(url)) {
      log.warn?.(`security: openExternal refused for ${short(url)}`);
      return false;
    }
    try {
      // mailto: keeps only to, subject, cc and body (urls.externalUrl)
      await electron.shell.openExternal(urls.externalUrl(url));
      return true;
    } catch (err) {
      log.warn?.(`security: openExternal failed: ${err?.message ?? err}`);
      return false;
    }
  }

  const hubOriginOf = (opts) => (typeof opts?.hubOrigin === 'function' ? opts.hubOrigin() : opts?.hubOrigin) ?? null;

  /** Decides one main-frame navigation; returns true to allow it. Side effects: external opens, handoffs. */
  function decideNavigation(wc, url) {
    const p = policies.get(wc);
    if (!p) return false;
    switch (p.kind) {
      case VIEW_SHELL:
        return false;
      case VIEW_TAB: {
        const action = urls.tabLinkAction(url, urlOpts);
        if (action === 'tab') return true;
        if (action === 'external') void openExternalSafe(url);
        return false;
      }
      case VIEW_HUB: {
        const action = urls.hubLinkAction(url, { hubOrigin: hubOriginOf(p.opts) });
        if (action === 'hub') return true;
        if (action === 'session') handoff(p.opts, url);
        else if (action === 'external') void openExternalSafe(url);
        return false;
      }
      default:
        return false;
    }
  }

  function handoff(opts, url) {
    if (typeof opts?.onSessionLink === 'function') {
      try {
        opts.onSessionLink(url);
      } catch (err) {
        log.warn?.(`security: session link handoff failed: ${err?.message ?? err}`);
      }
    } else {
      void openExternalSafe(url);
    }
  }

  /** The window.open policy of one webContents. */
  function decideOpen(wc, details) {
    const url = details?.url;
    const p = policies.get(wc);
    if (!p) return { action: 'deny' };
    switch (p.kind) {
      case VIEW_TAB: {
        const action = urls.tabLinkAction(url, urlOpts);
        if (action === 'tab') loadIn(wc, url);
        else if (action === 'external') void openExternalSafe(url);
        return { action: 'deny' };
      }
      case VIEW_HUB: {
        const action = urls.hubLinkAction(url, { hubOrigin: hubOriginOf(p.opts) });
        if (action === 'hub') loadIn(wc, url);
        else if (action === 'session') handoff(p.opts, url);
        else if (action === 'external') void openExternalSafe(url);
        return { action: 'deny' };
      }
      default:
        return { action: 'deny' };
    }
  }

  const navUrl = (event, url) => event?.url ?? url;

  function loadIn(wc, url) {
    Promise.resolve()
      .then(() => wc.loadURL(urls.parse(url).href))
      .catch((err) => log.debug?.(`security: load failed: ${err?.message ?? err}`));
  }

  /** Base listeners every webContents gets (idempotent). */
  function attach(wc) {
    if (!wc || attached.has(wc)) return;
    attached.add(wc);
    wc.on('will-attach-webview', (event) => event.preventDefault());
    wc.setWindowOpenHandler((details) => decideOpen(wc, details));
    wc.on('will-navigate', (event, url) => {
      // will-navigate is main-frame only; an explicit isMainFrame === false is let through.
      if (event?.isMainFrame === false) return;
      const target = navUrl(event, url);
      if (!decideNavigation(wc, target)) {
        event.preventDefault();
        log.debug?.(`security: navigation prevented to ${short(target)}`);
      }
    });
    wc.on('will-redirect', (event, url, isInPlace, isMainFrame) => {
      const main = event?.isMainFrame ?? isMainFrame;
      if (main === false) return;
      const target = navUrl(event, url);
      if (!decideNavigation(wc, target)) {
        event.preventDefault();
        log.debug?.(`security: redirect prevented to ${short(target)}`);
      }
    });
  }

  function applyGlobal() {
    if (applied) return;
    applied = true;
    electron.app.enableSandbox();
    electron.app.on('web-contents-created', (_event, wc) => attach(wc));
  }

  function webPreferences(kind, extra = {}) {
    kindOf(kind);
    const out = { ...SAFE_WEB_PREFERENCES };
    for (const [k, v] of Object.entries(extra ?? {})) {
      if (Object.hasOwn(SAFE_WEB_PREFERENCES, k)) throw new Error(`security: webPreferences.${k} is fixed by the baseline`);
      if (!EXTRA_KEYS.has(k)) throw new Error(`security: webPreferences.${k} is not allowed`);
      out[k] = v;
    }
    if (out.preload !== undefined && kind !== VIEW_SHELL) throw new Error('security: only the shell has a preload');
    if (kind === VIEW_SHELL && out.partition !== undefined) throw new Error('security: the shell uses the default session');
    if (kind === VIEW_TAB) {
      const p = out.partition;
      if (typeof p !== 'string' || !p.startsWith(TAB_PARTITION_PREFIX) || !TAB_ID_RE.test(p.slice(TAB_PARTITION_PREFIX.length))) {
        throw new Error('security: an account tab needs partition persist:tab-<tab id>');
      }
    }
    if (kind === VIEW_HUB) {
      if (out.partition === undefined) out.partition = HUB_PARTITION;
      if (out.partition !== HUB_PARTITION) throw new Error(`security: the hub view uses ${HUB_PARTITION}`);
    }
    return out;
  }

  function guardView(wc, kind, opts = {}) {
    kindOf(kind);
    if (!wc) throw new Error('security: guardView needs a webContents');
    const prev = policies.get(wc);
    if (prev && prev.kind !== kind) throw new Error(`security: webContents already guarded as ${prev.kind}`);
    attach(wc);
    policies.set(wc, { kind, opts: { ...opts } });
  }

  /** The permission rule of one session kind; details are Electron's permission details. */
  function permissionAllowed(kind, permission, requestingUrl, details) {
    const allowed = PERMISSIONS[kind] ?? [];
    if (!allowed.includes(permission)) return false;
    if (kind === VIEW_TAB) {
      // Only claude.ai pages (not a sign-in hop or an embedded frame elsewhere) get permissions.
      if (!urls.isClaudeUrl(requestingUrl, urlOpts)) return false;
      if (permission === 'media') {
        // Audio only (voice mode). A request lists mediaTypes; a check names one mediaType.
        if (Array.isArray(details?.mediaTypes)) return details.mediaTypes.length > 0 && details.mediaTypes.every((t) => t === 'audio');
        return details?.mediaType === 'audio';
      }
    }
    return true;
  }

  function guardSession(session, kind) {
    kindOf(kind);
    if (!session) throw new Error('security: guardSession needs a session');
    const prev = guardedSessions.get(session);
    if (prev === kind) return;
    if (prev) throw new Error(`security: session already guarded as ${prev}`);
    guardedSessions.set(session, kind);

    session.setPermissionRequestHandler((wc, permission, callback, details) => {
      const from = details?.requestingUrl ?? wc?.getURL?.() ?? '';
      const ok = permissionAllowed(kind, permission, from, details);
      if (!ok) log.debug?.(`security: ${kind} permission ${permission} denied`);
      callback(ok);
    });
    session.setPermissionCheckHandler((wc, permission, requestingOrigin, details) => {
      const from = details?.requestingUrl ?? requestingOrigin ?? wc?.getURL?.() ?? '';
      return permissionAllowed(kind, permission, from, details);
    });
    session.setDevicePermissionHandler(() => false);
    // No arguments to the callback = the request is denied.
    session.setDisplayMediaRequestHandler((_request, callback) => callback());
    session.on('will-download', (event, item) => {
      if (kind === VIEW_TAB) return; // the default save dialog
      event.preventDefault();
      try {
        item?.cancel?.();
      } catch {
        // already gone
      }
      log.info?.(`security: download cancelled in the ${kind} view`);
    });
  }

  return Object.freeze({ applyGlobal, webPreferences, guardView, guardSession, openExternalSafe, claudeOrigin });
}
