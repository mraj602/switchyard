// The app's data: who is signed in, one overview, the session list (loaded on first use) and
// the open session's detail. The stream patches it in place; polling refetches it.

import {
  AuthError, getMe, getOverview, getSessions, getSession, openStream, setCachedHandler,
  snoozeAlert, seenAlert,
} from './api.js';

const listeners = new Set();

export const store = {
  me: null,            // GET /v1/me: {device, owner, hub}
  overview: null,      // GET /v1/overview
  sessions: null,      // GET /v1/sessions, null until a view asks for it
  detail: null,        // GET /v1/sessions/{id} for the open session page
  detailId: null,
  live: 'connecting',  // connecting | live | polling | offline
  liveAt: 0,           // when `live` last changed
  everLive: false,     // the stream has connected at least once
  offline: null,       // {since: ms} while the service worker answers from its copy
  updatedAt: 0,
  error: null,
  unauthorized: false, // the last start() ended in a 401
};

export const subscribe = (fn) => { listeners.add(fn); return () => listeners.delete(fn); };
const emit = () => listeners.forEach((fn) => fn());

let onAuthLost = () => {};
export const setAuthLostHandler = (fn) => { onAuthLost = fn; };

// What the signed-in caller may do (scopes from /v1/me). A plain board login has `read`
// only: the push and owner controls stay hidden for it.
export const isOwner = () => !!(store.me && store.me.owner);
export const canPush = () => !!(store.me && (store.me.owner || (store.me.device.scopes || []).includes('push')));

async function guarded(fn) {
  try {
    const r = await fn();
    store.error = null;
    store.errorStatus = 0;
    return r;
  } catch (e) {
    if (e instanceof AuthError) { store.unauthorized = true; stop(); onAuthLost(); return undefined; }
    store.error = e.message;
    store.errorStatus = e.status || 0;
    // The live badge belongs to the stream alone (openStream's onStatus): a read that failed
    // (a 404 for a deleted session, a 403, even a network error) says nothing about the open
    // stream, and nothing would set the badge back to live while it stays open (UI audit G5).
    emit();
    return undefined;
  }
}

// The API client reports whether each read came live or from the worker's copy.
setCachedHandler((at) => {
  const was = store.offline;
  if (at) {
    const since = was ? Math.min(was.since, at) : at; // the oldest copy on screen
    if (!was || was.since !== since) { store.offline = { since }; emit(); }
  } else if (was) {
    store.offline = null;
    emit();
    refreshAll(); // the hub is back: replace every stored copy
  }
});

export async function refreshMe() {
  const m = await guarded(getMe);
  if (m) store.me = m;
  return !!m;
}

export async function refreshOverview() {
  const o = await guarded(getOverview);
  if (o) { store.overview = o; store.updatedAt = Date.now(); emit(); }
  return !!o;
}

export async function ensureSessions() {
  if (store.sessions) return;
  await refreshSessions();
}

async function refreshSessions() {
  const r = await guarded(getSessions);
  if (r) { store.sessions = r.sessions; store.updatedAt = Date.now(); emit(); }
}

export async function openDetail(id) {
  if (store.detailId !== id) { store.detail = null; store.detailId = id; }
  const d = await guarded(() => getSession(id));
  if (d && store.detailId === id) { store.detail = d; emit(); }
  else if (!d && store.detailId === id && store.error) { emit(); }
}

export function closeDetail() { store.detail = null; store.detailId = null; }

export async function refreshAll() {
  await refreshOverview();
  if (store.sessions) await refreshSessions();
  if (store.detailId) await openDetail(store.detailId);
}

// ---- alerts on sessions ------------------------------------------------------------------

// Statuses where an episode still has an alert row worth showing on its session.
const OPEN = new Set(['pending', 'fired', 'snoozed', 'seen', 'skipped']);

function patchSessionAlert(sessionId, brief) {
  const alert = brief && OPEN.has(brief.status) ? brief : null;
  const set = (s) => { if (s && s.id === sessionId) s.alert = alert; };
  const o = store.overview;
  if (o) o.needs_you.forEach(set);
  if (store.sessions) store.sessions.forEach(set);
  set(store.detail);
}

// The brief of the alert on a session, only while it can still push or be acknowledged.
export const actionable = (a) => !!a && (a.status === 'pending' || a.status === 'fired' || a.status === 'snoozed');

