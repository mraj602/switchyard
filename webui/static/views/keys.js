// Settings → Keys (keys.md 3.3, 3.5, 4.2): the hub key, the owner key and the devices'
// certificates, with the one next step shown. An owner session confirms a pending owner key
// (POST /v1/keys/owner/confirm); everyone else reads. The page never signs anything.

import { h } from '../components/dom.js';
import { section, toast, moreTabs, ownerHintNode } from '../components/ui.js';
import { copyButton } from '../components/cards.js';
import { store, isOwner, inApp } from '../store.js';
import { getKeys, confirmOwnerKey, listDevices } from '../api.js';
import { dateTime, until, agoLabel } from '../format.js';
import {
  groupFP, pendingExpiry, proposerName, certState, CERT_WORD, CERTIFY_HINT, uncertifiedList, nextStep,
  INIT_CMD, CERTIFY_CMD, REPLACE_CMD, OWNER_KEY_WHAT, COMPARE_PROMPT, READ_ONLY_NOTE, REPLACE_NOTE, revocationsLine,
} from '../components/keys-fmt.js';

const st = { info: null, names: null, error: null, busy: false };
const again = () => window.dispatchEvent(new Event('sy-rerender'));

export async function load() {
  st.error = null;
  const jobs = [getKeys().then((k) => { st.info = k; })];
  // The proposer may be a machine without keys of its own; only an owner can list devices.
  if (isOwner()) jobs.push(listDevices().then((r) => { st.names = r.devices; }).catch(() => {}));
  const res = await Promise.allSettled(jobs);
  const bad = res.find((r) => r.status === 'rejected' && r.reason && r.reason.name !== 'AuthError');
  if (bad) st.error = bad.reason.message;
  again();
}

async function onConfirm(p, btn) {
  btn.disabled = true;
  st.busy = true;
  try {
    await confirmOwnerKey(p.fp);
    toast('Owner key confirmed');
  } catch (ex) {
    toast(ex.message || 'Could not confirm the owner key');
  }
  st.busy = false;
  await load();
}

const kv = (k, v) => h('div', { class: 'row kv-row' }, h('span', { class: 'muted' }, k), h('span', { class: 'row-value' }, v));
const fp = (x, key) => h('span', { class: 'mono key-fp', 'data-key': key }, groupFP(x));
const cmdBlock = (cmd, key) => h('div', { class: 'col' },
  h('pre', { class: 'cmd mono', 'data-key': key }, cmd), copyButton(cmd, 'Copy command', `copy-${key}`));

const devices = () => (st.info && st.info.devices) || [];
const nameOf = (p) => proposerName(p, [...devices(), ...(st.names || [])]);

// The proposal waiting for an owner: fingerprint, who and when, the compare prompt and Confirm.
function pendingCard(p, owner) {
  const exp = pendingExpiry(p.created_at);
  return h('div', { class: 'panel pad-s col', 'data-key': 'key-pending' },
    h('p', {}, `Owner key proposed by ${nameOf(p)}${p.created_at ? ` ${agoLabel(p.created_at)} (${dateTime(p.created_at)})` : ''}.`),
    h('p', { class: 'key-fp-big' }, fp(p.fp, 'pending-fp')),
    h('p', { class: 'strong' }, 'Is this the fingerprint your terminal printed?'),
    h('p', { class: 'muted small' }, COMPARE_PROMPT),
    exp ? h('p', { class: 'muted small', 'data-key': 'pending-expiry', title: dateTime(exp) }, `Not confirmed ${until(exp)} (${dateTime(exp)}), it is deleted and you propose it again.`) : null,
    owner
      ? h('div', { class: 'actions' }, h('button', {
        type: 'button', class: 'btn btn-primary', 'data-key': 'confirm-owner-key', disabled: st.busy || !!store.offline,
        onclick: (e) => onConfirm(p, e.currentTarget),
      }, 'Yes, it matches: confirm'))
      : h('p', { class: 'muted small', 'data-key': 'keys-read-only' }, READ_ONLY_NOTE));
}

