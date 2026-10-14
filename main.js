// Switchyard — one window, one isolated claude.ai session per account, plus a usage dashboard.
// Unofficial; not affiliated with Anthropic.
const { app, BrowserWindow, WebContentsView, session, ipcMain, shell, Notification, Menu, Tray, nativeImage } = require('electron');
const path = require('node:path');
const os = require('node:os');
const fs = require('node:fs');
const http = require('node:http');
const crypto = require('node:crypto');
const { Worker } = require('node:worker_threads');
const store = require('./core/db');

if (process.env.SWITCHYARD_DATA) app.setPath('userData', path.resolve(process.env.SWITCHYARD_DATA));

// One instance per data dir. `--background` (used by the login autostart entry) starts with
// the window hidden: tabs still load and usage/hooks/transcripts keep being collected.
// Launching Switchyard again just shows the running instance's window.
const BACKGROUND = process.argv.includes('--background') || process.argv.includes('--hidden');
if (!app.requestSingleInstanceLock()) app.exit(0);
// `electron . --autostart=on|off` toggles run-at-login without opening the window (also when
// Switchyard is already running), for scripted installs.
function cliAutostart(argv) {
  const a = argv.find((x) => /^--autostart=(on|off)$/.test(x));
  if (!a) return false;
  setAutostart(a.endsWith('=on'));
  console.log(`[switchyard] run at login: ${a.endsWith('=on') ? 'on' : 'off'}`);
  return true;
}
app.on('second-instance', (_e, argv) => { if (!cliAutostart(argv)) showWindow(); });

const CLAUDE = 'https://claude.ai';
const TABBAR_H = 48;
const STRIP_H = 40;
const POLL_MS = 5 * 60 * 1000;
const HOOK_PORT = Number(process.env.SWITCHYARD_HOOK_PORT || 47821);
// Plain Chrome UA: claude.ai and Cloudflare treat the "Electron/x" token as a bot signal.
const UA = `Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/${process.versions.chrome} Safari/537.36`;
app.userAgentFallback = UA;

// ---------- persistence ----------
const dataDir = () => app.getPath('userData');
const file = (n) => path.join(dataDir(), n);
const readJson = (n, dflt) => { try { return JSON.parse(fs.readFileSync(file(n), 'utf8')); } catch { return dflt; } };
const writeJson = (n, v) => fs.writeFileSync(file(n), JSON.stringify(v, null, 2));

let accounts = [];          // [{id, label, color, track}]
let config = {};            // {hookToken}
const usage = new Map();    // id -> {status, email, plan, windows[], fetchedAt, error}
let events = [];            // recent Claude Code hook events (mirror of the DB tail)
let db;
let active = 'dashboard';

// Earlier releases' colours → the validated categorical palette (same hue family).
const RECOLOR = { '#4FD1C5': '#199e70', '#F2706B': '#e66767', '#9B8CF2': '#9085e9', '#F29E4C': '#d95926', '#5AA9F2': '#3987e5', '#E58CC8': '#d55181', '#B9C46A': '#008300', '#D9C38C': '#c98500' };

function loadState() {
  fs.mkdirSync(dataDir(), { recursive: true });
  accounts = readJson('accounts.json', []);
  let recolored = false;
  for (const a of accounts) if (RECOLOR[a.color]) { a.color = RECOLOR[a.color]; recolored = true; }
  config = readJson('config.json', {});
  if (!config.hookToken || config.hookPort !== HOOK_PORT) {
    // the shared hook script (bin/switchyard-hook.sh) reads token and port from here
    config.hookToken = config.hookToken || crypto.randomBytes(18).toString('base64url');
    config.hookPort = HOOK_PORT;
    writeJson('config.json', config);
  }
  db = store.open(file('usage.db'));
  events = db.recentEvents(100);
  if (recolored) writeJson('accounts.json', accounts);
  syncDbAccounts();
}
// The DB keeps a copy of the tab list so analytics (and a sync server) can label data.
function syncDbAccounts() {
  try { db.syncAccounts(accounts.map((a, i) => ({ id: a.id, label: a.label, color: a.color, email: a.email || null, ord: i, plan: (usage.get(a.id) || {}).plan || null }))); }
  catch (e) { console.error('[switchyard] account sync:', e.message); }
}
const saveAccounts = () => { writeJson('accounts.json', accounts); syncDbAccounts(); };

