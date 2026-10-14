// Settings: hub, this device, notifications (pwa.md 6), push preferences, ntfy, install
// status, app version and Unpair.

import { h } from '../components/dom.js';
import { section, select, toggle, toast, moreTabs } from '../components/ui.js';
import { quietHours } from '../components/quiet.js';
import { liveBadge } from '../components/nav.js';
import { store, isOwner, canPush } from '../store.js';
import { getSelf, getRules, getPushConfig, patchDevice, addTarget, deleteTarget, revokeDevice, putPushChannel } from '../api.js';
import { pwa, isStandalone, canInstall, promptInstall, checkForUpdate, updateReady, applyUpdate } from '../pwa.js';
import { pushStatus, turnOn, sendTest } from '../push.js';
import { isIOS, isAndroid, ntfyLink, expoTargetLabel, expoChannelBody } from '../platform.js';
import { dateTime, clock } from '../format.js';

const st = { self: null, push: null, config: null, hub: null, test: null, ntfy: null, busy: false, updateNote: '', error: null };
const again = () => window.dispatchEvent(new Event('sy-rerender'));

export async function load({ onLogout }) {
  st.onLogout = onLogout;
  st.error = null;
  const jobs = [
    getSelf().then((d) => { st.self = d; }),
    pushStatus().then((p) => { st.push = p; }),
    getPushConfig().then((c) => { st.config = c; }).catch(() => {}),
    getRules().then((r) => { st.hub = r.settings; }).catch(() => {}),
  ];
  const res = await Promise.allSettled(jobs);
  const bad = res.find((r) => r.status === 'rejected' && r.reason && r.reason.name !== 'AuthError');
  if (bad) st.error = bad.reason.message;
  again();
}

const row = (k, v) => h('div', { class: 'row kv-row' }, h('span', { class: 'muted' }, k), h('span', { class: 'row-value' }, v));

async function savePrefs(patch) {
  const prefs = { ...(st.self.prefs || {}), ...patch };
  st.self = await patchDevice('self', { prefs });
  again();
}

// ---- notifications ------------------------------------------------------------------------

const MESSAGES = {
  unsupported: 'Not supported in this browser. You can use an ntfy topic below instead.',
  'ios-old': 'Needs iOS 16.4 or newer for notifications. You can use an ntfy topic below instead.',
  'install-first': 'Install first: tap Share, then Add to Home Screen, and open Switchyard from your Home Screen. Notifications work only in the installed app.',
  blocked: 'Blocked in system settings. Allow notifications for this app there, then come back.',
  off: 'Off. Turn on to get a push when a session needs you.',
};

async function onTurnOn(e) {
  const b = e.currentTarget;
  b.disabled = true;
  try {
    await turnOn();
  } catch (ex) {
    toast(ex && ex.name === 'AbortError' ? 'The browser could not reach its push service. Try again on another network.' : (ex.message || 'Could not turn on notifications'));
  }
  st.push = await pushStatus();
  again();
}

async function onTest(target) {
  st.test = { busy: true };
  again();
  try { st.test = await sendTest(target.id); } catch (ex) { st.test = { outcome: 'failed', error: ex.message }; }
  again();
}

function testText(t) {
  if (!t || t.busy) return t && t.busy ? 'Sending…' : '';
  if (t.outcome === 'sent') return 'Sent. It should arrive in a moment.';
  return `The push service said: ${t.error || t.outcome}${t.http_status ? ' (' + t.http_status + ')' : ''}`;
}

