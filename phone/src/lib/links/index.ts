// Deep links (phone.md P7). Owner: WP-N. Pure: no URL class, no platform imports.
// Two vocabularies map onto AppRoute: a push payload's `url` (a PWA hash route) and an incoming
// switchyard:// URL (the allow-list). Anything else is refused (null) or opens Now.
import { INCOMING_HOSTS, ROUTE_ID, ROUTE_PATHS, SCHEME } from '../../contract/routes';
import type { AppRoute, SessionFilter } from '../../contract/routes';

const NOW: AppRoute = { name: 'now' };

const FILTERS: readonly string[] = ['needs_you', 'working', 'done', 'all', 'ended'];
const BY = /^[a-z_]{1,20}$/;
const SECRET = /^[A-Za-z0-9_-]{22}$/;
const CODE = /^\d{6}$/;
const HUB_FP = /^[a-z2-7]{24}$/i;
const ALERT_ID = /^al_[A-Za-z0-9_.:-]{1,77}$/;

/** An id from a link: the P7 pattern, and never a dot segment. */
function isId(s: string | undefined): s is string {
  return typeof s === 'string' && ROUTE_ID.test(s) && !s.includes('..');
}

function isAlertId(s: string | undefined): s is string {
  return typeof s === 'string' && ALERT_ID.test(s) && !s.includes('..');
}

function decode(s: string): string | null {
  try {
    return decodeURIComponent(s.replace(/\+/g, ' '));
  } catch {
    return null;
  }
}

/** First value of each query key; undecodable pairs are dropped. */
function parseQuery(q: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const pair of q.split('&')) {
    if (!pair) continue;
    const eq = pair.indexOf('=');
    const k = decode(eq < 0 ? pair : pair.slice(0, eq));
    const v = decode(eq < 0 ? '' : pair.slice(eq + 1));
    if (k === null || v === null) continue;
    if (!(k in out)) out[k] = v;
  }
  return out;
}

/** "a/b?x=1#frag" to segments (no empty ones from a trailing slash) and query. */
function splitPath(s: string): { segs: string[]; query: Record<string, string> } | null {
  const hash = s.indexOf('#');
  const noFrag = hash >= 0 ? s.slice(0, hash) : s;
  const qi = noFrag.indexOf('?');
  const path = qi >= 0 ? noFrag.slice(0, qi) : noFrag;
  const query = parseQuery(qi >= 0 ? noFrag.slice(qi + 1) : '');
  const raw = path.split('/');
  while (raw.length && raw[raw.length - 1] === '') raw.pop();
  const segs = raw[0] === '' ? raw.slice(1) : raw;
  // An empty segment in the middle ("a//b") is never valid.
  if (segs.some((x) => x === '')) return null;
  return { segs, query };
}

function sessionsRoute(query: Record<string, string>): AppRoute {
  const r: Extract<AppRoute, { name: 'sessions' }> = { name: 'sessions' };
  if (query.filter && FILTERS.includes(query.filter)) r.filter = query.filter as SessionFilter;
  if (query.by && BY.test(query.by)) r.by = query.by;
  return r;
}

/** The routes both vocabularies share. null = not a known route. */
function commonRoute(segs: string[], query: Record<string, string>): AppRoute | null {
  const [head, id] = segs;
  switch (head) {
    case 'sessions':
      if (segs.length === 1) return sessionsRoute(query);
      if (segs.length === 2 && isId(id)) {
        const r: Extract<AppRoute, { name: 'session' }> = { name: 'session', id };
        if (isAlertId(query.alert)) r.alert = query.alert;
        return r;
      }
      return null;
    case 'accounts':
      if (segs.length !== 1) return null;
      return isId(query.seat) ? { name: 'accounts', seat: query.seat } : { name: 'accounts' };
    case 'machines':
      if (segs.length !== 1) return null;
      return isId(query.machine) ? { name: 'machines', machine: query.machine } : { name: 'machines' };
    case 'alerts':
      return segs.length === 2 && isAlertId(id) ? { name: 'alert', id } : null;
    default:
      return null;
  }
}

/** A payload `url` ("/#/sessions/<id>?alert=…") to an app route; null when refused. */
export function hashToRoute(url: string): AppRoute | null {
  if (typeof url !== 'string') return null;
  // Like sw.js: a single leading slash, and no backslash (browsers read "/\" as "//").
  if (!url.startsWith('/') || url.startsWith('//') || url.includes('\\')) return null;
  const hash = url.indexOf('#');
  if (hash < 0) return null;
  const rest = url.slice(hash + 1);
  if (!rest.startsWith('/')) return null;
  const p = splitPath(rest);
  if (!p) return null;
  const { segs, query } = p;
  if (segs.length === 0) return NOW;
  if (segs[0] === 'more') {
    if (segs.length === 1) return { name: 'more' };
    if (segs.length === 2 && segs[1] === 'alerts') return { name: 'alerts-settings' };
    if (segs.length === 2 && segs[1] === 'settings') return { name: 'settings' };
    if (segs.length === 2 && segs[1] === 'peers') return { name: 'peers' };
    if (segs.length === 2 && segs[1] === 'analytics') return { name: 'analytics' };
    return null;
  }
  return commonRoute(segs, query);
}

