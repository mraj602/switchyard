// Owner sign-in: #/owner?s=<secret> from `sy hub owner-link`. Swaps the one-time secret for an
// owner session cookie, then opens Settings.

import { h } from '../components/dom.js';
import { ownerSignIn } from '../api.js';
import { deviceName } from '../platform.js';

const st = { phase: 'idle', error: '' };
const again = () => window.dispatchEvent(new Event('sy-rerender'));

export async function load({ route, onLoggedIn }) {
  const secret = route.query.get('s') || '';
  st.error = '';
  if (!secret) { st.phase = 'error'; st.error = 'This link has no sign-in secret. Run sy hub owner-link on the hub for a new one.'; return; }
  st.phase = 'working';
  again();
  try {
    await ownerSignIn(secret, deviceName(navigator.userAgent, null));
    history.replaceState(null, '', '#/owner'); // the secret leaves the address bar
    st.phase = 'done';
    await onLoggedIn();
    location.hash = '#/more/settings';
  } catch (e) {
    st.phase = 'error';
    st.error = e.status === 401 ? 'This sign-in link was already used or has expired. Run sy hub owner-link on the hub for a new one.'
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
