// Pure view helpers of the shell: everything that decides what to show, with no DOM, so
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

/** "claude-6", "claude-6 and claude-7", "a, b and c", "a, b and 2 more". */
export function nameList(labels) {
  const l = labels.filter(Boolean);
  if (l.length <= 1) return l.join('');
  if (l.length === 2) return `${l[0]} and ${l[1]}`;
  if (l.length === 3) return `${l[0]}, ${l[1]} and ${l[2]}`;
  return `${l[0]}, ${l[1]} and ${l.length - 2} more`;
}

/** The quiet line for hub accounts with no tab here, or '' when none. */
export function missingNotice(missing) {
  const l = Array.isArray(missing) ? missing.map((m) => m.label) : [];
  if (!l.length) return '';
  return `${nameList(l)} ${l.length === 1 ? 'has' : 'have'} no tab here.`;
}

/** The notice for a tab made for one account but signed in as another, or '' (a uuid match decided it in main). */
export function mismatchNotice(tab) {
  const m = tab?.mismatch;
  if (!m) return '';
  return `This tab is signed in as ${m.actual || 'another account'}, not ${m.expected}.`;
}

/** The one-click fix on that notice: "Use this tab for claude-7" (an account the hub does not know: "this account"). */
export function adoptLabel(tab) {
  const m = tab?.mismatch;
  return m ? `Use this tab for ${m.actual || 'this account'}` : '';
}

/** The second option on the notice. */
export function signOutLabel(tab) {
  const m = tab?.mismatch;
  return m ? `Sign out and sign in as ${m.expected}` : '';
}

/** The confirm text when another tab is already signed in as the account, else '' (no confirm). */
export function adoptConfirm(tab) {
  const m = tab?.mismatch;
  if (!m || !m.shared) return '';
  return `Another tab is already signed in as ${m.actual || 'this account'}. Use this one too?`;
}

