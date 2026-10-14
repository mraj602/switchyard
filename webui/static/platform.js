// What platform is this? Pure functions over a user-agent string and a few flags, so node
// can test them (pwa.md 7). Nothing here touches the DOM at import.

// Guess "iPhone · Safari" or "Pixel 9 · Chrome" for the device name sent when pairing.
export function deviceName(ua = '', uaData = null) {
  const brand = (() => {
    if (/Edg(e|A|iOS)?\//.test(ua)) return 'Edge';
    if (/(OPR|Opera)\//.test(ua)) return 'Opera';
    if (/SamsungBrowser\//.test(ua)) return 'Samsung Internet';
    if (/(Firefox|FxiOS)\//.test(ua)) return 'Firefox';
    if (/(Chrome|CriOS)\//.test(ua)) return 'Chrome';
    if (/Safari\//.test(ua)) return 'Safari';
    return 'Browser';
  })();
  let device = '';
  if (/iPad/.test(ua)) device = 'iPad';
  else if (/iPhone/.test(ua)) device = 'iPhone';
  else if (/Android/.test(ua)) {
    const m = /Android[^;)]*;\s*([^;)]+?)(?:\s+Build\/[^;)]*)?[;)]/.exec(ua);
    device = (uaData && uaData.model) || (m && m[1] !== 'K' && !/^(wv|Mobile)$/.test(m[1]) ? m[1].trim() : '') || 'Android';
  } else if (/Macintosh/.test(ua)) device = 'Mac';
  else if (/Windows/.test(ua)) device = 'Windows';
  else if (/Linux|X11|CrOS/.test(ua)) device = 'Linux';
  else device = 'This device';
  return `${device} · ${brand}`.slice(0, 60);
}

export const isIOS = (ua = '', maxTouchPoints = 0, platform = '') =>
  /iPhone|iPad|iPod/.test(ua) || (platform === 'MacIntel' && maxTouchPoints > 1); // iPadOS reports a Mac

export const isAndroid = (ua = '') => /Android/.test(ua);

// A phone or tablet: the pairing code kind is `phone` there, `browser` on a computer.
export const isMobile = (ua = '', maxTouchPoints = 0, platform = '') =>
  isIOS(ua, maxTouchPoints, platform) || isAndroid(ua) || /Mobile/.test(ua);

// iOS version as [major, minor], or null when unknown or not iOS.
export function iosVersion(ua = '') {
  const m = /OS (\d+)[_.](\d+)/.exec(ua);
  return m && /iPhone|iPad|iPod/.test(ua) ? [Number(m[1]), Number(m[2])] : null;
}

// Web Push on iOS and iPadOS needs 16.4.
export function iosNeedsUpgrade(ua = '') {
  const v = iosVersion(ua);
  return !!v && (v[0] < 16 || (v[0] === 16 && v[1] < 4));
}

// Which notification state to show in Settings (pwa.md 6). `env` carries what the page can
// see: { ua, standalone, hasPush, hasNotification, permission, subscribed, touch, platform }.
export function notificationState(env) {
  const ios = isIOS(env.ua, env.touch, env.platform);
  if (ios && iosNeedsUpgrade(env.ua)) return 'ios-old';
  if (ios && !env.standalone) return 'install-first';
  if (!env.hasPush || !env.hasNotification) return 'unsupported';
  if (env.permission === 'denied') return 'blocked';
  if (env.permission === 'granted' && env.subscribed) return 'on';
  return 'off';
}

// 6 digits shown as 482-913, from whatever was typed.
export function normalizeCode(text) {
  const d = String(text || '').replace(/\D/g, '').slice(0, 6);
  return d.length > 3 ? `${d.slice(0, 3)}-${d.slice(3)}` : d;
}
export const codeDigits = (text) => String(text || '').replace(/\D/g, '');
export const validCode = (text) => codeDigits(text).length === 6;

// applicationServerKey for pushManager.subscribe from the hub's base64url public key.
export function urlBase64ToBytes(b64) {
  const pad = '='.repeat((4 - (b64.length % 4)) % 4);
  const raw = atob((b64 + pad).replace(/-/g, '+').replace(/_/g, '/'));
  return Uint8Array.from(raw, (c) => c.charCodeAt(0));
}

export function bytesToUrlBase64(buf) {
  const bytes = new Uint8Array(buf);
  let s = '';
  for (const b of bytes) s += String.fromCharCode(b);
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

// Do two applicationServerKey values (ArrayBuffer or null) match the hub's base64url key?
export function sameKey(buf, b64) {
  if (!buf) return false;
  return bytesToUrlBase64(buf) === b64.replace(/=+$/, '');
}

// ntfy://host/topic for the ntfy app (uncertain support; the server and topic always work).
export function ntfyLink(server, topic) {
  try { return `ntfy://${new URL(server).host}/${topic}`; } catch (_) { return ''; }
}

// ---- the Switchyard phone app (Expo) ------------------------------------------------------

// "App · iPhone · 0.8.0" for an expo push target (platform ios|android, app_version).
export function expoTargetLabel(t) {
  const plat = t && t.platform === 'ios' ? 'iPhone' : t && t.platform === 'android' ? 'Android' : '';
  return ['App', plat, t && t.app_version].filter(Boolean).join(' · ');
}

// The link that opens the installed app on its pairing screen: switchyard://pair?u=&s=&c=
// (phone.md P2). `c` is left out when there is no code. Never log the result: it carries the secret.
export function appPairLink(origin, secret, code) {
  if (!origin || !secret) return '';
  const digits = codeDigits(code);
  return `switchyard://pair?u=${encodeURIComponent(origin)}&s=${encodeURIComponent(secret)}${digits ? `&c=${encodeURIComponent(digits)}` : ''}`;
}

// The body of PUT /v1/push/channels/expo (push.md 12.5). The access token is write-only: an empty
// `token` leaves the stored one alone, `clear` removes it, and access_token_set is never sent back.
export function expoChannelBody({ enabled, baseUrl, token, clear }) {
  const settings = {};
  const base = String(baseUrl || '').trim().replace(/\/+$/, '');
  if (base) settings.base_url = base;
  if (clear) settings.access_token = '';
  else if (String(token || '').trim()) settings.access_token = String(token).trim();
  return { enabled: !!enabled, settings };
}
