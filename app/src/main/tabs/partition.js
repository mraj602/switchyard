// Partition names and the user agent of an account tab (C1, C2). Owned by WP-B.

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
 * A plain Chrome user agent that names the real platform (Cloudflare treats the `Electron/x`
 * token as a bot signal; the prototype said X11 Linux on every OS).
 * @param {{platform: string, chrome: string}} p  chrome = process.versions.chrome
 */
export function userAgentFor({ platform, chrome }) {
  const token = PLATFORM_TOKEN[platform] ?? PLATFORM_TOKEN.linux;
  return `Mozilla/5.0 (${token}) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/${chrome} Safari/537.36`;
}
