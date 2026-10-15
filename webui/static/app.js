// Switchyard web app: hash router, shell and render loop. The same single-page app is the
// desktop board (sidebar) and the installable phone app (bottom tabs); CSS chooses.
//
// Routes: see route.js. Start-up asks GET /v1/me: a 401 opens the pair screen (installed
// app or phone) or the token login (a computer).

import { store, subscribe, start, stop, setAuthLostHandler, refreshAll, canPush } from './store.js';
import { logout, getJSON } from './api.js';
import { renderNav } from './components/nav.js';
import { h } from './components/dom.js';
import { untilShort } from './components/alerts.js';
import { parseRoute, PUBLIC_VIEWS } from './route.js';
import { isMobile } from './platform.js';
import { pwa, initPwa, subscribe as onPwa, updateReady, applyUpdate, isStandalone } from './pwa.js';
import { resubscribeIfNeeded } from './push.js';
import * as now from './views/now.js';
import * as sessions from './views/sessions.js';
import * as session from './views/session.js';
import * as accounts from './views/accounts.js';
import * as machines from './views/machines.js';
import * as login from './views/login.js';
import * as more from './views/more.js';
import * as alerts from './views/alerts.js';
import * as settings from './views/settings.js';
import * as devices from './views/devices.js';
import * as peers from './views/peers.js';
import * as analytics from './views/analytics.js';
import * as pair from './views/pair.js';
import * as owner from './views/owner.js';
import * as alertRedirect from './views/alert-redirect.js';

const VIEWS = { now, sessions, session, accounts, machines, login, more, alerts, settings, devices, peers, analytics, pair, owner, alertRedirect };
const navEl = document.getElementById('nav');
const mainEl = document.getElementById('main');
const announceEl = document.getElementById('announce');
const appEl = document.getElementById('app');
const bannersEl = document.getElementById('banners');

let authed = false;
let lastRouteKey = '';
let lastNeeds = null;

export { parseRoute };

function go(hash) {
  if (location.hash === hash) render(); else location.hash = hash;
}

// Where a signed-out visitor lands: the pair screen on a phone or in the installed app,
// the token login on a computer.
const unauthRoute = () => (isStandalone() || isMobile(navigator.userAgent, navigator.maxTouchPoints, navigator.platform) ? '#/pair' : '#/login');

function showUnauth() {
  authed = false;
  stop();
  appEl.classList.add('shell-login');
  navEl.replaceChildren();
  const route = parseRoute(location.hash);
  if (PUBLIC_VIEWS.has(route.view)) render(); else go(unauthRoute());
}

let booting = false; // start() emits while loading: do not bounce to the pair screen before it answers

async function boot() {
  booting = true;
  const ok = await start();
  booting = false;
  if (ok) {
    authed = true;
    appEl.classList.remove('shell-login');
    const v = parseRoute(location.hash).view;
    if (v === 'login') go('#/'); else render();
    if (canPush()) resubscribeIfNeeded();
  } else if (store.unauthorized || !store.error) {
    showUnauth();
  } else {
    // Hub unreachable and nothing stored: show the error where the page would be.
    appEl.classList.add('shell-login');
    mainEl.replaceChildren(errorBox(store.error));
  }
}

function errorBox(msg) {
  return h('div', { class: 'login-wrap' }, h('div', { class: 'card login-card' },
    h('p', {}, msg), h('button', { class: 'btn', type: 'button', onclick: boot }, 'Try again')));
}

let rendering = false, again = false;

