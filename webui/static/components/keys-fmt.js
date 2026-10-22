// The sentences and the step logic of Settings → Keys (docs/spec/keys.md 3.3, 3.5, 4.2, pwa.md 3.2).
// Pure, so node can test it (keys-fmt.test.cjs): no imports.

// A pending owner key that is not confirmed within this long is deleted by the hub (keys.md 3.3).
export const PENDING_MS = 24 * 3600 * 1000;

// "k7q2m4xa..." -> "k7q2-m4xa-..." (six groups of four, as the terminal prints it).
export function groupFP(fp) {
  const s = String(fp || '');
  return s.length === 24 ? s.match(/.{4}/g).join('-') : s;
}

// When a pending proposal is deleted if nobody confirms it; '' when its creation time is unknown.
export function pendingExpiry(createdAt) {
  const t = createdAt ? Date.parse(createdAt) : NaN;
  return Number.isNaN(t) ? '' : new Date(t + PENDING_MS).toISOString();
}

// The proposer's name: the device the hub knows by that id, else a plain "a device".
export function proposerName(pending, devices) {
  const d = (devices || []).find((x) => x.id === pending.proposed_by);
  return d && d.name ? d.name : 'a device';
}

// One device's certification: certified (the owner key signed its certificate) or hub-signed
// only (it works, but is not owner-certified yet: keys.md 4.2).
// A phone needs only the hub signature (keys.md 4.2: the hub learned its keys under the QR seal),
// so it is never waiting for sy keys certify.
export function certState(dev) {
  if (dev && dev.keys && dev.keys.owner_signed) return 'certified';
  return dev && dev.kind === 'phone' ? 'phone' : 'hub-signed';
}
export const CERT_WORD = { certified: 'Certified', phone: 'Hub-signed (enough for a phone)', 'hub-signed': 'Hub-signed only' };

export const CERTIFY_HINT = 'Run sy keys certify --all on the owner machine.';

// The devices that still need certifying.
export const uncertifiedList = (devices) => (devices || []).filter((d) => certState(d) === 'hub-signed');
export const uncertified = (devices) => uncertifiedList(devices).length;

export const INIT_CMD = 'sy keys init';
export const CERTIFY_CMD = 'sy keys certify --all';
export const REPLACE_CMD = 'sy keys init --replace';

export const OWNER_KEY_WHAT = 'The owner key is a signing key that stays on one machine of yours. It proves that you, not a stolen token, allow things like messages between your machines.';

export const COMPARE_PROMPT = 'Compare this with the fingerprint your terminal printed when you ran sy keys init. Confirm only if every group matches.';

export const READ_ONLY_NOTE = 'You can see these but not change them: confirming an owner key needs an owner session.';

export const REPLACE_NOTE = 'Lost the passphrase and the recovery code, or the key leaked? sy keys init --replace proposes a new owner key. Confirm it here, then run sy keys certify --all and, on each machine, sy keys trust-owner <fingerprint>.';

export const revocationsLine = (v) => (v > 0 ? `Revocation list version ${v}` : 'No revocations yet');

// The one step to show now (the owner's guided flow):
//   init     no owner key and nothing pending
//   confirm  a proposal is waiting
//   certify  an owner key is active, some devices are hub-signed only
//   done     everything certified (devices unknown counts as done: the viewer cannot see them)
// The API does not say which machines have pinned the owner key, so there is no "pin" step.
export function nextStep(info) {
  const k = info || {};
  if (!k.owner_key) return (k.pending || []).length ? 'confirm' : 'init';
  return uncertified(k.devices) ? 'certify' : 'done';
}
