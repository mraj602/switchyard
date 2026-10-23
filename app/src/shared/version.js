// Version comparison for the "update available" notice (docs/spec/desktop.md C8). Pure: no
// Node, no Electron. The app checks no update server: it compares its own version with the
// versions it already learns from `sy desktop info` (sy's own and the hub's), and the notice
// links to the project's release page through openExternalSafe.

import { RELEASES_URL } from './constants.js';

const VERSION_RE = /^v?(\d{1,6})\.(\d{1,6})\.(\d{1,6})(?:[-+.][0-9A-Za-z.+-]*)?$/;

/**
 * The numeric core of a release version: '0.6.0', 'v0.6.0', '0.6.0-rc.1', '0.6.0-3-gabc123'
 * (git describe) → [0, 6, 0]. Null for anything else ('dev', a commit id, junk).
 * @param {unknown} s
 * @returns {[number, number, number]|null}
 */
export function parseVersion(s) {
  if (typeof s !== 'string' || s.length > 64) return null;
  const m = VERSION_RE.exec(s.trim());
  return m ? [Number(m[1]), Number(m[2]), Number(m[3])] : null;
}

/** -1, 0 or 1 for two parsed versions. */
export function compareVersions(a, b) {
  for (let i = 0; i < 3; i++) {
    if (a[i] !== b[i]) return a[i] < b[i] ? -1 : 1;
  }
  return 0;
}

const fmt = (v) => v.join('.');

/**
 * What to tell the person about versions, or null when they agree (or cannot be compared).
 * kind 'update': the hub or sy runs a newer release than this app: an update is available.
 * kind 'older': the app is newer than the hub or sy: those should be updated (`sy update`).
 * @param {{app?: string|null, hub?: string|null, sy?: string|null}} v
 * @returns {{kind: 'update'|'older', app: string, version: string, from: 'hub'|'sy', url: string}|null}
 */
export function updateNotice({ app, hub, sy } = {}) {
  const mine = parseVersion(app);
  if (!mine) return null;
  const others = [['hub', parseVersion(hub)], ['sy', parseVersion(sy)]].filter(([, v]) => v);
  let newest = null;
  for (const [from, v] of others) {
    if (compareVersions(v, mine) > 0 && (!newest || compareVersions(v, newest.v) > 0)) newest = { from, v };
  }
  if (newest) {
    const version = fmt(newest.v);
    return { kind: 'update', app: fmt(mine), version, from: newest.from, url: releaseUrl(version) };
  }
  const older = others.find(([, v]) => compareVersions(v, mine) < 0);
  if (older) return { kind: 'older', app: fmt(mine), version: fmt(older[1]), from: older[0], url: releaseUrl(null) };
  return null;
}

/** The release page of a version (digits and dots only, from parseVersion), else the release list. */
export function releaseUrl(version) {
  const v = parseVersion(version);
  return v ? `${RELEASES_URL}/tag/v${fmt(v)}` : RELEASES_URL;
}

/** The line the shell shows for a notice. */
export function updateText(n) {
  if (!n) return '';
  const who = n.from === 'hub' ? 'Your hub' : 'sy on this computer';
  return n.kind === 'update'
    ? `Update available: ${who} runs Switchyard ${n.version}, this app is ${n.app}.`
    : `${who} runs Switchyard ${n.version}, older than this app (${n.app}). Update it with sy update.`;
}