function notifications() {
  const p = st.push;
  if (!canPush()) {
    return section('Notifications', h('p', { class: 'muted' }, 'This sign-in cannot receive pushes. Pair this phone with a code (Pair screen) or sign in as owner.'));
  }
  if (!p) return section('Notifications', h('p', { class: 'muted' }, st.error || 'Checking…'));
  const mine = p.targets.filter((t) => t.kind === 'webpush');
  let body;
  if (p.state === 'on') {
    body = [
      h('p', { class: 'status-ok strong', 'data-key': 'notif-state' }, `On · ${mine.length === 1 ? '1 target' : mine.length + ' targets'}`),
      p.target ? h('button', { type: 'button', class: 'btn', 'data-key': 'test-push', disabled: !!store.offline, onclick: () => onTest(p.target) }, 'Send test push') : null,
      st.test ? h('p', { class: 'small', role: 'status', 'data-key': 'test-result' }, testText(st.test)) : null,
    ];
  } else {
    body = [
      h('p', { 'data-key': 'notif-state' }, MESSAGES[p.state] || ''),
      p.state === 'off' ? h('button', { type: 'button', class: 'btn btn-primary', 'data-key': 'turn-on', onclick: onTurnOn }, 'Turn on notifications') : null,
    ];
  }
  return section('Notifications', h('div', { class: 'panel pad-s col' }, body));
}

function prefs() {
  if (!canPush() || !st.self) return null;
  const pr = st.self.prefs || {};
  const priv = pr.private == null ? 'default' : pr.private ? 'on' : 'off';
  return section('This phone',
    h('div', { class: 'panel' },
      h('div', { class: 'row' }, h('span', { class: 'row-text' }, h('span', {}, 'Private pushes'), h('span', { class: 'muted small' }, 'The lock screen says only "1 session needs you"; details load from your hub when opened.')),
        select({ label: 'Private pushes', value: priv, key: 'pref-private', options: [['default', `Hub default${st.hub ? (st.hub.private ? ' (on)' : ' (off)') : ''}`], ['on', 'On'], ['off', 'Off']],
          onChange: (v) => savePrefs({ private: v === 'default' ? null : v === 'on' }) })),
      h('div', { class: 'row' }, h('span', { class: 'row-text' }, h('span', {}, 'When you tap a push'), h('span', { class: 'muted small' }, 'Claude app uses the Remote Control link when there is one.')),
        select({ label: 'Tap behaviour', value: pr.tap || 'auto', key: 'pref-tap', options: [['auto', 'Automatic'], ['session', 'Open the session page'], ['claude', 'Open in the Claude app']],
          onChange: (v) => savePrefs({ tap: v }) })),
      h('div', { class: 'row' }, quietHours({
        device: true, value: pr.quiet_hours, hubValue: st.hub && st.hub.quiet_hours, onSave: (q) => savePrefs({ quiet_hours: q }),
      }))));
}

// ---- App (Expo push) ----------------------------------------------------------------------

const expo = { base: null, token: '', note: '' };

// The channel as the hub shows it: {kind:'expo', enabled, settings:{base_url, access_token_set}}.
const expoChannel = () => ((st.config && st.config.channels) || []).find((c) => c.kind === 'expo') || null;

async function saveExpo(ch, patch) {
  const baseUrl = expo.base == null ? (ch.settings && ch.settings.base_url) || '' : expo.base;
  const next = await putPushChannel('expo', expoChannelBody({ enabled: ch.enabled, baseUrl, ...patch }));
  st.config = { ...st.config, channels: st.config.channels.map((c) => (c.kind === 'expo' ? next : c)) };
  expo.token = '';
  expo.base = null;
}

function expoForm(ch) {
  const set = !!(ch.settings && ch.settings.access_token_set);
  const base = expo.base == null ? (ch.settings && ch.settings.base_url) || 'https://exp.host' : expo.base;
  const run = async (e, patch, note) => {
    e.currentTarget.disabled = true;
    try { await saveExpo(ch, patch); expo.note = note; } catch (ex) { expo.note = ex.message; }
    again();
  };
  return h('div', { class: 'col' },
    toggle({
      label: 'App (Expo push)', sub: 'Pushes and widget refreshes for the Switchyard phone app.', checked: ch.enabled, key: 'chan-expo',
      onChange: async (v) => { await saveExpo(ch, { enabled: v }); again(); },
    }),
    h('label', { class: 'field col-field' }, 'Base URL',
      h('input', { type: 'text', value: base, placeholder: 'https://exp.host', 'data-key': 'expo-base-url', autocomplete: 'off', spellcheck: 'false', oninput: (e) => { expo.base = e.target.value.trim(); } })),
    h('label', { class: 'field col-field' }, `Access token (${set ? 'set' : 'not set'})`,
      h('input', { type: 'password', value: '', placeholder: set ? 'Type a new token to replace it' : 'Optional', 'data-key': 'expo-token', autocomplete: 'off', spellcheck: 'false', oninput: (e) => { expo.token = e.target.value; } })),
    h('p', { class: 'muted small' }, 'The token is needed only if you turned on Expo "enhanced security" for the app (see phone/README). It is never shown again.'),
    h('div', { class: 'row' },
      h('button', { type: 'button', class: 'btn', 'data-key': 'expo-save', onclick: (e) => run(e, { token: expo.token }, 'Saved.') }, 'Save'),
      set ? h('button', { type: 'button', class: 'btn btn-sm', 'data-key': 'expo-clear', onclick: (e) => run(e, { clear: true }, 'Token removed.') }, 'Clear token') : null),
    expo.note ? h('p', { class: 'small', role: 'status', 'data-key': 'expo-note' }, expo.note) : null);
}

