// Account tabs: one WebContentsView per tab in partition persist:tab-<id>, the
// platform-true Chrome UA, staggered loads, the Cloudflare retry for hidden tabs, lastUrl, cookie
// flushes, Remove, and the tab:* handlers.
//
// Owned by WP-B. Contract: docs/spec/desktop.md C1, C2, C5.
// The tab list lives in app-state.json (`store`), written only here and by usage/ (reading
// flags, `stopped`, `seat`). A change is announced with the bus `tabs` event.

import * as nodeFs from 'node:fs/promises';
import * as nodePath from 'node:path';
import { randomBytes } from 'node:crypto';

import { CLAUDE_ORIGIN, CLAUDE_SESSION_LINK_PATTERN, READING_NEVER_ASKED, VIEW_TAB } from '../../shared/constants.js';
import { isClaudeUrl, isReadableUrl } from '../../shared/usage.js';
import { createCookieFlusher } from './cookies.js';
import { partitionFolder, partitionOf, userAgentFor } from './partition.js';

/** Start-up loads are staggered by this much per tab, so N tabs don't all hit Cloudflare at once. */
export const LOAD_STAGGER_MS = 1_500;
/** A hidden tab still on Cloudflare's "Just a moment" after this long is reloaded, at most twice. */
export const CF_RETRY_AFTER_MS = 25_000;
export const CF_MAX_RETRIES = 2;
/** lastUrl writes are batched for this long (claude.ai navigates in-page a lot). */
export const LAST_URL_DEBOUNCE_MS = 2_000;

const SESSION_LINK_RE = new RegExp(CLAUDE_SESSION_LINK_PATTERN);

/**
 * @param {{electron: object, security: object, window: object, store: object, bus: object, ipc: object,
 *          log: object, claudeOrigin?: string, timers?: {setTimeout: Function, clearTimeout: Function},
 *          fs?: object, path?: object, userData?: string, platform?: string, chromeVersion?: string,
 *          random?: (n: number) => Buffer}} deps
 * @returns {{
 *   start(): Promise<void>,                    // create views for the stored tabs, staggered
 *   list(): object[],                          // TabInfo[] (bus.js)
 *   view(id: string): object|undefined,        // the tab's WebContentsView
 *   openLink(url: string, seat?: {account: string, org: string}|null): boolean,
 *                                              // C7 handoff: tab with that seat, else first signed-in; false = none
 *   flushCookies(): Promise<void>,             // every tab's cookies.flushStore()
 *   stop(): Promise<void>,
 * }}
 */
