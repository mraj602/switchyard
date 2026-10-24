// Assembles ShellState (shared/ipc.js) from the bus (tabs, usage, hub, needs_you, settings)
// and the selected view, and pushes it to the shell debounced 100 ms. Never holds tokens or
// secrets: every field is copied by name from the bus payloads, nothing is passed through.
//
// Contract: docs/spec/desktop.md C3 ShellState. It registers the two
// channels: state:get (the snapshot) and view:select (window.select, then a push).
//
// Where each part comes from:
// - tabs: bus 'tabs' (TabInfo), in that order, merged with the newest bus 'usage' payload of
//   each tab and the seat label and colour from hub.seats() when the tab's seat
//   {account, org} matches a hub seat.
// - view: window.selected().
// - hub: bus 'hub' (HubState); needs_you from bus 'needs_you' when newer.
// - settings: bus 'settings', else the stored settings (store.get().settings) with
//   autostart false.
// - banners: setBanner(key, banner|null), for notices main wants to keep on screen.
// - about: deps.about (app, Electron and Chromium versions, platform), when boot gives it.
// - update: shared/version.js updateNotice of the app's version against the hub's and wy's.

import { WARNING_VERSION, READING_ON, READING_OFF, READING_NEVER_ASKED, STATE_PUSH_DEBOUNCE_MS, TAB_ID_RE } from '../shared/constants.js';
import { TOAST_LEVELS, SEAT_ID_PATTERN } from '../shared/ipc.js';
import { updateNotice } from '../shared/version.js';

const SEAT_ID_RE = new RegExp(SEAT_ID_PATTERN);

const READINGS = new Set([READING_ON, READING_OFF, READING_NEVER_ASKED]);
const READ_STATES = new Set(['idle', 'reading', 'retrying', 'stopped', 'off']);
const HUB_STATUSES = new Set(['ok', 'no_wy', 'not_connected', 'dial', 'needs_pairing', 'unreachable']);
const SERVICES = new Set(['running', 'stopped', 'missing']);

const str = (v, max = 500) => (typeof v === 'string' ? v.slice(0, max) : null);
const bool = (v) => v === true;
const num = (v) => (typeof v === 'number' && Number.isFinite(v) ? v : null);

/** The HubState before the hub module has said anything. */
export function defaultHub() {
  return {
    url: null, connected: false, reachable: false, paired: false, owner: false, version: null,
    local: false, service: null, admin: null, needs_you: 0, status: 'no_wy', storage_backend: null, error: null,
    removed: false, board_blocked: false, direct: false, owner_until: null, last_seen: null, machine: '',
    cert: null, insecure: false, pinned: false, wy_version: null,
  };
}

const FP_RE = /^([0-9A-F]{2}:){31}[0-9A-F]{2}$/;

/** The hub's certificate problem, field by field (HubCert); null when it is not one. */
function cleanCert(c) {
  if (!c || typeof c !== 'object' || typeof c.fingerprint !== 'string' || !FP_RE.test(c.fingerprint)) return null;
  return {
    url: str(c.url, 2048),
    origin: str(c.origin, 2048),
    fingerprint: c.fingerprint,
    pinned: typeof c.pinned === 'string' && FP_RE.test(c.pinned) ? c.pinned : null,
    changed: bool(c.changed),
    trusted: bool(c.trusted),
    reason: str(c.reason, 80),
    subject: str(c.subject, 200),
    issuer: str(c.issuer, 200),
    valid_to: str(c.valid_to, 40),
  };
}

function cleanStopped(s) {
  if (!s || typeof s !== 'object') return null;
  const status = num(s.status);
  return status === null ? null : { status, at: str(s.at, 64) };
}

function cleanWindows(ws) {
  if (!Array.isArray(ws)) return [];
  return ws.slice(0, 32).filter((w) => w && typeof w === 'object' && typeof w.key === 'string').map((w) => ({
    key: w.key.slice(0, 64),
    label: str(w.label, 64),
    used_pct: num(w.used_pct),
    resets_at: str(w.resets_at, 64),
  }));
}

function seatLook(seats, seat) {
  if (!seat || typeof seat !== 'object' || !Array.isArray(seats)) return null;
  return seats.find((s) => s && s.account === seat.account && s.org === seat.org) ?? null;
}

const seatName = (s) => str(s?.label ?? s?.email ?? s?.org_name, 24);

/**
 * The hub accounts with no tab on this computer: none of the tabs is signed in as the account or
 * was made for it (matched by account uuid, never by email). One entry per account, in hub order;
 * hidden seats and seats with nothing to call them by are left out.
 * @returns {{seat: string, label: string}[]}
 */
