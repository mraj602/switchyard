// A mock of the preload's `window.wy` with fixtures, so the renderer runs in a plain browser
// (development, screenshots, the headless smoke). Excluded from the package.
// Pick a scenario with ?scenario=<name>&view=<hub|settings|<tab id>>:
//   default | empty | missing | wrong[&shared=1][&unknown=1] | no_wy | needs_pairing | removed | direct_pairing | unreachable | board_blocked | owner | stopped | dark-data | warning
// It is a mock: no network, no storage, nothing leaves the page.

const params = new URLSearchParams(globalThis.location?.search || '');
const scenario = params.get('scenario') || 'default';

const iso = (offsetMs) => new Date(Date.now() + offsetMs).toISOString();
const MIN = 60_000;
const HOUR = 3_600_000;

const win = (key, used, resetIn) => ({ key, label: null, used_pct: used, resets_at: iso(resetIn) });
const tab = (id, name, extra = {}) => ({
  id, name, label: null, colour: null, seat: null, reading: 'on', needs_warning: false, send_to_hub: true,
  read_state: 'idle', stopped: null, retry_at: null, last_read: iso(-2 * MIN), windows: [], windows_source: 'hub', signed_in: true, ...extra,
});

function fixtures() {
  const tabs = [
    tab('t_0000000000000001', 'personal', {
      label: 'personal', colour: '#0e7c76', seat: 'seat_01HZZZZZZZZZZZZZZZZZZZZZZ1',
      windows: [win('five_hour', 62, 2 * HOUR), win('seven_day', 48, 3 * 24 * HOUR), win('seven_day_sonnet', 21, 3 * 24 * HOUR)],
    }),
    tab('t_0000000000000002', 'work', {
      label: 'work', colour: '#6a4fd6', seat: 'seat_01HZZZZZZZZZZZZZZZZZZZZZZ2',
      windows: [win('five_hour', 41, 3 * HOUR), win('seven_day', 30, 5 * 24 * HOUR), win('seven_day_opus', 12, 5 * 24 * HOUR)],
    }),
    tab('a1b2c3d4', 'client', { windows: [win('five_hour', 93, 40 * MIN), win('seven_day', 71, 24 * HOUR)], windows_source: 'local' }),
    tab('t_0000000000000004', 'research', { reading: 'off', read_state: 'off', last_read: null }),
  ];
  const hub = {
    url: 'https://hub.example.test', connected: true, reachable: true, paired: true, owner: false, version: '0.6.0',
    local: false, service: null, admin: null, needs_you: 3, status: 'ok',
  };
  const settings = { keep_running: true, notifications: true, start_hidden: false, autostart: false, close_notice_shown: true, storage_backend: 'gnome_libsecret' };
  // ?tabs=7 pads the strip with claude-N tabs (the top bar overflow check)
  for (let i = tabs.length + 1; i <= Number(params.get('tabs') || 0); i++) tabs.push(tab(`t_${String(i).padStart(16, '0')}`, `claude-${i}`, { label: `claude-${i}` }));
  if (params.get('ssh')) Object.assign(hub, { admin: 'ssh:hub-vm' });
  const s = { tabs, missing: [], view: params.get('view') || 'hub', hub, settings, banners: [] };

  switch (scenario) {
    case 'empty':
      s.tabs = [];
      break;
    case 'no_wy':
      Object.assign(s.hub, { connected: false, reachable: false, paired: false, url: null, version: null, status: 'no_wy', needs_you: 0 });
      break;
    case 'needs_pairing':
      Object.assign(s.hub, { paired: false, status: 'needs_pairing', needs_you: 0 });
      break;
    case 'stopped':
      s.tabs[1] = { ...s.tabs[1], read_state: 'stopped', stopped: { status: 429, at: iso(-10 * MIN) } };
      s.tabs[2] = { ...s.tabs[2], read_state: 'retrying', retry_at: iso(20 * MIN) };
      s.view = params.get('view') || s.tabs[1].id;
      break;
    case 'warning':
      s.tabs[3] = { ...s.tabs[3], reading: 'never_asked', needs_warning: true, read_state: 'idle' };
      s.view = params.get('view') || s.tabs[3].id;
      break;
    case 'missing':
      // hub accounts claude-6 and claude-7 have no tab here; the first tab was made for claude-1 but is signed in as another account
      s.hub.owner = true;
      s.hub.owner_until = iso(20 * 24 * HOUR);
      s.missing = [{ seat: 'seat_01HZZZZZZZZZZZZZZZZZZZZZ06', label: 'claude-6' }, { seat: 'seat_01HZZZZZZZZZZZZZZZZZZZZZ07', label: 'claude-7' }];
      s.tabs[3] = { ...s.tabs[3], name: 'claude-1', expected: 'claude-1', mismatch: { expected: 'claude-1', actual: 'work' }, reading: 'on', read_state: 'idle', last_read: iso(-MIN) };
      break;
    case 'wrong': {
      // a tab made for claude-6, signed in as claude-7 (?shared=1: another tab is claude-7's too; ?unknown=1: the hub does not know the account)
      s.hub.owner = true;
      s.hub.owner_until = iso(20 * 24 * HOUR);
      const unknown = !!params.get('unknown');
      s.tabs[3] = { ...s.tabs[3], name: 'claude-6', expected: 'claude-6', mismatch: { expected: 'claude-6', actual: unknown ? null : 'claude-7', shared: !!params.get('shared') }, reading: 'on', read_state: 'idle', last_read: iso(-MIN) };
      s.view = params.get('view') || s.tabs[3].id;
      break;
    }
    case 'owner':
      s.hub.owner = true;
      s.hub.owner_until = iso(20 * 24 * HOUR);
      break;
    case 'removed':
      Object.assign(s.hub, { paired: false, status: 'needs_pairing', removed: true, needs_you: 0, admin: 'local', local: true, service: 'running' });
      break;
    case 'direct_pairing':
      Object.assign(s.hub, { paired: false, status: 'needs_pairing', direct: true, needs_you: 0 });
      break;
    case 'unreachable':
      Object.assign(s.hub, { reachable: false, status: 'unreachable', needs_you: 0, last_seen: iso(-12 * MIN), local: true, service: 'stopped' });
      break;
    case 'board_blocked':
      Object.assign(s.hub, { board_blocked: true });
      break;
    default:
      break;
  }
  return s;
}

