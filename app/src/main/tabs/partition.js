// Partition names and the user agent of an account tab (C1, C2).

import { TAB_ID_RE, TAB_PARTITION_PREFIX } from '../../shared/constants.js';

/** `persist:tab-<id>`; throws for an id that does not match the tab id pattern. */
export function partitionOf(id) {
  if (typeof id !== 'string' || !TAB_ID_RE.test(id)) throw new Error(`bad tab id ${JSON.stringify(id)}`);
  return `${TAB_PARTITION_PREFIX}${id}`;
}

/** The folder name Chromium uses under <userData>/Partitions for a `persist:` partition. */
export function partitionFolder(id) {
  return partitionOf(id).slice('persist:'.length);
}

const PLATFORM_TOKEN = {
  darwin: 'Macintosh; Intel Mac OS X 10_15_7',
  win32: 'Windows NT 10.0; Win64; x64',
  linux: 'X11; Linux x86_64',
};

/**
 * A plain Chrome user agent that names the real platform. claude.ai's bot protection
 * (Cloudflare) rejects Electron's default user agent, which carries an `Electron/x` token, so
 * account tabs send the ordinary Chrome one instead. Policy note: the tab therefore identifies
 * itself as Chrome, not as Switchyard.
 *
 * The version is reduced to the major (`Chrome/152.0.0.0`), as Chrome itself has sent since
 * Chrome 110: a full build number is a string no real Chrome sends, and it changed with every
 * Electron patch update, so claude.ai's "trusted device" check kept asking to verify again.
 * @param {{platform: string, chrome: string}} p  chrome = process.versions.chrome
 */
export function userAgentFor({ platform, chrome }) {
  const token = PLATFORM_TOKEN[platform] ?? PLATFORM_TOKEN.linux;
  const major = /^\d+/.exec(String(chrome))?.[0] ?? '130';
  return `Mozilla/5.0 (${token}) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/${major}.0.0.0 Safari/537.36`;
}
