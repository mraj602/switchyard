// The one IPC table (docs/spec/desktop.md C3): every channel between the shell and main, its
// argument schema, its direction, the `window.sy` function name the preload exposes for it
// and the work package that owns its handler. Pure data and pure validation: main/ipc.js
// (WP-A) checks every call against it, src/preload/shell.cjs mirrors its names (a test checks
// they agree), and the renderer may import it from app://shell/shared/ipc.js.
//
// Wave 0 owns this file. Adding or changing a channel is a contract change: desktop.md C3
// first, then this table, then the preload.

import { TAB_ID_PATTERN, TAB_NAME_MAX } from './constants.js';

export const INVOKE = 'invoke';
export const PUSH = 'push';

// ---- schema helpers (a tiny, closed schema language; see validate below)

const TAB_ID = Object.freeze({ type: 'string', max: 18, pattern: new RegExp(TAB_ID_PATTERN) });
const TAB_NAME = Object.freeze({ type: 'string', min: 1, max: TAB_NAME_MAX });
const BOOL = Object.freeze({ type: 'bool' });
const obj = (props, required = []) => Object.freeze({ type: 'object', props: Object.freeze(props), required: Object.freeze(required) });
const withId = (props = {}, required = []) => obj({ id: TAB_ID, ...props }, ['id', ...required]);

/** Seat ids are made by the hub: 'seat_' + ULID (schema.sql). */
export const SEAT_ID_PATTERN = '^seat_[0-9A-HJKMNP-TV-Z]{26}$';
/** Seat label and colour (api.yaml SeatPatch; null clears). */
export const SEAT_LABEL_PATTERN = '^[a-z0-9][a-z0-9-]{0,23}$';
export const SEAT_COLOUR_PATTERN = '^#[0-9a-f]{6}$';
/** `sy account share` arguments (C11 argv allow-list). */
export const SHARE_FOLDER_PATTERN = '^~?[A-Za-z0-9._/-]{1,200}$';
export const SHARE_SPEC_PATTERN = '^[a-z,=:+-]{1,200}$';
/** Pairing code typed by the person: 123-456 or 123456. */
export const PAIR_CODE_PATTERN = '^\\d{3}-?\\d{3}$';
/** Hub route: starts '#/', at most 200 characters, no control characters or spaces. */
export const HUB_ROUTE_PATTERN = '^#/[^\\s\\x00-\\x1f\\x7f]*$';

/** Toast levels (push `toast`). */
export const TOAST_LEVELS = Object.freeze(['info', 'warn', 'error']);

/**
 * The table. Keys are channel names. For an invoke channel, `args` is null when the call
 * takes no argument (the preload passes none and main refuses one), else an object schema.
 * `returns` documents the reply (not checked). For a push channel, `args` is the payload
 * schema (checked by main before sending, in tests), and `fn` is null: the renderer
 * subscribes with `window.sy.on(<channel>, cb)`.
 */