function pairRoute(query: Record<string, string>): AppRoute {
  const r: Extract<AppRoute, { name: 'pair' }> = { name: 'pair' };
  const u = query.u;
  // Origin only: https, a plain host, no user info, path, query or backslash (parsePairInput
  // checks it again before anything is claimed).
  // http is let through to the pair screen, whose parsePairInput refuses it in a build that is
  // not a development one (phone.md P2).
  if (u && /^https?:\/\/[A-Za-z0-9.-]{1,253}(:\d{1,5})?\/?$/.test(u)) r.origin = u.replace(/\/$/, '');
  if (query.s && SECRET.test(query.s)) r.secret = query.s;
  if (query.c && CODE.test(query.c)) r.code = query.c;
  // keys.md 7: the QR's hub fingerprint and registration seal key. Without them a secret claim
  // would pair with no instant delivery, so a link that has them must carry them.
  if (query.h && HUB_FP.test(query.h)) r.hubFp = query.h.toLowerCase();
  if (query.k && SECRET.test(query.k)) r.sealKey = query.k;
  return r;
}

/** An incoming switchyard:// URL through the allow-list; anything else is Now. */
export function incomingToRoute(url: string): AppRoute {
  if (typeof url !== 'string' || url.includes('\\') || /[\s\u0000-\u001f]/.test(url)) return NOW;
  const m = /^([A-Za-z][A-Za-z0-9+.-]*):\/\/(.*)$/s.exec(url);
  if (!m || m[1]!.toLowerCase() !== SCHEME) return NOW;
  if (m[2]!.startsWith('/')) return NOW; // switchyard:///x has no host
  const p = splitPath(m[2]!);
  if (!p || p.segs.length === 0) return NOW;
  const host = p.segs[0]!.toLowerCase();
  if (!(INCOMING_HOSTS as readonly string[]).includes(host)) return NOW;
  const segs = [host, ...p.segs.slice(1)];
  if (host === 'now') return NOW;
  if (host === 'pair') return segs.length === 1 ? pairRoute(p.query) : NOW;
  if (host === 'settings') {
    // switchyard://settings/notifications: the link's persistent notification (push.md 14.4).
    // Only that one page: switchyard://settings alone stays off the allow-list.
    return segs.length === 2 && segs[1] === 'notifications' ? { name: 'notify-settings' } : NOW;
  }
  return commonRoute(segs, p.query) ?? NOW;
}

function qs(params: Record<string, string | undefined>): string {
  const parts = Object.entries(params)
    .filter((e): e is [string, string] => typeof e[1] === 'string' && e[1] !== '')
    .map(([k, v]) => `${k}=${encodeURIComponent(v)}`);
  return parts.length ? `?${parts.join('&')}` : '';
}

/** The expo-router href of a route ("/sessions/abc?alert=al_1"). */
export function routeToHref(route: AppRoute): string {
  switch (route.name) {
    case 'sessions':
      return ROUTE_PATHS.sessions + qs({ filter: route.filter, by: route.by });
    case 'session':
      return `/sessions/${encodeURIComponent(route.id)}${qs({ alert: route.alert })}`;
    case 'accounts':
      return ROUTE_PATHS.accounts + qs({ seat: route.seat });
    case 'machines':
      return ROUTE_PATHS.machines + qs({ machine: route.machine });
    case 'alert':
      return `/alerts/${encodeURIComponent(route.id)}`;
    case 'pair':
      // The names app/pair.tsx reads with useLocalSearchParams (not the link's u/s/c/h/k).
      return ROUTE_PATHS.pair + qs({ origin: route.origin, secret: route.secret, code: route.code, hub: route.hubFp, key: route.sealKey });
    default:
      return ROUTE_PATHS[route.name];
  }
}

/**
 * app/+native-intent.ts: whatever the OS hands expo-router, to an href on the allow-list. A
 * bare path ("/sessions/x", as some Android intents arrive) is read as switchyard://sessions/x.
 * A pair link only opens the Pair screen pre-filled; it never claims by itself.
 */
export function nativeIntentToHref(path: string): string {
  if (typeof path !== 'string') return '/';
  if (/^[A-Za-z][A-Za-z0-9+.-]*:/.test(path)) return routeToHref(incomingToRoute(path));
  if (path.startsWith('/') && !path.startsWith('//') && !path.includes('\\')) {
    return routeToHref(incomingToRoute(`${SCHEME}://${path.slice(1)}`));
  }
  return '/';
}