export function create(deps) {
  const { electron, security, window, store, bus, ipc, log } = deps;
  const claudeOrigin = deps.claudeOrigin ?? CLAUDE_ORIGIN;
  const timers = deps.timers ?? { setTimeout, clearTimeout };
  const fs = deps.fs ?? nodeFs;
  const path = deps.path ?? nodePath;
  const platform = deps.platform ?? process.platform;
  const chrome = deps.chromeVersion ?? process.versions.chrome ?? '130.0.0.0';
  const ua = userAgentFor({ platform, chrome });
  const homeUrl = `${claudeOrigin}/new`;

  /** @type {Map<string, {view: object, ses: object, loaded: boolean, cfRetries: number, cfTimer: any, loadTimer: any, urlTimer: any, pendingUrl: string|null}>} */
  const views = new Map();
  const cookies = createCookieFlusher({ sessions: () => [...views.values()].map((v) => v.ses), log });
  let handlersRegistered = false;
  let started = false;

  const records = () => store.get().tabs ?? [];
  const rec = (id) => records().find((t) => t.id === id) ?? null;

  const liveUrl = (id) => {
    try {
      return views.get(id)?.view.webContents.getURL() ?? '';
    } catch {
      return '';
    }
  };

  function info(t) {
    const v = views.get(t.id);
    return {
      id: t.id,
      name: t.name,
      reading: t.reading,
      warning_accepted: t.warning_accepted ?? 0,
      send_to_hub: t.send_to_hub !== false,
      stopped: t.stopped ?? null,
      seat: t.seat ?? null,
      last_url: t.last_url ?? null,
      signed_in: isReadableUrl(liveUrl(t.id), claudeOrigin),
      loaded: !!v?.loaded,
    };
  }
  const list = () => records().map(info);
  const emit = () => bus.emit('tabs', { tabs: list() });

  // ---- one view

  function createView(t, loadDelayMs) {
    const id = t.id;
    const ses = electron.session.fromPartition(partitionOf(id));
    ses.setUserAgent(ua);
    security.guardSession(ses, 'tab');
    const view = new electron.WebContentsView({
      // The partition names the same session as `ses` (session.fromPartition is idempotent).
      webPreferences: security.webPreferences('tab', { partition: partitionOf(id), backgroundThrottling: false }),
    });
    view.setBackgroundColor?.('#0F1115');
    const wc = view.webContents;
    wc.setUserAgent?.(ua);
    security.guardView(wc, 'tab', {});
    const v = { view, ses, loaded: false, cfRetries: 0, cfTimer: null, loadTimer: null, urlTimer: null, pendingUrl: null };
    views.set(id, v);

    wc.on('did-finish-load', () => {
      v.loaded = true;
      scheduleCloudflareCheck(id, v);
      emit();
    });
    const onNav = (_e, url) => {
      const u = typeof url === 'string' ? url : wc.getURL();
      rememberUrl(id, v, u);
      emit();
    };
    wc.on('did-navigate', onNav);
    wc.on('did-navigate-in-page', onNav);

    view.setVisible?.(false);
    window.addView(id, view, VIEW_TAB);

    const start = t.last_url && isClaudeUrl(t.last_url, claudeOrigin) ? t.last_url : `${claudeOrigin}/`;
    v.loadTimer = timers.setTimeout(() => {
      v.loadTimer = null;
      if (wc.isDestroyed()) return;
      Promise.resolve(wc.loadURL(start)).catch((err) => log.warn?.(`tabs: ${id}: load failed:`, err?.message ?? err));
    }, loadDelayMs);
    return v;
  }

  /** Cloudflare's interstitial can stall: reload a hidden tab still on it, never the visible one. */
  function scheduleCloudflareCheck(id, v) {
    if (v.cfTimer != null) timers.clearTimeout(v.cfTimer);
    v.cfTimer = timers.setTimeout(() => {
      v.cfTimer = null;
      const wc = v.view.webContents;
      if (wc.isDestroyed()) return;
      const title = wc.getTitle?.() ?? '';
      if (!/just a moment/i.test(title)) {
        v.cfRetries = 0;
        return;
      }
      if (window.selected() === id) return;
      if (v.cfRetries < CF_MAX_RETRIES) {
        v.cfRetries += 1;
        wc.reload();
      }
    }, CF_RETRY_AFTER_MS);
  }

  /** lastUrl: remembered per tab, written after a short debounce. */
  function rememberUrl(id, v, url) {
    if (!isClaudeUrl(url, claudeOrigin)) return;
    if (new URL(url).pathname.replace(/\/+$/, '') === '/logout') return;
    v.pendingUrl = url;
    if (v.urlTimer != null) timers.clearTimeout(v.urlTimer);
    v.urlTimer = timers.setTimeout(() => {
      v.urlTimer = null;
      void writeUrl(id, v);
    }, LAST_URL_DEBOUNCE_MS);
  }

  async function writeUrl(id, v) {
    const url = v.pendingUrl;
    v.pendingUrl = null;
    if (!url || !rec(id) || rec(id).last_url === url) return;
    await store.update((d) => {
      const t = d.tabs?.find((x) => x.id === id);
      if (t) t.last_url = url;
    });
  }

  function disposeView(id) {
    const v = views.get(id);
    if (!v) return null;
    for (const k of ['cfTimer', 'loadTimer', 'urlTimer']) if (v[k] != null) timers.clearTimeout(v[k]);
    v.cfTimer = v.loadTimer = v.urlTimer = null;
    views.delete(id);
    return v;
  }

  // ---- handlers

  const newId = () => `t_${(deps.random ?? randomBytes)(8).toString('hex')}`;

  async function addTab({ name }) {
    const trimmed = String(name).trim();
    if (!trimmed) throw new Error('a tab needs a name');
    const t = {
      id: newId(),
      name: trimmed.slice(0, 24),
      last_url: homeUrl,
      reading: READING_NEVER_ASKED,
      warning_accepted: 0,
      send_to_hub: true,
      stopped: null,
      seat: null,
    };
    await store.update((d) => {
      (d.tabs ??= []).push(t);
    });
    createView(t, 0);
    emit();
    return { id: t.id };
  }

  async function updateTab({ id, name }) {
    if (!rec(id)) throw new Error('no such tab');
    if (name === undefined) return;
    const trimmed = String(name).trim();
    if (!trimmed) throw new Error('a tab needs a name');
    await store.update((d) => {
      const t = d.tabs.find((x) => x.id === id);
      if (t) t.name = trimmed.slice(0, 24);
    });
    emit();
  }

  /** Remove: the tab, its view, its partition's storage, then the partition's folder. */
  async function removeTab({ id }) {
    if (!rec(id)) throw new Error('no such tab');
    const v = disposeView(id);
    await store.update((d) => {
      d.tabs = (d.tabs ?? []).filter((t) => t.id !== id);
    });
    if (window.selected() === id) window.select('hub');
    if (v) {
      window.removeView(id);
      const storagePath = v.ses.getStoragePath?.() ?? null;
      try {
        v.view.webContents.close?.();
      } catch (err) {
        log.warn?.(`tabs: ${id}: closing the view failed:`, err?.message ?? err);
      }
      try {
        await v.ses.clearStorageData();
        await v.ses.clearCache?.();
      } catch (err) {
        log.warn?.(`tabs: ${id}: clearing storage failed:`, err?.message ?? err);
      }
      await removePartitionFolder(id, storagePath);
    } else {
      // No view (a tab imported before this restart, not made yet): its folder may still
      // exist (the importer copied the prototype's partition there). Remove it all the same;
      // the folder is derived from the id only, so the same path guard holds.
      await removePartitionFolder(id, null);
    }
    emit();
  }

  async function removePartitionFolder(id, storagePath) {
    const userData = deps.userData ?? electron.app.getPath('userData');
    const root = path.resolve(userData, 'Partitions');
    let dir;
    try {
      dir = path.join(root, partitionFolder(id));
    } catch (err) {
      log.warn?.(`tabs: ${id}: no partition folder for that id; leaving it`);
      return;
    }
    // Only ever a tab's own folder directly under <userData>/Partitions.
    if (path.dirname(path.resolve(dir)) !== root) {
      log.warn?.(`tabs: ${id}: ${dir} is not under ${root}; leaving it`);
      return;
    }
    if (storagePath && path.resolve(storagePath) !== path.resolve(dir)) {
      log.warn?.(`tabs: ${id}: storage path ${storagePath} is not the expected folder; leaving it`);
      return;
    }
    try {
      await fs.rm(dir, { recursive: true, force: true });
    } catch (err) {
      log.warn?.(`tabs: ${id}: removing ${dir} failed:`, err?.message ?? err);
    }
  }

  async function reorderTabs({ ids }) {
    await store.update((d) => {
      const byId = new Map((d.tabs ?? []).map((t) => [t.id, t]));
      const out = [];
      for (const id of ids) if (byId.has(id)) out.push(byId.get(id));
      for (const t of d.tabs ?? []) if (!ids.includes(t.id)) out.push(t);
      d.tabs = out;
    });
    emit();
  }

  const withView = (id) => {
    const v = views.get(id);
    if (!v) throw new Error('no such tab');
    return v;
  };

  function register() {
    if (handlersRegistered) return;
    handlersRegistered = true;
    ipc.register('tab:add', addTab);
    ipc.register('tab:update', updateTab);
    ipc.register('tab:remove', removeTab);
    ipc.register('tab:reorder', reorderTabs);
    ipc.register('tab:reload', async ({ id }) => {
      withView(id).view.webContents.reload();
    });
    ipc.register('tab:home', async ({ id }) => {
      await withView(id).view.webContents.loadURL(homeUrl);
    });
    ipc.register('tab:external', async ({ id }) => {
      const url = withView(id).view.webContents.getURL();
      if (url) await security.openExternalSafe(url);
    });
  }

  // ---- interface

  return {
    async start() {
      if (started) return;
      started = true;
      register();
      records().forEach((t, i) => createView(t, i * LOAD_STAGGER_MS));
      cookies.start();
      emit();
    },
    list,
    view: (id) => views.get(id)?.view,
    openLink(url, seat = null) {
      if (typeof url !== 'string' || !SESSION_LINK_RE.test(url)) return false;
      const tabs = records();
      let pick = seat ? tabs.find((t) => t.seat && t.seat.account === seat.account && t.seat.org === seat.org && views.has(t.id)) : null;
      pick ??= tabs.find((t) => views.has(t.id) && isReadableUrl(liveUrl(t.id), claudeOrigin));
      if (!pick) return false;
      const v = views.get(pick.id);
      void v.view.webContents.loadURL(url);
      window.select(pick.id);
      window.showWindow();
      return true;
    },
    flushCookies: () => cookies.flush(),
    async stop() {
      cookies.stop();
      await cookies.flush();
      for (const [id, v] of views) if (v.pendingUrl) await writeUrl(id, v);
      for (const v of views.values()) {
        for (const k of ['cfTimer', 'loadTimer', 'urlTimer']) if (v[k] != null) timers.clearTimeout(v[k]);
        v.cfTimer = v.loadTimer = v.urlTimer = null;
      }
    },
  };
}
