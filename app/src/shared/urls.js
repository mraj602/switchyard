// URL rules of the desktop app (docs/spec/desktop.md C2, C5, C7). Pure functions over strings:
// no Node, no Electron, so main/security.js, the tabs and hub modules, the renderer and the tests
// all share one set of rules. Every function takes untrusted input and never throws.
//
// The policies themselves (what each view may load, what goes to the system
// browser) are applied by main/security.js; this file only answers questions about a URL.

import {
  CLAUDE_HOSTS,
  SIGN_IN_HOSTS,
  NOT_READABLE_PATHS,
  CLAUDE_SESSION_LINK_PATTERN,
  SHELL_ORIGIN,
  TAB_FRAME_HOSTS,
  INERT_FRAME_URLS,
} from './constants.js';

const SESSION_LINK_RE = new RegExp(CLAUDE_SESSION_LINK_PATTERN);

/**
 * Parses s as an absolute URL; null when it is not a string, too long, or not a URL.
 * @param {unknown} s
 * @returns {URL|null}
 */
export function parse(s) {
  if (s instanceof URL) return s;
  if (typeof s !== 'string' || s.length === 0 || s.length > 8192) return null;
  try {
    return new URL(s);
  } catch {
    return null;
  }
}

/** True when host equals base or is a subdomain of it (case-insensitive, no trailing dot). */
export function hostIs(host, base) {
  if (typeof host !== 'string' || typeof base !== 'string' || !host || !base) return false;
  const h = host.toLowerCase().replace(/\.$/, '');
  const b = base.toLowerCase();
  return h === b || h.endsWith(`.${b}`);
}

/** True for a loopback host: localhost, 127.0.0.0/8 or [::1]. */
export function isLoopbackHost(host) {
  if (typeof host !== 'string') return false;
  const h = host.toLowerCase().replace(/^\[|\]$/g, '');
  if (h === 'localhost' || h.endsWith('.localhost') || h === '::1') return true;
  return /^127\.\d{1,3}\.\d{1,3}\.\d{1,3}$/.test(h);
}

/**
 * The origin of a test claude.ai stand-in (WY_APP_TEST_CLAUDE_ORIGIN through test-hooks.js),
 * normalised; null unless it is an http(s) URL on a loopback host.
 * @param {unknown} s
 */
export function testOrigin(s) {
  const u = parse(s);
  if (!u || (u.protocol !== 'http:' && u.protocol !== 'https:') || !isLoopbackHost(u.hostname)) return null;
  return u.origin;
}

/** True when two URLs (or origins) have the same scheme, host and port. */
export function sameOrigin(a, b) {
  const x = parse(a);
  const y = parse(b);
  if (!x || !y || x.origin === 'null' || y.origin === 'null') return false;
  return x.origin === y.origin;
}

/**
 * True for a claude.ai page: https://claude.ai or a subdomain, or the test origin when one is
 * given (opts.claudeOrigin, unpackaged only).
 * @param {unknown} url
 * @param {{claudeOrigin?: string|null}} [opts]
 */
export function isClaudeUrl(url, opts = {}) {
  const u = parse(url);
  if (!u) return false;
  if (opts.claudeOrigin && sameOrigin(u, opts.claudeOrigin)) return true;
  return u.protocol === 'https:' && !u.username && !u.password && CLAUDE_HOSTS.some((h) => hostIs(u.hostname, h));
}

/** True for an https sign-in hop kept inside an account tab (anthropic.com, claude.com, Cloudflare challenges). */
export function isSignInHop(url) {
  const u = parse(url);
  return !!u && u.protocol === 'https:' && !u.username && !u.password && SIGN_IN_HOSTS.some((h) => hostIs(u.hostname, h));
}

/**
 * True when an account tab may load url itself (C2): claude.ai or a sign-in hop, https only
 * (or the test origin).
 * @param {unknown} url
 * @param {{claudeOrigin?: string|null}} [opts]
 */
export function tabMayLoad(url, opts = {}) {
  return isClaudeUrl(url, opts) || isSignInHop(url);
}

/** True for a URL shell.openExternal may receive: https: or mailto: only. */
export function isExternalAllowed(url) {
  const u = parse(url);
  if (!u) return false;
  if (u.protocol === 'https:') return !!u.hostname && !u.username && !u.password;
  if (u.protocol === 'mailto:') return u.pathname.length > 0;
  return false;
}

/** The only mailto: header fields passed on (no attach=, from=, bcc= or client-specific ones). */
export const MAILTO_KEYS = Object.freeze(['to', 'subject', 'cc', 'body']);

const decodePart = (v) => {
  try {
    return decodeURIComponent(v);
  } catch {
    return null;
  }
};

/**
 * The URL to hand to shell.openExternal, or null when it is not allowed (isExternalAllowed).
 * https: is passed as parsed; mailto: is rebuilt with its addresses and only the MAILTO_KEYS
 * fields (case-insensitive), every value decoded and encoded again.
 */