/** The toast after "Use this tab for ...": {text, action} where action = {label, seat} offers the account that lost its tab. */
export function adoptToast(result) {
  const name = result?.name || 'account';
  const offer = result?.offer;
  if (!offer) return { text: `This tab is now ${name}.`, action: null };
  return { text: `This tab is now ${name}. Add a tab for ${offer.label} to sign in to it.`, action: { label: `Add tab for ${offer.label}`, seat: offer.seat, name: offer.label } };
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

/**
 * Which tabs of the top bar fit. `widths` are the natural widths of the strip's tabs in order
 * (Hub first), `available` the room for them, `moreWidth` the width of the "N more" button and
 * `selected` the index of the shown tab (-1: none). Hub (index 0) and the selected tab always
 * stay; the rest are kept in order while they fit. Returns one boolean per tab; when any is
 * false the caller shows the "N more" menu button.
 */
export function fitTabs(widths, available, moreWidth, selected, gap = 2) {
  const n = widths.length;
  const total = widths.reduce((a, w) => a + w, 0) + gap * Math.max(0, n - 1);
  if (total <= available) return widths.map(() => true);
  const keep = widths.map((_, i) => i === 0 || i === selected);
  let used = widths.reduce((a, w, i) => a + (keep[i] ? w + gap : 0), 0) + moreWidth;
  for (let i = 0; i < n; i++) {
    if (keep[i]) continue;
    if (used + widths[i] + gap > available) break;
    keep[i] = true;
    used += widths[i] + gap;
  }
  return keep;
}

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

/**
 * Windows in strip order: 5h, 7d, then the rest by key. `primary` marks the two always shown.
 * `reset` is the words after the %: "15:10", "reset at Sat 10:10" once that time has passed (0 %
 * since), "not started" for an idle window (0 % with no reset time: claude.ai has not started
 * it; api.yaml Window.state idle), else ''.
 */
export function windowRows(tab, now = Date.now()) {
  const rank = (k) => (k === 'five_hour' ? 0 : k === 'seven_day' ? 1 : 2);
  return [...(tab.windows || [])]
    .sort((a, b) => rank(a.key) - rank(b.key) || String(a.key).localeCompare(String(b.key)))
    .map((w) => {
      let used = typeof w.used_pct === 'number' && Number.isFinite(w.used_pct) ? w.used_pct : null;
      const at = Date.parse(w.resets_at || '');
      let reset = clock(w.resets_at, now);
      if (used === 0 && !w.resets_at) reset = 'not started';
      else if (used !== null && reset && at <= now) [used, reset] = [0, `reset at ${reset}`];
      return { key: w.key, label: windowLabel(w), used, reset, primary: rank(w.key) < 2 };
    });
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
  // The hub's https certificate is not trusted, or not the pinned one: nothing else can work
  // until the person compares its fingerprint and trusts it (C6).
  if (hub.cert) {
    return hub.cert.changed
      ? { level: 'error', title: "The hub's certificate changed.", text: `${hub.cert.origin} now presents a different certificate than the one this computer trusted. If you did not replace it, someone may be intercepting the connection. Nothing is sent to the hub until you decide.`, command: null, action: 'cert' }
      : { level: 'warn', title: "This hub's certificate is not trusted yet.", text: `${hub.cert.origin} uses a certificate that this computer cannot check (for example a self-signed one). Compare its fingerprint with the one on the hub's computer, then trust it.`, command: null, action: 'cert' };
  }
  switch (hub.status) {
    case 'ok':
      if (hub.board_blocked) {
        // The board asked for its own sign-in and the app could not give it: its login page is never shown.
        return { level: 'error', title: 'The board could not be signed in.', text: hub.error || 'It is tried again shortly. If it keeps failing, unpair this computer in App settings and pair it again.', command: null, action: 'retry' };
      }
      // e.g. a 403 from the hub: shown, but the token is kept (only a 401 means it is dead)
      return hub.error ? { level: 'error', title: null, text: hub.error, command: null, action: null } : null;
    case 'no_wy':
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

// ---- the hub's certificate (C6) and plain http

/** The warning shown while the hub is reached over plain http from another computer. */
export const INSECURE_TEXT = 'Not encrypted: this hub uses plain http, so the hub token and everything the app exchanges with it cross the network unencrypted. Use an https address (for example tailscale serve or a reverse proxy) to fix this.';

/** The warning under the Connect form when the typed address is plain http to another computer. */
export function insecureAddress(text) {
  const t = String(text || '').trim();
  if (!/^http:\/\//i.test(t)) return false;
  try {
    const h = new URL(t).hostname.replace(/^\[|\]$/g, '').toLowerCase();
    return !(h === 'localhost' || h === '::1' || /^127\.\d{1,3}\.\d{1,3}\.\d{1,3}$/.test(h));
  } catch {
    return false;
  }
}

/** A fingerprint split in groups of four pairs, one group per line half, for comparing by eye. */
export function fingerprintGroups(fp) {
  const pairs = String(fp || '').split(':').filter(Boolean);
  const out = [];
  for (let i = 0; i < pairs.length; i += 4) out.push(pairs.slice(i, i + 4).join(':'));
  return out;
}

/** The command that prints the fingerprint on the hub's computer (or the computer serving https for it). */
export function fingerprintCommand(origin) {
  let u;
  try {
    u = new URL(origin);
  } catch {
    return '';
  }
  const host = u.hostname.replace(/^\[|\]$/g, '');
  const ip = /^[\d.]+$/.test(host) || host.includes(':');
  const target = host.includes(':') ? `[${host}]:${u.port || 443}` : `${host}:${u.port || 443}`;
  return `openssl s_client -connect ${target}${ip ? '' : ` -servername ${host}`} </dev/null 2>/dev/null | openssl x509 -noout -fingerprint -sha256`;
}

/** The top-bar chip: text and level for any state ("Hub · Connected"). */
export function hubChip(hub) {
  if (!hub) return { text: 'Hub · Unknown', level: 'info' };
  switch (hub.status) {
    case 'ok': return hub.board_blocked ? { text: 'Hub · Sign-in failed', level: 'error' } : { text: 'Hub · Connected', level: hub.error ? 'error' : 'ok' };
    case 'needs_pairing': return { text: hub.removed ? 'Hub · Removed' : 'Hub · Not paired', level: 'warn' };
    case 'unreachable': return hub.cert ? { text: hub.cert.changed ? 'Hub · Certificate changed' : 'Hub · Certificate', level: 'error' } : { text: 'Hub · Unreachable', level: 'error' };
    case 'dial': return { text: 'Hub · Forward mode', level: 'warn' };
    default: return { text: 'Hub · Not connected', level: 'info' };
  }
}

/**
 * What the one-click paths say while they wait. Through an SSH admin channel `wy` takes
 * ~15 s, so the person is told what is happening and how long it can take.
 * kind: 'pair' | 'owner'.
 */
export function progressText(hub, kind) {
  const ssh = !!(hub && hub.admin && hub.admin !== 'local' && !hub.local);
  const head = kind === 'owner' ? 'Signing in as owner…' : 'Pairing…';
  return ssh ? `${head} asking the hub's computer over SSH (can take 20 seconds)` : head;
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
    case 'no_wy': return 'Not connected';
    case 'not_connected': return 'Not connected';
    case 'dial': return 'Forward mode (unsupported)';
    case 'needs_pairing': return hub.removed ? 'Removed from the hub' : 'Needs pairing';
    case 'unreachable': return hub.cert ? (hub.cert.changed ? 'Certificate changed' : 'Certificate not trusted') : 'Unreachable';
    default: return 'Unknown';
  }
}

// ---- Claude Code folders (accounts:local) and sharing

/** Share switches of `wy account share`, in the order of the table, with their rows' names. */
export const SHARE_SWITCHES = Object.freeze([
  { id: 'settings', label: 'Settings (hooks, permissions, plugins on/off)', items: ['settings.json'] },
  { id: 'instructions', label: 'Instructions (CLAUDE.md, rules)', items: ['CLAUDE.md', 'rules'] },
  { id: 'addons', label: 'Skills, agents, commands', items: ['skills', 'agents', 'commands', 'keybindings.json'] },
  { id: 'plugins', label: 'Plugins', items: ['plugins'] },
  { id: 'mcp', label: 'MCP servers (sign-ins stay per account)', items: [] },
  { id: 'history', label: 'History and memory (transcripts, auto-memory)', items: ['projects'] },
  { id: 'prompts', label: 'Prompt history and plans', items: ['history.jsonl', 'plans'] },
]);

/** The `folders` array of an `wy account list --json` reply, whatever wrapper it has. */
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

/** `wy account login` command text for a folder (copied by the person; needs a terminal). */
export const loginCommand = (folder) => `wy account login ${folder.label || folder.folder}`;

/** Whether a folder path is acceptable as `accounts:share.folder`. */
export const SHARE_FOLDER_RE = /^~?[A-Za-z0-9._/-]{1,200}$/;