export const CHANNELS = Object.freeze({
  'state:get': { kind: INVOKE, fn: 'state', owner: 'A', args: null, returns: 'ShellState' },
  'view:select': {
    kind: INVOKE, fn: 'selectView', owner: 'A', returns: 'undefined',
    args: obj({ view: { type: 'string', max: 18, enum: ['hub', 'settings'], pattern: new RegExp(TAB_ID_PATTERN) } }, ['view']),
  },
  'hub:route': {
    kind: INVOKE, fn: 'hubRoute', owner: 'C', returns: 'undefined',
    args: obj({ route: { type: 'string', min: 2, max: 200, pattern: new RegExp(HUB_ROUTE_PATTERN) } }, ['route']),
  },

  'tab:add': { kind: INVOKE, fn: 'addTab', owner: 'B', returns: '{id}', args: obj({ name: TAB_NAME }, ['name']) },
  'tab:update': { kind: INVOKE, fn: 'updateTab', owner: 'B', returns: 'undefined', args: withId({ name: TAB_NAME }) },
  'tab:remove': { kind: INVOKE, fn: 'removeTab', owner: 'B', returns: 'undefined', args: withId() },
  'tab:reorder': {
    kind: INVOKE, fn: 'reorderTabs', owner: 'B', returns: 'undefined',
    args: obj({ ids: { type: 'array', items: TAB_ID, max: 100, unique: true } }, ['ids']),
  },
  'tab:reload': { kind: INVOKE, fn: 'reloadTab', owner: 'B', returns: 'undefined', args: withId() },
  'tab:home': { kind: INVOKE, fn: 'homeTab', owner: 'B', returns: 'undefined', args: withId() },
  'tab:external': { kind: INVOKE, fn: 'openTabExternal', owner: 'B', returns: 'undefined', args: withId() },

  'usage:accept-warning': {
    kind: INVOKE, fn: 'acceptWarning', owner: 'B', returns: 'undefined',
    args: withId({ version: { type: 'int', min: 1, max: 1000 } }, ['version']),
  },
  'usage:set-reading': { kind: INVOKE, fn: 'setReading', owner: 'B', returns: 'undefined', args: withId({ on: BOOL }, ['on']) },
  'usage:set-send': { kind: INVOKE, fn: 'setSend', owner: 'B', returns: 'undefined', args: withId({ on: BOOL }, ['on']) },
  'usage:resume': { kind: INVOKE, fn: 'resumeReading', owner: 'B', returns: 'undefined', args: withId() },
  'usage:refresh': { kind: INVOKE, fn: 'refreshUsage', owner: 'B', returns: 'undefined', args: obj({ id: TAB_ID }) },

  'hub:pair': {
    kind: INVOKE, fn: 'pair', owner: 'C', returns: '{ok, error?}',
    args: obj({ code: { type: 'string', max: 7, pattern: new RegExp(PAIR_CODE_PATTERN) } }), // {} = local or admin channel
  },
  'hub:owner-sign-in': { kind: INVOKE, fn: 'ownerSignIn', owner: 'C', args: null, returns: '{ok, error?}' },
  'hub:service': {
    kind: INVOKE, fn: 'hubService', owner: 'C', returns: '{service, health}',
    args: obj({ action: { type: 'string', max: 6, enum: ['status', 'start', 'stop'] } }, ['action']),
  },
  'seat:update': {
    kind: INVOKE, fn: 'updateSeat', owner: 'C', returns: '{ok, error?}',
    args: obj({
      seat: { type: 'string', max: 31, pattern: new RegExp(SEAT_ID_PATTERN) },
      label: { type: 'string', max: 24, pattern: new RegExp(SEAT_LABEL_PATTERN), nullable: true },
      colour: { type: 'string', max: 7, pattern: new RegExp(SEAT_COLOUR_PATTERN), nullable: true },
    }, ['seat']),
  },
  'accounts:local': { kind: INVOKE, fn: 'localAccounts', owner: 'C', args: null, returns: 'sy account list --json result' },
  'accounts:share': {
    kind: INVOKE, fn: 'shareAccount', owner: 'C', returns: '{ok, error?}',
    args: obj({
      folder: { type: 'string', max: 201, pattern: new RegExp(SHARE_FOLDER_PATTERN) },
      share: { type: 'string', max: 200, pattern: new RegExp(SHARE_SPEC_PATTERN) },
    }, ['folder', 'share']),
  },

  'settings:set': {
    kind: INVOKE, fn: 'setSettings', owner: 'E', returns: 'Settings',
    args: obj({ keep_running: BOOL, notifications: BOOL, autostart: BOOL, start_hidden: BOOL }),
  },

  // push (main → shell)
  state: { kind: PUSH, fn: null, owner: 'A', args: { type: 'any' }, returns: null }, // ShellState, debounced 100 ms
  toast: {
    kind: PUSH, fn: null, owner: 'A', returns: null,
    args: obj({ level: { type: 'string', max: 5, enum: TOAST_LEVELS }, text: { type: 'string', min: 1, max: 500 } }, ['level', 'text']),
  },
});

/** Invoke channel names, in table order. */
export const INVOKE_CHANNELS = Object.freeze(Object.keys(CHANNELS).filter((c) => CHANNELS[c].kind === INVOKE));
/** Push channel names, in table order. */
export const PUSH_CHANNELS = Object.freeze(Object.keys(CHANNELS).filter((c) => CHANNELS[c].kind === PUSH));
/** window.sy function name → channel. */
export const FUNCTIONS = Object.freeze(Object.fromEntries(INVOKE_CHANNELS.map((c) => [CHANNELS[c].fn, c])));

/** IpcArgError is what validate throws; main/ipc.js turns it into a rejected invoke. */
export class IpcArgError extends Error {
  constructor(channel, path, msg) {
    super(`${channel}: ${path || 'args'}: ${msg}`);
    this.name = 'IpcArgError';
    this.channel = channel;
  }
}

/**
 * Validates the arguments of one call and returns a clean copy (only schema keys, no
 * prototype tricks). Throws IpcArgError for an unknown channel, a missing or unknown key, a
 * wrong type, a string over its cap, a value outside its enum or pattern.
 * @param {string} channel
 * @param {unknown} args
 */
export function validate(channel, args) {
  const ch = Object.hasOwn(CHANNELS, channel) ? CHANNELS[channel] : null;
  if (!ch) throw new IpcArgError(channel, '', 'unknown channel');
  if (ch.args === null) {
    if (args !== undefined) throw new IpcArgError(channel, '', 'takes no arguments');
    return undefined;
  }
  return check(channel, '', ch.args, args);
}

