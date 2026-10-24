// Signed out (#/login): "Sign in to this hub" (flows TARGET, Browser board). Three ways in: open
// an owner link (pasted here, or opened from an owner device: More, Devices, Make another
// device owner), pair this browser with a code (read-only; the code comes from an owner device),
// or, as a fold-out, `wy hub owner-link` on the hub's computer. A read-only access token stays
// as a last fold-out. After a 401 a reason line says why. Inside the desktop app no CLI wording
// is shown: the app's Hub tab pairs and signs in.

import { h } from '../components/dom.js';
import { brandRow } from '../components/ui.js';
import { signedOutReason, ownerLinkTarget, OWNER_CLI } from '../components/notif-note.js';
import { login, AuthError } from '../api.js';
import { inApp, lostReason } from '../store.js';
import { isMobile } from '../platform.js';

function tokenForm(onLoggedIn) {
  const err = h('p', { class: 'form-error', role: 'alert', hidden: true });
  const input = h('input', {
    id: 'token', name: 'token', type: 'password', autocomplete: 'off', spellcheck: 'false',
    required: true, 'aria-describedby': 'token-help', placeholder: 'wy_b_…',
    // An old error must not sit under a field that is being retyped (L2).
    oninput: () => { err.hidden = true; },
  });
  const submit = h('button', { type: 'submit', class: 'btn' }, 'Sign in with the token');
  return h('form', {
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
      h('code', {}, 'wy hub token create --kind browser'), '.'),
    err, submit);
}

function ownerLinkForm() {
  const err = h('p', { class: 'form-error', role: 'alert', 'data-key': 'owner-link-error', hidden: true });
  const input = h('input', {
    id: 'owner-link', name: 'owner-link', type: 'text', inputmode: 'url', autocomplete: 'off', spellcheck: 'false', required: true,
    placeholder: `${location.origin}/#/owner?s=…`, 'data-key': 'owner-link-input', 'aria-describedby': 'owner-link-help',
    oninput: () => { err.hidden = true; },
  });
  return h('form', {
    class: 'col login-owner', 'data-key': 'owner-link-form', onsubmit: (e) => {
      e.preventDefault();
      const t = ownerLinkTarget(input.value, location.origin);
      if (t.error) {
        err.replaceChildren(t.error, ...(t.other ? [' ', h('a', { href: t.link, rel: 'noopener noreferrer' }, `Open ${t.other}`)] : []));
        err.hidden = false;
        input.focus();
        return;
      }
      input.value = '';
      location.hash = t.hash; // the owner view signs in and strips the secret
    },
  },
    h('label', { for: 'owner-link' }, 'Owner link'),
    h('p', { id: 'owner-link-help', class: 'muted small' }, 'Get one on an owner device: More, Devices, Make another device owner. It works once, for 10 minutes, and makes this browser owner for 30 days.'),
    input, err,
    h('button', { type: 'submit', class: 'btn btn-primary', 'data-key': 'owner-link-open' }, 'Open the owner link'));
}

function reasonLine() {
  const text = signedOutReason(lostReason(), isMobile(navigator.userAgent, navigator.maxTouchPoints, navigator.platform));
  return text ? h('p', { class: 'form-error', role: 'status', 'data-key': 'signed-out-reason' }, text) : null;
}

export function render({ onLoggedIn }) {
  if (inApp()) {
    // The desktop app's Hub view: the app hides the board while it pairs or signs in again;
    // if this page shows at all, point at the app, never at a terminal.
    const card = h('div', { class: 'login-card card', 'data-key': 'login-app' },
      h('h1', {}, 'Sign in to this hub'),
      reasonLine(),
      h('p', {}, 'The desktop app signs this board in. On the Hub tab, pair this computer again, or use Sign in as owner.'));
    return { title: 'Sign in · Watchyard', node: h('div', { class: 'login-wrap' }, h('div', { class: 'login-col' }, brandRow(), card)) };
  }
  const card = h('div', { class: 'login-card card' },
    h('h1', {}, 'Sign in to this hub'),
    reasonLine(),
    h('section', { class: 'col login-option', 'data-key': 'login-owner' },
      h('h2', {}, 'Open an owner link'),
      ownerLinkForm()),
    h('section', { class: 'col login-option', 'data-key': 'login-pair' },
      h('h2', {}, 'Pair with a code'),
      h('p', { class: 'muted small' }, 'For a read-only browser. Get a 6-digit code on an owner device: More, Devices, Add phone.'),
      h('a', { class: 'btn', href: '#/pair', 'data-key': 'to-pair' }, 'Pair with a code')),
    h('details', { class: 'login-more', 'data-key': 'owner-cli' }, h('summary', {}, 'On the hub’s computer'),
      h('p', {}, 'No owner device yet? On the hub’s computer, run'),
      h('pre', { class: 'cmd mono', 'data-key': 'owner-link-cmd' }, OWNER_CLI),
      h('p', {}, 'and open the link it prints in this browser. That signs you in as the owner, with every setting.')),
    h('details', { class: 'login-more', 'data-key': 'token-login' }, h('summary', {}, 'Use an access token instead'), tokenForm(onLoggedIn)));

  return { title: 'Sign in · Watchyard', node: h('div', { class: 'login-wrap' }, h('div', { class: 'login-col' }, brandRow(), card)) };
}
