// Installing and updating the app (pwa.md 4 and 7): service worker registration, the
// "New version ready" flow, the install prompt and what the page can tell about itself.
//
// State lives in `pwa`; call subscribe(fn) to redraw when it changes.

import { getHealth } from './api.js';

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
// waiting worker (the build check above), just reload.
let reloading = false;
export function applyUpdate() {
  const w = pwa.registration && pwa.registration.waiting;
  if (w) w.postMessage({ type: 'skip-waiting' });
  else location.reload();
}

function watch(reg) {
  const track = (w) => {
    if (!w) return;
    const check = () => { if (w.state === 'installed' && navigator.serviceWorker.controller) { pwa.waiting = true; emit(); } };
    w.addEventListener('statechange', check);
    check();
  };
  if (reg.waiting && navigator.serviceWorker.controller) { pwa.waiting = true; emit(); }
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

  const hadController = !!navigator.serviceWorker.controller;
  pwa.controlled = hadController;
  navigator.serviceWorker.addEventListener('controllerchange', () => {
    pwa.controlled = !!navigator.serviceWorker.controller;
    // The first activation claims the page: nothing to reload. An update the user asked for does.
    if (hadController && !reloading) { reloading = true; location.reload(); }
    else { askBuild(); emit(); }
  });
  navigator.serviceWorker.addEventListener('message', (e) => {
    const m = e.data || {};
    if (m.type === 'build') { pwa.build = m.build; emit(); }
  });

  try {
    pwa.registration = await navigator.serviceWorker.register('sw.js', { scope: '/' });
  } catch (_) {
    pwa.supported = false; // insecure context or blocked
    emit();
    return;
  }
  watch(pwa.registration);
  askBuild();
  navigator.serviceWorker.ready.then(() => { pwa.controlled = !!navigator.serviceWorker.controller; askBuild(); emit(); });
  checkForUpdate();

  // An installed iOS app is often resumed, not reloaded.
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible' && Date.now() - pwa.checkedAt > 3600e3) checkForUpdate();
  });
}