// Trend lines: last 30 days of polled windows.
const history = () => db.history(Date.now() - 30 * 864e5);
// Every poll is stored, but bursts (tab switches, in-page navigation) are capped at one per minute.
const lastSample = new Map();
function recordHistory(id, u, rawUsage) {
  if (u.status !== 'ok') return;
  const now = Date.now();
  if (now - (lastSample.get(id) || 0) < 60e3) return;
  lastSample.set(id, now);
  try { db.recordSnapshot(now, id, u, rawUsage); } catch (e) { console.error('[switchyard] store:', e.message); }
}

// ---------- window + views ----------
let win;
const views = new Map(); // id -> WebContentsView

function createWindow() {
  win = new BrowserWindow({
    width: 1440, height: 940, minWidth: 720, minHeight: 500, show: !BACKGROUND,
    backgroundColor: '#0F1115', title: 'Switchyard', icon: ICON,
    webPreferences: { preload: path.join(__dirname, 'preload.js'), contextIsolation: true, sandbox: true },
  });
  win.loadFile(path.join(__dirname, 'shell', 'index.html'));
  win.on('resize', layout);
  // Closing the window keeps Switchyard collecting in the background (unless turned off).
  win.on('close', (e) => {
    if (quitting || config.keepRunning === false) return;
    e.preventDefault();
    win.hide();
    if (!config.hideNoticeShown) {
      config.hideNoticeShown = true; writeJson('config.json', config);
      notify('Switchyard keeps tracking in the background', 'Open Switchyard again to see it. Quit from its menu (Ctrl+Q) or turn this off in Background & sync.');
    }
  });
  accounts.forEach(createView);
}
const ICON = path.join(__dirname, 'assets', 'icon.png');
let quitting = false;
function showWindow() {
  if (!win || win.isDestroyed()) return;
  if (win.isMinimized()) win.restore();
  win.show(); win.focus();
}

const isClaude = (url) => { try { const h = new URL(url).hostname; return h === 'claude.ai' || h.endsWith('.claude.ai'); } catch { return false; } };
// Sign-in can bounce through these; keep them inside the tab.
const isAuthHop = (url) => { try { return /(^|\.)(anthropic\.com|claude\.com|challenges\.cloudflare\.com)$/.test(new URL(url).hostname); } catch { return false; } };

function createView(acct) {
  const ses = session.fromPartition(`persist:acct-${acct.id}`);
  ses.setUserAgent(UA);
  const allowed = new Set(['notifications', 'clipboard-sanitized-write', 'clipboard-read', 'fullscreen', 'media']);
  ses.setPermissionRequestHandler((_wc, perm, cb) => cb(allowed.has(perm)));
  ses.setPermissionCheckHandler((_wc, perm) => allowed.has(perm));

  const view = new WebContentsView({
    webPreferences: { session: ses, contextIsolation: true, sandbox: true, backgroundThrottling: false },
  });
  view.setBackgroundColor('#0F1115');
  const wc = view.webContents;
  wc.setWindowOpenHandler(({ url }) => {
    if (isClaude(url) || isAuthHop(url)) wc.loadURL(url); else shell.openExternal(url);
    return { action: 'deny' };
  });
  wc.on('will-navigate', (e, url) => { if (!isClaude(url) && !isAuthHop(url)) { e.preventDefault(); shell.openExternal(url); } });
  // claude.ai signs in and switches orgs without a full page load, so refresh on in-page navigation too.
  let navTimer;
  wc.on('did-navigate-in-page', (_e, url) => {
    if (!isClaude(url)) return;
    acct.lastUrl = url; saveAccounts();
    clearTimeout(navTimer);
    navTimer = setTimeout(() => { if (isStale(acct.id, 60e3)) refreshUsage(acct.id); }, 3000);
  });
  wc.on('page-title-updated', (_e, title) => { acct.title = title; pushState(); });
  // Refresh usage right after a sign-in lands on the app.
  wc.on('did-finish-load', () => { if (isClaude(wc.getURL())) setTimeout(() => refreshUsage(acct.id), 1500); });
  // Cloudflare's interstitial can stall (or want a click, depending on the IP's reputation).
  // Reload a *hidden* tab still on it after 25s, at most twice; never the visible tab, where
  // the user may be clicking the verification.
  let cfRetries = 0;
  wc.on('did-finish-load', () => {
    setTimeout(() => {
      if (wc.isDestroyed()) return;
      if (!/just a moment/i.test(wc.getTitle())) { cfRetries = 0; return; }
      if (acct.id === active) return;
      if (cfRetries < 2) { cfRetries++; wc.reload(); }
    }, 25000);
  });
  // Stagger start-up loads so N accounts don't all hit the challenge in the same second.
  const start = acct.lastUrl && isClaude(acct.lastUrl) ? acct.lastUrl : `${CLAUDE}/`;
  setTimeout(() => { if (!wc.isDestroyed()) wc.loadURL(start); }, views.size * 1500);
  view.setVisible(false);
  win.contentView.addChildView(view);
  views.set(acct.id, view);
}

