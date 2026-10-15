// Owner sign-in: #/owner?s=<secret> from `sy hub owner-link`. Swaps the one-time secret for an
// owner session cookie, then opens Settings.

import { h } from '../components/dom.js';
import { ownerSignIn } from '../api.js';
import { deviceName } from '../platform.js';

const st = { phase: 'idle', error: '' };
const again = () => window.dispatchEvent(new Event('sy-rerender'));
// The secret this view already sent. It leaves the address bar at once, so a second load of
// the same visit (the stripped #/owner, or a repeated render) shows that result, not a
// second POST; a different secret (a fresh link pasted into this tab) is tried.
let sent = '';

const NEW_LINK = 'Run sy hub owner-link on the hub for a new one.';
const clock = (at) => { const d = at ? new Date(at) : null; return d && !isNaN(d) ? ` (${d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })})` : ''; };
function refusal(e) {
  switch (e.code) {
    case 'link_used': return `This sign-in link was already used${clock(e.at)}. ${NEW_LINK}`;
    case 'link_expired': return `This sign-in link expired${clock(e.at)}: links work for 10 minutes. ${NEW_LINK}`;
    case 'link_cancelled': return `This sign-in link was cancelled. ${NEW_LINK}`;
    case 'link_unknown': return `This hub does not know that sign-in link. Check that the whole link was opened, or: ${NEW_LINK}`;
  }
  return `This sign-in link was already used or has expired. ${NEW_LINK}`;
}

export async function load({ route, onLoggedIn }) {
  const secret = route.query.get('s') || '';
  if (secret) history.replaceState(null, '', '#/owner'); // the secret leaves the address bar, success or not
  if (secret ? secret === sent : sent) return;
  sent = secret;
  st.error = '';
  if (!secret) { st.phase = 'error'; st.error = `This link has no sign-in secret. ${NEW_LINK}`; return; }
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
      h('a', { class: 'btn', href: '#/login' }, 'Sign in with a token instead'))
    : h('div', { class: 'card login-card' }, h('h1', {}, 'Owner sign-in'), h('p', { class: 'muted', role: 'status' }, 'Signing in…'));
  return { title: 'Owner sign-in · Switchyard', node: h('div', { class: 'login-wrap' }, body) };
}
