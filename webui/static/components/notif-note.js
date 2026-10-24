// Whether the board runs inside the desktop app, how a non-owner becomes the owner, and what
// the Notifications row says when this sign-in cannot get Web Push (settings.js, SE5): the
// desktop app shows the hub's alerts itself (desktop.md C9), a phone pairs with a code, a
// desktop browser signs in as the owner or pairs.

// The desktop app's Hub view signs in with a board session the hub records as a browser device
// made from the desktop device: /v1/me names that device in `via` (api.yaml Me). An owner
// session opened in the app keeps the same `via`. The desktop token itself is kind desktop.
export function meInApp(me) {
  if (!me) return false;
  return !!((me.via && me.via.kind === 'desktop') || (me.device && me.device.kind === 'desktop'));
}

// The desktop app marks its Hub view so the board knows it is there even signed out (no
// /v1/me): a user agent token "WatchyardDesktop/<version>" on the view's session, or
// "?in=desktop" in the start URL (before the #). Pure: ua is navigator.userAgent, search
// location.search.
const UA_TOKEN = 'WatchyardDesktop';
export function markerInApp(ua, search) {
  const s = String(ua || '');
  if (new RegExp(`\\s${UA_TOKEN}/`).test(s)) return true;
  try { return new URLSearchParams(String(search || '')).get('in') === 'desktop'; } catch (_) { return false; }
}

// The one owner hint for a non-owner (flows TARGET, Browser board): in the app, the Hub tab has
// the button; in a browser, an owner link from an owner device, with the CLI only as the
// fold-out (OWNER_CLI).
export const OWNER_LINK_HOW = 'Sign in as owner: open an owner link from an owner device (More, Devices, Make another device owner).';
export const OWNER_APP_HOW = 'Use Sign in as owner on the Hub tab.';
export const OWNER_CLI = 'wy hub owner-link';
export function ownerHint(app) {
  return app ? OWNER_APP_HOW : OWNER_LINK_HOW;
}

// The same, as a clause after "sign in as the owner (…)".
export function ownerHow(app) {
  return app ? 'use Sign in as owner on the Hub tab' : 'open an owner link from an owner device: More, Devices, Make another device owner';
}

export function notifNote({ app, mobile } = {}) {
  if (app) {
    return { who: 'This app', state: 'The desktop app shows these notifications itself.', how: 'app-settings' };
  }
  return { who: mobile ? 'This phone' : 'This browser', state: 'Off: this sign-in only reads the board.', how: mobile ? 'pair-phone' : 'owner-or-pair' };
}

// ownerLinkTarget checks an owner link pasted on the sign-in page: it must be this hub's own
// origin and an #/owner?s= link. It answers {hash} to open, or {error} (and {other, link}: the
// other hub's origin and the pasted link, https or http only) to show.
export function ownerLinkTarget(text, origin) {
  const raw = String(text || '').trim();
  if (!raw) return { error: 'Paste the owner link first.' };
  let u;
  try { u = new URL(raw, origin); } catch (_) { return { error: 'That is not a link. Paste the whole owner link.' }; }
  if (u.origin !== origin) {
    const web = u.protocol === 'https:' || u.protocol === 'http:';
    return { error: web ? `That link is for ${u.origin}, not this hub (${origin}). Open it there.` : 'That is not an owner link. An owner link starts with https://.', ...(web ? { other: u.origin, link: u.href } : {}) };
  }
  if (!u.hash.startsWith('#/owner?') || !new URLSearchParams(u.hash.slice('#/owner?'.length)).get('s')) {
    return { error: 'That is not an owner link. An owner link ends in #/owner?s=…' };
  }
  return { hash: u.hash };
}

// The reason line of the sign-in page after a 401 (api.yaml Unauthorized reason), or ''.
export function signedOutReason(reason, mobile) {
  if (reason === 'revoked') return mobile ? 'This phone was removed from the hub.' : 'This browser was removed from the hub.';
  if (reason === 'owner_expired') return 'Your owner access ended.';
  if (reason === 'expired') return 'Your sign-in ended.';
  return '';
}