function layout() {
  if (!win) return;
  const [w, h] = win.getContentSize();
  for (const [id, view] of views) {
    const on = id === active;
    view.setVisible(on);
    if (on) view.setBounds({ x: 0, y: TABBAR_H + STRIP_H, width: w, height: Math.max(0, h - TABBAR_H - STRIP_H) });
  }
}

const isStale = (id, ms) => { const u = usage.get(id); return !u || u.status !== 'ok' || Date.now() - u.fetchedAt > ms; };

const PAGES = new Set(['dashboard', 'analytics']);
function select(id) {
  active = PAGES.has(id) || views.has(id) ? id : 'dashboard';
  layout();
  if (PAGES.has(active)) accounts.forEach((a) => { if (isStale(a.id, 60e3)) refreshUsage(a.id); });
  else if (isStale(active, 60e3)) refreshUsage(active);
  if (views.has(active)) views.get(active).webContents.focus();
  pushState();
}

// ---------- usage ----------
// Reads the same endpoints claude.ai's own Settings → Usage page uses, from inside that
// account's own session. Nothing leaves this device. Undocumented: may change without notice.
const USAGE_JS = `(async () => {
  const j = async (u) => { const r = await fetch(u, { credentials: 'include', headers: { accept: 'application/json' } });
    if (!r.ok) return { __status: r.status }; return r.json(); };
  const orgs = await j('/api/organizations');
  if (!Array.isArray(orgs)) return { error: 'organizations ' + (orgs && orgs.__status) , status: orgs && orgs.__status };
  const out = [];
  for (const o of orgs) {
    const u = await j('/api/organizations/' + o.uuid + '/usage');
    out.push({ uuid: o.uuid, name: o.name, capabilities: o.capabilities, rate_limit_tier: o.rate_limit_tier,
      billing_type: o.billing_type, raven_type: o.raven_type, usage: u });
  }
  const a = await j('/api/account');
  return { orgs: out, account: a && !a.__status ? { email: a.email_address || a.email, name: a.full_name || a.display_name } : null };
})()`;

function planOf(org) {
  const caps = org.capabilities || [];
  const tier = String(org.rate_limit_tier || '');
  if (caps.includes('raven') || org.raven_type) return 'Team';
  if (caps.includes('enterprise')) return 'Enterprise';
  if (/max_20x/.test(tier)) return 'Max 20x';
  if (/max/.test(tier) || caps.includes('claude_max')) return 'Max 5x';
  if (caps.includes('claude_pro')) return 'Pro';
  return caps.includes('chat') ? 'Free' : (tier || 'Unknown');
}

// Friendly names for the windows we recognise; anything else the API reports is shown under
// its raw key (e.g. iguana_necktie, a monthly dollar budget) rather than hidden.
const LABELS = { five_hour: '5-hour', seven_day: '7-day', seven_day_opus: '7-day · Opus', seven_day_sonnet: '7-day · Sonnet', seven_day_oauth_apps: '7-day · apps', seven_day_cowork: '7-day · Cowork', iguana_necktie: 'Cloud session credits' };
const ORDER = Object.keys(LABELS);

