// Owner sign-in: #/owner?s=<secret> from an owner device (More, Devices, Make another device
// owner), the hub's first start or `wy hub owner-link`. Swaps the one-time secret for an owner
// session cookie, then opens Settings.

import { h } from '../components/dom.js';
import { ownerSignIn } from '../api.js';
import { brandRow } from '../components/ui.js';
import { deviceName } from '../platform.js';
import { inApp } from '../store.js';
import { clock as clockOf, lifetime } from '../format.js';

const st = { phase: 'idle', error: '' };
const again = () => window.dispatchEvent(new Event('wy-rerender'));
// The secret this view already sent. It leaves the address bar at once, so a second load of
// the same visit (the stripped #/owner, or a repeated render) shows that result, not a
// second POST; a different secret (a fresh link pasted into this tab) is tried.
let sent = '';

// Where a new link comes from: an owner device first; the hub's computer only in a browser
// (inside the desktop app, the Hub tab's Sign in as owner).
const newLink = () => (inApp() ? 'Use Sign in as owner on the Hub tab for a new one.'
  : 'Get a new one on an owner device (More, Devices, Make another device owner), or run wy hub owner-link on the hub’s computer.');
const clock = (at) => { const t = at ? clockOf(at) : ''; return t ? ` (${t})` : ''; };
// lasted is how long the link worked: the hub sends the lifetime it was made with
// (`wy hub owner-link --ttl`), which is not always the default 10 minutes.
const lasted = (ttlS) => { const d = lifetime(ttlS); return d ? `: it worked for ${d} after it was made` : ''; };
function refusal(e) {
  switch (e.code) {
    case 'link_used': return `This sign-in link was already used${clock(e.at)}. ${newLink()}`;
    case 'link_expired': return `This sign-in link expired${clock(e.at)}${lasted(e.ttl_s)}. ${newLink()}`;
    case 'link_cancelled': return `This sign-in link was cancelled. ${newLink()}`;
    case 'link_unknown': return `This hub does not know that sign-in link. Check that the whole link was opened, or: ${newLink()}`;
  }
  return `This sign-in link was already used or has expired. ${newLink()}`;
}

export async function load({ route, onLoggedIn }) {
  const secret = route.query.get('s') || '';
  if (secret) history.replaceState(null, '', '#/owner'); // the secret leaves the address bar, success or not
  if (secret ? secret === sent : sent) return;
  sent = secret;
  st.error = '';
  if (!secret) { st.phase = 'error'; st.error = `This link has no sign-in secret. ${newLink()}`; return; }
  st.phase = 'working';
  again();
  try {
    await ownerSignIn(secret, deviceName(navigator.userAgent, null));
    st.phase = 'done';
    await onLoggedIn();
    location.hash = '#/more/settings';
  } catch (e) {
    st.phase = 'error';
    st.error = e.status === 401 ? refusal(e)
      : e.status === 429 ? 'Too many tries. Wait a few minutes and try again.' : e.message;
    again();
  }
}

export function render() {
  const body = st.phase === 'error'
    ? h('div', { class: 'card login-card' }, h('h1', {}, 'Owner sign-in'), h('p', { class: 'form-error', role: 'alert', 'data-key': 'owner-error' }, st.error),
      inApp() ? null : h('a', { class: 'btn', href: '#/login', 'data-key': 'owner-other-ways' }, 'Other ways to sign in'))
    : h('div', { class: 'card login-card' }, h('h1', {}, 'Owner sign-in'), h('p', { class: 'muted', role: 'status' }, 'Signing in…'));
  return { title: 'Owner sign-in · Watchyard', node: h('div', { class: 'login-wrap' }, h('div', { class: 'login-col' }, brandRow(), body)) };
}
