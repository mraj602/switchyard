// Installing and updating the app (pwa.md 4 and 7): service worker registration, the
// "Update ready" flow, the install prompt and what the page can tell about itself.
//
// State lives in `pwa`; call subscribe(fn) to redraw when it changes.

import { getHealth } from './api.js';
import { autoApplyOk } from './components/update-policy.js';
import { createPending } from './components/pending.js';

// Open dialogs and unsaved form edits: a reload by itself waits for these (pwa.md 7).
export const guard = createPending(document);

export const pwa = {
  supported: 'serviceWorker' in navigator,
  registration: null,
  waiting: false,         // a new worker is installed and waiting for "Reload"
  build: null,            // the build hash the running page was loaded with (from the worker)
  hubBuild: null,         // /health `ui`
  controlled: false,      // the worker controls this page (offline works)
  installEvent: null,     // saved beforeinstallprompt
  checkedAt: 0,
};

const listeners = new Set();
export const subscribe = (fn) => { listeners.add(fn); return () => listeners.delete(fn); };
const emit = () => listeners.forEach((fn) => fn());

export const isStandalone = () =>
  (window.matchMedia && window.matchMedia('(display-mode: standalone)').matches) || navigator.standalone === true;

export const canInstall = () => !!pwa.installEvent;

export async function promptInstall() {
  const ev = pwa.installEvent;
  if (!ev) return null;
  pwa.installEvent = null;
  emit();
  ev.prompt();
  const choice = await ev.userChoice;
  return choice && choice.outcome;
}

// A different build is on the hub and no worker is waiting for it yet (the browser has not
// checked), so the banner offers Reload too.
export const updateReady = () =>
  pwa.waiting || (!!pwa.build && !!pwa.hubBuild && pwa.build !== pwa.hubBuild);

// Take over with the waiting worker; the page reloads once on controllerchange. Without a
// waiting worker (the build check above), ask the browser for the new worker first: a plain
// reload would still be answered by the old worker's copy of the files.
let reloading = false;
let asked = false; // the person pressed Reload: the page reloads whatever is pending
function reloadNow() { if (!reloading) { reloading = true; location.reload(); } }
// A worker change that nobody asked for (another tab applied the update, or an automatic
// apply) waits until no dialog is open and no form has unsaved edits.
function reloadWhenIdle() {
  if (asked || !guard.pending()) { reloadNow(); return; }
  const t = setInterval(() => { if (!guard.pending()) { clearInterval(t); reloadNow(); } }, 1000);
}
export function applyUpdate() { asked = true; return doApply(); }
async function doApply() {
  const reg = pwa.registration;
  if (!reg) { reloadNow(); return; }
  if (!reg.waiting) { try { await reg.update(); } catch (_) { /* offline */ } }
  const inst = reg.installing;
  if (!reg.waiting && inst) {
    await Promise.race([
      new Promise((resolve) => {
        const done = () => { if (['installed', 'activated', 'redundant'].includes(inst.state)) resolve(); };
        inst.addEventListener('statechange', done);
        done();
      }),
      new Promise((resolve) => setTimeout(resolve, 10e3)),
    ]);
  }
  if (reg.waiting) reg.waiting.postMessage({ type: 'skip-waiting' });
  else reloadNow();
}

const PAGE_START = Date.now();
const AUTO_KEY = 'wy.autoUpdateAt';
const editing = () => {
  const a = document.activeElement;
  return !!a && (a.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(a.tagName));
};
function lastAutoAge() {
  try { const t = Number(sessionStorage.getItem(AUTO_KEY)); return t ? Date.now() - t : null; } catch (_) { return null; }
}

// Apply a waiting update without a click when that costs nothing (see update-policy.js).
export function applyIfIdle() {
  const w = pwa.registration && pwa.registration.waiting;
  const ok = autoApplyOk({
    waiting: !!w, editing: editing(), hidden: document.visibilityState === 'hidden',
    pageAgeMs: Date.now() - PAGE_START, sinceLastAutoMs: lastAutoAge(),
    pending: guard.pending(),
  });
  if (!ok) return false;
  try { sessionStorage.setItem(AUTO_KEY, String(Date.now())); } catch (_) { /* no storage */ }
  doApply();
  return true;
}

function watch(reg) {
  const track = (w) => {
    if (!w) return;
    const check = () => {
      if (w.state === 'installed' && navigator.serviceWorker.controller) { pwa.waiting = true; emit(); applyIfIdle(); }
    };
    w.addEventListener('statechange', check);
    check();
  };
  if (reg.waiting && navigator.serviceWorker.controller) { pwa.waiting = true; emit(); applyIfIdle(); }
  track(reg.installing);
  reg.addEventListener('updatefound', () => track(reg.installing));
}

async function askBuild() {
  const c = navigator.serviceWorker.controller;
  if (c) c.postMessage({ type: 'build' });
}

// "Check for update": ask the browser to fetch sw.js again and compare with /health.
export async function checkForUpdate() {
  pwa.checkedAt = Date.now();
  try { if (pwa.registration) await pwa.registration.update(); } catch (_) { /* offline */ }
  try { pwa.hubBuild = (await getHealth()).ui || null; } catch (_) { /* offline */ }
  emit();
  return updateReady();
}

export async function initPwa() {
  window.addEventListener('beforeinstallprompt', (e) => { e.preventDefault(); pwa.installEvent = e; emit(); });
  window.addEventListener('appinstalled', () => { pwa.installEvent = null; emit(); });
  if (!pwa.supported) return;

  let hadController = !!navigator.serviceWorker.controller;
  pwa.controlled = hadController;
  navigator.serviceWorker.addEventListener('controllerchange', () => {
    const was = hadController;
    hadController = !!navigator.serviceWorker.controller;
    pwa.controlled = hadController;
    // The first activation claims the page: nothing to reload. Every later change of worker is
    // an update, and the page reloads once to load its files.
    if (was && !reloading) reloadWhenIdle();
    else { askBuild(); emit(); }
  });
  navigator.serviceWorker.addEventListener('message', (e) => {
    const m = e.data || {};
    if (m.type === 'build') { pwa.build = m.build; emit(); }
  });

  try {
    pwa.registration = await navigator.serviceWorker.register('sw.js', { scope: '/', updateViaCache: 'none' });
  } catch (_) {
    pwa.supported = false; // insecure context or blocked
    emit();
    return;
  }
  watch(pwa.registration);
  askBuild();
  navigator.serviceWorker.ready.then(() => { pwa.controlled = !!navigator.serviceWorker.controller; askBuild(); emit(); });
  checkForUpdate();
  // A waiting build that was held back by an open dialog or an unsaved form goes in once
  // that is over (the policy still decides whether the moment is a free one).
  setInterval(() => { if (pwa.waiting) applyIfIdle(); }, 2000);

  // An installed iOS app is often resumed, not reloaded.
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'hidden') applyIfIdle();
    else if (Date.now() - pwa.checkedAt > 900e3) checkForUpdate();
  });
}
