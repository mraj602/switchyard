// Pure view helpers of the shell (WP-D): everything that decides what to show, with no DOM, so
// node --test covers it. app.js turns these results into elements.
// Shared code is imported as '../shared/<file>.js': that resolves to src/shared/ on disk (tests,
// a static server rooted at src/) and to app://shell/shared/ under the protocol (C2 mapping).

import { clock, agoLabel, humanKey } from '../shared/format.js';

export const COLOUR_RE = /^#[0-9a-f]{6}$/;
export const LABEL_RE = /^[a-z0-9][a-z0-9-]{0,23}$/;
export const PAIR_CODE_RE = /^\d{3}-?\d{3}$/;
export const TAB_NAME_MAX = 24;
const SEAT_FALLBACK = ['var(--seat-0)', 'var(--seat-1)', 'var(--seat-2)', 'var(--seat-3)'];

// ---- tabs

/** What the top bar and strip call a tab: the seat label when known, else the name. */
export const tabTitle = (tab) => tab.label || tab.name;

/** A CSS colour for a tab's dot: the seat's #rrggbb when valid, else a board seat token. */
export function tabColour(tab, index = 0) {
  if (typeof tab.colour === 'string' && COLOUR_RE.test(tab.colour.toLowerCase())) return tab.colour.toLowerCase();
  return SEAT_FALLBACK[((index % 4) + 4) % 4];
}

/** 0 → '', 3 → '3', 12 → '9+' (the badge on Hub). */
export function badgeText(n) {
  if (!Number.isInteger(n) || n <= 0) return '';
  return n > 9 ? '9+' : String(n);
}

/** Tab name rules of C3: 1 to 24 characters after trimming. Returns an error text or ''. */
export function nameError(name) {
  const s = String(name ?? '').trim();
  if (!s) return 'Give the tab a name.';
  if (s.length > TAB_NAME_MAX) return `At most ${TAB_NAME_MAX} characters.`;
  return '';
}

/** Seat label rule (SeatPatch): '' means clear. Returns an error text or ''. */
export function labelError(label) {
  const s = String(label ?? '');
  if (s === '') return '';
  return LABEL_RE.test(s) ? '' : 'Lower-case letters, digits and "-", up to 24, starting with a letter or digit.';
}

/** The order the keyboard walks: Hub first, then the tabs. */
export const viewOrder = (state) => ['hub', ...state.tabs.map((t) => t.id)];

/** Next view id when moving delta steps through the order, wrapping. */
export function cycleView(order, current, delta) {
  if (!order.length) return current;
  const i = order.indexOf(current);
  const from = i < 0 ? 0 : i;
  return order[(((from + delta) % order.length) + order.length) % order.length];
}

/** Ctrl/Cmd+digit → view id: 0 Hub, 1…9 the tabs. null when there is no such tab. */
export function viewForDigit(state, digit) {
  if (digit === 0) return 'hub';
  const t = state.tabs[digit - 1];
  return t ? t.id : null;
}

/** New order of ids after moving `id` by delta places; null when nothing moves. */
export function movedIds(ids, id, delta) {
  const from = ids.indexOf(id);
  const to = from + delta;
  if (from < 0 || to < 0 || to >= ids.length) return null;
  const out = ids.slice();
  out.splice(to, 0, out.splice(from, 1)[0]);
  return out;
}

/**
 * New order of ids after dropping `id` on `overId`: before it, or after it when `after`. The Hub
 * and "+" are not in `ids`, so they cannot move or be displaced. Null when nothing moves.
 */
export function droppedIds(ids, id, overId, after) {
  const from = ids.indexOf(id);
  if (from < 0 || id === overId || !ids.includes(overId)) return null;
  const rest = ids.filter((x) => x !== id);
  const at = rest.indexOf(overId) + (after ? 1 : 0);
  rest.splice(at, 0, id);
  return rest.every((x, i) => x === ids[i]) ? null : rest;
}

/** The view the shell is really showing: a dialog opened over a tab keeps that tab's strip. */
export const effectiveView = (state, overlayFor) => overlayFor || state.view;

// ---- the strip

/** Display label of a usage window: 5-hour, 7-day, else the hub label or the raw key. */
export function windowLabel(w) {
  if (w.key === 'five_hour') return '5-hour';
  if (w.key === 'seven_day') return '7-day';
  return w.label || humanKey(w.key);
}

/** Windows in strip order: 5h, 7d, then the rest by key. `primary` marks the two always shown. */
export function windowRows(tab, now = Date.now()) {
  const rank = (k) => (k === 'five_hour' ? 0 : k === 'seven_day' ? 1 : 2);
  return [...(tab.windows || [])]
    .sort((a, b) => rank(a.key) - rank(b.key) || String(a.key).localeCompare(String(b.key)))
    .map((w) => ({
      key: w.key,
      label: windowLabel(w),
      used: typeof w.used_pct === 'number' && Number.isFinite(w.used_pct) ? w.used_pct : null,
      reset: clock(w.resets_at, now),
      primary: rank(w.key) < 2,
    }));
}

