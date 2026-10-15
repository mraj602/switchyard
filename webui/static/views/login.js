// Login on a computer. The usual way in is an owner link from `sy hub owner-link`; pasting an
// access token (read-only) is the fallback.

import { h } from '../components/dom.js';
import { brandRow } from '../components/ui.js';
import { login, AuthError } from '../api.js';

export function render({ onLoggedIn }) {
  const err = h('p', { class: 'form-error', role: 'alert', hidden: true });
  const input = h('input', {
    id: 'token', name: 'token', type: 'password', autocomplete: 'off', spellcheck: 'false',
    required: true, 'aria-describedby': 'token-help', placeholder: 'sy_b_…',
    // An old error must not sit under a field that is being retyped (L2).
    oninput: () => { err.hidden = true; },
  });
  const submit = h('button', { type: 'submit', class: 'btn' }, 'Sign in with the token');

  const form = h('form', {
    class: 'col login-token', onsubmit: async (e) => {
      e.preventDefault();
      err.hidden = true;
      submit.disabled = true;
      try {
        await login(input.value.trim());
        input.value = '';
        onLoggedIn();
      } catch (ex) {
        err.textContent = ex instanceof AuthError || ex.status === 401 || ex.status === 403
          ? 'That token was not accepted. Check it and try again.' : ex.message;
        err.hidden = false;
        input.focus();
      } finally {
        submit.disabled = false;
      }
    },
  },
    h('label', { for: 'token' }, 'Access token'),
    input,
    h('p', { id: 'token-help', class: 'muted small' }, 'A token only lets this browser read the board. Make one on the hub with ',
      h('code', {}, 'sy hub token create --kind browser'), '.'),
    err, submit);

  const card = h('div', { class: 'login-card card' },
    h('h1', {}, 'Sign in'),
    h('p', {}, 'On the hub machine, run'),
    h('pre', { class: 'cmd mono', 'data-key': 'owner-link-cmd' }, 'sy hub owner-link'),
    h('p', {}, 'and open the link it prints in this browser. That signs you in as the owner, with every setting.'),
    h('details', { class: 'login-more', 'data-key': 'token-login' }, h('summary', {}, 'Use an access token instead'), form),
    h('p', { class: 'small' }, h('a', { href: '#/pair', 'data-key': 'to-pair' }, 'Pair with a 6-digit code instead')));

  return { title: 'Sign in · Switchyard', node: h('div', { class: 'login-wrap' }, h('div', { class: 'login-col' }, brandRow(), card)) };
}