function appPush(owner) {
  const targets = ((st.self && st.self.targets) || []).filter((t) => t.kind === 'expo');
  const ch = owner ? expoChannel() : null;
  if (!targets.length && !ch) return null;
  return section('App',
    targets.length ? h('div', { class: 'panel' }, targets.map((t) => h('div', { class: 'row', 'data-key': `app-${t.id}` },
      h('span', { class: 'row-text' }, h('span', {}, expoTargetLabel(t)), h('span', { class: 'muted small' }, t.status))))) : null,
    ch ? h('div', { class: 'panel pad-s col' }, expoForm(ch)) : null);
}

// ---- ntfy ---------------------------------------------------------------------------------

function ntfy() {
  if (!canPush() || !st.self) return null;
  const targets = (st.self.targets || []).filter((t) => t.kind === 'ntfy');
  const server = st.config && st.config.ntfy_default_server || 'https://ntfy.sh';
  let srv = server, topic = '', token = '';
  const form = h('form', {
    class: 'panel pad-s col',
    onsubmit: async (e) => {
      e.preventDefault();
      const b = form.querySelector('button[type=submit]');
      b.disabled = true;
      try {
        const body = { kind: 'ntfy', server: srv };
        if (topic) body.topic = topic;
        if (token) body.token = token;
        st.ntfy = await addTarget(body);
        st.self = await getSelf();
        again();
      } catch (ex) { toast(ex.message); } finally { b.disabled = false; }
    },
  },
    h('p', { class: 'muted small' }, 'ntfy is a fallback for phones where web push does not work. Leave the topic empty to get a random one.'),
    h('label', { class: 'field col-field' }, 'Server', h('input', { type: 'text', value: srv, 'data-key': 'ntfy-server', autocomplete: 'off', oninput: (e) => { srv = e.target.value.trim(); } })),
    h('label', { class: 'field col-field' }, 'Topic (optional)', h('input', { type: 'text', value: '', 'data-key': 'ntfy-topic', autocomplete: 'off', spellcheck: 'false', oninput: (e) => { topic = e.target.value.trim(); } })),
    h('label', { class: 'field col-field' }, 'Access token (optional)', h('input', { type: 'password', value: '', 'data-key': 'ntfy-token', autocomplete: 'off', oninput: (e) => { token = e.target.value.trim(); } })),
    h('button', { type: 'submit', class: 'btn', 'data-key': 'ntfy-add' }, 'Add ntfy topic'));
  const how = (t) => h('p', { class: 'small' }, 'Subscribe in the ntfy app: server ', h('code', {}, t.server || server), ', topic ', h('code', {}, t.topic || ''),
    t.server && t.topic ? h('span', {}, ' · ', h('a', { href: ntfyLink(t.server, t.topic) }, 'open in ntfy')) : null);
  return section('ntfy',
    targets.length ? h('div', { class: 'panel' }, targets.map((t) => h('div', { class: 'row ntfy-row' },
      h('span', { class: 'row-text' }, h('span', {}, `${t.server || server} · ${t.topic}`), how(t)),
      h('button', {
        type: 'button', class: 'btn btn-sm', 'data-key': `ntfy-del-${t.id}`,
        onclick: async () => { try { await deleteTarget(t.id); st.self = await getSelf(); again(); } catch (ex) { toast(ex.message); } },
      }, 'Remove')))) : null,
    st.ntfy ? h('p', { class: 'status-ok small', role: 'status' }, `Added topic ${st.ntfy.topic}`) : null,
    form);
}