// Seen and Snooze come back as the whole Alert; keep its brief on the session.
async function act(call, sessionId) {
  const a = await call();
  if (a) { patchSessionAlert(sessionId || a.session, a); emit(); }
  return a;
}
export const markSeen = (alertId, sessionId) => act(() => seenAlert(alertId), sessionId);
export const snooze = (alertId, sessionId, minutes) => act(() => snoozeAlert(alertId, minutes), sessionId);

// ---- stream events -----------------------------------------------------------------------

let overviewTimer = null;
function scheduleOverview() {
  clearTimeout(overviewTimer);
  overviewTimer = setTimeout(refreshOverview, 400);
}


function onEvent(type, data) {
  store.updatedAt = Date.now();
  switch (type) {
    case 'reset': refreshAll(); return;
    case 'session': applySession(data); break;
    case 'usage': applyUsage(data); break;
    case 'machine': applyMachine(data); break;
    case 'alert': if (data.session && data.alert) patchSessionAlert(data.session, data.alert); break;
    default: return; // hello, ping: nothing to draw
  }
  emit();
}

function applySession({ session, change }) {
  if (!session) return;
  if (store.sessions) {
    const i = store.sessions.findIndex((s) => s.id === session.id);
    if (i >= 0) store.sessions[i] = { ...store.sessions[i], ...session };
    else store.sessions.unshift(session);
  }
  const o = store.overview;
  if (o) {
    o.needs_you = o.needs_you.filter((s) => s.id !== session.id);
    if (session.needs_you) {
      o.needs_you.push(session);
      o.needs_you.sort((a, b) => Date.parse(a.state_since) - Date.parse(b.state_since));
    }
    o.counts = { ...o.counts, needs_you: o.needs_you.length };
  }
  if (store.detailId === session.id) openDetail(session.id);
  // Counts per state and per-seat live totals come from the hub.
  if (change !== 'update') scheduleOverview();
}

function applyUsage({ seat, windows }) {
  const s = store.overview && store.overview.seats.find((x) => x.id === seat);
  if (s && windows) s.windows = windows;
}

function applyMachine({ machine }) {
  const o = store.overview;
  if (!o || !machine) return;
  const i = o.machines.findIndex((m) => m.id === machine.id);
  if (i >= 0) o.machines[i] = machine;
  else o.machines.push(machine);
}

// ---- lifecycle ---------------------------------------------------------------------------

let closeStream = null, pollTimer = null, reconnectTimer = null;

// How long the stream may be down before the board says so.
export const RECONNECT_GRACE = 6000;

// What the live badge should say: 'live', 'connecting' (first connect), 'reconnecting' (down
// for longer than the grace), or 'polling'. A drop shorter than the grace still reads 'live'.
export function liveState(now = Date.now()) {
  const s = store.live;
  if (s === 'live') return 'live';
  if (s === 'polling') return 'polling';
  if (!store.everLive) return 'connecting';
  return now - store.liveAt < RECONNECT_GRACE ? 'live' : 'reconnecting';
}

// Resolves true when signed in and the overview loaded. On false, `store.unauthorized`
// says the hub answered 401 (pair or sign in); otherwise `store.error` holds the reason.
export async function start() {
  stop();
  store.unauthorized = false;
  store.error = null;
  if (!(await refreshMe())) return false;
  const ok = await refreshOverview();
  if (!ok) return false;
  closeStream = openStream({
    initialId: store.overview && Number.isInteger(store.overview.stream_id) ? store.overview.stream_id : null,
    onEvent,
    onResync: refreshAll,
    onStatus: (s) => {
      if (store.live === s) return;
      if (s !== 'live' && store.live === 'live') {
        // A short drop is normal: the badge says "Reconnecting…" only once it lasts (G5).
        clearTimeout(reconnectTimer);
        reconnectTimer = setTimeout(emit, RECONNECT_GRACE + 100);
      }
      if (s === 'live') store.everLive = true;
      store.live = s;
      store.liveAt = Date.now();
      emit();
      if (s === 'live' && store.offline) refreshAll(); // the hub is back: drop the stored copies
    },
  });
  // Poll slowly even while live (a safety net) and quickly when the stream is down.
  let tick = 0;
  pollTimer = setInterval(() => {
    tick++;
    if (store.live !== 'live' || tick % 4 === 0) refreshAll();
  }, 15e3);
  return true;
}

export function stop() {
  if (closeStream) closeStream();
  closeStream = null;
  clearInterval(pollTimer);
  pollTimer = null;
  store.me = null;
  store.overview = null;
  store.sessions = null;
  store.detail = null;
  store.detailId = null;
  store.offline = null;
}
