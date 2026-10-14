// The argv allow-list (C11): the only builder of `sy` command lines. build(purpose, params)
// returns {argv, stdin, timeout} for exactly the rows of the C11 table, or throws; no other
// argv can be produced. Every parameter is validated against its anchored pattern before it
// is placed in argv, and no secret ever goes into argv (the pairing code travels on stdin).

import { SY_TIMEOUT_MS, SY_LONG_TIMEOUT_MS } from '../../shared/constants.js';
import { SHARE_FOLDER_PATTERN, SHARE_SPEC_PATTERN } from '../../shared/ipc.js';

// No spaces: through an ssh admin channel the name is a remote command word (sy's CheckWord).
export const TOKEN_NAME_RE = /^[A-Za-z0-9._-]{1,40}$/;
export const SHARE_FOLDER_RE = new RegExp(SHARE_FOLDER_PATTERN);
export const SHARE_SPEC_RE = new RegExp(SHARE_SPEC_PATTERN);
export const SERVICE_ACTIONS = Object.freeze(['status', 'start', 'stop']);

const need = (re, value, what) => {
  // A type check keeps non-strings (an array or object with a toString) out of the regex.
  if (typeof value !== 'string' || !re.test(value)) throw new Error(`sy: bad ${what}`);
  return value;
};

/** Purposes (rows of the C11 table) mapped to a builder. */
const TABLE = {
  version: () => ({ argv: ['--version'], stdin: false, timeout: SY_TIMEOUT_MS }),
  machine: () => ({ argv: ['machine', 'show', '--json'], stdin: false, timeout: SY_TIMEOUT_MS }),
  append: () => ({ argv: ['outbox', 'append'], stdin: true, timeout: SY_TIMEOUT_MS }),
  flush: () => ({ argv: ['flush', '--quiet'], stdin: false, timeout: SY_LONG_TIMEOUT_MS }),
  info: () => ({ argv: ['desktop', 'info', '--json'], stdin: false, timeout: SY_TIMEOUT_MS }),
  token: (p) => {
    const name = need(TOKEN_NAME_RE, p.name, 'token name');
    const argv = ['desktop', 'token', '--json', '--name', name];
    if (p.code_stdin === true) argv.push('--code-stdin');
    else if (p.code_stdin !== undefined && p.code_stdin !== false) throw new Error('sy: bad code_stdin');
    return { argv, stdin: p.code_stdin === true, timeout: SY_LONG_TIMEOUT_MS };
  },
  owner: () => ({ argv: ['desktop', 'owner-link', '--json'], stdin: false, timeout: SY_LONG_TIMEOUT_MS }),
  service: (p) => {
    if (!SERVICE_ACTIONS.includes(p.action)) throw new Error('sy: bad service action');
    return { argv: ['desktop', 'hub', p.action, '--json'], stdin: false, timeout: SY_TIMEOUT_MS };
  },
  accounts: () => ({ argv: ['account', 'list', '--json'], stdin: false, timeout: SY_TIMEOUT_MS }),
  share: (p) => {
    const folder = need(SHARE_FOLDER_RE, p.folder, 'folder');
    // A leading dash would read as a flag; the C11 pattern allows it, the builder does not.
    if (folder.startsWith('-')) throw new Error('sy: bad folder');
    const share = need(SHARE_SPEC_RE, p.share, 'share');
    return { argv: ['account', 'share', folder, '--share', share, '--yes', '--json'], stdin: false, timeout: SY_TIMEOUT_MS };
  },
};

export const PURPOSES = Object.freeze(Object.keys(TABLE));

/**
 * @param {string} purpose
 * @param {object} [params]
 * @returns {{argv: string[], stdin: boolean, timeout: number}}
 */
export function build(purpose, params = {}) {
  if (typeof purpose !== 'string' || !Object.hasOwn(TABLE, purpose)) throw new Error(`sy: unknown purpose ${JSON.stringify(String(purpose))}`);
  if (typeof params !== 'object' || params === null) throw new Error('sy: params must be an object');
  return TABLE[purpose](params);
}