export function missingAccounts(seats, tabs) {
  if (!Array.isArray(seats)) return [];
  const have = new Set();
  for (const t of Array.isArray(tabs) ? tabs : []) {
    if (t?.seat?.account) have.add(t.seat.account);
    if (t?.expect?.account) have.add(t.expect.account);
  }
  const out = [];
  for (const s of seats) {
    if (!s || s.hidden || typeof s.account !== 'string' || have.has(s.account)) continue;
    const label = seatName(s);
    if (!label || typeof s.id !== 'string' || !SEAT_ID_RE.test(s.id)) continue;
    have.add(s.account);
    out.push({ seat: s.id, label });
  }
  return out;
}

/**
 * One TabState from a TabInfo (bus 'tabs'), the newest usage payload of that tab and the hub seats.
 * @returns {import('../shared/ipc.js').TabState}
 */
export function tabState(info, usage, seats, infos = []) {
  const reading = READINGS.has(info.reading) ? info.reading : READING_NEVER_ASKED;
  const accepted = Number.isSafeInteger(info.warning_accepted) ? info.warning_accepted : 0;
  const signedIn = bool(info.signed_in);
  const stopped = cleanStopped(usage?.stopped ?? info.stopped);
  const look = seatLook(seats, info.seat);
  let readState = READ_STATES.has(usage?.read_state) ? usage.read_state : null;
  if (reading === READING_OFF) readState = 'off';
  else if (!readState) readState = stopped ? 'stopped' : 'idle';
  // A tab made for a hub account, now signed in as another one (the usage reader learned its uuid).
  const expected = info.expect?.account ? (str(info.expect.label, 24) ?? 'the account') : null;
  let mismatch = null;
  if (expected && signedIn && info.seat?.account && info.seat.account !== info.expect.account) {
    const actual = Array.isArray(seats) ? seats.find((s) => s && s.account === info.seat.account) : null;
    // Another tab already signed in as the account this one is signed in as (and really is its tab).
    const shared = (Array.isArray(infos) ? infos : []).some((o) => o && o.id !== info.id && bool(o.signed_in) && o.seat?.account === info.seat.account
      && !(o.expect?.account && o.expect.account !== o.seat.account));
    mismatch = { expected, actual: seatName(actual), shared };
  }
  return {
    id: info.id,
    name: str(info.name, 24) ?? '',
    expected,
    mismatch,
    // A mismatched tab keeps its own name: the label, colour and seat of the account it landed in are not its.
    label: mismatch ? null : str(look?.label, 24),
    colour: mismatch ? null : str(look?.colour, 7),
    seat: !mismatch && typeof look?.id === 'string' && SEAT_ID_RE.test(look.id) ? look.id : null,
    reading,
    // C5: shown once a tab could read (signed in) and the accepted version is older, unless
    // the user said "Not now" (reading off).
    needs_warning: signedIn && reading !== READING_OFF && accepted < WARNING_VERSION,
    send_to_hub: info.send_to_hub !== false,
    read_state: readState,
    stopped,
    retry_at: str(usage?.retry_at, 64),
    last_read: str(usage?.last_read, 64),
    windows: cleanWindows(usage?.windows),
    windows_source: usage?.windows_source === 'hub' || usage?.windows_source === 'local' ? usage.windows_source : null,
    signed_in: signedIn,
  };
}

function hubState(h, needsYou) {
  const d = defaultHub();
  if (h && typeof h === 'object') {
    d.url = str(h.url, 2048);
    d.connected = bool(h.connected);
    d.reachable = bool(h.reachable);
    d.paired = bool(h.paired);
    d.owner = bool(h.owner);
    d.version = str(h.version, 64);
    d.local = bool(h.local);
    d.service = SERVICES.has(h.service) ? h.service : null;
    d.admin = str(h.admin, 500);
    d.needs_you = Number.isSafeInteger(h.needs_you) && h.needs_you >= 0 ? h.needs_you : 0;
    d.status = HUB_STATUSES.has(h.status) ? h.status : 'no_wy';
    d.storage_backend = str(h.storage_backend, 40);
    d.error = str(h.error, 500);
    d.removed = bool(h.removed);
    d.board_blocked = bool(h.board_blocked);
    d.direct = bool(h.direct);
    d.owner_until = str(h.owner_until, 40);
    d.last_seen = str(h.last_seen, 40);
    d.machine = str(h.machine, 80) ?? '';
    d.cert = cleanCert(h.cert);
    d.insecure = bool(h.insecure);
    d.pinned = bool(h.pinned);
    d.wy_version = str(h.wy_version, 64);
  }
  if (needsYou && Number.isSafeInteger(needsYou.count) && needsYou.count >= 0) d.needs_you = needsYou.count;
  return d;
}