let state = fixtures();
const listeners = { state: new Set(), toast: new Set() };
const emit = (ev, payload) => { for (const cb of listeners[ev]) cb(payload); };
const push = () => queueMicrotask(() => emit('state', structuredClone(state)));
const find = (id) => state.tabs.find((t) => t.id === id);
const later = (v, ms = 60) => new Promise((r) => setTimeout(() => r(v), ms));
let nextId = 5;

const folders = [
  { folder: '~/.claude', label: 'personal', colour: '#14b8a6', logged_in: true, email: 'you@example.com', plan: 'max_20x', primary: true, items: {}, mcp_sync: true },
  {
    folder: '~/.claude-2', label: 'work', colour: '#8b5cf6', logged_in: true, email: 'you@work.example', plan: 'team', mcp_sync: true,
    items: { 'settings.json': { state: 'linked' }, 'CLAUDE.md': { state: 'linked' }, rules: { state: 'linked' }, skills: { state: 'linked' }, agents: { state: 'linked' }, commands: { state: 'linked' }, plugins: { state: 'linked' }, projects: { state: 'separate' }, 'history.jsonl': { state: 'separate' }, plans: { state: 'separate' } },
  },
  {
    folder: '~/.claude-3', label: 'client', colour: '#f59e0b', logged_in: false, email: null, plan: null, mcp_sync: false,
    items: { 'settings.json': { state: 'linked' }, 'CLAUDE.md': { state: 'separate' }, rules: { state: 'separate' }, skills: { state: 'linked' }, agents: { state: 'linked' }, commands: { state: 'linked' }, plugins: { state: 'separate' }, projects: { state: 'separate' }, 'history.jsonl': { state: 'separate' }, plans: { state: 'separate' } },
  },
];