function windowsOf(u) {
  const windows = [];
  for (const [key, v] of Object.entries(u)) {
    if (!v || typeof v !== 'object' || Array.isArray(v) || !('utilization' in v) || v.utilization == null) continue;
    windows.push({
      key, label: LABELS[key] || key, pct: Math.round(v.utilization * 10) / 10, resetsAt: v.resets_at,
      usedDollars: v.used_dollars ?? null, limitDollars: v.limit_dollars ?? null, locked: v.locked_reason || null,
    });
  }
  windows.sort((a, b) => (ORDER.indexOf(a.key) + 1 || 99) - (ORDER.indexOf(b.key) + 1 || 99));
  // Model-scoped weekly limits (e.g. Fable) only appear in `limits`.
  for (const l of Array.isArray(u.limits) ? u.limits : []) {
    const model = l.scope && l.scope.model && l.scope.model.display_name;
    if (!model) continue;
    const key = `${l.group || l.kind}_${model}`.toLowerCase();
    windows.push({ key, label: `${l.group === 'weekly' ? '7-day' : l.group || l.kind} · ${model}`, pct: Math.round(l.percent || 0), resetsAt: l.resets_at, inactive: l.is_active === false });
  }
  return windows;
}

function normalize(raw, activeOrg) {
  if (!raw || raw.error) {
    const signedOut = raw && (raw.status === 401 || raw.status === 403);
    return { status: signedOut ? 'signed-out' : 'error', error: raw ? raw.error : 'no data' };
  }
  // An account can belong to several orgs (personal + Team). Use the one this tab is
  // working in (claude.ai's lastActiveOrg cookie), else the one reporting the most limits.
  const scored = raw.orgs
    .map((o) => ({ o, windows: o.usage && !o.usage.__status ? windowsOf(o.usage) : null }))
    .filter((x) => x.windows);
  const pick = scored.find((x) => x.o.uuid === activeOrg && x.windows.length)
    || scored.sort((a, b) => b.windows.length - a.windows.length)[0];
  if (!pick) return { status: 'error', error: 'no organization with usage data' };
  const org = pick.o;
  const windows = pick.windows;
  const b = org.usage.seven_day_breakdown;
  return {
    status: 'ok', email: raw.account && raw.account.email, org: org.name, orgUuid: org.uuid,
    plan: planOf(org), windows, fetchedAt: Date.now(), orgCount: raw.orgs.length,
    breakdown: b && Array.isArray(b.rows) ? { startedAt: b.window_started_at, rows: b.rows.map((r) => ({ key: r.key, label: r.display_name || r.key, percent: r.percent })) } : null,
    extraUsage: org.usage.extra_usage ? !!org.usage.extra_usage.is_enabled : null,
  };
}

async function refreshUsage(id) {
  const acct = accounts.find((a) => a.id === id);
  const view = views.get(id);
  if (!acct || !view) return;
  if (acct.track === false) { usage.set(id, { status: 'off' }); return pushState(); }
  const wc = view.webContents;
  if (!isClaude(wc.getURL())) { usage.set(id, { status: 'signed-out' }); return pushState(); }
  let raw;
  try { raw = await wc.executeJavaScriptInIsolatedWorld(1001, [{ code: USAGE_JS }]); }
  catch (e) {
    try { raw = await wc.executeJavaScript(USAGE_JS); } catch (e2) { raw = { error: String(e2.message || e2) }; }
  }
  if (process.env.SWITCHYARD_DUMP) writeJson(`usage-raw-${id}.json`, raw);
  const active0 = await wc.session.cookies.get({ url: CLAUDE, name: 'lastActiveOrg' }).catch(() => []);
  const u = normalize(raw, active0[0] && active0[0].value);
  const prev = usage.get(id);
  if (u.status === 'ok') {
    const planChanged = u.plan && (!prev || prev.plan !== u.plan);
    if (u.email && acct.email !== u.email) { acct.email = u.email; usage.set(id, u); saveAccounts(); }
    else if (planChanged) { usage.set(id, u); syncDbAccounts(); }
    const org = raw.orgs.find((o) => o.uuid === u.orgUuid);
    recordHistory(id, u, org && org.usage);
    warnIfCrossed(acct, prev, u);
  }
  usage.set(id, u);
  pushState();
}

function warnIfCrossed(acct, prev, u) {
  if (!prev || prev.status !== 'ok' || !Notification.isSupported()) return;
  for (const w of u.windows) {
    const before = (prev.windows.find((p) => p.key === w.key) || {}).pct || 0;
    for (const t of [80, 95]) {
      if (before < t && w.pct >= t) notify(`${acct.label}: ${w.label} at ${w.pct}%`, 'Consider switching account.', acct.id);
    }
  }
}

const refreshAll = () => accounts.forEach((a) => refreshUsage(a.id));

