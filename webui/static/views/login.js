// Login: paste a token, the hub answers with a session cookie.

import { h } from '../components/dom.js';
import { login, AuthError } from '../api.js';

export function render({ onLoggedIn }) {
  const err = h('p', { class: 'form-error', role: 'alert', hidden: true });
  const input = h('input', {
    id: 'token', name: 'token', type: 'password', autocomplete: 'off', spellcheck: 'false',
    required: true, 'aria-describedby': 'token-help', placeholder: 'sy_b_…',
  });
  const submit = h('button', { type: 'submit', class: 'btn btn-primary' }, 'Sign in');

  const form = h('form', {
    class: 'login-card card', onsubmit: async (e) => {
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
    h('h1', {}, 'Switchyard'),
    h('p', { class: 'muted' }, 'Paste an access token to open the board.'),
    h('label', { for: 'token' }, 'Access token'),
    input,
    h('p', { id: 'token-help', class: 'muted small' }, 'Make one on the hub machine with: sy hub token create --kind browser --name "my browser"'),
    err, submit,
    h('p', { class: 'small' }, h('a', { href: '#/pair', 'data-key': 'to-pair' }, 'Pair with a code instead')));

  return { title: 'Sign in · Switchyard', node: h('div', { class: 'login-wrap' }, form) };
}