const wy = {
  state: () => later(structuredClone(state), 20),
  selectView: async ({ view }) => { state.view = view; push(); },
  hubRoute: async () => later(undefined),
  addTab: async ({ name, seat }) => {
    const id = `t_${String(nextId++).padStart(16, '0')}`;
    if (seat) state.missing = (state.missing || []).filter((m) => m.seat !== seat);
    state.tabs.push(tab(id, name, { signed_in: false, reading: 'never_asked', needs_warning: false, last_read: null }));
    push();
    return { id };
  },
  updateTab: async ({ id, name }) => { const t = find(id); if (t && name) t.name = name; push(); },
  removeTab: async ({ id }) => { state.tabs = state.tabs.filter((t) => t.id !== id); if (state.view === id) state.view = 'hub'; push(); },
  reorderTabs: async ({ ids }) => { state.tabs.sort((a, b) => ids.indexOf(a.id) - ids.indexOf(b.id)); push(); },
  reloadTab: async () => undefined,
  homeTab: async () => undefined,
  adoptTab: async ({ id }) => {
    const t = find(id);
    const before = t?.mismatch;
    if (!t || !before) return { name: 'account', label: null, offer: null };
    const name = before.actual || 'account';
    Object.assign(t, { name, expected: before.actual ? name : null, mismatch: null });
    push();
    return { name, label: before.actual, offer: { seat: 'seat_01HZZZZZZZZZZZZZZZZZZZZZ06', label: before.expected } };
  },
  logoutTab: async () => undefined,
  openTabExternal: async () => undefined,
  acceptWarning: async ({ id }) => { const t = find(id); if (t) t.needs_warning = false; push(); },
  setReading: async ({ id, on }) => {
    const t = find(id);
    if (t) { t.reading = on ? 'on' : 'off'; t.read_state = on ? 'idle' : 'off'; t.needs_warning = false; }
    push();
  },
  setSend: async ({ id, on }) => { const t = find(id); if (t) t.send_to_hub = on; push(); },
  resumeReading: async ({ id }) => { const t = find(id); if (t) { t.stopped = null; t.read_state = 'reading'; } push(); },
  refreshUsage: async () => undefined,
  // ?slow=<ms> makes pair and owner sign-in take that long (the progress line); ?fail=1 makes pair fail
  pair: async () => { await later(0, Number(params.get('slow') || 0)); if (params.get('fail')) return { ok: false, error: 'ssh: connection timed out' }; state.hub.paired = true; state.hub.status = 'ok'; push(); return { ok: true }; },
  ownerSignIn: async () => { await later(0, Number(params.get('slow') || 0)); state.hub.owner = true; state.hub.owner_until = '2026-11-14T10:00:00Z'; push(); return later({ ok: true }, 200); },
  ownerSignOut: async () => { state.hub.owner = false; state.hub.owner_until = null; push(); return { ok: true }; },
  hubConnect: async () => { state.hub.paired = true; state.hub.status = 'ok'; state.hub.url = 'https://hub.example.com'; push(); return { ok: true }; },
  hubUnpair: async () => { state.hub.paired = false; state.hub.owner = false; state.hub.status = 'needs_pairing'; push(); return { ok: true, revoked: true }; },
  hubRetry: async () => ({ ok: true }),
  trustCert: async () => { state.hub.cert = null; state.hub.pinned = true; push(); return { ok: true, pinned: true }; },
  openRelease: async () => ({ ok: true }),
  hubService: async ({ action }) => ({ service: action === 'stop' ? 'stopped' : 'running', health: action !== 'stop' }),
  updateSeat: async ({ seat, label, colour }) => {
    const t = state.tabs.find((x) => x.seat === seat);
    if (t) { if (label !== undefined) t.label = label; if (colour !== undefined) t.colour = colour; }
    push();
    return { ok: true };
  },
  localAccounts: async () => later({ folders: structuredClone(folders) }, 120),
  shareAccount: async () => ({ ok: true }),
  setSettings: async (p) => { Object.assign(state.settings, p); push(); return structuredClone(state.settings); },
  on: (event, cb) => {
    if (!listeners[event]) throw new Error(`wy.on: unknown event ${event}`);
    listeners[event].add(cb);
    return () => listeners[event].delete(cb);
  },
};

globalThis.wy = Object.freeze(wy);
// The dev page also lets a test drive pushes: window.__mock.toast('warn', 'text'), .state(fn).
globalThis.__mock = {
  toast: (level, text) => emit('toast', { level, text }),
  mutate: (fn) => { fn(state); push(); },
  get state() { return state; },
};