// ---------- Remote Control session links ----------
// A Remote Control session writes `{"attachment":{"type":"remote_session_change","url":
// "https://claude.ai/code/session_…"}}` into its local transcript. Hooks tell us the
// transcript path, so we tail it (only bytes not seen yet) to find the session's claude.ai URL.
const transcripts = new Map(); // session_id -> {offset, url}
const REMOTE_RE = /"type":"remote_session_change","url":"(https:\/\/claude\.ai\/code\/session_[A-Za-z0-9]+)"/g;

function isTranscript(p) {
  if (typeof p !== 'string' || !p.endsWith('.jsonl')) return false;
  const real = path.resolve(p);
  return real.startsWith(os.homedir() + path.sep) && real.split(path.sep).includes('projects');
}

function remoteUrlFor(sessionId, transcriptPath) {
  if (!sessionId || !isTranscript(transcriptPath)) return null;
  const t = transcripts.get(sessionId) || { offset: 0, url: null };
  try {
    const size = fs.statSync(transcriptPath).size;
    if (size < t.offset) t.offset = 0; // rewritten
    if (size > t.offset) {
      const fd = fs.openSync(transcriptPath, 'r');
      const chunk = Buffer.alloc(Math.min(size - t.offset, 64 << 20));
      const n = fs.readSync(fd, chunk, 0, chunk.length, t.offset);
      fs.closeSync(fd);
      const text = chunk.toString('utf8', 0, n);
      // Only consume complete lines so a match split across reads isn't lost.
      const end = text.lastIndexOf('\n') + 1;
      for (const m of text.slice(0, end).matchAll(REMOTE_RE)) t.url = m[1];
      t.offset += Buffer.byteLength(text.slice(0, end));
    }
  } catch { /* transcript not readable: no link */ }
  transcripts.set(sessionId, t);
  return t.url;
}

// ---------- Claude Code hook receiver (localhost only) ----------
function notify(title, body, id, { code = false } = {}) {
  if (!Notification.isSupported()) return;
  const n = new Notification({ title, body, silent: false });
  n.on('click', () => { showWindow(); if (id) (code ? openCode(id, typeof code === 'string' ? code : null) : select(id)); });
  n.show();
}

function startHookServer() {
  const server = http.createServer((req, res) => {
    const url = new URL(req.url, 'http://localhost');
    if (req.method !== 'POST' || url.pathname !== '/hook') { res.writeHead(404).end(); return; }
    if (req.headers.authorization !== `Bearer ${config.hookToken}`) { res.writeHead(401).end(); return; }
    let body = '';
    req.on('data', (c) => { body += c; if (body.length > 1e6) req.destroy(); });
    req.on('end', () => { try { handleHook(url, body, res); } catch (e) { console.error('[switchyard] hook:', e); if (!res.headersSent) res.writeHead(500).end(); } });
  });
  server.on('error', (e) => console.error('[switchyard] hook server:', e.message));
  server.listen(HOOK_PORT, '127.0.0.1');
}

function handleHook(url, body, res) {
  let p = {};
  try { p = JSON.parse(body || '{}'); } catch {}
  // Hooks identify their account by the config dir's login email (stable even if the
  // tab is renamed); the label is a fallback for hooks installed by hand.
  const email = String(url.searchParams.get('email') || '').toLowerCase();
  const label0 = String(url.searchParams.get('account') || '').slice(0, 60);
  const acct = (email && accounts.find((a) => (a.email || '').toLowerCase() === email))
    || (label0 && accounts.find((a) => a.label.toLowerCase() === label0.toLowerCase()));
  const label = acct ? acct.label : (label0 || email || 'unknown');
  const ts = Date.now();

  const remoteUrl = remoteUrlFor(p.session_id, p.transcript_path);
  if (p.hook_event_name === 'SessionStart') {
    db.upsertSession({ sessionId: p.session_id, accountId: acct && acct.id, account: label, ts, cwd: p.cwd, transcriptPath: p.transcript_path, model: p.model });
    if (remoteUrl) db.setRemoteUrl(p.session_id, remoteUrl);
    res.writeHead(204).end();
    return;
  }
  if (remoteUrl) db.setRemoteUrl(p.session_id, remoteUrl);
  if (p.hook_event_name === 'Stop') scanSoon();
  const kind = p.hook_event_name === 'Stop' ? 'finished' : (p.notification_type || 'notification').replace(/_(prompt|dialog)$/, '');
  const text = p.hook_event_name === 'Stop'
    ? String(p.last_assistant_message || 'Finished').replace(/\s+/g, ' ').slice(0, 160)
    : String(p.message || '').slice(0, 200);
  const ev = { ts, account: label, accountId: acct && acct.id, kind, text, cwd: p.cwd ? path.basename(p.cwd) : '', session: p.session_id, url: remoteUrl };
  events.push(ev); events = events.slice(-100);
  db.addEvent(ev, p);
  syncSoon();
  // Pop a desktop notification only when a session is blocked on you; finished turns and
  // idle nudges fire after every reply, so they go to the feed and the tab badge only.
  if (kind !== 'idle' && kind !== 'finished') notify(`${label} · ${kind}${ev.cwd ? ' · ' + ev.cwd : ''}`, text || 'Claude Code needs you', acct && acct.id, { code: remoteUrl || true });
  pushState();
  res.writeHead(204).end();
}