// The one step to do now.
function nextBlock(k, owner) {
  const step = nextStep(k);
  const pend = k.pending || [];
  let body;
  if (step === 'init' && !owner) {
    // Only an owner sees a proposal, so this may be "none yet" or "waiting for you".
    body = [h('p', { class: 'strong' }, 'No owner key is active yet'),
      h('p', {}, 'The owner makes one with wy keys init on their main machine, then confirms it here with an owner sign-in.'),
      h('p', { class: 'muted small', 'data-key': 'keys-read-only' }, READ_ONLY_NOTE)];
  } else if (step === 'init') {
    body = [h('p', { class: 'strong' }, 'Step 1: make the owner key'),
      h('p', {}, 'On your main machine, in a normal terminal (not inside Claude Code), run:'),
      cmdBlock(INIT_CMD, 'cmd-keys-init'),
      h('p', { class: 'muted small' }, 'It asks for a passphrase, prints a recovery code to write down, and proposes the key to this hub. It then waits here for you to confirm it.')];
  } else if (step === 'confirm') {
    body = [h('p', { class: 'strong' }, 'Step 2: confirm the owner key'), ...pend.map((p) => pendingCard(p, owner))];
  } else if (step === 'certify') {
    const list = uncertifiedList(k.devices);
    body = [h('p', { class: 'strong' }, 'Step 3: certify your machines'),
      h('p', {}, 'On the owner machine, in a normal terminal, run:'),
      cmdBlock(CERTIFY_CMD, 'cmd-keys-certify'),
      h('p', { class: 'muted small' }, `Waiting for: ${list.map((d) => d.name).join(', ')}.`)];
  } else {
    body = [h('p', { class: 'strong status-ok', 'data-key': 'keys-done' }, 'All set.'),
      h('p', {}, 'Every machine is certified. Peers between machines can now message each other.')];
  }
  // A replacement proposal while a key is active is not a "step", but still needs confirming.
  const extra = step === 'done' || step === 'certify' ? pend.map((p) => h('div', { class: 'col' },
    h('p', { class: 'strong' }, 'A new owner key is waiting to replace this one'), pendingCard(p, owner))) : [];
  return section('Next step', h('div', { class: 'panel pad-s col', 'data-key': `keys-step-${step}` }, ...body), ...extra);
}

function ownerBlock(k) {
  const o = k.owner_key;
  return section('Owner key',
    h('div', { class: 'panel pad-s col' },
      h('p', { class: 'muted small' }, OWNER_KEY_WHAT),
      o ? kv('Active', fp(o.fp, 'owner-fp')) : kv('Active', h('span', { class: 'muted', 'data-key': 'owner-none' }, 'None yet')),
      o && o.confirmed_at ? kv('Confirmed', h('span', { title: dateTime(o.confirmed_at) }, agoLabel(o.confirmed_at))) : null,
      (k.pending || []).length ? kv('Pending', h('span', { 'data-key': 'owner-pending' }, `${k.pending.length} waiting for confirmation`)) : null),
    h('details', { class: 'tech', 'data-key': 'replace-owner' }, h('summary', {}, 'Replace the owner key'),
      h('div', { class: 'tech-body col' }, h('p', { class: 'muted small' }, REPLACE_NOTE), cmdBlock(REPLACE_CMD, 'cmd-keys-replace'))));
}

function deviceRows(k) {
  if (!k.devices) return h('p', { class: 'muted small', 'data-key': 'devices-hidden' }, 'Which devices are certified is shown to a sign-in that can write.');
  if (!k.devices.length) return h('p', { class: 'muted small' }, 'No device has registered keys yet.');
  return h('div', { class: 'panel', 'data-key': 'key-devices' }, k.devices.map((d) => {
    const c = certState(d);
    return h('div', { class: 'row status-row', 'data-key': `key-dev-${d.id}` },
      h('span', { class: 'row-text' }, h('b', {}, d.name), h('span', { class: 'muted small' }, d.kind)),
      h('span', { class: c !== 'hub-signed' ? 'status-ok' : 'status-warn', 'data-state': c }, CERT_WORD[c]));
  }));
}

function devicesBlock(k) {
  const need = k.owner_key ? uncertifiedList(k.devices).length : 0;
  return section('Devices',
    deviceRows(k),
    need ? h('p', { class: 'muted small', 'data-key': 'certify-hint' }, CERTIFY_HINT) : null);
}

function hubBlock(k) {
  return section('Hub key',
    h('div', { class: 'panel pad-s col' },
      kv('Fingerprint', fp(k.hub_key.fp, 'hub-fp')),
      kv('Revocations', h('span', { 'data-key': 'revocations' }, revocationsLine(k.revocations_version || 0)))));
}

export function render() {
  const owner = isOwner();
  const k = st.info;
  const head = h('header', { class: 'page-head col' }, h('h1', {}, inApp() ? 'Hub settings' : 'Settings'), moreTabs('keys', { owner }));
  if (!k) {
    return { title: 'Keys · Watchyard', node: h('div', { class: 'page' }, head, h('p', { class: 'muted', role: 'status' }, st.error || 'Loading…')) };
  }
  const node = h('div', { class: 'page', 'data-key': 'keys-page' }, head,
    st.error ? h('p', { class: 'form-error', role: 'alert' }, st.error) : null,
    nextBlock(k, owner),
    ownerBlock(k),
    devicesBlock(k),
    hubBlock(k),
    owner ? null : ownerHintNode({ app: inApp(), lead: 'Confirming an owner key needs an owner sign-in.', key: 'keys-owner-hint' }));
  return { title: 'Keys · Watchyard', node };
}