function check(channel, path, s, v) {
  const fail = (msg) => {
    throw new IpcArgError(channel, path, msg);
  };
  if (v === null && s.nullable) return null;
  switch (s.type) {
    case 'any':
      return v;
    case 'bool':
      if (typeof v !== 'boolean') fail('must be a boolean');
      return v;
    case 'int':
      if (!Number.isSafeInteger(v)) fail('must be an integer');
      if (s.min !== undefined && v < s.min) fail(`must be >= ${s.min}`);
      if (s.max !== undefined && v > s.max) fail(`must be <= ${s.max}`);
      return v;
    case 'string': {
      if (typeof v !== 'string') fail('must be a string');
      if (v.length > s.max) fail(`longer than ${s.max}`);
      if (s.min !== undefined && v.length < s.min) fail(`shorter than ${s.min}`);
      const inEnum = s.enum ? s.enum.includes(v) : false;
      const inPattern = s.pattern ? s.pattern.test(v) : false;
      if ((s.enum || s.pattern) && !inEnum && !inPattern) fail('not an allowed value');
      return v;
    }
    case 'array': {
      if (!Array.isArray(v)) fail('must be an array');
      if (v.length > s.max) fail(`more than ${s.max} items`);
      const out = v.map((x, i) => check(channel, `${path}[${i}]`, s.items, x));
      if (s.unique && new Set(out).size !== out.length) fail('items must be unique');
      return out;
    }
    case 'object': {
      if (typeof v !== 'object' || v === null || Array.isArray(v)) fail('must be an object');
      const proto = Object.getPrototypeOf(v);
      if (proto !== Object.prototype && proto !== null) fail('must be a plain object');
      const out = {};
      for (const k of Object.keys(v)) {
        if (!Object.hasOwn(s.props, k)) fail(`unknown key ${JSON.stringify(k.slice(0, 40))}`);
      }
      for (const k of s.required) {
        if (!Object.hasOwn(v, k) || v[k] === undefined) fail(`missing ${k}`);
      }
      for (const [k, ps] of Object.entries(s.props)) {
        if (Object.hasOwn(v, k) && v[k] !== undefined) out[k] = check(channel, path ? `${path}.${k}` : k, ps, v[k]);
      }
      return out;
    }
    default:
      fail(`bad schema type ${s.type}`);
  }
}

/**
 * Shapes carried over IPC (C3). Documentation only; state.js (WP-A) assembles ShellState.
 *
 * @typedef {object} ShellState
 * @property {TabState[]} tabs              in display order
 * @property {'hub'|'settings'|string} view  the selected view: 'hub', 'settings' or a tab id
 * @property {HubState} hub
 * @property {Settings} settings
 * @property {{level: 'info'|'warn'|'error', text: string}[]} [banners]  e.g. "sy not found: run sy setup"
 * @property {{app: string, electron: string, chrome: string, platform: string}} [about]  versions for Settings, About
 *
 * @typedef {object} TabState
 * @property {string} id
 * @property {string} name
 * @property {string|null} label      seat label from the hub when known
 * @property {string|null} colour     seat colour from the hub when known
 * @property {string|null} seat       hub seat id (seat_…) when known; seat:update needs it (WP-D)
 * @property {'on'|'off'|'never_asked'} reading
 * @property {boolean} needs_warning  true when the warning dialog should show (C5)
 * @property {boolean} send_to_hub
 * @property {'idle'|'reading'|'retrying'|'stopped'|'off'} read_state
 * @property {{status: number, at: string}|null} stopped
 * @property {string|null} retry_at   RFC 3339, while retrying
 * @property {string|null} last_read  RFC 3339
 * @property {StripWindow[]} windows  the strip: hub seat windows when known, else the local parse
 * @property {'hub'|'local'|null} windows_source
 * @property {boolean} signed_in
 *
 * @typedef {object} StripWindow
 * @property {string} key             five_hour, seven_day, seven_day_opus, …
 * @property {string|null} label
 * @property {number|null} used_pct
 * @property {string|null} resets_at  RFC 3339
 *
 * @typedef {object} HubState
 * @property {string|null} url
 * @property {boolean} connected      sy is connected to a hub (sy desktop info)
 * @property {boolean} reachable
 * @property {boolean} paired         the app holds a desktop token for url
 * @property {boolean} owner          the hub view has an owner session
 * @property {string|null} version
 * @property {boolean} local          the hub runs on this machine
 * @property {'running'|'stopped'|'missing'|null} service
 * @property {string|null} admin      admin channel ('local', a command prefix) or null
 * @property {number} needs_you
 * @property {'ok'|'no_sy'|'not_connected'|'dial'|'needs_pairing'|'unreachable'} status
 *
 * @typedef {object} Settings
 * @property {boolean} keep_running
 * @property {boolean} notifications
 * @property {boolean} start_hidden
 * @property {boolean} autostart      read from the OS (autostart.js), not stored
 * @property {boolean} close_notice_shown
 * @property {'basic_text'|string|null} [storage_backend]  safeStorage backend; 'basic_text' shows a warning
 */