async function render() {
  if (rendering) { again = true; return; }
  rendering = true;
  try {
    const route = parseRoute(location.hash);
    renderBanners();
    if (!authed && booting && !PUBLIC_VIEWS.has(route.view)) return;
    if (!authed && !PUBLIC_VIEWS.has(route.view)) { go(unauthRoute()); return; }
    if (authed && route.view === 'login') { go('#/'); return; }
    const view = VIEWS[route.view];
    const key = `${route.view}/${route.id || ''}`;
    const changed = key !== lastRouteKey;
    const ctx = { store, route, now: Date.now(), onLoggedIn: boot, onLogout: doLogout, rerender: render };

    if (changed) lastRouteKey = key;
    if (changed && view.load) view.load(ctx);

    if (PUBLIC_VIEWS.has(route.view)) {
      appEl.classList.add('shell-login');
    } else {
      appEl.classList.remove('shell-login');
      if (!store.overview) return;
      renderNav(navEl, { page: route.page, store, onLogout: doLogout });
    }

    const focusKey = document.activeElement && document.activeElement.dataset && document.activeElement.dataset.key;
    const { title, node } = view.render(ctx);
    mainEl.replaceChildren(node);
    document.title = title;
    appEl.dataset.page = route.page;
    if (changed) { window.scrollTo(0, 0); if (!PUBLIC_VIEWS.has(route.view)) mainEl.focus({ preventScroll: true }); }
    else if (focusKey) {
      const el = document.querySelector(`[data-key="${CSS.escape(focusKey)}"]`);
      if (el) el.focus({ preventScroll: true });
    }
    if (view.after) view.after(ctx, changed);
    announceNeeds();
  } finally {
    rendering = false;
    if (again) { again = false; render(); }
  }
}

function announceNeeds() {
  if (!store.overview) return;
  const n = store.overview.counts.needs_you;
  if (lastNeeds !== null && n !== lastNeeds) announceEl.textContent = n ? `${n} sessions need you` : 'Nothing needs you';
  lastNeeds = n;
  if ('setAppBadge' in navigator && authed && canPush()) {
    (n ? navigator.setAppBadge(n) : navigator.clearAppBadge()).catch(() => {});
  }
}

// ---- banners: offline and "new version ready" --------------------------------------------

function renderBanners() {
  const kids = [];
  if (store.offline) {
    const t = new Date(store.offline.since);
    const hm = `${String(t.getHours()).padStart(2, '0')}:${String(t.getMinutes()).padStart(2, '0')}`;
    kids.push(h('div', { class: 'banner banner-offline', role: 'status' }, `Offline · showing ${hm}`));
  }
  if (updateReady()) {
    kids.push(h('div', { class: 'banner banner-update', role: 'status' }, h('span', {}, 'New version ready'),
      h('button', { type: 'button', class: 'btn btn-primary btn-sm', 'data-key': 'update-reload', onclick: applyUpdate }, 'Reload')));
  }
  bannersEl.replaceChildren(...kids);
}

async function doLogout() {
  try { await logout(); } catch (_) { /* cookie may already be gone */ }
  showUnauth();
}

setAuthLostHandler(showUnauth);

// Redraw on data changes (coalesced to one frame) and on route changes.
let frame = 0;
const schedule = () => { if (!frame) frame = requestAnimationFrame(() => { frame = 0; render(); }); };
subscribe(schedule);
onPwa(schedule);
window.addEventListener('hashchange', render);
window.addEventListener('sy-rerender', render);
// "5 min ago" labels move even when no data arrives.
setInterval(() => { if (authed) render(); }, 30e3);
// A pending alert's "push in 40 s" counts down between redraws.
setInterval(() => {
  for (const el of document.querySelectorAll('[data-due]')) el.textContent = `push ${untilShort(el.dataset.due)}`;
}, 5e3);

// The service worker forwards pushes and routes taps to an open window.
if ('serviceWorker' in navigator) {
  navigator.serviceWorker.addEventListener('message', (e) => {
    const m = e.data || {};
    if (m.type === 'push' && authed) refreshAll();
    if (m.type === 'route' && typeof m.hash === 'string' && m.hash.startsWith('#/')) location.hash = m.hash;
  });
}

initPwa();
boot();

// Exposed for the smoke test and debugging.
window.__sy = { store, getJSON, pwa };
