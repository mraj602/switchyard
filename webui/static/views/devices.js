// Devices (owner): machines, phones, browsers and owner sessions; rename, revoke, send a test
// notification; Add phone / Add machine / Add desktop app with a pairing code (pairing.md 2.4, 6).

import { h } from '../components/dom.js';
import { toast, moreTabs } from '../components/ui.js';
import { qrSvg } from '../components/qr.js';
import { pairErrorView } from '../components/pair-error.js';
import { copyButton } from '../components/cards.js';
import { targetKindText, usable } from '../components/targets.js';
import { isOwner } from '../store.js';
import { listDevices, patchDevice, revokeDevice, testTarget, makePairCode, getPairCode, cancelPairCode } from '../api.js';
import { agoLabel, day, dateTime, clock, deviceLabel } from '../format.js';

const st = { devices: null, error: null, dialog: null, results: {} };
const again = () => window.dispatchEvent(new Event('sy-rerender'));

const KINDS = ['phone', 'machine', 'desktop'];
const TITLE = { phone: 'Add a phone', machine: 'Add a machine', desktop: 'Add the desktop app' };

export async function load({ route }) {
  st.error = null;
  // Only the owner may list devices; anyone else would get a 403 (DV5).
  if (!isOwner()) { again(); return; }
  try { st.devices = (await listDevices()).devices; } catch (e) { if (e.name !== 'AuthError') st.error = e.message; }
  // A link (?add=phone) opens the dialog, but a code is made only on a click: reloading or
  // going Back must not make another one (DV4).
  const add = route.query.get('add');
  if (KINDS.includes(add) && !(st.dialog && st.dialog.kind === add)) st.dialog = { kind: add, ask: true };
  again();
}

// ---- Add phone / machine / desktop app dialog ---------------------------------------------

let pollTimer = null;
let tickTimer = null;

function closeDialog() {
  clearInterval(pollTimer); clearInterval(tickTimer);
  st.dialog = null;
  if (location.hash.includes('add=')) history.replaceState(null, '', '#/more/devices');
  again();
}

async function openDialog(kind) {
  if (st.dialog && st.dialog.kind === kind && st.dialog.code) return;
  st.dialog = { kind, busy: true };
  again();
  try {
    const code = await makePairCode({ kind });
    st.dialog = { kind, code, status: 'live', left: Math.max(0, Math.round((Date.parse(code.expires_at) - Date.now()) / 1000)) };
  } catch (ex) {
    st.dialog = { kind, error: { message: ex.message, code: ex.code || '' } };
    again();
    return;
  }
  again();
  clearInterval(pollTimer); clearInterval(tickTimer);
  pollTimer = setInterval(async () => {
    const d = st.dialog;
    if (!d || !d.code || d.status !== 'live') return;
    try {
      const s = await getPairCode(d.code.id);
      if (s.status !== 'live') {
        d.status = s.status; d.device = s.device;
        if (s.status === 'claimed') { try { st.devices = (await listDevices()).devices; } catch (_) { /* keep the old list */ } }
        again();
      }
    } catch (_) { /* try again in 2 s */ }
  }, 2000);
  tickTimer = setInterval(() => {
    const d = st.dialog;
    if (!d || !d.code) return;
    d.left = Math.max(0, Math.round((Date.parse(d.code.expires_at) - Date.now()) / 1000));
    const el = document.querySelector('[data-countdown]');
    if (el) el.textContent = countdown(d.left);
    if (d.left === 0 && d.status === 'live') { d.status = 'expired'; again(); }
  }, 1000);
}

// The two ways to add a machine (machines.md 6.1): from your laptop over your own SSH, or on the
// server itself with the one-line install.
function machineCommands(c) {
  let hub = '<hub>';
  try { hub = new URL(c.url).origin; } catch (_) { hub = location.origin; }
  const install = `curl -fsSL https://github.com/mraj602/switchyard/releases/latest/download/install.sh | sh -s -- -- --hub join:${hub} --code ${c.code}`;
  return h('div', { class: 'col machine-cmds' },
    h('p', {}, h('strong', {}, 'From your laptop'), ' (uses your own SSH; the server needs no internet):'),
    h('pre', { class: 'cmd mono', 'data-key': 'cmd-machine-add' }, 'sy machine add user@host'),
    h('p', {}, h('strong', {}, 'On the server itself'), ' (it must reach GitHub and this hub):'),
    h('pre', { class: 'cmd mono', 'data-key': 'cmd-install' }, install),
    copyButton(install, 'Copy install command', 'copy-install'),
    h('p', {}, 'Either way, give it this code when it asks:'));
}