export function externalUrl(url) {
  if (!isExternalAllowed(url)) return null;
  const u = parse(url);
  if (u.protocol !== 'mailto:') return u.href;
  const q = [];
  for (const part of u.search.replace(/^\?/, '').split('&')) {
    if (!part) continue;
    const i = part.indexOf('=');
    const key = decodePart(i < 0 ? part : part.slice(0, i))?.toLowerCase();
    const value = decodePart(i < 0 ? '' : part.slice(i + 1));
    if (key === undefined || value === null || !MAILTO_KEYS.includes(key)) continue;
    q.push(`${key}=${encodeURIComponent(value)}`);
  }
  return `mailto:${u.pathname}${q.length ? `?${q.join('&')}` : ''}`;
}

/** True for https: only (the hub view's external rule). */
export function isHttps(url) {
  const u = parse(url);
  return !!u && u.protocol === 'https:' && !!u.hostname;
}

/** True for a board link that hands off to an account tab: https://claude.ai/code/session_… (C7). */
export function isClaudeSessionLink(url) {
  return typeof url === 'string' && SESSION_LINK_RE.test(url);
}

/** True for a URL of the shell itself (app://shell/…). */
export function isShellUrl(url) {
  return typeof url === 'string' && url.startsWith(`${SHELL_ORIGIN}/`);
}

/**
 * True when a tab on url can be read (C5): a claude.ai URL whose path is not /login or /logout
 * (or under them). The other readable conditions (reading on, warning accepted, not stopped)
 * are tab state, checked by the usage module.
 * @param {unknown} url
 * @param {{claudeOrigin?: string|null}} [opts]
 */
export function isReadablePage(url, opts = {}) {
  const u = parse(url);
  if (!u || !isClaudeUrl(u, opts)) return false;
  const p = u.pathname.replace(/\/+$/, '') || '/';
  return !NOT_READABLE_PATHS.some((np) => p === np || p.startsWith(`${np}/`));
}

/**
 * What an account tab does with a link it tries to open or navigate to (C2):
 * 'tab' = load it in the same tab, 'external' = the system browser, 'deny' = drop it.
 * @param {unknown} url
 * @param {{claudeOrigin?: string|null}} [opts]
 * @returns {'tab'|'external'|'deny'}
 */
export function tabLinkAction(url, opts = {}) {
  if (tabMayLoad(url, opts)) return 'tab';
  if (isExternalAllowed(url)) return 'external';
  return 'deny';
}

/** True for about:blank and about:srcdoc (with or without a fragment): a subframe with no network load. */
export function isInertFrameUrl(url) {
  if (typeof url !== 'string') return false;
  const base = url.split('#')[0].toLowerCase();
  return INERT_FRAME_URLS.includes(base);
}

/**
 * The URL a blob: URL was made by (blob:https://claude.ai/<uuid> → https://claude.ai), else null.
 * @param {URL} u
 */
function blobOrigin(u) {
  if (u.protocol !== 'blob:') return null;
  const inner = parse(u.pathname);
  return inner && inner.origin !== 'null' ? inner : null;
}

/**
 * May a subframe of an account tab load url (C2)? https on a TAB_FRAME_HOSTS host (or a
 * subdomain), the test origin, about:blank / about:srcdoc, or a blob: URL made by an allowed
 * origin. Everything else is blocked.
 * @param {unknown} url
 * @param {{claudeOrigin?: string|null}} [opts]
 */
export function tabFrameAllowed(url, opts = {}) {
  if (isInertFrameUrl(url)) return true;
  const u = parse(url);
  if (!u) return false;
  const inner = blobOrigin(u);
  if (inner) return tabFrameAllowed(inner.href, opts);
  if (opts.claudeOrigin && sameOrigin(u, opts.claudeOrigin)) return true;
  return u.protocol === 'https:' && !u.username && !u.password && TAB_FRAME_HOSTS.some((h) => hostIs(u.hostname, h));
}

/**
 * May a subframe of the hub view load url (C2)? The hub's own origin (or a blob: URL it made),
 * about:blank and about:srcdoc only.
 * @param {unknown} url
 * @param {{hubOrigin?: string|null}} [opts]
 */
export function hubFrameAllowed(url, opts = {}) {
  if (isInertFrameUrl(url)) return true;
  const u = parse(url);
  if (!u || !opts.hubOrigin) return false;
  const inner = blobOrigin(u);
  return sameOrigin(inner ?? u, opts.hubOrigin);
}

/** The host of url for a log line (no path or query, which may carry secrets); the scheme when there is no host. */
export function hostForLog(url) {
  const u = parse(url);
  if (!u) return '(not a URL)';
  return (u.host || u.protocol).slice(0, 253);
}

/**
 * What the hub view does with a link (C2, C7): 'hub' = load it in the hub view (same origin),
 * 'session' = hand off to an account tab, 'external' = the system browser (https only),
 * 'deny' = drop it.
 * @param {unknown} url
 * @param {{hubOrigin?: string|null}} [opts]
 * @returns {'hub'|'session'|'external'|'deny'}
 */
export function hubLinkAction(url, opts = {}) {
  if (opts.hubOrigin && sameOrigin(url, opts.hubOrigin)) return 'hub';
  if (isClaudeSessionLink(url)) return 'session';
  if (isHttps(url) && isExternalAllowed(url)) return 'external';
  return 'deny';
}