// ---------- IPC ----------
function state() {
  return {
    active,
    accounts: accounts.map((a) => ({ ...a, usage: usage.get(a.id) || { status: 'loading' } })),
    events: events.slice(-30).reverse(),
    history: history(),
    hook: { port: HOOK_PORT, token: config.hookToken, script: path.join(__dirname, 'bin', 'connect.sh') },
  };
}
function pushState() { if (win && !win.isDestroyed()) win.webContents.send('state', state()); }

ipcMain.handle('state', () => state());
ipcMain.handle('select', (_e, id) => select(id));
ipcMain.handle('add', (_e, { label, color, track }) => {
  label = String(label || '').trim().slice(0, 40) || `account-${accounts.length + 1}`;
  const acct = { id: crypto.randomBytes(4).toString('hex'), label, color: color || '#4FD1C5', track: track !== false };
  accounts.push(acct); saveAccounts();
  createView(acct);
  select(acct.id);
  return acct.id;
});
ipcMain.handle('update', (_e, id, patch) => {
  const a = accounts.find((x) => x.id === id); if (!a) return;
  if (patch.label) a.label = String(patch.label).slice(0, 40);
  if (patch.color) a.color = patch.color;
  if ('track' in patch) a.track = !!patch.track;
  saveAccounts(); pushState();
});
ipcMain.handle('remove', async (_e, id) => {
  const view = views.get(id);
  if (view) { win.contentView.removeChildView(view); view.webContents.close(); views.delete(id); }
  const ses = session.fromPartition(`persist:acct-${id}`);
  await ses.clearStorageData().catch(() => {});
  accounts = accounts.filter((a) => a.id !== id); saveAccounts();
  usage.delete(id);
  if (active === id) active = 'dashboard';
  layout(); pushState();
});
ipcMain.handle('reorder', (_e, ids) => {
  accounts.sort((a, b) => ids.indexOf(a.id) - ids.indexOf(b.id)); saveAccounts(); pushState();
});
ipcMain.handle('refresh', (_e, id) => (id ? refreshUsage(id) : refreshAll()));
// "Jump in" from Claude Code activity: show that account's claude.ai/code (Remote Control and
// cloud sessions are listed there). Local hook session ids don't map to claude.ai/code URLs.
// Open a specific Remote Control session when we know its URL, else the claude.ai/code list.
function openCode(id, url) {
  const v = views.get(id); if (!v) return;
  const target = typeof url === 'string' && /^https:\/\/claude\.ai\/code\/session_[A-Za-z0-9]+$/.test(url) ? url : null;
  if (target) { if (v.webContents.getURL() !== target) v.webContents.loadURL(target); }
  else if (!/^https:\/\/claude\.ai\/code(\/|$|\?)/.test(v.webContents.getURL())) v.webContents.loadURL(`${CLAUDE}/code`);
  select(id);
}
ipcMain.handle('code', (_e, id, url) => openCode(id, url));
ipcMain.handle('reload', (_e, id) => { const v = views.get(id); if (v) v.webContents.reload(); });
ipcMain.handle('home', (_e, id) => { const v = views.get(id); if (v) v.webContents.loadURL(`${CLAUDE}/`); });
ipcMain.handle('external', (_e, id) => { const v = views.get(id); if (v) shell.openExternal(v.webContents.getURL()); });