/**
 * The reading state of a tab as one line and the action that goes with it.
 * kind: signed_out | never_asked | off | stopped | retrying | reading | idle | waiting.
 * action: 'review' (open the warning), 'resume', or null.
 */
export function readingStatus(tab, now = Date.now()) {
  if (tab.reading === 'never_asked' || tab.needs_warning) {
    return { kind: 'never_asked', text: 'Usage reading not enabled', action: 'review', actionLabel: 'Review' };
  }
  if (tab.reading === 'off' || tab.read_state === 'off') return { kind: 'off', text: 'Reading off', action: null };
  if (tab.stopped) {
    const s = tab.stopped.status;
    if (s === 403) return { kind: 'stopped', text: 'Usage not available for this account (403).', action: 'resume', actionLabel: 'Resume' };
    const why = s === 429 ? 'rate limited' : s === 401 ? 'signed out' : 'error';
    return { kind: 'stopped', text: `Stopped: ${why} (${s}).`, action: 'resume', actionLabel: 'Resume' };
  }
  if (!tab.signed_in) return { kind: 'signed_out', text: 'Sign in to read usage', action: null };
  if (tab.read_state === 'retrying') {
    const at = clock(tab.retry_at, now);
    return { kind: 'retrying', text: at ? `Retrying at ${at}` : 'Retrying', action: null };
  }
  if (tab.read_state === 'reading') return { kind: 'reading', text: 'Reading…', action: null };
  const last = agoLabel(tab.last_read, now);
  return { kind: 'idle', text: last ? `read ${last}` : 'Waiting for the first read', action: null };
}

/** Last-read clock for the title attribute ("last read 14:05"). */
export const lastReadTitle = (tab, now) => (tab.last_read ? `last read ${clock(tab.last_read, now)}` : '');

// ---- hub banners

/**
 * The card a hub status needs on the Hub tab, or null when the board can show (status ok, no error).
 * {level, title, text, command, action}. action: 'connect' (address + code), 'pair' (code field),
 * 'pair-click' (one click: admin channel or local hub), 'retry' (Try again, + 'service' on a local hub:
 * Start hub), 'service', or null.
 */
export function hubBanner(hub, now = Date.now()) {
  if (!hub) return null;
  switch (hub.status) {
    case 'ok':
      if (hub.board_blocked) {
        // The board asked for its own sign-in and the app could not give it: its login page is never shown.
        return { level: 'error', title: 'The board could not be signed in.', text: hub.error || 'It is tried again shortly. If it keeps failing, unpair this computer in App settings and pair it again.', command: null, action: 'retry' };
      }
      // e.g. a 403 from the hub: shown, but the token is kept (only a 401 means it is dead)
      return hub.error ? { level: 'error', title: null, text: hub.error, command: null, action: null } : null;
    case 'no_sy':
    case 'not_connected':
      return { level: 'info', title: 'Connect to a hub.', text: "Enter your hub's address. Account tabs work without a hub.", command: null, action: 'connect' };
    case 'dial':
      return { level: 'warn', title: null, text: 'This computer reaches its hub in forward mode, which is not supported in the desktop app. Use the hub from a browser, or connect this computer directly.', command: null, action: null };
    case 'needs_pairing': {
      const oneClick = !!(hub.admin || hub.local);
      const how = oneClick
        ? "This computer can reach the hub's computer, so pairing needs no code."
        : 'Get a code on any owner device: More, Devices, Add desktop app.';
      return hub.removed
        ? { level: 'warn', title: 'This computer was removed from the hub.', text: `Someone removed it in Devices. Pair again to see the board. ${how}`, command: null, action: oneClick ? 'pair-click' : 'pair' }
        : { level: 'info', title: 'Pair this computer.', text: how, command: null, action: oneClick ? 'pair-click' : 'pair' };
    }
    case 'unreachable': {
      const seen = hub.last_seen ? ` Last seen ${agoLabel(hub.last_seen, now) || 'a while ago'}.` : '';
      return { level: 'error', title: "Can't reach the hub.", text: `${hub.url || 'The hub'}.${seen}`, command: null, action: hub.local ? 'retry-service' : 'retry' };
    }
    default:
      return null;
  }
}

/** Whether the board itself can be shown (the hub module has a view to put over the shell). */
export const hubUsable = (hub) => !!hub && hub.status === 'ok' && !hub.board_blocked;

/** "14 Nov" for an RFC 3339 time, or ''. */
export function shortDate(iso) {
  const t = Date.parse(iso ?? '');
  if (!Number.isFinite(t)) return '';
  return new Date(t).toLocaleDateString('en-GB', { day: 'numeric', month: 'short' });
}

