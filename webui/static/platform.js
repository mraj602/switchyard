// What platform is this? Pure functions over a user-agent string and a few flags, so node
// can test them (pwa.md 7). Nothing here touches the DOM at import.

// Guess "iPhone · Safari" or "Pixel 9 · Chrome" for the device name sent when pairing.
export function deviceName(ua = '', uaData = null) {
  // A scripted browser says so in its user agent; name it for what it is (DV1).
  if (/HeadlessChrome\//.test(ua)) return 'Headless Chrome';
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

// ---- installing the board, and the Switchyard phone app -----------------------------------

// How to install the board as an app in this browser (SE3), or '' when the page itself can
// offer the install prompt. `ios` and `standalone` come from the page.
export function installHint(ua = '', { ios = false, standalone = false } = {}) {
  if (standalone) return 'Installed: you are running the app.';
  if (ios) return 'Tap Share, then Add to Home Screen. Open Switchyard from your Home Screen.';
  if (isAndroid(ua)) return 'Open the browser menu and choose Install app (or Add to Home screen).';
  if (/(Firefox|FxiOS)\//.test(ua)) return 'Optional: Firefox does not install web apps on a computer. The board works the same in a tab.';
  if (/Edg\//.test(ua)) return 'Optional: open the browser menu, then Apps, then Install this site as an app.';
  if (/(Chrome|Chromium)\//.test(ua)) return 'Optional: use the install icon at the right end of the address bar, or the browser menu, then Cast, save and share, then Install page as app.';
  if (/Macintosh/.test(ua) && /Safari\//.test(ua)) return 'Optional: choose File, then Add to Dock.';
  return 'Optional: if your browser can install web apps, look for Install in its menu.';
}

// The link that opens the installed app on its pairing screen: switchyard://pair?u=&s=&c=&h=&k=
// (phone.md P2). `c`, `h` and `k` are left out when absent. `h` (the hub key's fingerprint) and `k`
// (the registration seal key) are the QR's keys.md 7 pins: without them the app claims a secret
// with no keys and gets no instant delivery. Never log the result: it carries the secret.
export function appPairLink(origin, secret, code, hubFp, sealKey) {
  if (!origin || !secret) return '';
  const digits = codeDigits(code);
  return `switchyard://pair?u=${encodeURIComponent(origin)}&s=${encodeURIComponent(secret)}${digits ? `&c=${encodeURIComponent(digits)}` : ''}` +
    `${hubFp ? `&h=${encodeURIComponent(hubFp)}` : ''}${sealKey ? `&k=${encodeURIComponent(sealKey)}` : ''}`;
}