// ---------- background worker: transcripts, analytics, server sync ----------
let worker;
const pending = new Map();
let reqId = 0;
const workerStatus = { scan: null, progress: null, sync: null };
function startWorker() {
  worker = new Worker(path.join(__dirname, 'core', 'worker.js'), {
    workerData: { dbPath: file('usage.db'), server: config.server && config.server.url ? config.server : null },
  });
  worker.on('message', (m) => {
    if (m.type === 'result') {
      const p = pending.get(m.id); if (!p) return;
      pending.delete(m.id);
      if (m.error) p.reject(new Error(m.error)); else p.resolve(m.data);
    } else if (m.type === 'scanned') { workerStatus.scan = { at: Date.now(), files: m.files, rows: m.rows, ms: m.ms }; workerStatus.progress = null; }
    else if (m.type === 'progress') workerStatus.progress = m;
    else if (m.type === 'sync') workerStatus.sync = m;
    else if (m.type === 'error') console.error(`[switchyard] worker ${m.where}:`, m.error);
  });
  worker.on('error', (e) => console.error('[switchyard] worker:', e));
  worker.on('exit', () => { for (const p of pending.values()) p.reject(new Error('worker restarted')); pending.clear(); if (!quitting) setTimeout(startWorker, 5000); });
}
function analytics(q) {
  const clean = {};
  for (const k of ['from', 'to', 'tz']) if (Number.isFinite(q && q[k])) clean[k] = q[k];
  if (q && Array.isArray(q.accounts)) clean.accounts = q.accounts.map(String).slice(0, 50);
  return new Promise((resolve, reject) => {
    const id = ++reqId;
    pending.set(id, { resolve, reject });
    worker.postMessage({ type: 'compute', id, q: clean });
    setTimeout(() => { if (pending.delete(id)) reject(new Error('analytics timed out')); }, 120e3);
  });
}
let scanTimer;
// push hook events to the self-hosted server right away so phone notifications aren't a minute late
let syncTimer;
function syncSoon() { if (!config.server || !config.server.url) return; clearTimeout(syncTimer); syncTimer = setTimeout(() => worker && worker.postMessage({ type: 'sync' }), 500); }
function scanSoon() { clearTimeout(scanTimer); scanTimer = setTimeout(() => worker && worker.postMessage({ type: 'scan' }), 4000); }
ipcMain.handle('analytics', (_e, q) => analytics(q));

// ---------- run at login (Linux: XDG autostart entry; macOS/Windows: login item) ----------
const AUTOSTART = path.join(os.homedir(), '.config', 'autostart', 'switchyard.desktop');
const LAUNCHER = path.join(os.homedir(), '.local', 'share', 'applications', 'switchyard.desktop');
function desktopEntry(background) {
  const q = (x) => `"${String(x).replace(/(["\\`$])/g, '\\$1')}"`;
  const env = process.env.SWITCHYARD_DATA ? `env SWITCHYARD_DATA=${q(path.resolve(process.env.SWITCHYARD_DATA))} ` : '';
  const exec = app.isPackaged ? q(process.execPath) : `${q(process.execPath)} ${q(app.getAppPath())}`;
  return ['[Desktop Entry]', 'Type=Application', 'Name=Switchyard', 'Comment=Usage, limits and alerts for several Claude accounts (unofficial)',
    `Exec=${env}${exec}${background ? ' --background' : ''}`, `Icon=${ICON}`, 'Terminal=false', 'Categories=Utility;Development;', 'StartupWMClass=switchyard',
    ...(background ? ['X-GNOME-Autostart-enabled=true', 'X-GNOME-Autostart-Delay=10'] : []), ''].join('\n');
}
function getAutostart() {
  if (process.platform === 'linux') return fs.existsSync(AUTOSTART);
  return app.getLoginItemSettings({ args: ['--background'] }).openAtLogin;
}
function setAutostart(on) {
  if (process.platform === 'linux') {
    if (on) {
      fs.mkdirSync(path.dirname(AUTOSTART), { recursive: true });
      fs.writeFileSync(AUTOSTART, desktopEntry(true));
      fs.mkdirSync(path.dirname(LAUNCHER), { recursive: true });
      fs.writeFileSync(LAUNCHER, desktopEntry(false)); // so it can be opened from the app grid
    } else fs.rmSync(AUTOSTART, { force: true });
  } else app.setLoginItemSettings({ openAtLogin: !!on, args: ['--background'] });
}