// ---- install, version, unpair -------------------------------------------------------------

function installBlock() {
  const ua = navigator.userAgent;
  let how;
  if (isStandalone()) how = 'Installed: you are running the app.';
  else if (isIOS(ua, navigator.maxTouchPoints, navigator.platform)) how = 'Tap Share, then Add to Home Screen. Open Switchyard from your Home Screen.';
  else if (canInstall()) how = 'Not installed. Install it for a full-screen app and notifications.';
  else if (isAndroid(ua)) how = 'Open the browser menu and choose Install app (or Add to Home screen).';
  else how = 'Optional: use the install icon in the address bar, or File, Add to Dock.';
  return section('Install',
    h('div', { class: 'panel pad-s col' },
      h('p', { 'data-key': 'install-state' }, how),
      canInstall() ? h('button', { type: 'button', class: 'btn btn-primary', 'data-key': 'install-app', onclick: promptInstall }, 'Install app') : null,
      !pwa.supported ? h('p', { class: 'muted small' }, 'No offline support here: the page needs to be opened over https.') : null));
}

function versionBlock() {
  return section('App version',
    h('div', { class: 'panel pad-s col' },
      row('Build', h('span', { class: 'mono', 'data-key': 'build' }, pwa.build || (pwa.supported ? 'checking…' : 'no service worker'))),
      row('Hub build', h('span', { class: 'mono' }, pwa.hubBuild || '–')),
      h('div', { class: 'actions' },
        h('button', {
          type: 'button', class: 'btn', 'data-key': 'check-update',
          onclick: async (e) => { e.currentTarget.disabled = true; const r = await checkForUpdate(); st.updateNote = r ? '' : 'You are up to date.'; again(); },
        }, 'Check for update'),
        updateReady() ? h('button', { type: 'button', class: 'btn btn-primary', onclick: applyUpdate }, 'Reload to update') : null),
      st.updateNote ? h('p', { class: 'muted small', role: 'status' }, st.updateNote) : null));
}

function deviceBlock() {
  const me = store.me;
  const d = me.device;
  const unpair = d.kind === 'phone';
  return section('This device',
    h('div', { class: 'panel pad-s col' },
      row('Name', d.name),
      row('Kind', `${d.kind}${me.owner ? ' · owner session' : ''}`),
      d.expires_at ? row('Session ends', dateTime(d.expires_at)) : null,
      h('div', { class: 'actions' },
        unpair ? h('button', {
          type: 'button', class: 'btn btn-danger', 'data-key': 'unpair',
          onclick: async () => {
            if (!confirm('Unpair this phone? It will stop receiving pushes and must be paired again.')) return;
            try { await revokeDevice('self'); } catch (ex) { toast(ex.message); return; }
            st.onLogout();
          },
        }, 'Unpair this phone') : h('button', { type: 'button', class: 'btn', 'data-key': 'signout', onclick: () => st.onLogout() }, 'Sign out'))));
}

export function render({ store }) {
  const owner = isOwner();
  const node = h('div', { class: 'page' },
    h('header', { class: 'page-head' }, h('h1', {}, 'Settings')),
    moreTabs('settings', { owner }),
    section('Hub',
      h('div', { class: 'panel pad-s col' },
        row('Address', h('span', { class: 'mono', 'data-key': 'hub-address' }, location.origin)),
        row('Version', (store.me.hub && store.me.hub.version) || '–'),
        row('Status', liveBadge(store.live, store.offline)),
        store.offline ? row('Last update', clock(new Date(store.offline.since).toISOString())) : null)),
    notifications(),
    prefs(),
    ntfy(),
    appPush(owner),
    installBlock(),
    versionBlock(),
    deviceBlock(),
    owner ? h('p', { class: 'muted small' }, h('a', { href: '#/more/devices' }, 'Manage paired devices')) : null);
  return { title: 'Settings · Switchyard', node };
}