/** UpdateNotice (shared/ipc.js) without the URL, which main alone opens (app:open-release). */
function updateState(app, h) {
  const n = updateNotice({ app, hub: h?.version, wy: h?.wy_version });
  return n ? { kind: n.kind, app: n.app, version: n.version, from: n.from } : null;
}

function settingsState(s, stored, hubBackend) {
  const src = s && typeof s === 'object' ? s : { ...(stored ?? {}), autostart: false };
  const out = {
    keep_running: src.keep_running !== false,
    notifications: src.notifications !== false,
    start_hidden: bool(src.start_hidden),
    autostart: bool(src.autostart),
    close_notice_shown: bool(src.close_notice_shown),
  };
  if (typeof src.storage_backend === 'string') out.storage_backend = src.storage_backend.slice(0, 40);
  else if (typeof hubBackend === 'string') out.storage_backend = hubBackend.slice(0, 40); // the hub's token store knows it too
  return out;
}

/**
 * @param {{bus: object, ipc: object, window: object, store?: object, hub?: object,
 *          timers?: {setTimeout: Function, clearTimeout: Function}, log?: object}} deps
 *   hub: only seats() is used (label and colour); store: only get().settings, before E's first event
 * @returns {{
 *   snapshot(): object,
 *   changed(): void,
 *   setBanner(key: string, banner: {level: 'info'|'warn'|'error', text: string}|null): void,
 *   stop(): void,
 * }}
 */
/** The About lines, copied by name. */
export function aboutState(a) {
  return { app: str(a.app, 40) ?? '', electron: str(a.electron, 40) ?? '', chrome: str(a.chrome, 40) ?? '', platform: str(a.platform, 40) ?? '' };
}

export function create(deps) {
  const { bus, ipc, window } = deps;
  const timers = deps.timers ?? globalThis;
  const log = deps.log ?? console;
  /** tab id → newest usage payload */
  const usage = new Map();
  const banners = new Map();
  let timer = null;
  let stopped = false;

  const seats = () => {
    try {
      return deps.hub?.seats?.() ?? [];
    } catch {
      return [];
    }
  };

  function tabsInfo() {
    const t = bus.last('tabs')?.tabs;
    return Array.isArray(t) ? t.filter((x) => x && typeof x.id === 'string' && TAB_ID_RE.test(x.id)) : [];
  }

  function snapshot() {
    const s = seats();
    return {
      tabs: tabsInfo().map((info, _i, all) => tabState(info, usage.get(info.id), s, all)),
      missing: missingAccounts(s, tabsInfo()),
      view: window.selected(),
      hub: hubState(bus.last('hub'), bus.last('needs_you')),
      settings: settingsState(bus.last('settings'), deps.store?.get?.().settings, bus.last('hub')?.storage_backend),
      banners: [...banners.values()],
      ...(deps.about ? { about: aboutState(deps.about) } : {}),
      update: updateState(deps.about?.app, bus.last('hub')),
    };
  }

  function push() {
    timer = null;
    if (stopped) return;
    try {
      ipc.send('state', snapshot());
    } catch (err) {
      log.error?.('state: push failed:', err);
    }
  }

  function changed() {
    if (stopped || timer !== null) return;
    timer = timers.setTimeout(push, STATE_PUSH_DEBOUNCE_MS);
  }

  function setBanner(key, banner) {
    if (typeof key !== 'string' || !key) throw new Error('state.setBanner: bad key');
    if (banner === null) banners.delete(key);
    else {
      if (!TOAST_LEVELS.includes(banner?.level) || typeof banner.text !== 'string' || !banner.text) throw new Error('state.setBanner: bad banner');
      banners.set(key, { level: banner.level, text: banner.text.slice(0, 500) });
    }
    changed();
  }

  const offs = [
    bus.on('tabs', (p) => {
      const ids = new Set((Array.isArray(p.tabs) ? p.tabs : []).map((t) => t?.id));
      for (const id of usage.keys()) if (!ids.has(id)) usage.delete(id);
      changed();
    }),
    bus.on('usage', (p) => {
      if (typeof p.id === 'string') usage.set(p.id, p);
      changed();
    }),
    bus.on('hub', changed),
    bus.on('needs_you', changed),
    bus.on('settings', changed),
    window.onSelect ? window.onSelect(changed) : () => {},
  ];

  ipc.register('state:get', () => snapshot());
  ipc.register('view:select', ({ view }) => {
    if (view !== 'hub' && view !== 'settings' && !tabsInfo().some((t) => t.id === view)) throw new Error('view:select: unknown tab');
    window.select(view);
    changed();
  });
  if (window.shell) ipc.watchShell?.(window.shell);

  return Object.freeze({
    snapshot,
    changed,
    setBanner,
    stop() {
      stopped = true;
      if (timer !== null) timers.clearTimeout(timer);
      timer = null;
      for (const off of offs) off();
    },
  });
}