function settings() {
  return {
    autostart: getAutostart(), keepRunning: config.keepRunning !== false, background: BACKGROUND,
    server: { url: (config.server && config.server.url) || '', hasToken: !!(config.server && config.server.token) },
    status: workerStatus, dataDir: dataDir(),
  };
}
ipcMain.handle('settings', () => settings());
ipcMain.handle('settings:set', (_e, patch = {}) => {
  if ('autostart' in patch) setAutostart(!!patch.autostart);
  if ('keepRunning' in patch) config.keepRunning = !!patch.keepRunning;
  if (patch.server) {
    const url = String(patch.server.url || '').trim();
    if (url && !/^https?:\/\/[^\s]+$/i.test(url)) throw new Error('Server URL must start with http:// or https://');
    const token = patch.server.token != null && patch.server.token !== '' ? String(patch.server.token).trim() : (config.server && config.server.token) || '';
    config.server = url ? { url, token } : null;
    if (worker) worker.postMessage({ type: 'server', server: config.server });
  }
  writeJson('config.json', config);
  buildMenu();
  return settings();
});
ipcMain.handle('sync-now', () => { if (worker) worker.postMessage({ type: 'sync' }); });

// ---------- app ----------
let tray;
function buildMenu() {
  Menu.setApplicationMenu(Menu.buildFromTemplate([
    { label: 'Switchyard', submenu: [
      { label: 'Dashboard', accelerator: 'CmdOrCtrl+0', click: () => { showWindow(); select('dashboard'); } },
      { label: 'Analytics', accelerator: 'CmdOrCtrl+Shift+A', click: () => { showWindow(); select('analytics'); } },
      ...Array.from({ length: 9 }, (_, i) => ({ label: `Account ${i + 1}`, accelerator: `CmdOrCtrl+${i + 1}`, click: () => accounts[i] && select(accounts[i].id) })),
      { type: 'separator' },
      { label: 'Refresh usage', accelerator: 'CmdOrCtrl+Shift+R', click: refreshAll },
      { label: 'Run at login (in the background)', type: 'checkbox', checked: getAutostart(), click: (it) => { setAutostart(it.checked); } },
      { label: 'Keep running when the window is closed', type: 'checkbox', checked: config.keepRunning !== false, click: (it) => { config.keepRunning = it.checked; writeJson('config.json', config); } },
      { type: 'separator' },
      { role: 'reload', label: 'Reload shell', accelerator: 'CmdOrCtrl+Alt+R' },
      { role: 'toggleDevTools' },
      { label: 'Quit Switchyard', accelerator: 'CmdOrCtrl+Q', click: () => app.quit() },
    ] },
    { role: 'editMenu' },
  ]));
  if (tray) tray.setContextMenu(Menu.buildFromTemplate([
    { label: 'Open Switchyard', click: () => showWindow() },
    { label: 'Analytics', click: () => { showWindow(); select('analytics'); } },
    { label: 'Refresh usage', click: refreshAll },
    { type: 'separator' },
    { label: 'Quit', click: () => app.quit() },
  ]));
}

app.whenReady().then(() => {
  loadState();
  if (cliAutostart(process.argv) && !BACKGROUND) { app.exit(0); return; }
  // Tray icon where the desktop supports one (GNOME needs the AppIndicator extension).
  try { tray = new Tray(nativeImage.createFromPath(ICON).resize({ width: 22, height: 22 })); tray.setToolTip('Switchyard'); tray.on('click', showWindow); } catch { tray = null; }
  buildMenu();
  createWindow();
  startHookServer();
  startWorker();
  setInterval(refreshAll, POLL_MS);
  setInterval(flushSessions, 30e3);
});

// Persist sign-ins promptly (Chromium otherwise batches cookie writes), so a crash or
// kill never costs a login.
function flushSessions() {
  return Promise.all(accounts.map((a) => session.fromPartition(`persist:acct-${a.id}`).cookies.flushStore().catch(() => {})));
}
let flushed = false;
app.on('before-quit', (e) => {
  quitting = true;
  if (flushed) return;
  e.preventDefault();
  flushSessions().finally(() => { flushed = true; if (worker) worker.terminate(); app.quit(); });
});
app.on('window-all-closed', () => app.quit());