// The desktop app (desktop.md C4, C6): on its computer, pipe the code into sy, or type it in
// the app's Hub settings.
function desktopCommands() {
  return h('div', { class: 'col desktop-cmds' },
    h('p', {}, 'On the computer with the desktop app, run:'),
    h('pre', { class: 'cmd mono', 'data-key': 'cmd-desktop' }, 'sy desktop token --code-stdin'),
    copyButton('sy desktop token --code-stdin', 'Copy command', 'copy-desktop'),
    h('p', {}, 'and paste this code when it asks for it. You can also type the code in the desktop app’s Hub settings.'));
}

const countdown = (s) => `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;

function dialog() {
  const d = st.dialog;
  if (!d) return null;
  const title = TITLE[d.kind] || TITLE.phone;
  let body;
  if (d.ask) {
    body = h('div', { class: 'col' },
      h('p', {}, d.kind === 'phone'
        ? 'This makes a one-time code for the Switchyard Android app or a phone’s browser. It works once and expires after 10 minutes.'
        : 'This makes a one-time code. It works once and expires after 10 minutes.'),
      h('button', { type: 'button', class: 'btn btn-primary', 'data-key': 'make-code', onclick: () => openDialog(d.kind) }, 'Make a code'));
  } else if (d.busy) body = h('p', { class: 'muted' }, 'Making a code…');
  else if (d.error) {
    const e = pairErrorView(d.error);
    body = h('div', { class: 'form-error col', role: 'alert', 'data-key': 'pair-error', 'data-code': e.code },
      h('strong', {}, e.title), h('p', { 'data-key': 'pair-error-message' }, e.message));
  }
  else if (d.status === 'claimed') {
    body = h('div', { class: 'col' },
      h('p', { class: 'status-ok strong', 'data-key': 'pair-claimed' }, `Paired: ${d.device ? d.device.name : 'new device'}`),
      st.devices && d.device ? testButton(st.devices.find((x) => x.id === d.device.id)) : null);
  } else if (d.status && d.status !== 'live') {
    body = h('p', { class: 'muted' }, d.status === 'cancelled' ? 'This code was cancelled.' : 'This code expired. Close and make a new one.');
  } else {
    const c = d.code;
    const digits = [
      h('p', { class: 'pair-digits mono', 'data-key': 'pair-digits' }, c.code),
      h('p', { class: 'muted small' }, 'Expires in ', h('span', { 'data-countdown': '' }, countdown(d.left || 0)), ' · works once'),
    ];
    // Only a phone can use the QR code and the link; a machine or the desktop app takes the
    // code through a command (DV2).
    body = d.kind === 'phone'
      ? h('div', { class: 'pair-box' },
        qrSvg(c.qr, 'QR code to pair'),
        h('div', { class: 'col' },
          h('p', {}, 'Scan the QR code with the Switchyard Android app, or with the phone camera to use the browser. Or open this hub on the phone and type this code:'),
          digits,
          h('details', { class: 'tech', 'data-key': 'pair-link' }, h('summary', {}, 'Pairing link'),
            h('div', { class: 'tech-body' }, h('p', { class: 'muted small break' }, c.url), copyButton(c.url, 'Copy link', 'copy-pair-link')))))
      : h('div', { class: 'col' }, d.kind === 'machine' ? machineCommands(c) : desktopCommands(), digits);
  }
  const dlg = h('dialog', { class: 'dialog', 'aria-label': title, 'data-key': 'pair-dialog' },
    h('div', { class: 'dialog-head' }, h('h2', {}, title), h('button', { type: 'button', class: 'linkish', onclick: closeDialog, 'data-key': 'dialog-close' }, 'Close')),
    body,
    d.code && d.status === 'live' ? h('button', {
      type: 'button', class: 'btn', 'data-key': 'pair-cancel',
      onclick: async () => { try { await cancelPairCode(d.code.id); } catch (ex) { toast(ex.message); } closeDialog(); },
    }, 'Cancel this code') : null);
  dlg.addEventListener('cancel', (e) => { e.preventDefault(); closeDialog(); });
  queueMicrotask(() => { if (dlg.isConnected && !dlg.open) dlg.showModal(); });
  return dlg;
}

// ---- the list -----------------------------------------------------------------------------

function testButton(dev) {
  const t = dev && (dev.targets || []).find(usable);
  if (!t) return null;
  const res = st.results[t.id];
  return h('span', { class: 'test-cell' },
    h('button', {
      type: 'button', class: 'linkish', 'data-key': `test-${t.id}`,
      onclick: async () => {
        st.results[t.id] = 'Sending…'; again();
        try { const r = await testTarget(t.id); st.results[t.id] = r.outcome === 'sent' ? 'Sent' : `${r.outcome}${r.error ? ': ' + r.error : ''}`; } catch (ex) { st.results[t.id] = ex.message; }
        again();
      },
    }, 'Send a test'),
    res ? h('span', { class: 'muted small', role: 'status' }, ` ${res}`) : null);
}

const STATUS = { ok: 'working', gone: 'no longer valid', failing: 'failing', new: 'not used yet', disabled: 'off' };

function targetText(t) {
  const what = t.kind === 'ntfy' ? `ntfy ${t.topic || ''}`.trim()
    : t.kind === 'webpush' ? `${targetKindText(t)}${t.endpoint_host ? ` (${t.endpoint_host})` : ''}` : targetKindText(t);
  const bits = [what, STATUS[t.status] || t.status];
  if (t.status !== 'ok' && t.last_error) bits.push(t.last_error);
  return bits.join(' · ');
}

const KIND_TEXT = { machine: 'Machine', phone: 'Phone', browser: 'Browser', desktop: 'Desktop app', owner: 'Owner' };

function kindText(d) {
  if (d.owner_session) return 'Browser · owner';
  return KIND_TEXT[d.kind] || d.kind;
}

function deviceRow(d, now, all) {
  const targets = d.targets || [];
  const label = deviceLabel(d, all);
  const rename = () => {
    const name = prompt('Name for this device', d.name);
    if (!name || name === d.name) return;
    patchDevice(d.id, { name: name.trim() }).then((u) => { Object.assign(d, u); again(); }).catch((ex) => toast(ex.message));
  };
  // Created time and address tell apart two devices with the same name (DV1).
  return h('li', { class: `dev-row${d.revoked_at ? ' is-revoked' : ''}`, 'data-device': d.id },
    h('div', { class: 'dev-main' },
      h('div', { class: 'strong', 'data-key': `dev-name-${d.id}` }, label, d.current ? h('span', { class: 'tag' }, 'this device') : null),
      h('div', { class: 'muted small' }, [kindText(d), d.created_at ? `added ${dateTime(d.created_at)}` : '',
        d.last_used_at ? `last used ${agoLabel(d.last_used_at, now)}` : 'never used', d.last_used_ip ? `from ${d.last_used_ip}` : '',
        d.expires_at && !d.revoked_at ? `signed in until ${day(d.expires_at)}` : '', d.revoked_at ? `revoked ${clock(d.revoked_at)}` : ''].filter(Boolean).join(' · ')),
      targets.length ? h('ul', { class: 'dev-targets small' }, targets.map((t) => h('li', { class: t.status === 'gone' || t.status === 'failing' ? 'status-warn' : 'muted' }, targetText(t)))) : null),
    d.revoked_at ? null : h('div', { class: 'dev-actions' },
      testButton(d),
      h('button', { type: 'button', class: 'linkish', 'data-key': `rename-${d.id}`, onclick: rename }, 'Rename'),
      d.current ? null : h('button', {
        type: 'button', class: 'linkish danger', 'data-key': `revoke-${d.id}`,
        onclick: async () => {
          if (!confirm(`Revoke ${label}? It stops working at once.`)) return;
          try { await revokeDevice(d.id); st.devices = (await listDevices()).devices; again(); } catch (ex) { toast(ex.message); }
        },
      }, 'Revoke')));
}

export function render({ now }) {
  const owner = isOwner();
  const head = h('header', { class: 'page-head col' }, h('div', { class: 'head-row' }, h('h1', {}, 'Devices'),
    owner ? h('div', { class: 'actions' },
      h('button', { type: 'button', class: 'btn btn-primary', 'data-key': 'add-phone', onclick: () => openDialog('phone') }, 'Add phone'),
      h('button', { type: 'button', class: 'btn', 'data-key': 'add-machine', onclick: () => openDialog('machine') }, 'Add machine'),
      h('button', { type: 'button', class: 'btn', 'data-key': 'add-desktop', onclick: () => openDialog('desktop') }, 'Add desktop app')) : null),
  moreTabs('devices', { owner }));
  if (!owner) {
    return { title: 'Devices · Switchyard', node: h('div', { class: 'page' }, head,
      h('p', { class: 'calm' }, 'Sign in as owner on the hub: run ', h('code', {}, 'sy hub owner-link'), ' there and open the link it prints.')) };
  }
  const list = st.devices;
  const node = h('div', { class: 'page' }, head,
    !list ? h('p', { class: 'muted' }, st.error || 'Loading…')
      : list.length === 0 ? h('p', { class: 'calm' }, 'No devices yet.')
      : h('ul', { class: 'panel dev-list' }, list.map((d) => deviceRow(d, now, list))),
    dialog());
  return { title: 'Devices · Switchyard', node };
}