/** The top-bar chip: text and level for any state ("Hub · Connected"). */
export function hubChip(hub) {
  if (!hub) return { text: 'Hub · Unknown', level: 'info' };
  switch (hub.status) {
    case 'ok': return hub.board_blocked ? { text: 'Hub · Sign-in failed', level: 'error' } : { text: 'Hub · Connected', level: hub.error ? 'error' : 'ok' };
    case 'needs_pairing': return { text: hub.removed ? 'Hub · Removed' : 'Hub · Not paired', level: 'warn' };
    case 'unreachable': return { text: 'Hub · Unreachable', level: 'error' };
    case 'dial': return { text: 'Hub · Forward mode', level: 'warn' };
    default: return { text: 'Hub · Not connected', level: 'info' };
  }
}

/** The owner pill next to the chip (a paired hub only): "Read-only" or "Owner · until 14 Nov". */
export function ownerPill(hub) {
  if (!hub || hub.status !== 'ok' || hub.board_blocked) return null;
  if (!hub.owner) return { text: 'Read-only', owner: false };
  const d = shortDate(hub.owner_until);
  return { text: d ? `Owner · until ${d}` : 'Owner', owner: true };
}

/** One-line hub status for Settings. */
export function hubSummary(hub) {
  if (!hub) return 'Hub: unknown';
  switch (hub.status) {
    // "v0.6.0", but a dev build's version is the word "dev": no "vdev".
    case 'ok': return hub.version ? `Connected · ${/^\d/.test(hub.version) ? 'v' : ''}${hub.version}` : 'Connected';
    case 'no_sy': return 'Not connected';
    case 'not_connected': return 'Not connected';
    case 'dial': return 'Forward mode (unsupported)';
    case 'needs_pairing': return hub.removed ? 'Removed from the hub' : 'Needs pairing';
    case 'unreachable': return 'Unreachable';
    default: return 'Unknown';
  }
}

// ---- Claude Code folders (accounts:local) and sharing

/** Share switches of `sy account share`, in the order of the table, with their rows' names. */
export const SHARE_SWITCHES = Object.freeze([
  { id: 'settings', label: 'Settings (hooks, permissions, plugins on/off)', items: ['settings.json'] },
  { id: 'instructions', label: 'Instructions (CLAUDE.md, rules)', items: ['CLAUDE.md', 'rules'] },
  { id: 'addons', label: 'Skills, agents, commands', items: ['skills', 'agents', 'commands', 'keybindings.json'] },
  { id: 'plugins', label: 'Plugins', items: ['plugins'] },
  { id: 'mcp', label: 'MCP servers (sign-ins stay per account)', items: [] },
  { id: 'history', label: 'History and memory (transcripts, auto-memory)', items: ['projects'] },
  { id: 'prompts', label: 'Prompt history and plans', items: ['history.jsonl', 'plans'] },
]);

/** The `folders` array of an `sy account list --json` reply, whatever wrapper it has. */
export function foldersOf(reply) {
  if (Array.isArray(reply)) return reply;
  if (reply && Array.isArray(reply.folders)) return reply.folders;
  if (reply && reply.state && Array.isArray(reply.state.folders)) return reply.state.folders;
  return [];
}

/** The primary folder (the source of shared items), if the list says which. */
export function isPrimary(folder) {
  return folder.primary === true || folder.folder === '~/.claude';
}

/**
 * State of one switch for one folder: 'source' (primary), 'on', 'off', 'elsewhere' (linked to
 * another folder: shown but not editable) or 'unknown' (no data for it).
 */
export function switchState(folder, sw) {
  if (isPrimary(folder)) return 'source';
  if (sw.id === 'mcp') {
    if (typeof folder.mcp_sync === 'boolean') return folder.mcp_sync ? 'on' : 'off';
    if (typeof folder.mcp === 'boolean') return folder.mcp ? 'on' : 'off';
    return 'unknown';
  }
  const states = sw.items.map((n) => folder.items?.[n]?.state).filter(Boolean);
  if (!states.length) return 'unknown';
  if (states.includes('elsewhere')) return 'elsewhere';
  if (states.every((s) => s === 'linked' || s === 'copy')) return 'on';
  return 'off';
}

/**
 * The `--share` value for turning `changed` on or off for a folder: the switches that stay on,
 * plus the changed one (or `-name` when it is turned off, which the CLI needs to un-share).
 * Matches ^[a-z,=:+-]{1,200}$.
 */
export function shareSpec(folder, changed, on) {
  const parts = [];
  for (const sw of SHARE_SWITCHES) {
    const st = sw.id === changed ? (on ? 'on' : 'off') : switchState(folder, sw);
    if (st === 'on' || st === 'elsewhere') parts.push(sw.id);
    else if (sw.id === changed) parts.push(`-${sw.id}`);
  }
  return parts.join(',');
}

/** `sy account login` command text for a folder (copied by the person; needs a terminal). */
export const loginCommand = (folder) => `sy account login ${folder.label || folder.folder}`;

/** Whether a folder path is acceptable as `accounts:share.folder`. */
export const SHARE_FOLDER_RE = /^~?[A-Za-z0-9._/-]{1,200}$/;
