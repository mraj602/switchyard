// Logic of the service worker that needs no browser: which requests are cached data, how a
// push payload becomes a notification, where a tap goes. A classic script: sw.js loads it
// with importScripts, and webui/static/sw-lib.test.cjs loads it into a node `vm` context.
// Everything is exposed as one global, SyLib.

(function (root) {
  'use strict';

  // Paths whose GET responses are kept for offline use (pwa.md 4). `/v1/sessions/<id>` is
  // kept for the last SESSION_KEEP sessions only.
  const DATA_EXACT = new Set([
    '/v1/me', '/v1/overview', '/v1/sessions', '/v1/accounts', '/v1/machines',
    '/v1/alerts', '/v1/rules', '/v1/devices/self', '/v1/push/config',
  ]);
  const SESSION_KEEP = 20;
  const SESSION_PATH = /^\/v1\/sessions\/[^/]+$/;

  // Never intercepted: the live stream, one-time action tokens.
  const PASS_PREFIXES = ['/v1/stream', '/v1/actions/'];

  function pathOf(url) {
    try { return new URL(url, 'http://hub.invalid').pathname; } catch (_) { return ''; }
  }

  // Is this a GET the data cache should answer when the hub cannot be reached?
  function isDataRequest(url, method) {
    if ((method || 'GET').toUpperCase() !== 'GET') return false;
    const p = pathOf(url);
    if (PASS_PREFIXES.some((x) => p === x || p.startsWith(x))) return false;
    return DATA_EXACT.has(p) || SESSION_PATH.test(p);
  }

  const isSessionDetail = (url) => SESSION_PATH.test(pathOf(url));

  // Given the cached session-detail URLs in the order they were stored (oldest first),
  // the ones to delete so that only the newest `keep` remain.
  function sessionsToEvict(urls, keep) {
    const k = keep == null ? SESSION_KEEP : keep;
    const details = urls.filter(isSessionDetail);
    return details.length > k ? details.slice(0, details.length - k) : [];
  }

  // Navigations to the app itself.
  const isNavigation = (url) => {
    const p = pathOf(url);
    return p === '/' || p === '/index.html';
  };

  const shellCacheName = (build) => `sy-shell-${build}`;
  const DATA_CACHE = 'sy-data';
  const isCurrentCache = (name, build) => name === shellCacheName(build) || name === DATA_CACHE;

  // ---- push payloads (push.md 6.5) ------------------------------------------------------

  const FALLBACK = Object.freeze({
    v: 1, type: 'alert', title: 'Switchyard · something needs you', body: 'Open the app to see what.',
    tag: 'sy-fallback', url: '/#/', tap: 'session', needs_you: null, actions: null,
  });

  // Never throws: a payload that is missing, not JSON or not an object is shown as a
  // generic notice (iOS revokes subscriptions whose pushes show nothing).
  function parsePayload(text) {
    let p;
    try { p = typeof text === 'string' ? JSON.parse(text) : text; } catch (_) { return { ...FALLBACK }; }
    if (!p || typeof p !== 'object' || Array.isArray(p)) return { ...FALLBACK };
    const out = { ...FALLBACK, ...p };
    if (typeof out.title !== 'string' || !out.title) out.title = FALLBACK.title;
    if (typeof out.body !== 'string') out.body = '';
    if (typeof out.tag !== 'string' || !out.tag) out.tag = FALLBACK.tag;
    if (typeof out.url !== 'string' || !out.url.startsWith('/') || out.url.startsWith('//') || out.url.includes('\\')) out.url = FALLBACK.url;
    if (!['auto', 'session', 'claude'].includes(out.tap)) out.tap = 'auto';
    if (!out.actions || typeof out.actions !== 'object') out.actions = null;
    return out;
  }

  const hasActions = (p) => !!(p.actions && (p.actions.snooze || p.actions.seen));

  function notificationOptions(p, base) {
    const b = base || '';
    const ts = Date.parse(p.ts);
    const actions = [];
    if (p.actions && p.actions.snooze) actions.push({ action: 'snooze', title: 'Snooze 15 min' });
    if (p.actions && p.actions.seen) actions.push({ action: 'seen', title: 'Seen' });
    return {
      body: p.body, tag: p.tag, renotify: true,
      timestamp: Number.isNaN(ts) ? Date.now() : ts,
      icon: `${b}icons/icon-192.png`, badge: `${b}icons/badge-96.png`,
      data: p, actions: hasActions(p) ? actions : [], requireInteraction: false,
    };
  }

  const badgeCount = (p) => (Number.isInteger(p.needs_you) && p.needs_you > 0 ? p.needs_you : 0);

  // ---- tapping a push -------------------------------------------------------------------

  const httpsOnly = (u) => {
    if (typeof u !== 'string') return null;
    try { const x = new URL(u); return x.protocol === 'https:' ? x.href : null; } catch (_) { return null; }
  };

  // Does choosing the target need a reachability probe of the hub?
  const needsProbe = (p) => p.tap === 'auto' && !!httpsOnly(p.remote_url);

  // { kind: 'claude' | 'app', url }. `reachable` is whether GET /health answered within
  // 1.5 s (only asked when needsProbe).
  function chooseTarget(p, reachable) {
    const remote = httpsOnly(p.remote_url);
    const deep = typeof p.url === 'string' && p.url.startsWith('/') && !p.url.startsWith('//') && !p.url.includes('\\') ? p.url : '/#/';
    if (remote && p.tap === 'claude') return { kind: 'claude', url: remote };
    if (remote && p.tap === 'auto' && reachable === false) return { kind: 'claude', url: remote };
    return { kind: 'app', url: deep };
  }

  // The action token for a notification button, or null.
  function actionToken(p, action) {
    const t = p && p.actions && p.actions[action];
    return typeof t === 'string' && /^a1\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$/.test(t) ? t : null;
  }

  // A hash route ("#/sessions/x") from an app deep link ("/#/sessions/x").
  const hashOf = (url) => {
    const i = url.indexOf('#');
    return i >= 0 ? url.slice(i) : '#/';
  };

  // The body for POST /v1/push/targets from PushSubscription.toJSON().
  function targetBody(sub, name) {
    const j = sub && sub.toJSON ? sub.toJSON() : sub;
    const body = { kind: 'webpush', endpoint: j.endpoint, keys: { p256dh: j.keys && j.keys.p256dh, auth: j.keys && j.keys.auth } };
    if (name) body.name = name;
    return body;
  }

  root.SyLib = {
    DATA_CACHE, SESSION_KEEP,
    isDataRequest, isSessionDetail, sessionsToEvict, isNavigation, shellCacheName, isCurrentCache,
    parsePayload, notificationOptions, badgeCount, hasActions,
    needsProbe, chooseTarget, actionToken, hashOf, targetBody,
  };
})(typeof self !== 'undefined' ? self : globalThis);
